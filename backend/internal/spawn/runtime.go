package spawn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/grigory51/brigade/backend/internal/agent"
)

// Runtime brigade в контейнере сессии.
//
// Всё, что нужно brigade для работы агента (демон, node, CLI/адаптер, MCP-сервер), НЕ
// берётся из образа сессии, а приезжает read-only volume'ами, наполненными из базового
// образа. Это даёт две вещи:
//
//   - пользователь может запустить сессию на СВОЁМ образе (ubuntu, golang, python) —
//     достаточно совместимой libc и пользователя с uid 1001;
//   - бинарь демона гарантированно наш: ему brigade передаёт по RPC приватный SSH-ключ,
//     токен Claude и секреты MCP, а подменить содержимое ro-volume образ не может.
//
// Состав слоёв на сессию берётся из манифеста агента (internal/agent).

// runtimeDonorDir — каталог-донор в базовом образе, откуда наполняются volume'ы слоёв.
const runtimeDonorDir = agent.RuntimeRoot

// PingDocker проверяет доступность docker-демона по адресу host (пусто — адрес из
// окружения). Нужна интерфейсу: показать, можно ли вообще переключиться в docker-режим, до
// того как пользователь это сделает и перезапустит приложение.
func PingDocker(ctx context.Context, host string) error {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return err
	}
	defer cli.Close()
	_, err = cli.Ping(ctx)
	return err
}

// EnsureImage гарантирует наличие образа локально: отсутствующий подтягивается из реестра.
// Возвращает true, если образ был стянут этим вызовом (нужно для отката при превышении
// квоты — стянутое нами удаляем, локально собранное пользователем не трогаем).
func (s *DockerSpawner) EnsureImage(ctx context.Context, ref string) (bool, error) {
	if _, err := s.cli.ImageInspect(ctx, ref); err == nil {
		return false, nil
	} else if !client.IsErrNotFound(err) {
		return false, fmt.Errorf("spawn: inspect image %s: %w", ref, err)
	}
	rc, err := s.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return false, fmt.Errorf("spawn: pull image %s: %w", ref, err)
	}
	defer rc.Close()
	// Поток прогресса нужно дочитать до конца: закрытие раньше обрывает загрузку.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return false, fmt.Errorf("spawn: pull image %s: %w", ref, err)
	}
	return true, nil
}

// RemoveImage удаляет образ (без force): используется для отката образа, стянутого сверх
// квоты. Занятый контейнерами образ не удалится — это ожидаемо, ошибку возвращаем как есть.
func (s *DockerSpawner) RemoveImage(ctx context.Context, ref string) error {
	_, err := s.cli.ImageRemove(ctx, ref, image.RemoveOptions{})
	return err
}

// ImageInfo — сведения об образе, нужные квоте и проверке совместимости.
type ImageInfo struct {
	// Size — размер образа целиком (сумма слоёв).
	Size int64
	// Layers — diff_id слоёв. По общим префиксам считается, сколько образы делят между
	// собой (см. auth.imageWeights).
	Layers []string
}

// InspectImage возвращает размер и слои образа.
func (s *DockerSpawner) InspectImage(ctx context.Context, ref string) (ImageInfo, error) {
	insp, err := s.cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageInfo{}, fmt.Errorf("spawn: inspect image %s: %w", ref, err)
	}
	return ImageInfo{Size: insp.Size, Layers: insp.RootFS.Layers}, nil
}

// imageProbe — скрипт проверки образа. Запускается интерпретатором из runtime-слоя, то
// есть заодно проверяет совместимость libc: собранный под glibc 2.31 node в образе
// постарее просто не стартует.
//
// Проверять сам uid бессмысленно (docker запускает процесс с числовым uid в любом образе) —
// проверяем то, что реально ломается без подготовленного пользователя: запись в /etc/passwd
// (без неё git не определяет автора коммита) и домашний каталог, принадлежащий агенту (в
// него пишутся ~/.claude.json, ~/.gitconfig; созданный докером под bind-mount он был бы
// root-owned и недоступен на запись).
const imageProbe = `const fs=require("fs"),cp=require("child_process");
let user=false,home=false,git=false;
try{user=fs.readFileSync("/etc/passwd","utf8").split("\n").some(l=>l.split(":")[2]===String(process.getuid()))}catch{}
try{home=fs.statSync(process.env.HOME).uid===process.getuid()}catch{}
try{cp.execSync("git --version",{stdio:"ignore"});git=true}catch{}
process.stdout.write(JSON.stringify({user,home,git}))`

