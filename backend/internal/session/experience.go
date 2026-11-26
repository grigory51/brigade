package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/acp/acpremote"
	"github.com/grigory51/brigade/backend/internal/plugin"
	"github.com/grigory51/brigade/backend/internal/store"
)

var (
	ErrExperienceUnavailable = errors.New("session: experience is unavailable or misconfigured")
	ErrExperienceNotIdleACP  = errors.New("session: experience requires a live idle persistent ACP session")
)

// SetExperience меняет только активный experience существующей сессии. Native ID,
// workspace, настройки агента и история не меняются; новые turn ждут окончания перехода.
func (r *Registry) SetExperience(ctx context.Context, sessionID, userID, experienceID string) (result store.Session, err error) {
	unlock := r.lockSession(sessionID)
	defer unlock()
	sess, err := r.Get(ctx, sessionID, userID)
	if err != nil {
		return store.Session{}, err
	}
	if sess.Kind != store.SessionKindACP || sess.ExecutionPolicy == store.SessionExecutionOnDemand || sess.Status != store.SessionStatusRunning {
		return store.Session{}, ErrExperienceNotIdleACP
	}
	r.mu.Lock()
	lv := r.live[sessionID]
	r.mu.Unlock()
	if lv == nil || lv.owner != userID || lv.client == nil || sess.AgentSessionID == "" || lv.client.SessionID() != sess.AgentSessionID {
		return store.Session{}, ErrExperienceNotIdleACP
	}
	client, ok := lv.client.(interface {
		ReserveIdle(context.Context) (func(), error)
	})
	if !ok {
		return store.Session{}, ErrExperienceNotIdleACP
	}
	if sess.ExperienceID == experienceID {
		return sess, nil
	}
	release, err := client.ReserveIdle(ctx)
	if err != nil {
		return store.Session{}, err
	}
	defer release()
	previous := sess
	sess.ExperienceID, sess.ExperienceVersion = experienceID, ""
	sess.ExperienceOwnerID = nil
	if experienceID != "" {
		target := plugin.RuntimeTarget(sess.Mode == store.SessionModeDocker)
		selected, err := r.store.GetPlugin(ctx, userID, experienceID, "", target)
		if err != nil {
			return store.Session{}, fmt.Errorf("%w: %w", ErrExperienceUnavailable, err)
		}
		sess.ExperienceVersion, sess.ExperienceOwnerID = selected.Version, &selected.OwnerID
		installed, manifest, err := r.experience(ctx, sess)
		if err != nil {
			return store.Session{}, fmt.Errorf("%w: %w", ErrExperienceUnavailable, err)
		}
		if _, err := r.experienceServer(ctx, sess, manifest, installed.BundlePath); err != nil {
			return store.Session{}, fmt.Errorf("%w: %w", ErrExperienceUnavailable, err)
		}
	}

	if remote, ok := lv.client.(*acpremote.Client); ok {
		token := r.agentToken(ctx, sess)
		opts, err := r.acpDaemonOptions(ctx, sess, token, sess.AgentSessionID)
		if err != nil {
			return store.Session{}, err
		}
		opts.Reconfigure = true
		// Сохраняем конфигурацию компенсации до изменения store/runtime. Она нужна,
		// если RPC применился, но ответ потерялся или запрос был отменён на границе commit.
		rollback, err := r.acpDaemonOptions(ctx, previous, token, previous.AgentSessionID)
		if err != nil {
			return store.Session{}, err
		}
		rollback.Reconfigure = true
		if err := r.store.UpdateSessionExperience(ctx, sessionID, sess.ExperienceID, sess.ExperienceVersion, sess.ExperienceOwnerID); err != nil {
			return store.Session{}, err
		}
		if _, err := remote.Configure(ctx, opts); err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			var runtimeErr error
			switch connect.CodeOf(err) {
			case connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodeUnavailable, connect.CodeUnknown, connect.CodeDataLoss:
				_, runtimeErr = remote.Configure(cleanup, rollback)
			}
			storeErr := r.store.UpdateSessionExperience(cleanup, sessionID, previous.ExperienceID, previous.ExperienceVersion, previous.ExperienceOwnerID)
			if errors.Is(err, acp.ErrSessionUnrecoverable) || runtimeErr != nil {
				r.mu.Lock()
				delete(r.live, sessionID)
				r.mu.Unlock()
				storeErr = errors.Join(storeErr, r.store.UpdateSessionStatus(cleanup, sessionID, store.SessionStatusFailed), lv.terminate(cleanup))
			}
			if runtimeErr != nil || storeErr != nil {
				return store.Session{}, errors.Join(err, fmt.Errorf("session: experience rollback failed: %w", errors.Join(runtimeErr, storeErr)))
			}
			return store.Session{}, err
		}
		return sess, nil
	}

	local, ok := lv.client.(*acp.Client)
	if !ok {
		return store.Session{}, ErrExperienceNotIdleACP
	}
	opts, err := r.acpLocalOptions(ctx, sess, r.agentToken(ctx, sess))
	if err != nil {
		return store.Session{}, err
	}
	opts.ResumeSessionID, opts.StrictResume = sess.AgentSessionID, true
	runtime, err := r.prepareLocalExperience(ctx, sess)
	if err != nil {
		return store.Session{}, err
	}
	rollbackOptions := local.ResumeOptions()
	var replacement *acp.Client
	committed := false
	defer func() {
		if !committed {
			if replacement != nil {
				err = errors.Join(err, replacement.CloseForReload())
			}
			_ = runtime.Close()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			restored, restoreErr := acp.New(cleanup, rollbackOptions)
			if restoreErr != nil {
				r.mu.Lock()
				delete(r.live, sessionID)
				r.mu.Unlock()
				_ = lv.plugin.Close()
				err = errors.Join(err, acp.ErrSessionUnrecoverable, fmt.Errorf("session: experience runtime rollback: %w", restoreErr), r.store.UpdateSessionStatus(cleanup, sessionID, store.SessionStatusFailed))
				return
			}
			restored.PreserveHistory(local)
			r.setACPHooks(previous, restored)
			oldLive := *lv
			oldLive.client = restored
			r.mu.Lock()
			r.live[sessionID] = &oldLive
			r.mu.Unlock()
		}
	}()
	// Native session допускает одного writer: старый процесс должен освободить
	// блокировку до session/load. Turn-барьер и session lock удерживаются до commit/rollback.
	if err := local.CloseForReload(); err != nil {
		return store.Session{}, err
	}
	replacement, err = acp.New(ctx, opts)
	if err != nil {
		return store.Session{}, err
	}
	replacement.PreserveHistory(local)
	if err := ctx.Err(); err != nil {
		return store.Session{}, err
	}
	if err := r.store.UpdateSessionExperience(ctx, sessionID, sess.ExperienceID, sess.ExperienceVersion, sess.ExperienceOwnerID); err != nil {
		return store.Session{}, err
	}
	r.setACPHooks(sess, replacement)
	newLive := *lv
	newLive.client, newLive.plugin = replacement, runtime
	r.mu.Lock()
	r.live[sessionID] = &newLive
	r.mu.Unlock()
	committed = true
	_ = lv.plugin.Close()
	return sess, nil
}
