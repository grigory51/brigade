package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/store"
)

const testSHA = "0123456789012345678901234567890123456789"

type fakeWorkspace struct {
	path   string
	opened int
}

func (w *fakeWorkspace) Get(_ context.Context, sessionID, userID string) (store.Session, error) {
	if sessionID != "session" || userID != "owner" {
		return store.Session{}, store.ErrNotFound
	}
	return store.Session{ID: sessionID, UserID: userID}, nil
}

func (w *fakeWorkspace) OpenWorkspaceRoot(ctx context.Context, sessionID, userID string) (*os.Root, error) {
	if _, err := w.Get(ctx, sessionID, userID); err != nil {
		return nil, err
	}
	w.opened++
	return os.OpenRoot(w.path)
}

type fakeSecrets struct{ users []string }

func (s *fakeSecrets) SecretValues(_ context.Context, userID string) (map[string]string, error) {
	s.users = append(s.users, userID)
	return map[string]string{"TOKEN": "private-user-token"}, nil
}

func writeFixture(t *testing.T, root, name, value string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func checkout(t *testing.T, root, remote string) {
	t.Helper()
	writeFixture(t, root, ".git/config", "[remote \"origin\"]\n url = "+remote+"\n")
	writeFixture(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	writeFixture(t, root, ".git/refs/heads/main", testSHA+"\n")
}

func testEndpoint(server *httptest.Server, provider Provider) Endpoint {
	return Endpoint{Provider: provider, RemoteHost: "git.example", APIURL: server.URL, TokenSecret: "TOKEN", AllowedCIDRs: []string{"127.0.0.1/32"}}
}

func trustTestServer(ep *endpoint, server *httptest.Server) {
	ep.client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
}

func TestGetCIGitHubOwnershipAndVault(t *testing.T) {
	workspace := &fakeWorkspace{path: t.TempDir()}
	checkout(t, filepath.Join(workspace.path, "project"), "git@git.example:team/repo.git")
	secrets := &fakeSecrets{}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer private-user-token" {
			t.Error("request must be read-only and use the user's vault token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/repos/team/repo/actions/runs":
			if req.URL.Query().Get("head_sha") != testSHA {
				t.Error("missing SHA filter")
			}
			fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"name":"Build","head_sha":%q,"status":"completed","conclusion":"failure","html_url":"https://git.example/run/1"}]}`, testSHA)
		case "/repos/team/repo/actions/runs/1/jobs":
			io.WriteString(w, `{"total_count":1,"jobs":[{"id":2,"name":"test","status":"completed","conclusion":"failure","html_url":"https://git.example/jobs/2"}]}`)
		case "/repos/team/repo/deployments":
			fmt.Fprintf(w, `[{"id":3,"environment":"production","sha":%q,"ref":"main"}]`, testSHA)
		case "/repos/team/repo/deployments/3/statuses":
			io.WriteString(w, `[{"state":"failure","environment_url":"https://app.example","log_url":"https://git.example/deploy/3"}]`)
		default:
			t.Errorf("unexpected request %s", req.URL)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	svc, err := New(workspace, secrets, Config{Endpoints: []Endpoint{testEndpoint(server, GitHub)}})
	if err != nil {
		t.Fatal(err)
	}
	trustTestServer(svc.endpoints["git.example"], server)
	if _, err := svc.GetCI(context.Background(), "session", "intruder", "project"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("owner check: %v", err)
	}
	if workspace.opened != 0 || len(secrets.users) != 0 || requests.Load() != 0 {
		t.Fatal("unauthorized request touched workspace, vault or network")
	}
	result, err := svc.GetCI(context.Background(), "session", "owner", "project")
	if err != nil {
		t.Fatal(err)
	}
	if result.Partial || result.CommitSHA != testSHA || result.Repository != "git.example/team/repo" || result.Runs[0].Conclusion != "failure" || result.Runs[0].Jobs[0].Conclusion != "failure" || result.Deployments[0].Status != "failure" || result.Deployments[0].EnvironmentURL != "https://app.example" {
		t.Fatalf("unexpected snapshot: %+v", result)
	}
	if len(secrets.users) != 1 || secrets.users[0] != "owner" || requests.Load() != 4 {
		t.Fatal("unexpected credential scope or request count")
	}
	if strings.Contains(fmt.Sprintf("%+v", result), "private-user-token") {
		t.Fatal("snapshot leaked token")
	}
	if _, err := svc.GetCI(context.Background(), "session", "owner", "project"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("throttle: %v", err)
	}
	if requests.Load() != 4 {
		t.Fatal("throttled request reached provider")
	}
}

func TestGitLabPipelineAndDeploymentWindow(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("PRIVATE-TOKEN") != "token" || req.Method != "GET" {
			t.Error("incorrect GitLab auth or method")
		}
		switch req.URL.EscapedPath() {
		case "/projects/team%2Fsubgroup%2Frepo/pipelines":
			if req.URL.Query().Get("sha") != testSHA {
				t.Error("missing SHA filter")
			}
			fmt.Fprintf(w, `[{"id":8,"sha":%q,"status":"manual","web_url":"https://git.example/pipeline/8"}]`, testSHA)
		case "/projects/team%2Fsubgroup%2Frepo/pipelines/8/jobs":
			io.WriteString(w, `[{"id":9,"name":"deploy","status":"manual","stage":"release","allow_failure":true,"web_url":"javascript:alert(1)"}]`)
		case "/projects/team%2Fsubgroup%2Frepo/deployments":
			if req.URL.Query().Get("sha") != "" {
				t.Error("GitLab deployments do not support SHA filter")
			}
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprintf(w, `[{"id":10,"sha":%q,"status":"running","environment":{"name":"production","external_url":"https://app.example"},"deployable":{"web_url":"https://git.example/job/9"}},{"id":11,"sha":"another-commit","status":"success"}]`, testSHA)
		default:
			t.Errorf("unexpected path: %s", req.URL.EscapedPath())
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	ep, err := newEndpoint(testEndpoint(server, GitLab))
	if err != nil {
		t.Fatal(err)
	}
	trustTestServer(ep, server)
	r := providerRequest{endpoint: ep, token: "token", userID: "owner"}
	var result Snapshot
	if err := r.gitlab(context.Background(), repository{path: "team/subgroup/repo", sha: testSHA}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Partial || len(result.Warnings) == 0 || len(result.Deployments) != 1 || result.Deployments[0].Status != "running" || result.Runs[0].Status != "manual" || !result.Runs[0].Jobs[0].AllowFailure || result.Runs[0].Jobs[0].URL != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestProviderHTTPGuards(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				hits.Add(1)
				w.Header().Set("Location", "/redirect-target")
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, strings.Repeat("x", (2<<20)+1))
				}
			}))
			defer server.Close()
			ep, err := newEndpoint(testEndpoint(server, GitHub))
			if err != nil {
				t.Fatal(err)
			}
			trustTestServer(ep, server)
			r := providerRequest{endpoint: ep, token: "token", userID: "owner"}
			var result any
			_, err = r.get(context.Background(), "/probe", nil, &result)
			if err == nil || hits.Load() != 1 || strings.Contains(err.Error(), "token") && !errors.Is(err, ErrCredentials) {
				t.Fatalf("unexpected response: %v, hits=%d", err, hits.Load())
			}
			if status == 429 {
				if !errors.Is(err, ErrRateLimited) {
					t.Fatalf("rate limit error: %v", err)
				}
				_, err = r.get(context.Background(), "/probe", nil, &result)
				if !errors.Is(err, ErrRateLimited) || hits.Load() != 1 {
					t.Fatal("provider cooldown bypassed")
				}
			}
		})
	}
}

func TestEndpointPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.100.100.200", "::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "0.0.0.0", "224.0.0.1"} {
		if allowedAddress(netip.MustParseAddr(ip).Unmap(), nil) {
			t.Errorf("allowed private address %s", ip)
		}
	}
	allowed := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
	if !allowedAddress(netip.MustParseAddr("10.1.2.3"), allowed) || allowedAddress(netip.MustParseAddr("10.2.2.3"), allowed) {
		t.Fatal("explicit CIDR scope not enforced")
	}
	for _, api := range []string{"http://git.example/api/v4", "https://user:token@git.example", "file:///etc/passwd", "https://git.example?endpoint=evil"} {
		if _, err := newEndpoint(Endpoint{Provider: GitHub, RemoteHost: "git.example", APIURL: api}); err == nil {
			t.Errorf("accepted unsafe endpoint %s", api)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("private request should not reach server") }))
	defer server.Close()
	cfg := testEndpoint(server, GitHub)
	cfg.AllowedCIDRs = nil
	ep, err := newEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := providerRequest{endpoint: ep}
	var response any
	if _, err := r.get(context.Background(), "/probe", nil, &response); err == nil {
		t.Fatal("private IP was reached without allowlist")
	}
}

func TestRepositoryMetadata(t *testing.T) {
	for _, mode := range []string{"loose", "packed", "detached", "external-gitdir", "external-ref", "include-only"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			checkout(t, dir, "https://ignored:credentials@git.example/team/repo.git")
			switch mode {
			case "packed":
				if err := os.Remove(filepath.Join(dir, ".git/refs/heads/main")); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, dir, ".git/packed-refs", testSHA+" refs/heads/main\n")
			case "detached":
				writeFixture(t, dir, ".git/HEAD", testSHA)
			case "external-gitdir":
				if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(dir, "git-metadata")); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, dir, ".git", "gitdir: /outside/workspace\n")
			case "external-ref":
				writeFixture(t, dir, ".git/HEAD", "ref: refs/../../outside\n")
			case "include-only":
				writeFixture(t, dir, ".git/config", "[include]\n path = /outside/config\n")
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			repo, err := readRepository(context.Background(), root)
			if strings.HasPrefix(mode, "external-") || mode == "include-only" {
				if !errors.Is(err, ErrWorkspace) {
					t.Fatalf("unsafe metadata: %v", err)
				}
				return
			}
			if err != nil || repo.sha != testSHA || repo.host != "git.example" || repo.path != "team/repo" {
				t.Fatalf("unexpected repo: %+v, %v", repo, err)
			}
		})
	}
}

func TestProjectPathEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, projectPath := range []string{"../outside", outside, "escape", "..\\outside"} {
		svc, err := New(&fakeWorkspace{path: dir}, &fakeSecrets{}, DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.GetCI(context.Background(), "session", "owner", projectPath); !errors.Is(err, ErrWorkspace) {
			t.Fatalf("accepted %q: %v", projectPath, err)
		}
	}
}

func TestWorkspacePreconditionsDoNotConsumeProviderAdmission(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  error
	}{
		{
			name: "no remote",
			setup: func(t *testing.T, root string) {
				writeFixture(t, root, ".git/config", "[core]\n repositoryformatversion = 0\n")
				writeFixture(t, root, ".git/HEAD", "ref: refs/heads/main\n")
			},
			want: ErrNoRemote,
		},
		{
			name: "no HEAD",
			setup: func(t *testing.T, root string) {
				writeFixture(t, root, ".git/config", "[remote \"origin\"]\n url = https://git.example/team/repo.git\n")
			},
			want: ErrNoHEAD,
		},
		{
			name: "unsupported metadata",
			setup: func(t *testing.T, root string) {
				checkout(t, root, "file:///tmp/repo")
			},
			want: ErrUnsupportedWorkspace,
		},
		{
			name:  "not a Git workspace",
			setup: func(*testing.T, string) {},
			want:  ErrWorkspace,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := &fakeWorkspace{path: t.TempDir()}
			test.setup(t, workspace.path)
			svc, err := New(workspace, &fakeSecrets{}, DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.GetCI(context.Background(), "session", "owner", ""); !errors.Is(err, test.want) {
				t.Fatalf("workspace precondition: %v", err)
			}
			if !svc.begin("owner") {
				t.Fatal("workspace precondition consumed provider admission")
			}
			svc.finish("owner")
		})
	}
}

func TestRemoteAndURLValidation(t *testing.T) {
	for _, remote := range []string{"git@git.example:team/repo.git", "ssh://git@git.example:2222/team/repo.git", "https://git.example/team/repo.git"} {
		repo, err := parseRemote(remote)
		if err != nil || repo.host != "git.example" || repo.path != "team/repo" {
			t.Fatalf("parse %q: %+v, %v", remote, repo, err)
		}
	}
	for _, remote := range []string{"/tmp/repo", "https://git.example/team/../private", "https://git.example/team%2frepo", "https://git.example/team/repo?token=secret", "file:///tmp/repo"} {
		if _, err := parseRemote(remote); err == nil {
			t.Errorf("unsafe remote accepted: %s", remote)
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "//evil.example", "https://user:secret@git.example/path"} {
		if safeURL(raw) != "" {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestCanceledRequestAndRequestBudget(t *testing.T) {
	ep, err := newEndpoint(DefaultConfig().Endpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	r := providerRequest{endpoint: ep, count: 12}
	var result any
	if _, err := r.get(context.Background(), "/probe", url.Values{}, &result); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("request budget: %v", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r.count = 0
	if _, err := r.get(ctx, "/probe", nil, &result); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
}

func TestAuthenticatedCooldownIsolation(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		if req.Header.Get("Authorization") == "Bearer limited" || req.Header.Get("Authorization") == "" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	ep, err := newEndpoint(testEndpoint(server, GitHub))
	if err != nil {
		t.Fatal(err)
	}
	trustTestServer(ep, server)
	limited := providerRequest{endpoint: ep, userID: "alice", token: "limited"}
	other := providerRequest{endpoint: ep, userID: "bob", token: "other"}
	var result any
	if _, err := limited.get(context.Background(), "/probe", nil, &result); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	if _, err := other.get(context.Background(), "/probe", nil, &result); err != nil {
		t.Fatalf("one user's rate limit affected another: %v", err)
	}
	if _, err := limited.get(context.Background(), "/probe", nil, &result); !errors.Is(err, ErrRateLimited) || hits.Load() != 2 {
		t.Fatal("user cooldown was not retained")
	}
	anonymous := providerRequest{endpoint: ep, userID: "charlie"}
	anonymousOther := providerRequest{endpoint: ep, userID: "dora"}
	if _, err := anonymous.get(context.Background(), "/probe", nil, &result); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	if _, err := anonymousOther.get(context.Background(), "/probe", nil, &result); !errors.Is(err, ErrRateLimited) || hits.Load() != 3 {
		t.Fatal("anonymous shared-IP cooldown missing")
	}
	if _, err := other.get(context.Background(), "/probe", nil, &result); err != nil {
		t.Fatalf("anonymous limit affected authenticated caller: %v", err)
	}
}

func TestSnapshotSizeBound(t *testing.T) {
	snapshot := Snapshot{Repository: "git.example/team/repo", CommitSHA: testSHA}
	for range maxRuns {
		run := Run{Name: strings.Repeat("Ж", 10000), Status: "completed"}
		for range maxJobs {
			run.Jobs = append(run.Jobs, Job{Name: strings.Repeat("\x01", 1000), URL: "https://git.example/" + strings.Repeat("x", 1800), Status: "completed", Conclusion: "failure"})
		}
		snapshot.Runs = append(snapshot.Runs, run)
	}
	for range maxDeployments {
		snapshot.Deployments = append(snapshot.Deployments, Deployment{Environment: strings.Repeat("x", 10000)})
	}
	for range 16 {
		snapshot.Warnings = append(snapshot.Warnings, strings.Repeat("\x01", 256))
	}
	snapshot.bound()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxSnapshotBytes || !snapshot.Partial {
		t.Fatalf("snapshot bound: bytes=%d partial=%v", len(data), snapshot.Partial)
	}
	for _, run := range snapshot.Runs {
		if len(run.Name) > 256 {
			t.Fatal("run field is not bounded")
		}
		for _, job := range run.Jobs {
			if len(job.Name) > 256 {
				t.Fatal("job field is not bounded")
			}
		}
	}
}

func TestAggregateProviderBudget(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		io.WriteString(w, `"`+strings.Repeat("x", 500<<10)+`"`)
	}))
	defer server.Close()
	ep, err := newEndpoint(testEndpoint(server, GitHub))
	if err != nil {
		t.Fatal(err)
	}
	trustTestServer(ep, server)
	r := providerRequest{endpoint: ep}
	var response any
	for range 4 {
		if _, err := r.get(context.Background(), "/probe", nil, &response); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.get(context.Background(), "/probe", nil, &response); !errors.Is(err, ErrUpstream) {
		t.Fatalf("aggregate budget not enforced: %v", err)
	}
	if r.bytesRead > (2<<20)+1 {
		t.Fatalf("aggregate reads unbounded: %d", r.bytesRead)
	}
}

func TestConcurrencyAdmission(t *testing.T) {
	svc, err := New(nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		if !svc.begin(fmt.Sprint(i)) {
			t.Fatal("unexpected admission failure")
		}
	}
	if svc.begin("fifth") {
		t.Fatal("concurrency limit exceeded")
	}
	svc.finish("0")
	if !svc.begin("fifth") {
		t.Fatal("released slot was not reused")
	}
}

func TestEndpointSelectionBeforeVault(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir, "https://attacker.example/team/repo.git")
	secrets := &fakeSecrets{}
	svc, err := New(&fakeWorkspace{path: dir}, secrets, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetCI(context.Background(), "session", "owner", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unknown remote: %v", err)
	}
	if len(secrets.users) != 0 {
		t.Fatal("unknown remote accessed vault")
	}
}

func TestDefaultGitLabRequiresToken(t *testing.T) {
	dir := t.TempDir()
	checkout(t, dir, "git@gitlab.com:team/repo.git")
	svc, err := New(&fakeWorkspace{path: dir}, &fakeSecrets{}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.endpoints["gitlab.com"]; !ok {
		t.Fatal("GitLab.com default is missing")
	}
	if _, err := svc.GetCI(context.Background(), "session", "owner", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("GitLab requested without its vault token: %v", err)
	}
	if !svc.begin("owner") {
		t.Fatal("missing token consumed provider admission")
	}
	svc.finish("owner")
}
