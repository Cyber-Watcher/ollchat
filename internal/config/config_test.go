package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig кладёт кусок TOML во временный файл и загружает его.
func writeConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	// Имя сервера совпадает с general.default_server по умолчанию, иначе
	// проверка конфига остановится раньше, чем дойдёт до настроек ввода.
	full := body + `
[[servers]]
name = "local"
url  = "http://127.0.0.1:11434"
`
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("запись конфига: %v", err)
	}
	cfg, _, err := Load(path)
	return cfg, err
}

// По умолчанию курсор выглядит так же, как раньше: мигающий блок цвета терминала.
func TestCursorDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Input.Cursor.Shape != CursorBlock {
		t.Errorf("форма курсора по умолчанию = %q, ожидался %q", cfg.Input.Cursor.Shape, CursorBlock)
	}
	if !cfg.Input.Cursor.Blink {
		t.Error("по умолчанию курсор должен мигать")
	}
	if cfg.Input.Cursor.Color != "" {
		t.Errorf("по умолчанию цвет курсора задаёт терминал, получено %q", cfg.Input.Cursor.Color)
	}
	if !cfg.Input.Mouse {
		t.Error("по умолчанию мышь работает на приложение")
	}
}

func TestCursorShapeAccepted(t *testing.T) {
	for _, shape := range []string{CursorBlock, CursorUnderline, CursorBar} {
		cfg, err := writeConfig(t, "[input.cursor]\nshape = \""+shape+"\"\n")
		if err != nil {
			t.Fatalf("форма %q должна приниматься, получена ошибка: %v", shape, err)
		}
		if cfg.Input.Cursor.Shape != shape {
			t.Errorf("форма курсора = %q, ожидалась %q", cfg.Input.Cursor.Shape, shape)
		}
	}
}

// Опечатка в конфиге должна останавливать запуск, а не превращаться молча
// в значение по умолчанию: иначе пользователь не поймёт, почему настройка
// не подействовала.
func TestCursorShapeRejectsUnknown(t *testing.T) {
	_, err := writeConfig(t, "[input.cursor]\nshape = \"beam\"\n")
	if err == nil {
		t.Fatal("неизвестная форма курсора должна быть ошибкой")
	}
	if !strings.Contains(err.Error(), "input.cursor.shape") {
		t.Errorf("в сообщении об ошибке нет имени настройки: %v", err)
	}
}

func TestCursorColorAccepted(t *testing.T) {
	for _, c := range []string{"", "0", "212", "255", "#f0d", "#ff87d7"} {
		if err := checkColor(c); err != nil {
			t.Errorf("цвет %q должен приниматься: %v", c, err)
		}
	}
}

func TestCursorColorRejectsGarbage(t *testing.T) {
	for _, c := range []string{"256", "-1", "розовый", "#12", "#gggggg", "#1234567"} {
		if err := checkColor(c); err == nil {
			t.Errorf("цвет %q должен отвергаться", c)
		}
	}
}

func TestCursorColorErrorNamesSetting(t *testing.T) {
	_, err := writeConfig(t, "[input.cursor]\ncolor = \"розовый\"\n")
	if err == nil {
		t.Fatal("недопустимый цвет курсора должен быть ошибкой")
	}
	if !strings.Contains(err.Error(), "input.cursor.color") {
		t.Errorf("в сообщении об ошибке нет имени настройки: %v", err)
	}
}

// Мышь и мигание — булевы настройки: важно, что false из файла не теряется
// на фоне значения по умолчанию true.
func TestInputBooleansOverrideDefaults(t *testing.T) {
	cfg, err := writeConfig(t, "[input]\nmouse = false\n\n[input.cursor]\nblink = false\n")
	if err != nil {
		t.Fatalf("загрузка конфига: %v", err)
	}
	if cfg.Input.Mouse {
		t.Error("mouse = false из конфига не применился")
	}
	if cfg.Input.Cursor.Blink {
		t.Error("blink = false из конфига не применился")
	}
}

// Образец конфига из --init-config обязан сам проходить проверку: иначе
// пользователь получит файл, с которым приложение не запустится.
func TestTemplateIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(Template), 0o600); err != nil {
		t.Fatalf("запись образца: %v", err)
	}
	cfg, exists, err := Load(path)
	if err != nil {
		t.Fatalf("образец конфига не проходит проверку: %v", err)
	}
	if !exists {
		t.Fatal("образец конфига не найден после записи")
	}
	if cfg.Input.Cursor.Shape != CursorBlock {
		t.Errorf("в образце форма курсора = %q, ожидался %q", cfg.Input.Cursor.Shape, CursorBlock)
	}
}

// chat_timeout отдельно от timeout: см. docs/TimeOutPlan.md. Без него в
// конфиге таймаут заголовков /api/chat должен получать щедрое значение по
// умолчанию, не совпадающее с коротким timeout быстрых вызовов.
func TestServerChatTimeoutDefault(t *testing.T) {
	cfg, err := writeConfig(t, "")
	if err != nil {
		t.Fatalf("загрузка конфига: %v", err)
	}
	srv := cfg.Servers[0]
	if got := srv.TimeoutDuration(); got != 300*time.Second {
		t.Errorf("timeout по умолчанию = %v, ожидалось 300s", got)
	}
	if got := srv.ChatTimeoutDuration(); got != 30*time.Minute {
		t.Errorf("chat_timeout по умолчанию = %v, ожидалось 30m", got)
	}
}

func TestServerChatTimeoutOverride(t *testing.T) {
	cfg, err := writeConfig(t, `
[[servers]]
name         = "custom"
url          = "http://127.0.0.1:11434"
chat_timeout = "2s"
`)
	if err != nil {
		t.Fatalf("загрузка конфига: %v", err)
	}
	srv, ok := cfg.ServerByName("custom")
	if !ok {
		t.Fatal("сервер custom не найден")
	}
	if got := srv.ChatTimeoutDuration(); got != 2*time.Second {
		t.Errorf("chat_timeout = %v, ожидалось 2s", got)
	}
}

func TestServerChatTimeoutRejectsGarbage(t *testing.T) {
	_, err := writeConfig(t, `
[[servers]]
name         = "custom"
url          = "http://127.0.0.1:11434"
chat_timeout = "не число"
`)
	if err == nil {
		t.Fatal("некорректный chat_timeout должен останавливать запуск")
	}
	if !strings.Contains(err.Error(), "chat_timeout") {
		t.Errorf("ошибка должна называть настройку chat_timeout, получено: %v", err)
	}
}
