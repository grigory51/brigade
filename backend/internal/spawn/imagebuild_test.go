package spawn

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/grigory51/brigade/backend/internal/imagebuild"
)

type imageBuildAPI struct {
	mu             sync.Mutex
	base           image.InspectResponse
	baseAvailable  bool
	images         map[string]image.InspectResponse
	labels         map[string]map[string]string
	extraImages    []image.Summary
	listQueries    []url.Values
	requests       []string
	buildQueries   []url.Values
	archives       []map[string]string
	removed        []string
	stream         string
	buildStatus    int
	cacheStatus    int
	targetStatus   int
	removeStatus   int
	baseStatus     int
	pullStatus     int
	skipBuiltImage bool
	waitForCancel  bool
	cancelled      chan struct{}
}

func newImageBuildAPI(t *testing.T) (*DockerSpawner, *imageBuildAPI) {
	t.Helper()
	api := &imageBuildAPI{
		base: image.InspectResponse{
			ID:           "sha256:" + strings.Repeat("a", 64),
			Os:           "linux",
			Architecture: "arm64",
			Variant:      "v8",
		},
		baseAvailable: true,
		images:        make(map[string]image.InspectResponse),
		labels:        make(map[string]map[string]string),
		stream:        "{\"stream\":\"setup output\\n\"}\n{\"status\":\"done\"}\n{\"aux\":{\"ID\":\"built\"}}\n",
		cancelled:     make(chan struct{}),
	}
	server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"), client.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &DockerSpawner{cli: cli, baseImage: "brigade/agent:test"}, api
}

func (api *imageBuildAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.51")
	api.requests = append(api.requests, r.Method+" "+path)
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int) {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "{\"message\":\"fake Docker failure %d\"}", status)
	}
	switch {
	case r.Method == http.MethodGet && path == "/images/json":
		api.listQueries = append(api.listQueries, r.URL.Query())
		if api.cacheStatus != 0 {
			fail(api.cacheStatus)
			return
		}
		args, err := filters.FromJSON(r.URL.Query().Get("filters"))
		if err != nil || len(args.Get("label")) != 1 {
			fail(http.StatusBadRequest)
			return
		}
		key := strings.TrimPrefix(args.Get("label")[0], imageBuildKeyLabel+"=")
		images := append([]image.Summary{}, api.extraImages...)
		for ref, info := range api.images {
			if api.labels[ref][imageBuildKeyLabel] == key {
				images = append(images, image.Summary{ID: info.ID, RepoTags: []string{ref}, Labels: api.labels[ref]})
			}
		}
		_ = json.NewEncoder(w).Encode(images)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		if ref == "brigade/agent:test" {
			if api.baseStatus != 0 {
				fail(api.baseStatus)
			} else if !api.baseAvailable {
				fail(http.StatusNotFound)
			} else {
				_ = json.NewEncoder(w).Encode(api.base)
			}
			return
		}
		if strings.HasPrefix(ref, "brigade-build:") && api.targetStatus != 0 {
			fail(api.targetStatus)
			return
		}
		if info, ok := api.images[ref]; ok {
			_ = json.NewEncoder(w).Encode(info)
		} else {
			fail(http.StatusNotFound)
		}
	case r.Method == http.MethodPost && path == "/images/create":
		if api.pullStatus != 0 {
			fail(api.pullStatus)
			return
		}
		api.baseAvailable = true
		_, _ = io.WriteString(w, "{\"status\":\"pulled\"}\n")
	case r.Method == http.MethodPost && path == "/build":
		api.buildQueries = append(api.buildQueries, r.URL.Query())
		files := make(map[string]string)
		reader := tar.NewReader(r.Body)
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil || header.Typeflag != tar.TypeReg || header.Mode != 0600 {
				fail(http.StatusBadRequest)
				return
			}
			content, err := io.ReadAll(reader)
			if err != nil {
				fail(http.StatusBadRequest)
				return
			}
			files[header.Name] = string(content)
		}
		api.archives = append(api.archives, files)
		if api.buildStatus != 0 {
			fail(api.buildStatus)
			return
		}
		if !api.skipBuiltImage {
			ref := r.URL.Query().Get("t")
			api.images[ref] = image.InspectResponse{ID: "sha256:" + strings.Repeat("b", 64)}
			var labels map[string]string
			if err := json.Unmarshal([]byte(r.URL.Query().Get("labels")), &labels); err != nil {
				fail(http.StatusBadRequest)
				return
			}
			api.labels[ref] = labels
		}
		_, _ = io.WriteString(w, api.stream)
		if api.waitForCancel {
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				close(api.cancelled)
			case <-time.After(5 * time.Second):
			}
		}
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/images/"):
		ref := strings.TrimPrefix(path, "/images/")
		api.removed = append(api.removed, ref)
		if !strings.HasPrefix(ref, "brigade-build:") || r.URL.Query().Get("force") == "1" || r.URL.Query().Get("noprune") != "1" {
			fail(http.StatusBadRequest)
			return
		}
		if api.removeStatus != 0 {
			fail(api.removeStatus)
			return
		}
		delete(api.images, ref)
		delete(api.labels, ref)
		_, _ = io.WriteString(w, "[]")
	default:
		fail(http.StatusNotFound)
	}
}

