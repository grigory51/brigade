package agentimage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/grigory51/brigade/backend/internal/imagebuild"
	"github.com/grigory51/brigade/backend/internal/store"
)

const maxBuildLog = 256 << 10
const maxBuildScript = 64 << 10

var ErrBuildBusy = errors.New("сборка уже запущена")
var ErrBuildQueueFull = errors.New("очередь сборки заполнена; попробуйте позже")
var ErrBuildNotFound = errors.New("сборка не найдена")

type buildJob struct {
	mu     sync.Mutex
	record store.ImageBuild
	cancel context.CancelFunc
}

// Builds управляет жизненным циклом сборки независимо от способа исполнения.
type Builds struct {
	mu       sync.Mutex
	jobs     map[string]*buildJob
	mutating map[string]bool
	store    *store.Store
	images   *Service
	builder  imagebuild.Builder
	slots    chan struct{}
	timeout  time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewBuilds(st *store.Store, images *Service, builder imagebuild.Builder, timeout time.Duration, parallel int) (*Builds, error) {
	if timeout <= 0 || parallel < 1 {
		return nil, errors.New("image build: неверные ограничения сборки")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := st.InterruptImageBuilds(ctx); err != nil {
		cancel()
		return nil, err
	}
	return &Builds{jobs: make(map[string]*buildJob), mutating: make(map[string]bool), store: st, images: images, builder: builder,
		slots: make(chan struct{}, parallel), timeout: timeout, ctx: ctx, cancel: cancel}, nil
}

func (s *Builds) Close() {
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Builds) Start(ctx context.Context, userID, name, script string, noCache bool) (store.ImageBuild, error) {
	if s.builder == nil {
		return store.ImageBuild{}, ErrUnavailable
	}
	name = strings.TrimSpace(name)
	if _, err := imageRef(userID, name); err != nil {
		return store.ImageBuild{}, err
	}
	if strings.TrimSpace(script) == "" || len(script) > maxBuildScript || strings.ContainsRune(script, 0) {
		return store.ImageBuild{}, errors.New("укажите скрипт размером до 64 КиБ без нулевых символов")
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return store.ImageBuild{}, errors.New("Brigade останавливается")
	}
	if s.activeLocked(userID) || s.mutating[userID] {
		s.mu.Unlock()
		return store.ImageBuild{}, ErrBuildBusy
	}
	// Очередь ограничена независимо от количества пользователей инстанса.
	active := 0
	for user := range s.jobs {
		if s.activeLocked(user) {
			active++
		}
	}
	if active >= cap(s.slots)*4 {
		s.mu.Unlock()
		return store.ImageBuild{}, ErrBuildQueueFull
	}
	b := store.ImageBuild{ID: uuid.NewString(), Name: name, Script: script, Status: "queued"}
	buildCtx, cancel := context.WithTimeout(s.ctx, s.timeout)
	job := &buildJob{record: b, cancel: cancel}
	s.jobs[userID] = job
	s.wg.Add(1)
	s.mu.Unlock()
	// Резервируем заявку до обращения к БД, не блокируя управление другими задачами.
	persistCtx, persistCancel := context.WithTimeout(ctx, 10*time.Second)
	stopCancel := context.AfterFunc(buildCtx, persistCancel)
	err := s.store.StartImageBuild(persistCtx, userID, b)
	stopCancel()
	persistCancel()
	if err != nil {
		cancel()
		s.mu.Lock()
		if s.jobs[userID] == job {
			delete(s.jobs, userID)
		}
		s.mu.Unlock()
		s.wg.Done()
		return b, err
	}
	go s.run(buildCtx, userID, job, noCache)
	return b, nil
}

func (s *Builds) Get(ctx context.Context, userID string) (store.ImageBuild, error) {
	s.mu.Lock()
	job := s.jobs[userID]
	s.mu.Unlock()
	if job != nil {
		job.mu.Lock()
		record := job.record
		job.mu.Unlock()
		// Повторное чтение восстанавливает сохранение после временного отказа БД.
		if record.Status != "queued" && record.Status != "building" {
			if err := s.store.UpdateImageBuild(ctx, userID, record); err == nil {
				s.mu.Lock()
				if s.jobs[userID] == job {
					delete(s.jobs, userID)
				}
				s.mu.Unlock()
			}
		}
		return record, nil
	}
	return s.store.GetImageBuild(ctx, userID)
}

func (s *Builds) Cancel(ctx context.Context, userID, id string) (store.ImageBuild, error) {
	s.mu.Lock()
	job := s.jobs[userID]
	if job != nil {
		job.mu.Lock()
		matches := job.record.ID == id
		job.mu.Unlock()
		if matches {
			job.cancel()
		}
	}
	s.mu.Unlock()
	b, err := s.Get(ctx, userID)
	if err != nil {
		return b, err
	}
	if b.ID == "" || b.ID != id {
		return store.ImageBuild{}, ErrBuildNotFound
	}
	return b, nil
}

// SetImages не позволяет сохранению устаревшего списка затереть результат сборки.
func (s *Builds) SetImages(ctx context.Context, userID string, refs []string) (Settings, error) {
	s.mu.Lock()
	if s.activeLocked(userID) || s.mutating[userID] {
		s.mu.Unlock()
		return Settings{}, ErrBuildBusy
	}
	s.mutating[userID] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.mutating, userID); s.mu.Unlock() }()
	return s.images.Set(ctx, userID, refs)
}

