package agentimage

import (
	"context"
	"errors"
	"testing"
)

func TestSetOnlyAcceptsAlreadyRegisteredImages(t *testing.T) {
	st := buildStore(t)
	ctx := context.Background()
	if err := st.SetAgentImages(ctx, "u1", []string{"legacy:image"}); err != nil {
		t.Fatal(err)
	}
	images := New(st, &buildDocker{ensure: func(context.Context, string) (bool, error) {
		t.Fatal("client-supplied image must not reach Docker")
		return false, nil
	}}, 100)
	for _, ref := range []string{"ghcr.io/example/agent:v1", "brigade-build:someone-else", "legacy:image"} {
		if _, err := images.Set(ctx, "u2", []string{ref}); err == nil {
			t.Fatalf("accepted unregistered image %q", ref)
		}
	}
	if _, err := images.Set(ctx, "u1", []string{"legacy:image", "new:image"}); err == nil {
		t.Fatal("accepted an extra image")
	}
	got, err := images.Set(ctx, "u1", []string{"legacy:image"})
	if err != nil || len(got.Images) != 1 {
		t.Fatalf("existing image: %+v %v", got, err)
	}
	got, err = images.Set(ctx, "u1", nil)
	if err != nil || len(got.Images) != 0 {
		t.Fatalf("remove legacy image: %+v %v", got, err)
	}
}

func TestPublishNamedImageReplacementAndIsolation(t *testing.T) {
	st := buildStore(t)
	docker := &buildDocker{}
	images := New(st, docker, 50)
	ctx := context.Background()
	for _, build := range []struct{ user, source string }{{"u1", "first"}, {"u2", "other"}, {"u1", "second"}} {
		ref, err := images.publishBuilt(ctx, build.user, "python-tools", build.source)
		if err != nil || ref != "brigade-build:"+build.user+".python-tools" {
			t.Fatalf("publish: %q %v", ref, err)
		}
		settings, err := images.List(ctx, build.user)
		if err != nil || len(settings.Images) != 1 || settings.UsedBytes != 50 {
			t.Fatalf("replacement charged both versions: %+v %v", settings, err)
		}
		if DisplayName(ref) != "python-tools" {
			t.Fatal("namespace leaked into display name")
		}
	}
	if docker.tags["brigade-build:u1.python-tools"] != "second" || docker.tags["brigade-build:u2.python-tools"] != "other" {
		t.Fatalf("cross-user overwrite: %v", docker.tags)
	}
	for _, failure := range []string{"quota", "compatibility", "tag", "database"} {
		t.Run(failure, func(t *testing.T) {
			images.quota = 50
			docker.checkErr, docker.tagErr = nil, nil
			switch failure {
			case "quota":
				images.quota = 1
			case "compatibility":
				docker.checkErr = errors.New("incompatible")
			case "tag":
				docker.tagErr = errors.New("tag failed")
			case "database":
				if _, err := st.DB().Exec(`CREATE TRIGGER fail_images BEFORE UPDATE ON user_settings BEGIN SELECT RAISE(FAIL,'storage unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := images.publishBuilt(ctx, "u1", "python-tools", "bad"); err == nil {
				t.Fatal("expected failure")
			}
			if docker.tags["brigade-build:u1.python-tools"] != "second" {
				t.Fatal("failed rebuild replaced working image")
			}
		})
	}
}

func TestImageNameValidationAndLegacyDisplay(t *testing.T) {
	for _, name := range []string{"", "../escape", "UPPER", "a:b", "white space", "-tools"} {
		if _, err := imageRef("u1", name); err == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	for _, ref := range []string{"brigade-build:32e0c842-81d0-48bb-ad55-987796247789", "ghcr.io/example/agent:v1"} {
		if DisplayName(ref) != ref {
			t.Fatalf("changed legacy reference %q", ref)
		}
	}
}

func TestPublishCachedImageUnderAnotherNameCountsLayersOnce(t *testing.T) {
	st := buildStore(t)
	docker := &buildDocker{}
	images := New(st, docker, 60)
	ctx := context.Background()
	first, err := images.publishBuilt(ctx, "u1", "first", "built")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := images.publishBuilt(ctx, "u1", "second", first); err != nil {
		t.Fatal(err)
	}
	settings, err := images.List(ctx, "u1")
	if err != nil || settings.UsedBytes != 50 || len(settings.Images) != 2 {
		t.Fatalf("alias counted twice: %+v %v", settings, err)
	}
	images.quota = 100
	if _, err := images.publishBuilt(ctx, "u1", "first", "rebuilt"); err != nil {
		t.Fatal(err)
	}
	if docker.tags["brigade-build:u1.second"] != "built" {
		t.Fatal("cleanup removed another named image")
	}
}
