package spawn

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/opencontainers/go-digest"

	"github.com/grigory51/brigade/backend/internal/imagebuild"
)

var _ imagebuild.Builder = (*DockerSpawner)(nil)

var imageBuildRequestID = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)

const (
	imageBuildKeyLabel       = "brigade.build.key"
	imageBuildCreatedAtLabel = "brigade.build.created_at"
)

func (s *DockerSpawner) Build(ctx context.Context, request imagebuild.Request, log func(string)) (result imagebuild.Result, err error) {
	if !imageBuildRequestID.MatchString(request.ID) {
		return result, errors.New("spawn: image build: request ID must be a valid nonempty Docker tag")
	}
	if log == nil {
		log = func(string) {}
	}
	if _, err := s.EnsureImage(ctx, s.baseImage); err != nil {
		return result, fmt.Errorf("spawn: image build: ensure base: %w", err)
	}
	base, err := s.cli.ImageInspect(ctx, s.baseImage)
	if err != nil {
		return result, fmt.Errorf("spawn: image build: inspect base: %w", err)
	}
	if _, err := digest.Parse(base.ID); err != nil {
		return result, fmt.Errorf("spawn: image build: invalid base image ID: %w", err)
	}
	if base.Os == "" || base.Architecture == "" {
		return result, errors.New("spawn: image build: base image platform is missing")
	}
	platform := base.Os + "/" + base.Architecture
	if base.Variant != "" {
		platform += "/" + base.Variant
	}
	log(fmt.Sprintf("Базовый образ: %s (%s, %s)\n", s.baseImage, base.ID, platform))
	if base.Config != nil && base.Config.Labels["org.opencontainers.image.version"] != "" {
		log("Версия базы: " + base.Config.Labels["org.opencontainers.image.version"] + "\n")
	}
	// JSON обеспечивает однозначные границы полей, включая произвольный текст скрипта.
	inputs := []string{base.ID, platform, request.Script, request.UserID}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return result, fmt.Errorf("spawn: image build: encode cache key: %w", err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if !request.NoCache {
		images, err := s.cli.ImageList(ctx, image.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", imageBuildKeyLabel+"="+key)),
		})
		if err != nil {
			return result, fmt.Errorf("spawn: image build: list cached images: %w", err)
		}
		var cachedTag string
		var cachedCreatedAt int64
		for _, candidate := range images {
			if candidate.Labels[imageBuildKeyLabel] != key {
				continue
			}
			createdAt, err := strconv.ParseInt(candidate.Labels[imageBuildCreatedAtLabel], 10, 64)
			if err != nil {
				continue
			}
			for _, candidateTag := range candidate.RepoTags {
				if !strings.HasPrefix(candidateTag, "brigade-build:") {
					continue
				}
				if cachedTag == "" || createdAt > cachedCreatedAt || (createdAt == cachedCreatedAt && candidateTag < cachedTag) {
					cachedTag, cachedCreatedAt = candidateTag, createdAt
				}
			}
		}
		if cachedTag != "" {
			return imagebuild.Result{Image: cachedTag, Created: false}, nil
		}
	}
	tag := "brigade-build:" + request.ID
	if _, err := s.cli.ImageInspect(ctx, tag); err == nil {
		return result, errors.New("spawn: image build: request ID already has an image")
	} else if !client.IsErrNotFound(err) {
		return result, fmt.Errorf("spawn: image build: inspect target image: %w", err)
	}

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	dockerfile := fmt.Sprintf("FROM %s\nUSER root\nCOPY setup.sh /tmp/brigade-setup.sh\nRUN [\"bash\", \"-eu\", \"/tmp/brigade-setup.sh\"]\nUSER %d:%d\n", base.ID, AgentUID, AgentGID)
	for _, file := range []struct{ name, content string }{
		{"Dockerfile", dockerfile},
		{"setup.sh", request.Script},
	} {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0600, Size: int64(len(file.content)), Typeflag: tar.TypeReg}); err != nil {
			return result, fmt.Errorf("spawn: image build: archive header: %w", err)
		}
		if _, err := io.WriteString(writer, file.content); err != nil {
			return result, fmt.Errorf("spawn: image build: archive content: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return result, fmt.Errorf("spawn: image build: close archive: %w", err)
	}

	// Попытка владеет только собственным новым тегом, но не кешем или базовым образом.
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, cleanupErr := s.cli.ImageRemove(cleanupCtx, tag, image.RemoveOptions{PruneChildren: false}); cleanupErr != nil && !client.IsErrNotFound(cleanupErr) {
			cleanupErr = fmt.Errorf("spawn: image build: remove failed image %s: %w", tag, cleanupErr)
			log(cleanupErr.Error() + "\n")
			err = errors.Join(err, cleanupErr)
		}
	}()
	response, err := s.cli.ImageBuild(ctx, &archive, build.ImageBuildOptions{
		Tags:        []string{tag},
		Dockerfile:  "Dockerfile",
		Platform:    platform,
		NoCache:     request.NoCache,
		Remove:      true,
		ForceRemove: true,
		Labels: map[string]string{
			imageBuildKeyLabel:       key,
			imageBuildCreatedAtLabel: strconv.FormatInt(time.Now().UnixNano(), 10),
		},
	})
	if err != nil {
		return result, fmt.Errorf("spawn: image build: Docker API: %w", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var streamErr error
	for {
		var message jsonmessage.JSONMessage
		if decodeErr := decoder.Decode(&message); decodeErr != nil {
			if !errors.Is(decodeErr, io.EOF) {
				streamErr = errors.Join(streamErr, fmt.Errorf("decode Docker stream: %w", decodeErr))
				// Некорректное сообщение не должно оставлять поток работающей сборки непрочитанным.
				if _, drainErr := io.Copy(io.Discard, response.Body); drainErr != nil {
					streamErr = errors.Join(streamErr, fmt.Errorf("drain Docker stream: %w", drainErr))
				}
			}
			break
		}
		if message.Stream != "" {
			log(message.Stream)
		}
		if message.Status != "" {
			log(message.Status + "\n")
		}
		if message.ErrorMessage != "" {
			log(message.ErrorMessage + "\n")
			if streamErr == nil {
				streamErr = errors.New(message.ErrorMessage)
			}
		}
		if message.Error != nil && (message.ErrorMessage == "" || message.Error.Message != message.ErrorMessage) {
			log(message.Error.Error() + "\n")
			if streamErr == nil {
				streamErr = message.Error
			}
		}
	}
	if ctx.Err() != nil {
		streamErr = errors.Join(streamErr, ctx.Err())
	}
	if streamErr != nil {
		return result, fmt.Errorf("spawn: image build: %w", streamErr)
	}
	if _, err := s.cli.ImageInspect(ctx, tag); err != nil {
		return result, fmt.Errorf("spawn: image build: inspect built image: %w", err)
	}
	return imagebuild.Result{Image: tag, Created: true}, nil
}