func (s *Builds) activeLocked(userID string) bool {
	job := s.jobs[userID]
	if job == nil {
		return false
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.record.Status == "queued" || job.record.Status == "building"
}

func (s *Builds) run(ctx context.Context, userID string, job *buildJob, noCache bool) {
	defer s.wg.Done()
	defer job.cancel()
	var result imagebuild.Result
	var buildErr error
	lastSave := time.Time{}
	writeLog := func(chunk string) {
		job.mu.Lock()
		job.record.Log += strings.ToValidUTF8(chunk, "�")
		if len(job.record.Log) > maxBuildLog {
			job.record.Log = strings.ToValidUTF8(job.record.Log[len(job.record.Log)-maxBuildLog:], "")
		}
		record := job.record
		job.mu.Unlock()
		if time.Since(lastSave) >= time.Second && buildErr == nil {
			if err := s.store.UpdateImageBuild(ctx, userID, record); err != nil {
				buildErr = fmt.Errorf("не удалось сохранить лог сборки: %w", err)
				job.cancel()
			}
			lastSave = time.Now()
		}
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
		job.mu.Lock()
		job.record.Status = "building"
		request := imagebuild.Request{ID: job.record.ID, UserID: userID, Script: job.record.Script, NoCache: noCache}
		job.mu.Unlock()
		writeLog("Подготовка базового образа и сборка окружения…\n")
		if buildErr == nil && ctx.Err() == nil {
			var err error
			result, err = s.builder.Build(ctx, request, writeLog)
			if buildErr == nil {
				buildErr = err
			}
		}
	case <-ctx.Done():
	}
	if buildErr == nil {
		buildErr = ctx.Err()
	}
	if buildErr == nil {
		writeLog("Проверка совместимости образа и квоты…\n")
		if buildErr == nil {
			var published string
			published, buildErr = s.images.publishBuilt(ctx, userID, job.record.Name, result.Image)
			if buildErr == nil {
				if result.Created && result.Image != published {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					if err := s.images.docker.RemoveImage(cleanupCtx, result.Image); err != nil {
						log.Printf("image build: remove staging tag %s: %v", result.Image, err)
					}
					cancel()
				}
				result.Image = published
			}
		}
	}
	if buildErr != nil && result.Created {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := s.images.docker.RemoveImage(cleanupCtx, result.Image); err != nil {
			log.Printf("image build: cleanup %s: %v", result.Image, err)
		}
		cancel()
	}
	job.mu.Lock()
	switch {
	case buildErr == nil:
		job.record.Status, job.record.Image = "succeeded", result.Image
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		job.record.Status, job.record.Error = "failed", "Превышено время сборки. Сократите скрипт или увеличьте image_build.timeout."
	case s.ctx.Err() != nil:
		job.record.Status, job.record.Error = "interrupted", "Brigade остановлен во время сборки. Запустите сборку повторно."
	case errors.Is(buildErr, context.Canceled):
		job.record.Status, job.record.Error = "cancelled", "Сборка отменена. Предыдущие образы сохранены."
	default:
		job.record.Status, job.record.Error = "failed", buildErr.Error()
	}
	record := job.record
	job.mu.Unlock()
	// Финальный статус должен сохраниться и после отмены задачи или shutdown.
	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	persistErr := s.store.UpdateImageBuild(persistCtx, userID, record)
	if persistErr != nil {
		log.Printf("image build %s: сохранить результат: %v", record.ID, persistErr)
	}
	cancel()
	s.mu.Lock()
	if persistErr == nil && s.jobs[userID] == job {
		delete(s.jobs, userID)
	}
	s.mu.Unlock()
}
