// Package config загружает конфигурацию brigade из YAML-файла с override через env.
// Источник истины — koanf: yaml-файл задаёт значения по умолчанию, переменные
// окружения с префиксом BRIGADE_ их перекрывают (вложенность через двойное подчёркивание).
package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Mode определяет, где спавнятся агенты: локально в хост-процессе или в Docker-контейнере.
type Mode string

const (
	ModeLocal  Mode = "local"
	ModeDocker Mode = "docker"
)

// Config — полная конфигурация сервиса.
type Config struct {
	// Mode — режим спавна агентов (local|docker).
	Mode Mode `koanf:"mode"`
	// Addr — адрес HTTP-сервера, например ":8080".
	Addr string `koanf:"addr"`

	SQLitePath string `koanf:"sqlite_path"`

	JWT  JWTConfig  `koanf:"jwt"`
	Seed SeedConfig `koanf:"seed"`

	// WorkDir — корневая рабочая директория; в docker-режиме её подпапки
	// bind-mount'ятся в контейнеры сессий.
	WorkDir string `koanf:"work_dir"`

	// ClaudeCodeOAuthToken — долгоживущий подписочный токен Claude Code
	// (формат sk-ant-oat01-..., создаётся командой `claude setup-token`).
	// Пробрасывается агенту через переменную окружения CLAUDE_CODE_OAUTH_TOKEN.
	// Целевая модель аутентификации — подписка Claude, а не API-ключ с оплатой
	// по токенам. По соображениям безопасности задаётся через env
	// (BRIGADE_CLAUDE_CODE_OAUTH_TOKEN), но допустимо и значение из yaml.
	ClaudeCodeOAuthToken string `koanf:"claude_code_oauth_token"`
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
	switch c.Mode {
	case ModeLocal, ModeDocker:
	case "":
		return fmt.Errorf("config: mode не задан (ожидается %q или %q)", ModeLocal, ModeDocker)
	default:
		return fmt.Errorf("config: недопустимый mode %q (ожидается %q или %q)", c.Mode, ModeLocal, ModeDocker)
	}

	if c.Addr == "" {
		return fmt.Errorf("config: addr не задан")
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

	return nil
}
