// Package agentimage — образы контейнеров агента, которые пользователь может выбрать при
// создании сессии, и квота на их суммарный вес.
//
// Новые образы добавляются только результатом сборки скрипта поверх базы инстанса.
// Ранее сохранённые образы остаются доступны существующим пользователям.
//
// Квота нужна, чтобы список образов не забил диск хоста: образы тяжёлые, а добавлять их
// пользователь может сколько угодно. Считается не сумма размеров, а реальный прирост места
// (общие слои учитываются один раз).
package agentimage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/client"

	"github.com/grigory51/brigade/backend/internal/spawn"
	"github.com/grigory51/brigade/backend/internal/store"
)

// ErrUnavailable — образы настраиваются только в docker-режиме: в local-режиме агент
// работает процессом на хосте, и контейнера, которому нужен образ, нет.
var ErrUnavailable = errors.New("выбор образа доступен только в docker-режиме")

// ErrQuotaExceeded — суммарный вес образов пользователя не влезает в квоту.
var ErrQuotaExceeded = errors.New("превышена квота на образы")

// Docker — то, что нужно сервису от докера. Интерфейс держим здесь: в local-режиме он nil,
// и пакет не тянет за собой докер-клиент.
type Docker interface {
	// BaseImage — образ агента по умолчанию этого инстанса: он же донор runtime-слоёв и
	// база, вес которой не засчитывается пользовательским образам поверх неё.
	BaseImage() string
	EnsureImage(ctx context.Context, ref string) (bool, error)
	InspectImage(ctx context.Context, ref string) (spawn.ImageInfo, error)
	CheckImage(ctx context.Context, ref string) error
	RemoveImage(ctx context.Context, ref string) error
	TagImage(ctx context.Context, source, target string) error
}

// Image — образ в списке пользователя с его весом.
type Image struct {
	Ref   string
	Bytes int64
}

// Settings — список образов пользователя и состояние квоты.
type Settings struct {
	Images       []Image
	DefaultImage string
	UsedBytes    int64
	QuotaBytes   int64
}

// Service — операции над списком образов пользователя.
type Service struct {
	mutation chan struct{}
	store    *store.Store
	docker   Docker
	quota    int64
}

// New собирает сервис. docker == nil (local-режим) — операции возвращают ErrUnavailable.
// quota — предел суммарного веса образов на пользователя в байтах; 0 — без ограничения.
func New(st *store.Store, docker Docker, quota int64) *Service {
	return &Service{store: st, docker: docker, quota: quota, mutation: make(chan struct{}, 1)}
}

