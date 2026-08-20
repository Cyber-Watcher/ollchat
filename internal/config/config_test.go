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

// По умолчанию журнал получает свой файл на каждый запуск: на одной машине
// одновременно работает несколько экземпляров ollchat.
func TestLogFilePatternDefault(t *testing.T) {
	cfg, err := writeConfig(t, "")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if cfg.Log.FilePattern != DefaultFilePattern {
		t.Errorf("file_pattern по умолчанию = %q, ожидался %q", cfg.Log.FilePattern, DefaultFilePattern)
	}
	p, err := cfg.Log.NamePattern()
	if err != nil {
		t.Fatalf("разбор шаблона по умолчанию: %v", err)
	}
	if !p.PerSession() {
		t.Error("шаблон по умолчанию должен давать свой файл на каждый запуск")
	}
	ts := time.Date(2026, 8, 20, 14, 30, 5, 0, time.Local)
	if got := p.Name(ts); got != "chat-2026-08-20_14-30-05.md" {
		t.Errorf("имя файла = %q, ожидалось chat-2026-08-20_14-30-05.md", got)
	}
}

// Шаблон без часов оставляет прежнее поведение — один файл на день.
func TestLogFilePatternDaily(t *testing.T) {
	cfg, err := writeConfig(t, "[log]\nfile_pattern = \"chat-%Y-%m-%d.md\"\n")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	p, err := cfg.Log.NamePattern()
	if err != nil {
		t.Fatalf("разбор шаблона: %v", err)
	}
	if p.PerSession() {
		t.Error("шаблон без часов не должен требовать файл на каждый запуск")
	}
}

// Конфиги, созданные до появления file_pattern, обязаны работать как прежде.
func TestLogLegacyPatternStillWorks(t *testing.T) {
	cfg, err := writeConfig(t, "[log]\npattern = \"chat-2006-01-02.md\"\n")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if cfg.Log.FilePattern != "" {
		t.Errorf("устаревший pattern не должен подменяться умолчанием, получено %q", cfg.Log.FilePattern)
	}
	p, err := cfg.Log.NamePattern()
	if err != nil {
		t.Fatalf("разбор шаблона: %v", err)
	}
	ts := time.Date(2026, 8, 20, 14, 30, 5, 0, time.Local)
	if got := p.Name(ts); got != "chat-2026-08-20.md" {
		t.Errorf("имя файла = %q, ожидалось chat-2026-08-20.md", got)
	}
	if cfg.Log.LegacyPatternIgnored() {
		t.Error("одна лишь устаревшая настройка должна действовать, а не игнорироваться")
	}
}

// Заданы обе настройки — выигрывает новая, о старой предупреждаем.
func TestLogFilePatternWinsOverLegacy(t *testing.T) {
	cfg, err := writeConfig(t, "[log]\nfile_pattern = \"chat-%Y-%m-%d_%H-%M-%S.md\"\npattern = \"chat-2006-01-02.md\"\n")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if !cfg.Log.LegacyPatternIgnored() {
		t.Error("устаревшая настройка при обеих заданных должна считаться недействующей")
	}
	p, err := cfg.Log.NamePattern()
	if err != nil {
		t.Fatalf("разбор шаблона: %v", err)
	}
	if !p.PerSession() {
		t.Error("действовать должен file_pattern")
	}
}

// Опечатка в шаблоне — ошибка запуска с именем настройки, а не тихая замена.
func TestLogFilePatternInvalid(t *testing.T) {
	_, err := writeConfig(t, "[log]\nfile_pattern = \"chat-%Q.md\"\n")
	if err == nil {
		t.Fatal("недопустимый шаблон должен отклоняться")
	}
	if !strings.Contains(err.Error(), "log.file_pattern") {
		t.Errorf("ошибка %q должна называть настройку log.file_pattern", err)
	}
}

// Умолчания оформления: песочный код без заливки и тема gruvbox в блоках.
func TestThemeDefaults(t *testing.T) {
	cfg, err := writeConfig(t, "")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if cfg.Theme.Style != ThemeAuto {
		t.Errorf("стиль по умолчанию = %q, ожидался %q", cfg.Theme.Style, ThemeAuto)
	}
	if cfg.Theme.CodeTheme != DefaultCodeTheme {
		t.Errorf("тема подсветки по умолчанию = %q, ожидалась %q", cfg.Theme.CodeTheme, DefaultCodeTheme)
	}
	if cfg.Theme.InlineCode != DefaultInlineCode {
		t.Errorf("цвет кода в тексте по умолчанию = %q, ожидался %q", cfg.Theme.InlineCode, DefaultInlineCode)
	}
	if cfg.Theme.CodeBG != "" || cfg.Theme.InlineCodeBG != "" {
		t.Errorf("заливок по умолчанию быть не должно: %q и %q", cfg.Theme.CodeBG, cfg.Theme.InlineCodeBG)
	}
}

// Опечатка в имени темы chroma обязана быть ошибкой запуска: сама chroma
// на неизвестное имя не ругается, а молча берёт запасную тему swapoff.
func TestThemeRejectsUnknownCodeTheme(t *testing.T) {
	_, err := writeConfig(t, "[theme]\ncode_theme = \"gruvbocks\"\n")
	if err == nil {
		t.Fatal("неизвестная тема подсветки должна отклоняться")
	}
	if !strings.Contains(err.Error(), "theme.code_theme") {
		t.Errorf("ошибка %q должна называть настройку theme.code_theme", err)
	}
	if !strings.Contains(err.Error(), "gruvbox") {
		t.Errorf("ошибка должна перечислять доступные темы: %q", err)
	}
}

