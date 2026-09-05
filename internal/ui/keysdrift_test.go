package ui

import (
	"strings"
	"testing"
)

// Каждая клавиша из таблицы с подписью упомянута в справке под тем же
// именем: таблица и текст справки — два места, и без сверки они расходятся.
func TestHelpMentionsEveryKey(t *testing.T) {
	m := newTestModel(t)
	help := helpText(m.sections())
	for _, b := range keys.all() {
		label := b.Help().Key
		if label == "" {
			continue
		}
		if !strings.Contains(help, label) {
			t.Errorf("клавиша %s (%s) не упомянута в справке", label, b.Help().Desc)
		}
	}
}