// CheckImage проверяет, что образ пригоден для сессий. Разовая проверка при добавлении
// образа в настройки: несовместимость должна всплыть здесь, а не сломанной сессией.
func (s *DockerSpawner) CheckImage(ctx context.Context, ref string) error {
	mounts, _, err := s.runtimeMounts(ctx, []agent.Layer{agent.LayerNode})
	if err != nil {
		return err
	}
	node := agent.LayerNode.BinPath() + "/node"
	out, err := s.runOnce(ctx, ref, mounts, []string{node, "-e", imageProbe})
	if err != nil {
		return fmt.Errorf("образ не подходит для сессий: %w (нужен дистрибутив с glibc ≥ 2.31; alpine и musl не поддерживаются)", err)
	}

	var probe struct{ User, Home, Git bool }
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &probe); err != nil {
		return fmt.Errorf("образ не подходит для сессий: непонятный ответ проверки %q", strings.TrimSpace(out))
	}
	var missing []string
	if !probe.User || !probe.Home {
		missing = append(missing, fmt.Sprintf("пользователь с uid %d и его домашний каталог %s (`RUN useradd -u %d -m -d %s agent`)",
			AgentUID, AgentHome, AgentUID, AgentHome))
	}
	if !probe.Git {
		missing = append(missing, "git (`RUN apt-get update && apt-get install -y git ca-certificates`)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("образу не хватает: %s", strings.Join(missing, "; "))
	}
	return nil
}

// runOnce запускает разовый контейнер и возвращает его stdout. Контейнер удаляется всегда.
func (s *DockerSpawner) runOnce(ctx context.Context, ref string, mounts []mount.Mount, cmd []string) (string, error) {
	created, err := s.cli.ContainerCreate(ctx,
		// HOME задаётся так же, как у сессии: проверка смотрит именно тот каталог, в
		// который агент потом будет писать.
		&container.Config{Image: ref, Cmd: cmd, User: agentUser, Env: sanitizedEnv([]string{"HOME=" + AgentHome})},
		&container.HostConfig{Mounts: mounts}, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("создание контейнера: %w", err)
	}
	defer func() {
		_ = s.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("запуск контейнера: %w", err)
	}
	statusCh, errCh := s.cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return "", fmt.Errorf("ожидание контейнера: %w", err)
	case st := <-statusCh:
		out := containerOutput(ctx, s.cli, created.ID)
		if st.StatusCode != 0 {
			return out, fmt.Errorf("контейнер завершился с кодом %d: %s", st.StatusCode, strings.TrimSpace(out))
		}
		return out, nil
	}
}

// runtimeMounts готовит volume'ы перечисленных слоёв и возвращает их read-only mount'ы
// вместе с PATH-префиксом (каталоги bin слоёв в порядке объявления).
func (s *DockerSpawner) runtimeMounts(ctx context.Context, layers []agent.Layer) ([]mount.Mount, string, error) {
	digest, err := s.baseImageDigest(ctx)
	if err != nil {
		return nil, "", err
	}

	mounts := make([]mount.Mount, 0, len(layers))
	bins := make([]string, 0, len(layers))
	for _, l := range layers {
		name := runtimeVolumeName(l.Name, digest)
		if err := s.ensureRuntimeVolume(ctx, name, l.Name); err != nil {
			return nil, "", err
		}
		mounts = append(mounts, mount.Mount{
			Type: mount.TypeVolume, Source: name, Target: l.Path(), ReadOnly: true,
		})
		if bin := l.BinPath(); bin != "" {
			bins = append(bins, bin)
		}
	}
	return mounts, strings.Join(bins, ":"), nil
}

// ensureRuntimeVolume создаёт volume слоя и наполняет его из базового образа. Имя включает
// digest базового образа, поэтому обновление brigade даёт новые volume'ы, а не смешивает
// компоненты разных версий. Уже наполненные в этом процессе volume'ы пропускаются.
func (s *DockerSpawner) ensureRuntimeVolume(ctx context.Context, name, layer string) error {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeReady == nil {
		s.runtimeReady = map[string]bool{}
	}
	if s.runtimeReady[name] {
		return nil
	}
	if _, err := s.cli.VolumeCreate(ctx, volume.CreateOptions{Name: name}); err != nil {
		return fmt.Errorf("spawn: runtime volume %s: %w", name, err)
	}
	// Наполняем из базового образа (он наш) под root: volume создаётся root-owned, а права
	// файлов сохраняет `cp -a` — агенту нужен только доступ на чтение.
	src := runtimeDonorDir + "/" + layer
	out, err := s.runAsRoot(ctx, s.baseImage,
		[]mount.Mount{{Type: mount.TypeVolume, Source: name, Target: "/out"}},
		[]string{"sh", "-c", fmt.Sprintf("test -d %s && cp -a %s/. /out/", src, src)})
	if err != nil {
		// Обычная причина — образ агента собран версией brigade без runtime-слоёв: их
		// раскладывает docker/claude-agent/Dockerfile, и после обновления brigade образ
		// нужно пересобрать.
		return fmt.Errorf("spawn: в образе %s нет runtime-слоя %q (%s: %w) — пересоберите образ агента: docker build -t %s -f docker/claude-agent/Dockerfile .",
			s.baseImage, layer, strings.TrimSpace(out), err, s.baseImage)
	}
	s.runtimeReady[name] = true
	return nil
}