func TestThemeRejectsUnknownStyle(t *testing.T) {
	_, err := writeConfig(t, "[theme]\nstyle = \"darkness\"\n")
	if err == nil {
		t.Fatal("неизвестный стиль должен отклоняться")
	}
	if !strings.Contains(err.Error(), "theme.style") {
		t.Errorf("ошибка %q должна называть настройку theme.style", err)
	}
}

func TestThemeRejectsBadColor(t *testing.T) {
	_, err := writeConfig(t, "[theme]\ninline_code = \"песочный\"\n")
	if err == nil {
		t.Fatal("недопустимый цвет должен отклоняться")
	}
	if !strings.Contains(err.Error(), "theme.inline_code") {
		t.Errorf("ошибка %q должна называть настройку theme.inline_code", err)
	}
}

// Прежний вид возвращается пустой темой подсветки.
func TestThemeEmptyCodeThemeAllowed(t *testing.T) {
	cfg, err := writeConfig(t, "[theme]\ncode_theme = \"\"\n")
	if err != nil {
		t.Fatalf("пустая тема подсветки должна приниматься: %v", err)
	}
	if cfg.Theme.CodeTheme != "" {
		t.Errorf("пустая тема не должна подменяться умолчанием, получено %q", cfg.Theme.CodeTheme)
	}
}

// Умолчание правок цветов: красные ключи YAML заменяются синими.
func TestThemeTokensDefault(t *testing.T) {
	cfg, err := writeConfig(t, "")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if got := cfg.Theme.Tokens["NameTag"]; got != DefaultTokens["NameTag"] {
		t.Errorf("правка NameTag по умолчанию = %q, ожидалась %q", got, DefaultTokens["NameTag"])
	}
}

// Пустой раздел означает «цвета темы как есть» и не подменяется умолчанием —
// иначе от правки нельзя было бы отказаться.
func TestThemeTokensEmptySectionKeepsThemeColors(t *testing.T) {
	cfg, err := writeConfig(t, "[theme.tokens]\n")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if len(cfg.Theme.Tokens) != 0 {
		t.Errorf("пустой раздел не должен получать умолчание, получено %v", cfg.Theme.Tokens)
	}
}

// Свои правки заменяют умолчание целиком, а не дописываются к нему.
func TestThemeTokensReplaceDefault(t *testing.T) {
	cfg, err := writeConfig(t, "[theme.tokens]\nKeyword = \"#fe8019\"\n")
	if err != nil {
		t.Fatalf("проверка конфига: %v", err)
	}
	if _, ok := cfg.Theme.Tokens["NameTag"]; ok {
		t.Error("умолчание NameTag пережило пользовательский раздел")
	}
	if cfg.Theme.Tokens["Keyword"] != "#fe8019" {
		t.Errorf("своя правка не применилась: %v", cfg.Theme.Tokens)
	}
}

func TestThemeTokensRejectsUnknownToken(t *testing.T) {
	_, err := writeConfig(t, "[theme.tokens]\nYamlKey = \"#83a598\"\n")
	if err == nil {
		t.Fatal("неизвестный вид токена должен отклоняться")
	}
	if !strings.Contains(err.Error(), "theme.tokens") || !strings.Contains(err.Error(), "NameTag") {
		t.Errorf("ошибка должна называть настройку и подсказать имена: %q", err)
	}
}

// Номер ANSI здесь недопустим: chroma читает его как шестнадцатеричное число
// и молча даёт другой цвет — 179 превратился бы в #000179.
func TestThemeTokensRejectsAnsiNumber(t *testing.T) {
	_, err := writeConfig(t, "[theme.tokens]\nNameTag = \"179\"\n")
	if err == nil {
		t.Fatal("номер цвета ANSI в правках токенов должен отклоняться")
	}
	if !strings.Contains(err.Error(), "theme.tokens.NameTag") {
		t.Errorf("ошибка %q должна называть настройку", err)
	}
}

// Правки без темы подсветки не действуют — об этом надо сказать, а не молчать.
func TestThemeTokensRequireCodeTheme(t *testing.T) {
	_, err := writeConfig(t, "[theme]\ncode_theme = \"\"\n\n[theme.tokens]\nNameTag = \"#83a598\"\n")
	if err == nil {
		t.Fatal("правки без code_theme должны отклоняться")
	}
	if !strings.Contains(err.Error(), "theme.code_theme") {
		t.Errorf("ошибка %q должна объяснять связь с theme.code_theme", err)
	}
}

// А вот пустая тема сама по себе, без правок, остаётся законной: так
// возвращаются цвета базового стиля glamour.
func TestThemeEmptyCodeThemeWithoutTokensAllowed(t *testing.T) {
	cfg, err := writeConfig(t, "[theme]\ncode_theme = \"\"\n\n[theme.tokens]\n")
	if err != nil {
		t.Fatalf("пустая тема без правок должна приниматься: %v", err)
	}
	if len(cfg.Theme.Tokens) != 0 {
		t.Errorf("правок быть не должно: %v", cfg.Theme.Tokens)
	}
}
