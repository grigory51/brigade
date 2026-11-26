// Package coding reads CI metadata without forwarding provider credentials to sessions.
package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/grigory51/brigade/backend/internal/store"
)

type Provider string

const (
	GitHub              Provider = "github"
	GitLab              Provider = "gitlab"
	RefreshAfterSeconds          = 15
	refreshInterval              = RefreshAfterSeconds * time.Second
	maxRuns                      = 5
	maxJobs                      = 50
	maxDeployments               = 5
	maxSnapshotBytes             = 128 << 10
)

var (
	ErrInvalidRequest       = errors.New("coding: session_id required")
	ErrWorkspace            = errors.New("coding: workspace Git metadata is unavailable or unsupported")
	ErrNoRemote             = errors.New("coding: workspace has no origin remote")
	ErrNoHEAD               = errors.New("coding: workspace has no resolved HEAD")
	ErrUnsupportedWorkspace = errors.New("coding: workspace Git metadata is unsupported")
	ErrNotConfigured        = errors.New("coding: CI endpoint or user token is not configured")
	ErrRateLimited          = errors.New("coding: CI rate limit reached; retry later")
	ErrUpstream             = errors.New("coding: CI provider request failed")
	ErrCredentials          = errors.New("coding: CI provider denied access; check token permissions")
)

// Config is trusted server configuration, never plugin or git remote input.
type Config struct {
	Endpoints []Endpoint `koanf:"endpoints"`
}

type Endpoint struct {
	Provider    Provider `koanf:"provider"`
	RemoteHost  string   `koanf:"remote_host"`
	APIURL      string   `koanf:"api_url"`
	TokenSecret string   `koanf:"token_secret"`
	// Private destinations require explicit CIDRs, even for configured hosts.
	AllowedCIDRs []string `koanf:"allowed_cidrs"`
}

// DefaultConfig supports GitHub and GitLab with credentials in the existing
// per-user vault. Only public GitHub requests may be anonymous.
func DefaultConfig() Config {
	return Config{Endpoints: []Endpoint{
		{Provider: GitHub, RemoteHost: "github.com", APIURL: "https://api.github.com", TokenSecret: "CODING_GITHUB_TOKEN"},
		{Provider: GitLab, RemoteHost: "gitlab.com", APIURL: "https://gitlab.com/api/v4", TokenSecret: "CODING_GITLAB_TOKEN"},
	}}
}

type Workspace interface {
	Get(context.Context, string, string) (store.Session, error)
	OpenWorkspaceRoot(context.Context, string, string) (*os.Root, error)
}

type Secrets interface {
	SecretValues(context.Context, string) (map[string]string, error)
}

type Snapshot struct {
	Provider    Provider
	Repository  string
	CommitSHA   string
	Runs        []Run
	Deployments []Deployment
	Partial     bool
	Warnings    []string
	FetchedAt   time.Time
}

type Run struct {
	ID, Name, Status, Conclusion, URL, Ref string
	Jobs                                   []Job
	JobsPartial                            bool
}

type Job struct {
	ID, Name, Status, Conclusion, URL, Stage string
	AllowFailure                             bool
}

type Deployment struct {
	ID, Environment, Status, URL, EnvironmentURL, Ref, CommitSHA string
}

// Throttling is per user rather than per repository, so changing remotes or
// session IDs cannot bypass it. Entries expire and admission is bounded.
type userRequest struct {
	next   time.Time
	active bool
}

type Service struct {
	workspace Workspace
	secrets   Secrets
	endpoints map[string]*endpoint
	mu        sync.Mutex
	users     map[string]userRequest
	active    int
}

func New(workspace Workspace, secrets Secrets, cfg Config) (*Service, error) {
	s := &Service{workspace: workspace, secrets: secrets, endpoints: make(map[string]*endpoint), users: make(map[string]userRequest)}
	for _, item := range cfg.Endpoints {
		ep, err := newEndpoint(item)
		if err != nil {
			return nil, err
		}
		if _, exists := s.endpoints[ep.RemoteHost]; exists {
			return nil, errors.New("coding: duplicate remote host")
		}
		s.endpoints[ep.RemoteHost] = ep
	}
	return s, nil
}

func (s *Service) GetCI(ctx context.Context, sessionID, userID, projectPath string) (Snapshot, error) {
	if sessionID == "" || userID == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	if projectPath == "" {
		projectPath = "."
	}
	if len(projectPath) > 4096 || !filepath.IsLocal(projectPath) || strings.ContainsAny(projectPath, "\\\x00") {
		return Snapshot{}, ErrWorkspace
	}
	// Check ownership before touching the workspace, vault, limiter or network.
	if _, err := s.workspace.Get(ctx, sessionID, userID); err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	root, err := s.workspace.OpenWorkspaceRoot(ctx, sessionID, userID)
	if err != nil {
		return Snapshot{}, ErrWorkspace
	}
	defer root.Close()
	project, err := root.OpenRoot(filepath.FromSlash(projectPath))
	if err != nil {
		return Snapshot{}, ErrWorkspace
	}
	defer project.Close()
	repo, err := readRepository(ctx, project)
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, err
	}
	ep, ok := s.endpoints[repo.host]
	if !ok {
		return Snapshot{}, ErrNotConfigured
	}
	if ep.Provider == GitHub && len(strings.Split(repo.path, "/")) != 2 {
		return Snapshot{}, workspaceError(ErrUnsupportedWorkspace)
	}
	values, err := s.secrets.SecretValues(ctx, userID)
	if err != nil {
		return Snapshot{}, errors.New("coding: cannot read user credentials")
	}
	token := values[ep.TokenSecret]
	if ep.Provider == GitLab && token == "" {
		return Snapshot{}, ErrNotConfigured
	}
	// Local precondition failures must not consume the per-user provider budget.
	// Admission happens only once the request can reach the configured provider.
	if !s.begin(userID) {
		return Snapshot{}, ErrRateLimited
	}
	defer s.finish(userID)
	result := Snapshot{Provider: ep.Provider, Repository: repo.host + "/" + repo.path, CommitSHA: repo.sha}
	request := providerRequest{endpoint: ep, token: token, userID: userID}
	if ep.Provider == GitHub {
		err = request.github(ctx, repo, &result)
	} else {
		err = request.gitlab(ctx, repo, &result)
	}
	if err != nil {
		return Snapshot{}, err
	}
	if ctx.Err() != nil {
		return Snapshot{}, ctx.Err()
	}
	result.FetchedAt = time.Now()
	result.bound()
	return result, nil
}

