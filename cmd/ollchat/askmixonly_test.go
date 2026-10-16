package main

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// --mix-only без подмеса — отказ, а не пустые строки: пустая выдача читалась
// бы замером как «поиск ничего не нашёл» (этап 112, О7).
func TestAskMixOnlyNeedsMix(t *testing.T) {
	cfg := config.Default()
	srv := &config.Server{Name: "проба", URL: "http://ollama.example:11434"}
	err := runAsk(cfg, srv, "модель", askOpts{Question: "вопрос", Mix: "off", MixOnly: true})
	if err == nil || !strings.Contains(err.Error(), "--mix-only") {
		t.Fatalf("ждали отказ про --mix-only, получено %v", err)
	}
}

// Строка JSON говорит, что модель не спрашивали: иначе пустой ответ через
// неделю не отличить от сбоя.
func TestAskMixOnlyRecorded(t *testing.T) {
	cfg := config.Default()
	o := askOpts{Mix: "books", MixOnly: true}
	m := askSettingsMap(o, askSettings(cfg, o), cfg)
	if m["mix_only"] != true {
		t.Errorf("mix_only не записан: %v", m)
	}
}
