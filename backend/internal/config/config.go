// Package config загружает конфигурацию brigade из YAML-файла с override через env.
// Источник истины — koanf: yaml-файл задаёт значения по умолчанию, переменные
// окружения с префиксом BRIGADE_ их перекрывают (вложенность через двойное подчёркивание).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Mode определяет, как инстанс спавнит агентов: local — процесс на хосте в pty;
// docker — отдельный контейнер на сессию. Это свойство инстанса, не сессии:
// пользователь его не выбирает, все сессии наследуют режим сервиса.
type Mode string

const (
	ModeLocal  Mode = "local"
	ModeDocker Mode = "docker"
)

// Config — полная конфигурация сервиса.
type Config struct {
	// Mode — режим спавна агентов инстанса (local|docker). Дефолт local.
	Mode Mode `koanf:"mode"`
	// Addr — адрес HTTP-сервера, например ":8080".
	Addr string `koanf:"addr"`

	SQLitePath string `koanf:"sqlite_path"`

	JWT  JWTConfig  `koanf:"jwt"`
	Seed SeedConfig `koanf:"seed"`

	// WorkDir — корневая рабочая директория; для docker-сессий её подпапки
	// bind-mount'ятся в контейнеры.
	WorkDir string `koanf:"work_dir"`

	// MaxContainers — потолок на число одновременных docker-контейнеров (ACP —
	// контейнер на сессию, docker-CLI — общий на пользователя). Не задано (0) → дефолт
	// 16; отрицательное значение отключает лимит. Применяется только в docker-режиме.
	MaxContainers int `koanf:"max_containers"`

	// ClaudeHomeDir — базовый каталог на хосте для персональных ~/.claude
	// пользователей (docker-режим). brigade создаёт подкаталог <ClaudeHomeDir>/<userID>
	// и bind-mount'ит его в /home/agent/.claude во все контейнеры пользователя, чтобы
	// авторизация Claude (`/login`) была общей между его CLI- и ACP-сессиями. Пусто —
	// фича выключена (используется прежний named volume состояния по дереву сессий).
	ClaudeHomeDir string `koanf:"claude_home_dir"`

	Preview PreviewConfig `koanf:"preview"`
	TLS     TLSConfig     `koanf:"tls"`
	Memory  MemoryConfig  `koanf:"memory"`
}

// MemoryConfig — личная память пользователя (git-репо заметок). Источник истины — файлы,
// durability — git-remote. Пустой remote выключает фичу.
type MemoryConfig struct {
	// Remote — git-remote заметок (любой: GitHub / self-hosted / локальный bare).
	// Env: BRIGADE_MEMORY__REMOTE. Пусто — фича выключена.
	Remote string `koanf:"remote"`
	// Dir — рабочая копия на хосте (git working clone). Env: BRIGADE_MEMORY__DIR.
	// Пусто при включённой памяти → дефолт <home>/.brigade/memory.
	Dir string `koanf:"dir"`
	// SSHKey — путь к приватному SSH-ключу для доступа к remote по git@-URL (без пароля).
	// Env: BRIGADE_MEMORY__SSH_KEY. Пусто — git использует SSH-настройки хоста (~/.ssh).
	SSHKey string `koanf:"ssh_key"`
}

// PreviewConfig — публикация dev-серверов сессий через встроенный L7-прокси.
// Запрос на {sessionId}-{port}.{Domain} проксируется к соответствующему порту сессии
// (local — 127.0.0.1, docker — IP контейнера); маршрут детерминирован и не требует
// регистрации.
type PreviewConfig struct {
	Enabled bool `koanf:"enabled"`
	// Mode — способ адресации preview:
	//   - "subdomain" (дефолт): {sessionId}-{port}.{Domain} — требует wildcard-DNS;
	//   - "cookie": один хост CookieHost, выбор сессии/порта через cookie
	//     (?id=<id>-<port> ставит cookie и редиректит) — для сред без wildcard
	//     (например, netbird expose, где wildcard-поддомены не поддержаны).
	Mode string `koanf:"mode"`
	// Domain — базовый домен preview-поддоменов (mode=subdomain): "localhost" для
	// разработки (браузеры резолвят *.localhost сами) либо домен с wildcard-DNS.
	Domain string `koanf:"domain"`
	// CookieHost — хост cookie-режима целиком (mode=cookie), напр.
	// "preview.brigade.example.com". Один хост обслуживает по одному активному
	// dev-серверу на браузер.
	CookieHost string `koanf:"cookie_host"`
	// Scheme — схема публичных preview-URL (http|https). https предполагает
	// TLS-терминацию: встроенную (TLS.Addr) либо внешнюю.
	Scheme string `koanf:"scheme"`
	// ExternalPort — порт в публичных preview-URL, если он отличается от порта
	// прослушивания (например, 443 при внешнем TLS-терминаторе). 0 — использовать
	// порт из Addr (или TLS.Addr при scheme=https со встроенным TLS).
	ExternalPort int `koanf:"external_port"`
}

