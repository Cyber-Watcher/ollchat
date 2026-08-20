package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Конфиг, где заданы обе настройки имени файла журнала, — след ручного
// обновления с прежней версии. Молчать нельзя: пользователь правит pattern
// и не понимает, почему имя файла не меняется.
func TestBothLogPatternsGiveHint(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.Log.FilePattern = config.DefaultFilePattern
		cfg.Log.Pattern = "chat-2006-01-02.md"
	})

	hints := blocksOfKind(m, blockHint)
	var found string
	for _, h := range hints {
		if strings.Contains(h.text, "file_pattern") {
			found = h.text
		}
	}
	if found == "" {
		t.Fatalf("подсказки про журнал нет, подсказки: %v", hints)
	}
	if !strings.Contains(found, config.DefaultFilePattern) {
		t.Errorf("в подсказке нет действующего шаблона:\n%s", found)
	}
}

// Обычный конфиг — только новая настройка — подсказок про журнал не даёт.
func TestSingleLogPatternIsQuiet(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.Log.FilePattern = config.DefaultFilePattern
		cfg.Log.Pattern = ""
	})
	for _, h := range blocksOfKind(m, blockHint) {
		if strings.Contains(h.text, "file_pattern") {
			t.Errorf("лишняя подсказка про журнал:\n%s", h.text)
		}
	}
}