// List возвращает образы пользователя с весами и состоянием квоты.
func (s *Service) List(ctx context.Context, userID string) (Settings, error) {
	if s.docker == nil {
		return Settings{QuotaBytes: s.quota}, nil
	}
	settings := Settings{DefaultImage: s.docker.BaseImage(), QuotaBytes: s.quota}
	refs, err := s.refs(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	weights := s.weigh(ctx, refs)
	for _, ref := range refs {
		settings.Images = append(settings.Images, Image{Ref: ref, Bytes: weights[ref]})
		settings.UsedBytes += weights[ref]
	}
	return settings, nil
}

// Set удаляет или переупорядочивает сохранённые образы. Добавление разрешено только
// внутреннему пути завершения сборки, а не произвольной ссылке клиента.
func (s *Service) Set(ctx context.Context, userID string, refs []string) (Settings, error) {
	select {
	case s.mutation <- struct{}{}:
	case <-ctx.Done():
		return Settings{}, ctx.Err()
	}
	defer func() { <-s.mutation }()
	if s.docker == nil {
		return Settings{}, ErrUnavailable
	}
	known, err := s.refs(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	for _, ref := range normalize(refs) {
		if !contains(known, ref) {
			return Settings{}, errors.New("добавить образ можно только сборкой из скрипта в разделе «Среда агента»")
		}
	}
	return s.set(ctx, userID, refs)
}

func (s *Service) publishBuilt(ctx context.Context, userID, name, source string) (string, error) {
	target, err := imageRef(userID, name)
	if err != nil {
		return "", err
	}
	select {
	case s.mutation <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.mutation }()
	refs, err := s.refs(ctx, userID)
	if err != nil {
		return "", err
	}
	if err := s.docker.CheckImage(ctx, source); err != nil {
		return "", err
	}
	next, err := s.docker.InspectImage(ctx, source)
	if err != nil {
		return "", err
	}
	// Считаем квоту с новым содержимым, не меняя рабочий тег до проверки.
	candidates := make([]string, 0, len(refs)+1)
	for _, ref := range refs {
		if ref != target {
			candidates = append(candidates, ref)
		}
	}
	weights := s.weigh(ctx, append(candidates, source))
	var used int64
	for _, bytes := range weights {
		used += bytes
	}
	if s.quota > 0 && used > s.quota {
		return "", fmt.Errorf("%w: занято %s из %s", ErrQuotaExceeded, humanBytes(used), humanBytes(s.quota))
	}
	previous, err := s.docker.InspectImage(ctx, target)
	if err != nil && !client.IsErrNotFound(err) {
		return "", err
	}
	if err := s.docker.TagImage(ctx, source, target); err != nil {
		return "", err
	}
	if !contains(refs, target) {
		refs = append(refs, target)
	}
	if err := s.store.SetAgentImages(ctx, userID, refs); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var rollbackErr error
		if previous.ID != "" {
			rollbackErr = s.docker.TagImage(rollbackCtx, previous.ID, target)
		} else {
			rollbackErr = s.docker.RemoveImage(rollbackCtx, target)
		}
		return "", errors.Join(err, rollbackErr)
	}
	// Удаление по ID может снять единственный оставшийся alias даже без force.
	// Убираем только образ без тегов; занятый контейнером Docker сохранит.
	if previous.ID != "" && previous.ID != next.ID {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		old, inspectErr := s.docker.InspectImage(cleanupCtx, previous.ID)
		if inspectErr != nil && !client.IsErrNotFound(inspectErr) {
			log.Printf("image build: inspect previous image %s: %v", previous.ID, inspectErr)
		} else if inspectErr == nil && len(old.Tags) == 0 {
			if err := s.docker.RemoveImage(cleanupCtx, previous.ID); err != nil {
				log.Printf("image build: retain previous image %s: %v", previous.ID, err)
			}
		}
		cancel()
	}
	return target, nil
}

func (s *Service) set(ctx context.Context, userID string, refs []string) (Settings, error) {
	if s.docker == nil {
		return Settings{}, ErrUnavailable
	}
	refs = normalize(refs)

	known, err := s.refs(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	var pulled []string
	rollback := func() {
		for _, ref := range pulled {
			if err := s.docker.RemoveImage(context.WithoutCancel(ctx), ref); err != nil {
				log.Printf("agentimage: откат образа %s: %v", ref, err)
			}
		}
	}

	for _, ref := range refs {
		if contains(known, ref) {
			continue // уже в списке — проверен при добавлении
		}
		wasPulled, err := s.docker.EnsureImage(ctx, ref)
		if err != nil {
			rollback()
			return Settings{}, fmt.Errorf("образ %s недоступен: %w", ref, err)
		}
		if wasPulled {
			pulled = append(pulled, ref)
		}
		if err := s.docker.CheckImage(ctx, ref); err != nil {
			rollback()
			return Settings{}, fmt.Errorf("%s: %w", ref, err)
		}
	}

	weights := s.weigh(ctx, refs)
	var used int64
	for _, w := range weights {
		used += w
	}
	if s.quota > 0 && used > s.quota {
		rollback()
		return Settings{}, fmt.Errorf("%w: занято %s из %s", ErrQuotaExceeded, humanBytes(used), humanBytes(s.quota))
	}

	if err := s.store.SetAgentImages(ctx, userID, refs); err != nil {
		rollback()
		return Settings{}, err
	}
	// После commit не выполняем отменяемых операций: ошибка чтения не должна
	// превращать успешно добавленный образ в кандидата на rollback.
	out := Settings{DefaultImage: s.docker.BaseImage(), QuotaBytes: s.quota, UsedBytes: used}
	for _, ref := range refs {
		out.Images = append(out.Images, Image{Ref: ref, Bytes: weights[ref]})
	}
	return out, nil
}

// Resolve проверяет, что образ разрешён пользователю, и возвращает его. Пустая ссылка —
// базовый образ brigade (сервер подставит его сам).
func (s *Service) Resolve(ctx context.Context, userID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	refs, err := s.refs(ctx, userID)
	if err != nil {
		return "", err
	}
	if !contains(refs, ref) {
		return "", fmt.Errorf("образ %s не в списке ваших образов", ref)
	}
	return ref, nil
}

func (s *Service) refs(ctx context.Context, userID string) ([]string, error) {
	settings, err := s.store.GetUserSettings(ctx, userID)
	if err != nil {
		return nil, err
	}
	return settings.AgentImages, nil
}

// weigh считает вес каждого образа: размер за вычетом самого крупного образа, чьи слои
// являются его префиксом. Так общая база (базовый образ brigade или общий предок двух
// пользовательских образов) не засчитывается дважды. Образ, который не удалось
// проинспектировать (удалён из докера мимо brigade), весит ноль.
func (s *Service) weigh(ctx context.Context, refs []string) map[string]int64 {
	infos := map[string]spawn.ImageInfo{}
	for _, ref := range append(append([]string{}, refs...), s.docker.BaseImage()) {
		if _, ok := infos[ref]; ok {
			continue
		}
		info, err := s.docker.InspectImage(ctx, ref)
		if err != nil {
			log.Printf("agentimage: вес образа %s: %v", ref, err)
			continue
		}
		infos[ref] = info
	}

	out := make(map[string]int64, len(refs))
	for _, ref := range refs {
		info, ok := infos[ref]
		if !ok {
			continue
		}
		var base int64
		for other, oinfo := range infos {
			// Одинаковое содержимое под разными тегами оплачивается один раз.
			sameLayers := len(info.Layers) > 0 && slices.Equal(oinfo.Layers, info.Layers)
			sharedAlias := sameLayers && (other == s.docker.BaseImage() || other < ref)
			if other != ref && (isPrefix(oinfo.Layers, info.Layers) || sharedAlias) && oinfo.Size > base {
				base = oinfo.Size
			}
		}
		if w := info.Size - base; w > 0 {
			out[ref] = w
		}
	}
	return out
}

// isPrefix — являются ли слои a строгим префиксом слоёв b (b собран поверх a).
func isPrefix(a, b []string) bool {
	if len(a) == 0 || len(a) >= len(b) {
		return false
	}
	for i, l := range a {
		if b[i] != l {
			return false
		}
	}
	return true
}

// normalize убирает пустые ссылки и дубли, сохраняя порядок.
func normalize(refs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.ContainsAny(ref, " \t") || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// humanBytes форматирует размер для сообщения пользователю.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %sБ", float64(n)/float64(div), []string{"К", "М", "Г", "Т"}[exp])
}
