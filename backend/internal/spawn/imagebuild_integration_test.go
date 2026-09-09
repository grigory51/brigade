package spawn

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"github.com/grigory51/brigade/backend/internal/imagebuild"
)

// Интеграционная проверка требует явного выбора базы и Docker endpoint.
func TestImageBuildDockerIntegration(t *testing.T) {
	base := os.Getenv("BRIGADE_TEST_IMAGE_BUILD_BASE")
	if base == "" {
		t.Skip("set BRIGADE_TEST_IMAGE_BUILD_BASE and DOCKER_HOST to run a real build")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	s := &DockerSpawner{cli: cli, baseImage: base}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	request := imagebuild.Request{
		ID: fmt.Sprintf("smoke-%d", time.Now().UnixNano()), UserID: "integration-test",
		Script: "apt-get update\napt-get install -y jq\njq --version\n", NoCache: true,
	}
	result, err := s.Build(ctx, request, func(line string) { t.Log(line) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := s.RemoveImage(cleanup, result.Image); err != nil {
			t.Error(err)
		}
	}()
	if err := s.CheckImage(ctx, result.Image); err != nil {
		t.Fatal(err)
	}
	request.ID += "-cached"
	request.NoCache = false
	cached, err := s.Build(ctx, request, nil)
	if err != nil || cached.Created || cached.Image != result.Image {
		t.Fatalf("cache: %+v, %v", cached, err)
	}
	named := "brigade-build:integration." + request.ID
	if err := s.TagImage(ctx, result.Image, named); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := s.RemoveImage(cleanup, named); err != nil {
			t.Error(err)
		}
	}()
	original, err := s.InspectImage(ctx, result.Image)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := s.InspectImage(ctx, named)
	if err != nil || alias.ID != original.ID {
		t.Fatalf("named image: %+v %v", alias, err)
	}
}