func TestImageBuildContextAndReuse(t *testing.T) {
	s, api := newImageBuildAPI(t)
	request := imagebuild.Request{ID: "first", UserID: "user", Script: "echo '$SECRET'\n# FROM malicious\n"}
	var logs []string
	result, err := s.Build(context.Background(), request, func(line string) { logs = append(logs, line) })
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Image != "brigade-build:first" {
		t.Fatalf("unexpected result: %+v", result)
	}
	request.ID = "second"
	reused, err := s.Build(context.Background(), request, nil)
	if err != nil || reused.Created || reused.Image != result.Image {
		t.Fatalf("reuse = %+v, %v", reused, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.buildQueries) != 1 || len(api.archives) != 1 {
		t.Fatalf("expected one build, got %d", len(api.buildQueries))
	}
	query := api.buildQueries[0]
	for key, want := range map[string]string{"dockerfile": "Dockerfile", "platform": "linux/arm64/v8", "forcerm": "1"} {
		if query.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, query.Get(key), want)
		}
	}
	// The SDK omits rm=true and an unset version; the daemon defaults to removal and BuilderV1.
	for _, key := range []string{"rm", "version"} {
		if query.Has(key) {
			t.Errorf("%s = %q, want omitted daemon default", key, query.Get(key))
		}
	}
	for _, key := range []string{"nocache", "pull", "remote", "session", "securityopt", "extrahosts", "buildargs"} {
		if query.Get(key) != "" && query.Get(key) != "0" && query.Get(key) != "null" {
			t.Errorf("unexpected build option %s=%s", key, query.Get(key))
		}
	}
	wantDockerfile := "FROM " + api.base.ID + "\nUSER root\nCOPY setup.sh /tmp/brigade-setup.sh\nRUN [\"bash\", \"-eu\", \"/tmp/brigade-setup.sh\"]\nUSER 1001:1001\n"
	if want := map[string]string{"Dockerfile": wantDockerfile, "setup.sh": request.Script}; !reflect.DeepEqual(api.archives[0], want) {
		t.Fatalf("build context = %#v, want %#v", api.archives[0], want)
	}
	baseLog := "Базовый образ: brigade/agent:test (" + api.base.ID + ", linux/arm64/v8)\n"
	if !reflect.DeepEqual(logs, []string{baseLog, "setup output\n", "done\n"}) {
		t.Fatalf("logs = %#v", logs)
	}
	if len(api.removed) != 0 || query.Get("t") != result.Image || len(api.images) != 1 {
		t.Fatalf("cleanup: removed=%v, images=%v", api.removed, api.images)
	}
	labels := api.labels[result.Image]
	createdAt, err := strconv.ParseInt(labels[imageBuildCreatedAtLabel], 10, 64)
	if err != nil || createdAt <= 0 || len(labels[imageBuildKeyLabel]) != 64 {
		t.Fatalf("build labels = %v", labels)
	}
}

