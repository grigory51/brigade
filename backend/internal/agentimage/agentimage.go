// Package agentimage — образы контейнеров агента, которые пользователь может выбрать при
// создании сессии, и квота на их суммарный вес.
//
// Образ пользователя ничем не обязан базовому: компоненты brigade (демон, node, адаптер,
// MCP-сервер) приезжают в контейнер read-only volume'ами (см. internal/spawn). От образа
// требуется лишь совместимость — её проверяет docker-спавнер перед добавлением.
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
	"strings"

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
	store  *store.Store
	docker Docker
	quota  int64
}

// New собирает сервис. docker == nil (local-режим) — операции возвращают ErrUnavailable.
// quota — предел суммарного веса образов на пользователя в байтах; 0 — без ограничения.
func New(st *store.Store, docker Docker, quota int64) *Service {
	return &Service{store: st, docker: docker, quota: quota}
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

// Set перезаписывает список образов пользователя. Отсутствующие локально подтягиваются из
// реестра, каждый проверяется на пригодность для сессий, затем считается квота. При отказе
// образы, стянутые этим вызовом, удаляются — иначе неудачная попытка оставляла бы на диске
// именно то, от чего защищает квота. Образы, лежавшие локально (пользователь собрал их
// сам), не трогаем.
func (s *Service) Set(ctx context.Context, userID string, refs []string) (Settings, error) {
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
	return s.List(ctx, userID)
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
			if other != ref && isPrefix(oinfo.Layers, info.Layers) && oinfo.Size > base {
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
