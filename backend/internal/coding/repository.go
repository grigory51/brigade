package coding

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var shaPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)
var pathPartPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

type repository struct{ host, path, sha string }

// Git is used only as a config parser over bounded stdin. It never opens the
// checkout, follows includes, executes hooks, resolves remotes or receives tokens.
func readRepository(ctx context.Context, root *os.Root) (repository, error) {
	gitDir := ".git"
	info, err := root.Stat(gitDir)
	if err != nil {
		return repository{}, ErrWorkspace
	}
	if !info.IsDir() {
		raw, err := readMetadata(root, gitDir)
		if err != nil {
			return repository{}, workspaceError(ErrUnsupportedWorkspace)
		}
		value, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
		if !ok || !filepath.IsLocal(value) {
			return repository{}, workspaceError(ErrUnsupportedWorkspace)
		}
		gitDir = filepath.Clean(value)
	}
	commonDir := gitDir
	if raw, err := readMetadata(root, filepath.Join(gitDir, "commondir")); err == nil {
		value := strings.TrimSpace(string(raw))
		if filepath.IsAbs(value) {
			return repository{}, workspaceError(ErrUnsupportedWorkspace)
		}
		commonDir = filepath.Join(gitDir, value)
		if !filepath.IsLocal(commonDir) {
			return repository{}, workspaceError(ErrUnsupportedWorkspace)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return repository{}, workspaceError(ErrUnsupportedWorkspace)
	}
	config, err := readMetadata(root, filepath.Join(commonDir, "config"))
	if errors.Is(err, os.ErrNotExist) {
		return repository{}, workspaceError(ErrNoRemote)
	}
	if err != nil {
		return repository{}, workspaceError(ErrUnsupportedWorkspace)
	}
	parseCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(parseCtx, "git", "config", "--no-includes", "--file", "-", "--null", "--get", "remote.origin.url")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(string(config))
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return repository{}, workspaceError(ErrNoRemote)
		}
		return repository{}, workspaceError(ErrUnsupportedWorkspace)
	}
	repo, err := parseRemote(strings.TrimSuffix(string(output), "\x00"))
	if err != nil {
		return repository{}, workspaceError(ErrUnsupportedWorkspace)
	}
	ref := "HEAD"
	for range 8 {
		dir := commonDir
		if ref == "HEAD" {
			dir = gitDir
		}
		raw, err := readMetadata(root, filepath.Join(dir, ref))
		if errors.Is(err, os.ErrNotExist) && ref == "HEAD" {
			return repository{}, workspaceError(ErrNoHEAD)
		}
		if errors.Is(err, os.ErrNotExist) && ref != "HEAD" {
			packed, packedErr := readMetadata(root, filepath.Join(commonDir, "packed-refs"))
			if errors.Is(packedErr, os.ErrNotExist) {
				return repository{}, workspaceError(ErrNoHEAD)
			}
			if packedErr != nil {
				return repository{}, workspaceError(ErrUnsupportedWorkspace)
			}
			for _, line := range strings.Split(string(packed), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[1] == ref && shaPattern.MatchString(fields[0]) {
					repo.sha = strings.ToLower(fields[0])
					return repo, nil
				}
			}
			return repository{}, workspaceError(ErrNoHEAD)
		}
		if err != nil {
			return repository{}, workspaceError(ErrUnsupportedWorkspace)
		}
		value := strings.TrimSpace(string(raw))
		if shaPattern.MatchString(value) {
			repo.sha = strings.ToLower(value)
			return repo, nil
		}
		next, ok := strings.CutPrefix(value, "ref: ")
		if !ok || !strings.HasPrefix(next, "refs/") || !filepath.IsLocal(next) || filepath.ToSlash(filepath.Clean(next)) != next {
			return repository{}, workspaceError(ErrUnsupportedWorkspace)
		}
		ref = next
	}
	return repository{}, workspaceError(ErrUnsupportedWorkspace)
}

func readMetadata(root *os.Root, name string) ([]byte, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, ErrWorkspace
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, ErrWorkspace
	}
	return data, nil
}

func parseRemote(raw string) (repository, error) {
	if raw == "" || len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return repository{}, ErrWorkspace
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return repository{}, ErrWorkspace
		}
		switch u.Scheme {
		case "https", "http", "ssh":
		default:
			return repository{}, ErrWorkspace
		}
		// Credentials from remotes are never used or returned; authentication comes from vault.
		host, path = u.Hostname(), strings.TrimPrefix(u.Path, "/")
	} else {
		left, right, ok := strings.Cut(raw, ":")
		if !ok {
			return repository{}, ErrWorkspace
		}
		if _, after, ok := strings.Cut(left, "@"); ok {
			left = after
		}
		host, path = left, right
	}
	host = strings.ToLower(host)
	path = strings.TrimSuffix(path, ".git")
	if host == "" || strings.ContainsAny(host, "/@?#% ") || len(strings.Split(path, "/")) < 2 {
		return repository{}, ErrWorkspace
	}
	for _, part := range strings.Split(path, "/") {
		if !pathPartPattern.MatchString(part) || part == "." || part == ".." {
			return repository{}, ErrWorkspace
		}
	}
	return repository{host: host, path: path}, nil
}