func TestImageBuildCacheKey(t *testing.T) {
	for _, change := range []string{"base", "os", "architecture", "variant", "script", "user"} {
		t.Run(change, func(t *testing.T) {
			s, api := newImageBuildAPI(t)
			request := imagebuild.Request{ID: "first", UserID: "user", Script: "echo test"}
			first, err := s.Build(context.Background(), request, nil)
			if err != nil {
				t.Fatal(err)
			}
			api.mu.Lock()
			switch change {
			case "base":
				api.base.ID = "sha256:" + strings.Repeat("c", 64)
			case "os":
				api.base.Os = "other"
			case "architecture":
				api.base.Architecture = "amd64"
			case "variant":
				api.base.Variant = ""
			case "script":
				request.Script += "\n"
			case "user":
				request.UserID = "other"
			}
			api.mu.Unlock()
			request.ID = "second"
			second, err := s.Build(context.Background(), request, nil)
			if err != nil || !second.Created || second.Image == first.Image {
				t.Fatalf("first=%+v, second=%+v, err=%v", first, second, err)
			}
		})
	}
}

func TestImageBuildNoCache(t *testing.T) {
	s, api := newImageBuildAPI(t)
	request := imagebuild.Request{ID: "cached", UserID: "user", Script: "echo test"}
	cached, err := s.Build(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.NoCache = true
	request.ID = ""
	if _, err := s.Build(context.Background(), request, nil); err == nil {
		t.Fatal("NoCache without an ID succeeded")
	}
	request.ID = "first"
	first, err := s.Build(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = "second"
	second, err := s.Build(context.Background(), request, nil)
	if err != nil || !first.Created || !second.Created || first.Image == second.Image || first.Image == cached.Image || second.Image == cached.Image {
		t.Fatalf("cached=%+v, first=%+v, second=%+v, err=%v", cached, first, second, err)
	}
	if _, err := s.Build(context.Background(), request, nil); err == nil {
		t.Fatal("repeated NoCache ID overwrote an existing image")
	}
	request.NoCache = false
	request.ID = "normal-after-nocache"
	reused, err := s.Build(context.Background(), request, nil)
	if err != nil || reused.Created || reused.Image != second.Image {
		t.Fatalf("normal build after NoCache = %+v, %v; want %s", reused, err, second.Image)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.images) != 3 || len(api.buildQueries) != 3 {
		t.Fatalf("images=%v, builds=%d", api.images, len(api.buildQueries))
	}
	if len(api.listQueries) != 2 {
		t.Fatalf("NoCache queried the cache: %v", api.listQueries)
	}
	for _, ref := range []string{first.Image, second.Image} {
		if api.labels[ref][imageBuildKeyLabel] != api.labels[cached.Image][imageBuildKeyLabel] {
			t.Fatalf("NoCache changed the cache key: %v", api.labels)
		}
	}
	for _, query := range api.buildQueries[1:] {
		if query.Get("nocache") != "1" {
			t.Fatalf("NoCache build options = %v", query)
		}
	}
}

func TestImageBuildErrorsAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		stream    string
		configure func(*imageBuildAPI)
		want      string
	}{
		{name: "error", stream: "{\"error\":\"script failed\"}\n{\"status\":\"tail\"}\n", want: "script failed"},
		{name: "detail", stream: "{\"errorDetail\":{\"message\":\"detail failed\",\"code\":1}}\n{\"status\":\"tail\"}\n", want: "detail failed"},
		{name: "duplicate", stream: "{\"error\":\"failed\",\"errorDetail\":{\"message\":\"failed\"}}\n{\"status\":\"tail\"}\n", want: "failed"},
		{name: "malformed", stream: "{\"stream\":\"output\"}\ninvalid\n", want: "decode Docker stream"},
		{name: "truncated", stream: "{\"stream\":", want: "unexpected EOF"},
		{name: "http", configure: func(api *imageBuildAPI) { api.buildStatus = 500 }, want: "Docker API"},
		{name: "missing image", configure: func(api *imageBuildAPI) { api.skipBuiltImage = true; api.stream = "" }, want: "inspect built image"},
		{name: "cleanup", stream: "{\"error\":\"script failed\"}\n", configure: func(api *imageBuildAPI) { api.removeStatus = 500 }, want: "remove failed image"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, api := newImageBuildAPI(t)
			if test.stream != "" {
				api.stream = test.stream
			}
			if test.configure != nil {
				test.configure(api)
			}
			api.images["brigade-build:existing"] = api.base
			var logs []string
			result, err := s.Build(context.Background(), imagebuild.Request{ID: "failed", UserID: "user", Script: "false"}, func(line string) { logs = append(logs, line) })
			if err == nil || !strings.Contains(err.Error(), test.want) || result != (imagebuild.Result{}) {
				t.Fatalf("result=%+v, error=%v, want %q", result, err, test.want)
			}
			if strings.Contains(test.stream, "tail") && (len(logs) == 0 || logs[len(logs)-1] != "tail\n") {
				t.Fatalf("stream was not read through the error: %v", logs)
			}
			if test.name == "duplicate" && !reflect.DeepEqual(logs[1:], []string{"failed\n", "tail\n"}) {
				t.Fatalf("duplicate error logs: %v", logs)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.removed) != 1 || api.removed[0] != api.buildQueries[0].Get("t") {
				t.Fatalf("removed=%v, builds=%v", api.removed, api.buildQueries)
			}
			if _, exists := api.images["brigade-build:existing"]; !exists {
				t.Fatal("existing image was removed")
			}
			if api.removeStatus == 0 && len(api.images) != 1 {
				t.Fatalf("failed image leaked: %v", api.images)
			}
			for _, request := range api.requests {
				if strings.Contains(request, "/prune") || strings.HasSuffix(request, "/tag") {
					t.Fatalf("unexpected request: %s", request)
				}
			}
		})
	}
}

func TestImageBuildBaseAndCacheFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*imageBuildAPI)
		want      string
	}{
		{"base inspect", func(api *imageBuildAPI) { api.baseStatus = 500 }, "ensure base"},
		{"base pull", func(api *imageBuildAPI) { api.baseAvailable = false; api.pullStatus = 500 }, "ensure base"},
		{"base ID", func(api *imageBuildAPI) { api.base.ID = "invalid\nRUN false" }, "invalid base image ID"},
		{"base OS", func(api *imageBuildAPI) { api.base.Os = "" }, "platform is missing"},
		{"base architecture", func(api *imageBuildAPI) { api.base.Architecture = "" }, "platform is missing"},
		{"cache list", func(api *imageBuildAPI) { api.cacheStatus = 500 }, "list cached images"},
		{"target inspect", func(api *imageBuildAPI) { api.targetStatus = 500 }, "inspect target image"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, api := newImageBuildAPI(t)
			test.configure(api)
			_, err := s.Build(context.Background(), imagebuild.Request{ID: "failed", UserID: "user"}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.buildQueries) != 0 || len(api.removed) != 0 {
				t.Fatalf("unexpected build or cleanup: %v", api.requests)
			}
		})
	}
}

func TestImageBuildPullsBase(t *testing.T) {
	s, api := newImageBuildAPI(t)
	api.baseAvailable = false
	if _, err := s.Build(context.Background(), imagebuild.Request{ID: "pulled", UserID: "user"}, nil); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	want := []string{"GET /images/brigade/agent:test/json", "POST /images/create", "GET /images/brigade/agent:test/json"}
	if len(api.requests) < len(want) || !reflect.DeepEqual(api.requests[:len(want)], want) {
		t.Fatalf("request order = %v", api.requests)
	}
}

func TestImageBuildCancellation(t *testing.T) {
	s, api := newImageBuildAPI(t)
	api.waitForCancel = true
	api.stream = "{\"stream\":\"running\"}\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := s.Build(ctx, imagebuild.Request{ID: "cancelled", UserID: "user"}, func(line string) {
		if line == "running" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || result != (imagebuild.Result{}) {
		t.Fatalf("result=%+v, error=%v", result, err)
	}
	select {
	case <-api.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Docker build request was not cancelled")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.removed) != 1 || len(api.images) != 0 {
		t.Fatalf("cancelled build was not cleaned up: removed=%v, images=%v", api.removed, api.images)
	}
}

