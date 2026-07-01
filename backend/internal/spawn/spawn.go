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

// Mode определяет реализацию Spawner.
type Mode string

const (
	ModeLocal  Mode = "local"
	ModeDocker Mode = "docker"
)

// Spec описывает параметры первичного запуска агента.
type Spec struct {
	// SessionID — идентификатор сессии brigade. Используется как значение label
	// brigade.session.id для docker-контейнера и для сопоставления при Reattach.
	SessionID string

	// AgentType — тип агента (например, "claude-code-cli"). Зарезервировано для
	// выбора команды/образа; текущие реализации запускают `claude`.
	AgentType string

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
	// (docker-режим переиспользует уже существующий контейнер и их игнорирует).
	Cwd   string
	Env   []string
	Image string
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