func workspaceError(reason error) error {
	return fmt.Errorf("%w: %w", ErrWorkspace, reason)
}

func (s *Service) begin(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, state := range s.users {
		if !state.active && !now.Before(state.next) {
			delete(s.users, id)
		}
	}
	if _, exists := s.users[userID]; exists || s.active >= 4 || len(s.users) >= 1024 {
		return false
	}
	s.users[userID] = userRequest{active: true}
	s.active++
	return true
}

func (s *Service) finish(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID] = userRequest{next: time.Now().Add(refreshInterval)}
	s.active--
}

func (s *Snapshot) warn(message string) {
	s.Partial = true
	if len(s.Warnings) < 16 {
		s.Warnings = append(s.Warnings, clip(message, 256))
	}
}

func clip(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	return strings.Clone(value)
}

// Bound individual fields and the combined serialized payload. The reserved
// space covers envelope fields, warnings and the Connect/protobuf JSON mapping.
func (s *Snapshot) bound() {
	truncated := len(s.Repository) > 512
	s.Repository = clip(s.Repository, 512)
	s.CommitSHA = clip(s.CommitSHA, 64)
	if len(s.Runs) > maxRuns {
		s.Runs = s.Runs[:maxRuns]
		truncated = true
	}
	if len(s.Deployments) > maxDeployments {
		s.Deployments = s.Deployments[:maxDeployments]
		truncated = true
	}
	if len(s.Warnings) > 16 {
		s.Warnings = s.Warnings[:16]
	}
	for i := range s.Warnings {
		s.Warnings[i] = clip(s.Warnings[i], 256)
	}
	envelope := *s
	envelope.Runs, envelope.Deployments = nil, nil
	encoded, _ := json.Marshal(envelope)
	remaining := maxSnapshotBytes - 1024 - len(encoded)
	for i := range s.Runs {
		run := &s.Runs[i]
		if len(run.Name) > 256 || len(run.Ref) > 256 || len(run.Status) > 64 || len(run.Conclusion) > 64 {
			truncated = true
		}
		run.ID, run.Name, run.Ref = clip(run.ID, 32), clip(run.Name, 256), clip(run.Ref, 256)
		run.Status, run.Conclusion, run.URL = clip(run.Status, 64), clip(run.Conclusion, 64), safeURL(run.URL)
		base := *run
		base.Jobs = nil
		encoded, _ := json.Marshal(base)
		remaining -= len(encoded) + 1
	}
	for i := range s.Runs {
		run := &s.Runs[i]
		jobs := run.Jobs
		run.Jobs = nil
		if len(jobs) > maxJobs {
			jobs = jobs[:maxJobs]
			run.JobsPartial = true
			truncated = true
		}
		for _, job := range jobs {
			if len(job.Name) > 256 || len(job.Stage) > 256 || len(job.Status) > 64 || len(job.Conclusion) > 64 {
				truncated = true
			}
			job.ID, job.Name, job.Stage = clip(job.ID, 32), clip(job.Name, 256), clip(job.Stage, 256)
			job.Status, job.Conclusion, job.URL = clip(job.Status, 64), clip(job.Conclusion, 64), safeURL(job.URL)
			encoded, _ := json.Marshal(job)
			if len(encoded)+1 > remaining {
				run.JobsPartial = true
				truncated = true
				continue
			}
			remaining -= len(encoded) + 1
			run.Jobs = append(run.Jobs, job)
		}
	}
	deployments := s.Deployments
	s.Deployments = nil
	for _, deployment := range deployments {
		if len(deployment.Environment) > 256 || len(deployment.Ref) > 256 || len(deployment.Status) > 64 {
			truncated = true
		}
		deployment.ID, deployment.Environment, deployment.Ref = clip(deployment.ID, 32), clip(deployment.Environment, 256), clip(deployment.Ref, 256)
		deployment.Status, deployment.CommitSHA = clip(deployment.Status, 64), clip(deployment.CommitSHA, 64)
		deployment.URL, deployment.EnvironmentURL = safeURL(deployment.URL), safeURL(deployment.EnvironmentURL)
		encoded, _ := json.Marshal(deployment)
		if len(encoded)+1 > remaining {
			truncated = true
			continue
		}
		remaining -= len(encoded) + 1
		s.Deployments = append(s.Deployments, deployment)
	}
	if truncated {
		s.warn("CI fields or items were truncated to enforce the response size limit.")
	}
}