func TestImageBuildSelectsNewestUsableCacheEntry(t *testing.T) {
	s, api := newImageBuildAPI(t)
	request := imagebuild.Request{ID: "original", UserID: "user", Script: "echo test"}
	original, err := s.Build(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	key := api.labels[original.Image][imageBuildKeyLabel]
	api.labels[original.Image][imageBuildCreatedAtLabel] = "1"
	api.extraImages = []image.Summary{
		{RepoTags: []string{"brigade-build:older"}, Labels: map[string]string{imageBuildKeyLabel: key, imageBuildCreatedAtLabel: "10"}},
		{RepoTags: []string{"other:tag", "brigade-build:newest"}, Labels: map[string]string{imageBuildKeyLabel: key, imageBuildCreatedAtLabel: "30"}},
		{RepoTags: []string{"brigade-build:middle"}, Labels: map[string]string{imageBuildKeyLabel: key, imageBuildCreatedAtLabel: "20"}},
		{RepoTags: []string{"brigade-build:wrong-key"}, Labels: map[string]string{imageBuildKeyLabel: "other", imageBuildCreatedAtLabel: "100"}},
		{RepoTags: []string{"brigade-build-tmp:unpublished", "other:tag"}, Labels: map[string]string{imageBuildKeyLabel: key, imageBuildCreatedAtLabel: "100"}},
		{RepoTags: []string{"<none>:<none>"}, Labels: map[string]string{imageBuildKeyLabel: key, imageBuildCreatedAtLabel: "100"}},
		{RepoTags: []string{"brigade-build:invalid-time"}, Labels: map[string]string{imageBuildKeyLabel: key, imageBuildCreatedAtLabel: "invalid"}},
		{RepoTags: []string{"brigade-build:missing-time"}, Labels: map[string]string{imageBuildKeyLabel: key}},
	}
	api.mu.Unlock()
	request.ID = "reuse"
	result, err := s.Build(context.Background(), request, nil)
	if err != nil || result.Created || result.Image != "brigade-build:newest" {
		t.Fatalf("cache selection = %+v, %v", result, err)
	}
}

func TestImageBuildRejectedImageIsNotReused(t *testing.T) {
	s, api := newImageBuildAPI(t)
	request := imagebuild.Request{ID: "rejected", UserID: "user", Script: "echo test"}
	rejected, err := s.Build(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveImage(context.Background(), rejected.Image); err != nil {
		t.Fatal(err)
	}
	request.ID = "retry"
	retry, err := s.Build(context.Background(), request, nil)
	if err != nil || !retry.Created || retry.Image != "brigade-build:retry" {
		t.Fatalf("build after rejection = %+v, %v", retry, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.images) != 1 || len(api.labels) != 1 {
		t.Fatalf("rejected image or alias leaked: images=%v, labels=%v", api.images, api.labels)
	}
}

func TestImageBuildRetainsOnlyFirstOutputError(t *testing.T) {
	s, api := newImageBuildAPI(t)
	api.stream = "{\"error\":\"first error\"}\n" + strings.Repeat("{\"error\":\"later error\"}\n", 10000) + "{\"status\":\"tail\"}\n"
	count := 0
	last := ""
	_, err := s.Build(context.Background(), imagebuild.Request{ID: "errors", UserID: "user"}, func(line string) {
		count++
		last = line
	})
	if err == nil || err.Error() != "spawn: image build: first error" {
		t.Fatalf("unbounded or missing output error: %v", err)
	}
	if count != 10003 || last != "tail\n" {
		t.Fatalf("logs were not delivered synchronously and completely: count=%d, last=%q", count, last)
	}
}

func TestImageBuildRejectsInvalidRequestID(t *testing.T) {
	for _, id := range []string{"", "with/slash", "with:colon", "with space", "with\nnewline", strings.Repeat("a", 129)} {
		t.Run(id, func(t *testing.T) {
			s, api := newImageBuildAPI(t)
			if _, err := s.Build(context.Background(), imagebuild.Request{ID: id}, nil); err == nil {
				t.Fatal("invalid request ID was accepted")
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.requests) != 0 {
				t.Fatalf("invalid ID caused Docker requests: %v", api.requests)
			}
		})
	}
}
