// Package spawn определяет интерфейс Spawner и две его реализации.
//
// Spawner запускает кодинг-агент (целевой — Claude Code) и отдаёт Handle с доступом
// к псевдотерминалу агента (raw stdin/stdout), управлением размером терминала и
// ожиданием завершения. Две реализации:
//
//   - local.go  — агент запускается как процесс хост-машины в pty (creack/pty);
//   - docker.go — агент запускается в отдельном контейнере на сессию (TTY-attach).
//
// Помимо первичного запуска (Spawn) поддерживается восстановление после рестарта
// бэкенда (Reattach): local переподнимает процесс через `claude --resume <id>`,
// docker заново подключается (attach) к уже работающему контейнеру, найденному по
// label `brigade.session.id`.
package spawn

import (
	"context"
	"io"
)

// Spec описывает параметры первичного запуска агента.
type Spec struct {
	// SessionID — идентификатор сессии brigade. Используется как значение label
	// brigade.session.id для docker-контейнера и для сопоставления при Reattach.
	SessionID string

	// UserID — владелец сессии. В docker-режиме CLI непустой UserID включает схему
	// «общий контейнер на пользователя»: сессия запускается docker exec'ом в
	// долгоживущем контейнере brigade-user-<UserID> (label brigade.user.id), а не в
	// собственном контейнере. Контейнер переживает отдельные сессии — Claude
	// привязывает авторизацию к контейнеру (fingerprint), и контейнер-на-сессию
	// сбрасывал логин, несмотря на общий home. Пусто — legacy-схема (контейнер на
	// сессию). local-режим поле игнорирует.
	UserID string

	// Cwd — рабочая директория агента.
	//
	// В local-режиме это путь на хост-машине. В docker-режиме это путь на хосте,
	// который bind-mount'ится внутрь контейнера (см. ContainerWorkdir).
	Cwd string

	// Env — дополнительные переменные окружения агента в форме "KEY=VALUE"
	// (например, "CLAUDE_CODE_OAUTH_TOKEN=...").
	Env []string

	// Image — образ контейнера (только docker-режим). Если пусто, используется
	// значение по умолчанию реализации.
	Image string

	// HomeHost — путь на хосте к персональному home пользователя
	// (<claude_home_dir>/<userID>), bind-mount'ится в /home/agent контейнера целиком.
	// Общий для всех контейнеров пользователя (per-user): состояние Claude (~/.claude,
	// ~/.claude.json) и рабочие файлы (~/workspace) переживают сессии и видны во всех
	// его сессиях; между разными пользователями home не пересекается. Пусто — не
	// монтировать (фича выключена, docker назначит эфемерный home/named volume).
	HomeHost string

	// Hostname — hostname контейнера. Задаётся равным логину пользователя, чтобы у
	// всех его контейнеров он совпадал: Claude привязывает креды к machine/hostname,
	// и при разных hostname авторизация одной сессии не видна в другой. Пусто —
	// docker назначает hostname по container id (только docker-режим).
	Hostname string
}

// Persisted описывает сохранённое в БД состояние сессии, достаточное для Reattach
// после рестарта бэкенда.
type Persisted struct {
	// SessionID — идентификатор сессии brigade.
	SessionID string

	// AgentSessionID — идентификатор сессии агента для возобновления
	// (`claude --resume <AgentSessionID>` в local-режиме).
	AgentSessionID string

	// ContainerLabel — значение label brigade.session.id, по которому в
	// docker-режиме отыскивается контейнер сессии. Совпадает с SessionID, хранится
	// отдельно для явности контракта persist.
	ContainerLabel string

	// Cwd, Env, Image — параметры запуска, нужные local-режиму для resume
	// (legacy docker-режим переиспользует уже существующий контейнер и их игнорирует;
	// shared docker-режим использует Env для нового exec'а).
	Cwd   string
	Env   []string
	Image string

	// UserID, HomeHost, Hostname — параметры общего per-user контейнера (docker CLI,
	// shared-схема): по UserID контейнер находится/пересоздаётся, HomeHost/Hostname
	// сохраняют авторизацию Claude при пересоздании. Для legacy-сессий (непустой
	// ContainerLabel) игнорируются.
	UserID   string
	HomeHost string
	Hostname string
}

// Handle — управление запущенным агентом.
//
// ReadWriteCloser связан с pty агента: Read отдаёт raw-вывод (stdout/stderr через
// TTY), Write шлёт ввод (stdin), Close завершает поток. Это позволяет напрямую
// прокидывать поток в WebSocket терминала через io.Copy.
type Handle interface {
	io.ReadWriteCloser

	// Terminate окончательно завершает агента и освобождает его ресурсы. В отличие от
	// Close, который лишь разрывает поток ввода-вывода (для docker — отключает attach,
	// оставляя контейнер живым ради reconnect), Terminate доводит дело до конца:
	// local — завершает процесс и реапит его (без зомби); docker — останавливает и
	// удаляет контейнер сессии. Вызывается при Stop/Delete сессии. Идемпотентна.
	Terminate(ctx context.Context) error

	// Resize меняет размер псевдотерминала агента.
	Resize(cols, rows uint16) error

	// Wait блокируется до завершения агента и возвращает ошибку завершения (nil при
	// нормальном выходе с кодом 0).
	Wait() error

	// ExitCode возвращает код завершения. Валиден только после возврата Wait;
	// до завершения возвращает -1.
	ExitCode() int

	// AgentSessionID — идентификатор сессии агента для persist и последующего resume.
	// Может быть пустым, если агент его ещё не сообщил.
	AgentSessionID() string

	// ContainerLabel — значение label brigade.session.id для docker-режима, по
	// которому контейнер находится при Reattach. Пусто для local-режима.
	ContainerLabel() string

	// History возвращает копию сохранённого хвоста вывода терминала. Отдаётся новому
	// WebSocket-подключению перед стримом живого вывода, чтобы при переподключении
	// или перезагрузке страницы клиент восстановил содержимое экрана.
	History() []byte
}

// Spawner запускает агентов и восстанавливает их после рестарта бэкенда.
type Spawner interface {
	// Spawn запускает нового агента по Spec.
	Spawn(ctx context.Context, spec Spec) (Handle, error)

	// Reattach восстанавливает доступ к ранее запущенному агенту по сохранённому
	// состоянию: local — повторный запуск с `--resume`, docker — attach к
	// существующему контейнеру.
	Reattach(ctx context.Context, persisted Persisted) (Handle, error)
}
