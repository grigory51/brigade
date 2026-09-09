package agentimage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/errdefs"
	"github.com/grigory51/brigade/backend/internal/imagebuild"
	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
)

type testBuilder func(context.Context, imagebuild.Request, func(string)) (imagebuild.Result, error)

func (f testBuilder) Build(ctx context.Context, r imagebuild.Request, log func(string)) (imagebuild.Result, error) {
	return f(ctx, r, log)
}

type buildDocker struct {
	mu       sync.Mutex
	removed  []string
	checkErr error
	ensure   func(context.Context, string) (bool, error)
	tags     map[string]string
	tagErr   error
}

func (*buildDocker) BaseImage() string { return "base" }
func (d *buildDocker) EnsureImage(ctx context.Context, ref string) (bool, error) {
	if d.ensure != nil {
		return d.ensure(ctx, ref)
	}
	return false, nil
}
func (d *buildDocker) InspectImage(_ context.Context, ref string) (spawn.ImageInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ref = strings.TrimPrefix(ref, "id:")
	if source, ok := d.tags[ref]; ok {
		ref = source
	} else if strings.HasPrefix(ref, "brigade-build:") {
		return spawn.ImageInfo{}, errdefs.NotFound(errors.New("no image"))
	}
	if ref == "base" {
		return spawn.ImageInfo{Size: 100, Layers: []string{"base"}}, nil
	}
	var tags []string
	for tag, source := range d.tags {
		if source == ref {
			tags = append(tags, tag)
		}
	}
	return spawn.ImageInfo{ID: "id:" + ref, Tags: tags, Size: 150, Layers: []string{"base", ref}}, nil
}
func (d *buildDocker) CheckImage(ctx context.Context, ref string) error {
	if d.ensure != nil {
		if _, err := d.ensure(ctx, ref); err != nil {
			return err
		}
	}
	return d.checkErr
}
func (d *buildDocker) TagImage(_ context.Context, source, target string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	source = strings.TrimPrefix(source, "id:")
	if d.tagErr != nil {
		return d.tagErr
	}
	if d.tags == nil {
		d.tags = make(map[string]string)
	}
	if underlying, ok := d.tags[source]; ok {
		source = underlying
	}
	d.tags[target] = source
	return nil
}
func (d *buildDocker) RemoveImage(_ context.Context, ref string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removed = append(d.removed, ref)
	delete(d.tags, ref)
	if id, ok := strings.CutPrefix(ref, "id:"); ok {
		for tag, source := range d.tags {
			if source == id {
				delete(d.tags, tag)
			}
		}
	}
	return nil
}

func buildStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "builds.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.DB().Exec(`INSERT INTO users (id,username,password_hash,created_at) VALUES ('u1','one','',0),('u2','two','',0),('u3','three','',0),('u4','four','',0),('u5','five','',0)`)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func awaitBuild(t *testing.T, s *Builds, user string) store.ImageBuild {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		b, err := s.Get(context.Background(), user)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status != "queued" && b.Status != "building" {
			return b
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("сборка не завершилась")
	return store.ImageBuild{}
}