// runAsRoot — runOnce от root (наполнение volume'ов требует записи).
func (s *DockerSpawner) runAsRoot(ctx context.Context, ref string, mounts []mount.Mount, cmd []string) (string, error) {
	created, err := s.cli.ContainerCreate(ctx,
		&container.Config{Image: ref, Cmd: cmd, User: "0:0"},
		&container.HostConfig{Mounts: mounts}, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("создание контейнера: %w", err)
	}
	defer func() {
		_ = s.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("запуск контейнера: %w", err)
	}
	statusCh, errCh := s.cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return "", fmt.Errorf("ожидание контейнера: %w", err)
	case st := <-statusCh:
		out := containerOutput(ctx, s.cli, created.ID)
		if st.StatusCode != 0 {
			return out, errors.New("копирование не удалось")
		}
		return out, nil
	}
}

// baseImageDigest — идентификатор базового образа: ключ версии runtime-volume'ов. Образ
// при необходимости подтягивается: его могли удалить мимо brigade (`docker system prune`),
// и в этом случае сессия должна дождаться загрузки, а не упасть.
func (s *DockerSpawner) baseImageDigest(ctx context.Context) (string, error) {
	s.baseImageMu.Lock()
	defer s.baseImageMu.Unlock()
	if s.baseImageDigestCache != "" {
		return s.baseImageDigestCache, nil
	}
	if s.baseImage != DefaultImage && strings.HasSuffix(s.baseImage, ":latest") {
		// `latest` обновляется между релизами, но ImageInspect видит только локальную копию.
		// Pull один раз на процесс гарантирует, что runtime-volume и контейнеры сверяются с
		// опубликованным агентом, не добавляя registry round-trip к каждой сессии.
		rc, err := s.cli.ImagePull(ctx, s.baseImage, image.PullOptions{})
		if err != nil {
			return "", fmt.Errorf("spawn: pull базового образа %s: %w", s.baseImage, err)
		}
		_, copyErr := io.Copy(io.Discard, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return "", fmt.Errorf("spawn: pull базового образа %s: %w", s.baseImage, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("spawn: pull базового образа %s: %w", s.baseImage, closeErr)
		}
	} else if _, err := s.EnsureImage(ctx, s.baseImage); err != nil {
		return "", fmt.Errorf("spawn: базовый образ %s недоступен: %w", s.baseImage, err)
	}
	insp, err := s.cli.ImageInspect(ctx, s.baseImage)
	if err != nil {
		return "", fmt.Errorf("spawn: базовый образ %s недоступен: %w", s.baseImage, err)
	}
	id := strings.TrimPrefix(insp.ID, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	s.baseImageDigestCache = id
	return s.baseImageDigestCache, nil
}

func runtimeVolumeName(layer, digest string) string {
	return "brigade-rt-" + layer + "-" + digest
}

// daemonBinPath — путь бинаря демона внутри контейнера: слой runtime, а не образ сессии.
func daemonBinPath() string { return agent.LayerDaemon.BinPath() + "/brigade" }

// sanitizedEnv дополняет переменные контейнера защитой от подмены: LD_PRELOAD и
// LD_LIBRARY_PATH из ENV чужого образа перехватили бы динамические вызовы процессов,
// которым brigade передаёт секреты. Пустые значения гасят их независимо от образа.
func sanitizedEnv(env []string) []string {
	return append(append([]string{}, env...), "LD_PRELOAD=", "LD_LIBRARY_PATH=")
}

// agentUser — пользователь процессов агента в контейнере. Числовой вид работает и в
// образах, где записи в /etc/passwd для этого uid нет.
var agentUser = fmt.Sprintf("%d:%d", AgentUID, AgentGID)

// runtimeState — часть DockerSpawner, кеширующая наполненные runtime-volume'ы. Кеш живёт в
// памяти процесса: после рестарта brigade наполнение повторится (идемпотентно и занимает
// секунды), зато не нужен маркер готовности внутри volume.
type runtimeState struct {
	runtimeMu            sync.Mutex
	runtimeReady         map[string]bool
	baseImageMu          sync.Mutex
	baseImageDigestCache string
}

// containerOutput читает логи завершившегося контейнера одной строкой. Best-effort: вывод
// нужен только для диагностики (сообщение об ошибке), поэтому ошибки чтения гасятся.
func containerOutput(ctx context.Context, cli *client.Client, id string) string {
	rc, err := cli.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var out, errOut bytes.Buffer
	// Логи не-TTY контейнера идут мультиплексированным потоком docker — разбираем его.
	if _, err := stdcopy.StdCopy(&out, &errOut, rc); err != nil {
		return out.String()
	}
	if out.Len() > 0 {
		return out.String()
	}
	return errOut.String()
}