// TLSConfig — встроенная TLS-терминация всего сервера (UI, API и preview-поддомены).
// Сертификат должен покрывать и сам домен, и wildcard preview-поддоменов
// (SAN: domain + *.domain — wildcard сам по себе корень не покрывает).
type TLSConfig struct {
	// Addr — адрес HTTPS-listener'а, например ":443". Пусто — TLS выключен.
	// Plain-listener на Addr при этом продолжает работать (локальный доступ).
	Addr     string `koanf:"addr"`
	CertFile string `koanf:"cert_file"`
	KeyFile  string `koanf:"key_file"`
}

type JWTConfig struct {
	// Secret — ключ подписи JWT. Задаётся через env в проде.
	Secret string `koanf:"secret"`
	// AccessTTL — TTL access-токена (короткий).
	AccessTTL time.Duration `koanf:"access_ttl"`
	// RefreshTTL — TTL refresh-токена.
	RefreshTTL time.Duration `koanf:"refresh_ttl"`
}

// SeedConfig — стартовый пользователь, создаётся при первом запуске, если БД пуста.
type SeedConfig struct {
	Username string `koanf:"username"`
	Password string `koanf:"password"`
}

// Load читает конфиг из указанного yaml-файла и применяет env-override (префикс BRIGADE_).
func Load(path string) (*Config, error) {
	k := koanf.New(".")

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("config: load file %q: %w", path, err)
	}

	// BRIGADE_JWT__SECRET → jwt.secret: префикс снимается, двойное подчёркивание
	// заменяется разделителем уровней, имя приводится к нижнему регистру.
	err := k.Load(env.Provider("BRIGADE_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "BRIGADE_")
		s = strings.ReplaceAll(s, "__", ".")
		return strings.ToLower(s)
	}), nil)
	if err != nil {
		return nil, fmt.Errorf("config: load env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate проверяет согласованность загруженной конфигурации. Цель — поймать
// заведомо нерабочие значения на старте, а не при первом обращении к ним.
func (c *Config) Validate() error {
	// Пустой mode трактуем как local (дефолт), явный недопустимый — ошибка.
	switch c.Mode {
	case "":
		c.Mode = ModeLocal
	case ModeLocal, ModeDocker:
	default:
		return fmt.Errorf("config: недопустимый mode %q (ожидается %q или %q)", c.Mode, ModeLocal, ModeDocker)
	}

	if c.Addr == "" {
		return fmt.Errorf("config: addr не задан")
	}
	// Лимит контейнеров: отсутствующее поле koanf даёт 0 — трактуем как дефолт 16.
	// Отключить лимит можно отрицательным значением (registry.atContainerLimit: <=0 —
	// без лимита).
	if c.MaxContainers == 0 {
		c.MaxContainers = 16
	}
	if c.SQLitePath == "" {
		return fmt.Errorf("config: sqlite_path не задан")
	}
	if c.WorkDir == "" {
		return fmt.Errorf("config: work_dir не задан")
	}
	// work_dir приводим к абсолютному пути: ACP-агент отвергает относительный cwd, а в
	// docker-режиме относительный путь некорректен для bind-mount. Нормализуем здесь, у
	// источника дефолта.
	if abs, err := filepath.Abs(c.WorkDir); err != nil {
		return fmt.Errorf("config: resolve work_dir %q: %w", c.WorkDir, err)
	} else {
		c.WorkDir = abs
	}

	// claude_home_dir (если задан) — абсолютный путь: это источник для bind-mount на
	// хост-демон docker, относительный путь некорректен.
	if c.ClaudeHomeDir != "" {
		abs, err := filepath.Abs(c.ClaudeHomeDir)
		if err != nil {
			return fmt.Errorf("config: resolve claude_home_dir %q: %w", c.ClaudeHomeDir, err)
		}
		c.ClaudeHomeDir = abs
	}

	if c.JWT.Secret == "" {
		return fmt.Errorf("config: jwt.secret не задан")
	}
	if c.JWT.AccessTTL <= 0 {
		return fmt.Errorf("config: jwt.access_ttl должен быть положительным")
	}
	if c.JWT.RefreshTTL <= 0 {
		return fmt.Errorf("config: jwt.refresh_ttl должен быть положительным")
	}
	// Refresh-токен живёт дольше access-токена; иначе обновление теряет смысл.
	if c.JWT.RefreshTTL <= c.JWT.AccessTTL {
		return fmt.Errorf("config: jwt.refresh_ttl (%s) должен быть больше jwt.access_ttl (%s)", c.JWT.RefreshTTL, c.JWT.AccessTTL)
	}

	if c.Seed.Username == "" {
		return fmt.Errorf("config: seed.username не задан")
	}
	if c.Seed.Password == "" {
		return fmt.Errorf("config: seed.password не задан")
	}

	if c.Preview.Enabled {
		switch c.Preview.Mode {
		case "", "subdomain":
			c.Preview.Mode = "subdomain"
			if err := validateBareHost("preview.domain", c.Preview.Domain, true); err != nil {
				return err
			}
		case "cookie":
			if err := validateBareHost("preview.cookie_host", c.Preview.CookieHost, true); err != nil {
				return err
			}
		default:
			return fmt.Errorf("config: invalid preview.mode %q (expected subdomain or cookie)", c.Preview.Mode)
		}
		switch c.Preview.Scheme {
		case "http", "https":
		case "":
			return fmt.Errorf("config: preview.scheme is required when preview is enabled (http or https)")
		default:
			return fmt.Errorf("config: invalid preview.scheme %q (expected http or https)", c.Preview.Scheme)
		}
		if p := c.Preview.ExternalPort; p < 0 || p > 65535 {
			return fmt.Errorf("config: preview.external_port %d out of range", p)
		}
	}

	if c.TLS.Addr != "" {
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return fmt.Errorf("config: tls.cert_file and tls.key_file are required when tls.addr is set")
		}
	}

	// Память включена ⇔ задан remote. Рабочую копию нормализуем к абсолютному пути (её
	// подкаталоги — git working tree, относительный путь ненадёжен). Дефолт — под $HOME.
	if c.Memory.Remote != "" {
		if c.Memory.Dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("config: resolve home for memory.dir: %w", err)
			}
			c.Memory.Dir = filepath.Join(home, ".brigade", "memory")
		}
		abs, err := filepath.Abs(c.Memory.Dir)
		if err != nil {
			return fmt.Errorf("config: resolve memory.dir %q: %w", c.Memory.Dir, err)
		}
		c.Memory.Dir = abs

		// SSH-ключ (если задан) — абсолютный путь; проверяем существование на старте,
		// чтобы неверный путь падал сразу, а не при первом push.
		if c.Memory.SSHKey != "" {
			keyAbs, err := filepath.Abs(c.Memory.SSHKey)
			if err != nil {
				return fmt.Errorf("config: resolve memory.ssh_key %q: %w", c.Memory.SSHKey, err)
			}
			if _, err := os.Stat(keyAbs); err != nil {
				return fmt.Errorf("config: memory.ssh_key %q: %w", keyAbs, err)
			}
			c.Memory.SSHKey = keyAbs
		}
	}

	return nil
}

// validateBareHost проверяет, что значение — голое имя хоста (без схемы и без
// ведущей/замыкающей точки). required — обязательно ли непустое.
func validateBareHost(field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("config: %s is required", field)
		}
		return nil
	}
	if strings.Contains(v, "://") || strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") {
		return fmt.Errorf("config: %s %q must be a bare host name (no scheme, no leading/trailing dot)", field, v)
	}
	return nil
}