func TestBuildSuccessSurvivesRequestAndPersists(t *testing.T) {
	st := buildStore(t)
	docker := &buildDocker{}
	images := New(st, docker, 100)
	entered, release := make(chan struct{}), make(chan struct{})
	builder := testBuilder(func(ctx context.Context, r imagebuild.Request, out func(string)) (imagebuild.Result, error) {
		if r.UserID != "u1" || r.Script != "echo hello" || !r.NoCache {
			t.Error("неверный запрос сборщику")
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return imagebuild.Result{}, ctx.Err()
		}
		out(strings.Repeat("x", maxBuildLog+100))
		return imagebuild.Result{Image: "built", Created: true}, nil
	})
	s, err := NewBuilds(st, images, builder, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithCancel(context.Background())
	b, err := s.Start(ctx, "u1", "tools", "echo hello", true)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-entered
	if _, err := s.Start(context.Background(), "u1", "tools", "again", false); !errors.Is(err, ErrBuildBusy) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.SetImages(context.Background(), "u1", nil); !errors.Is(err, ErrBuildBusy) {
		t.Fatalf("set during build: %v", err)
	}
	if _, err := s.Cancel(context.Background(), "u2", b.ID); !errors.Is(err, ErrBuildNotFound) {
		t.Fatalf("cross-user cancel: %v", err)
	}
	other, err := s.Get(context.Background(), "u2")
	if err != nil || other.ID != "" {
		t.Fatalf("cross-user get: %+v %v", other, err)
	}
	close(release)
	got := awaitBuild(t, s, "u1")
	if got.Status != "succeeded" || got.Image != "brigade-build:u1.tools" || got.Name != "tools" || len(got.Log) > maxBuildLog {
		t.Fatalf("result: %+v", got)
	}
	s.Close()
	persisted, err := st.GetImageBuild(context.Background(), "u1")
	if err != nil || persisted.Status != "succeeded" || persisted.Script != "echo hello" {
		t.Fatalf("persistence: %+v %v", persisted, err)
	}
	settings, err := images.List(context.Background(), "u1")
	if err != nil || len(settings.Images) != 1 || settings.UsedBytes != 50 {
		t.Fatalf("images: %+v %v", settings, err)
	}
}

func TestBuildRejectedPreservesImages(t *testing.T) {
	for _, reason := range []string{"script", "quota", "compatibility"} {
		t.Run(reason, func(t *testing.T) {
			st := buildStore(t)
			docker := &buildDocker{}
			quota := int64(1000)
			if reason == "quota" {
				quota = 60
			}
			images := New(st, docker, quota)
			if err := st.SetAgentImages(context.Background(), "u1", []string{"previous"}); err != nil {
				t.Fatal(err)
			}
			if reason == "compatibility" {
				docker.checkErr = errors.New("missing git")
			}
			builder := testBuilder(func(context.Context, imagebuild.Request, func(string)) (imagebuild.Result, error) {
				if reason == "script" {
					return imagebuild.Result{}, errors.New("exit 1")
				}
				return imagebuild.Result{Image: "built", Created: true}, nil
			})
			s, err := NewBuilds(st, images, builder, time.Minute, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Start(context.Background(), "u1", "tools", "echo test", false); err != nil {
				t.Fatal(err)
			}
			got := awaitBuild(t, s, "u1")
			if got.Status != "failed" || got.Error == "" {
				t.Fatalf("result: %+v", got)
			}
			settings, err := images.List(context.Background(), "u1")
			if err != nil || len(settings.Images) != 1 || settings.Images[0].Ref != "previous" {
				t.Fatalf("lost images: %+v %v", settings, err)
			}
			docker.mu.Lock()
			defer docker.mu.Unlock()
			if reason != "script" && (len(docker.removed) != 1 || docker.removed[0] != "built") {
				t.Fatalf("cleanup: %v", docker.removed)
			}
		})
	}
}

func TestBuildQueueCancelTimeoutAndRestart(t *testing.T) {
	st := buildStore(t)
	docker := &buildDocker{}
	images := New(st, docker, 0)
	started := make(chan string, 5)
	builder := testBuilder(func(ctx context.Context, r imagebuild.Request, _ func(string)) (imagebuild.Result, error) {
		started <- r.UserID
		<-ctx.Done()
		return imagebuild.Result{}, ctx.Err()
	})
	s, err := NewBuilds(st, images, builder, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Start(context.Background(), "u1", "tools", "sleep 10", false)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := s.Start(context.Background(), "u2", "tools", "sleep 10", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"u3", "u4"} {
		if _, err := s.Start(context.Background(), user, "tools", "sleep 10", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Start(context.Background(), "u5", "tools", "sleep 10", false); !errors.Is(err, ErrBuildQueueFull) {
		t.Fatalf("queue: %v", err)
	}
	select {
	case <-started:
		t.Fatal("parallel limit exceeded")
	default:
	}
	if _, err := s.Cancel(context.Background(), "u2", second.ID); err != nil {
		t.Fatal(err)
	}
	if got := awaitBuild(t, s, "u2"); got.Status != "cancelled" {
		t.Fatalf("queued cancel: %+v", got)
	}
	if _, err := s.Cancel(context.Background(), "u1", first.ID); err != nil {
		t.Fatal(err)
	}
	if got := awaitBuild(t, s, "u1"); got.Status != "cancelled" {
		t.Fatalf("running cancel: %+v", got)
	}
	s.Close()
	if err := st.StartImageBuild(context.Background(), "u1", store.ImageBuild{ID: "abandoned", Script: "echo retained", Status: "building"}); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewBuilds(st, images, builder, 20*time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, err := restarted.Get(context.Background(), "u1")
	if err != nil || got.Status != "interrupted" || got.Script != "echo retained" {
		t.Fatalf("restart: %+v %v", got, err)
	}
	if _, err := restarted.Start(context.Background(), "u1", "tools", "sleep 10", false); err != nil {
		t.Fatal(err)
	}
	if got := awaitBuild(t, restarted, "u1"); got.Status != "failed" || !strings.Contains(got.Error, "время") {
		t.Fatalf("timeout: %+v", got)
	}
}

func TestBuildValidationAndUnavailable(t *testing.T) {
	st := buildStore(t)
	s, err := NewBuilds(st, New(st, nil, 0), nil, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Start(context.Background(), "u1", "tools", "echo test", false); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	s.builder = testBuilder(func(context.Context, imagebuild.Request, func(string)) (imagebuild.Result, error) {
		t.Fatal("builder called")
		return imagebuild.Result{}, nil
	})
	for _, script := range []string{"", " \n", "a\x00b", strings.Repeat("a", maxBuildScript+1)} {
		if _, err := s.Start(context.Background(), "u1", "tools", script, false); err == nil {
			t.Fatal("invalid script accepted")
		}
	}
}

func TestBuildRetainsResultWhenFinalPersistenceFails(t *testing.T) {
	st := buildStore(t)
	_, err := st.DB().Exec(`CREATE TRIGGER fail_build_result BEFORE UPDATE ON image_builds WHEN NEW.status = 'succeeded' BEGIN SELECT RAISE(FAIL,'temporarily unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	builder := testBuilder(func(context.Context, imagebuild.Request, func(string)) (imagebuild.Result, error) {
		return imagebuild.Result{Image: "built", Created: true}, nil
	})
	s, err := NewBuilds(st, New(st, &buildDocker{}, 100), builder, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Start(context.Background(), "u1", "tools", "echo test", false); err != nil {
		t.Fatal(err)
	}
	got := awaitBuild(t, s, "u1")
	if got.Status != "succeeded" || got.Image != "brigade-build:u1.tools" {
		t.Fatalf("lost result: %+v", got)
	}
	if _, err = st.DB().Exec(`DROP TRIGGER fail_build_result`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetImageBuild(context.Background(), "u1")
	if err != nil || got.Status != "succeeded" {
		t.Fatalf("persistence retry: %+v %v", got, err)
	}
}

func TestImagePullDoesNotBlockOtherUsersBuildControls(t *testing.T) {
	st := buildStore(t)
	pulling, release := make(chan struct{}), make(chan struct{})
	docker := &buildDocker{ensure: func(ctx context.Context, ref string) (bool, error) {
		if ref == "slow" {
			close(pulling)
			select {
			case <-release:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return false, nil
	}}
	builder := testBuilder(func(ctx context.Context, _ imagebuild.Request, _ func(string)) (imagebuild.Result, error) {
		<-ctx.Done()
		return imagebuild.Result{}, ctx.Err()
	})
	s, err := NewBuilds(st, New(st, docker, 100), builder, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pulled := make(chan error, 1)
	go func() { _, err := s.images.publishBuilt(context.Background(), "u1", "tools", "slow"); pulled <- err }()
	<-pulling
	defer func() {
		close(release)
		if err := <-pulled; err != nil {
			t.Error(err)
		}
	}()
	done := make(chan error, 1)
	go func() {
		b, err := s.Start(context.Background(), "u2", "tools", "echo test", false)
		if err == nil {
			_, err = s.Get(context.Background(), "u2")
		}
		if err == nil {
			_, err = s.Cancel(context.Background(), "u2", b.ID)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pull заблокировал управление сборкой другого пользователя")
	}
}

func TestDatabaseWaitDoesNotHoldBuildLocks(t *testing.T) {
	st := buildStore(t)
	builder := testBuilder(func(ctx context.Context, _ imagebuild.Request, _ func(string)) (imagebuild.Result, error) {
		<-ctx.Done()
		return imagebuild.Result{}, ctx.Err()
	})
	s, err := NewBuilds(st, New(st, &buildDocker{}, 100), builder, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	record := store.ImageBuild{ID: "blocked", Script: "sleep", Status: "queued"}
	if err := st.StartImageBuild(context.Background(), "u1", record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	job := &buildJob{record: record, cancel: cancel}
	s.jobs["u1"] = job
	s.wg.Add(1)
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := st.DB().Stats().WaitCount
	go s.run(ctx, "u1", job, false)
	for deadline := time.Now().Add(time.Second); st.DB().Stats().WaitCount == before; {
		if time.Now().After(deadline) {
			t.Fatal("worker did not reach database")
		}
		time.Sleep(time.Millisecond)
	}
	startCtx, stopStart := context.WithTimeout(context.Background(), time.Second)
	defer stopStart()
	second := make(chan error, 1)
	go func() { _, err := s.Start(startCtx, "u2", "tools", "sleep", false); second <- err }()
	for deadline := time.Now().Add(time.Second); st.DB().Stats().WaitCount < before+2; {
		if time.Now().After(deadline) {
			t.Fatal("start did not reach database")
		}
		time.Sleep(time.Millisecond)
	}
	controlCtx, stopControls := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopControls()
	done := make(chan error, 1)
	go func() {
		if _, err := s.Start(controlCtx, "u1", "tools", "duplicate", false); !errors.Is(err, ErrBuildBusy) {
			done <- err
			return
		}
		_, err := s.Get(controlCtx, "u1")
		if err == nil {
			_, err = s.Cancel(controlCtx, "u1", record.ID)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DB wait blocked build controls")
	}
	_ = conn.Close()
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
