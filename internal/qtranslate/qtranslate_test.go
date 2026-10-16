package qtranslate

import (
	"context"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

func TestClean(t *testing.T) {
	cases := map[string]string{
		"What is a goroutine?":                  "What is a goroutine?",
		"\n  \"What is a goroutine?\"  \nnote":  "What is a goroutine?",
		"Translation: How does `EXPLAIN` work?": "How does `EXPLAIN` work?",
		"«Как?»":                                "Как?",
		"":                                      "",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestHasCyrillic(t *testing.T) {
	if !HasCyrillic("что такое goroutine") || !HasCyrillic("Ёж") {
		t.Error("кириллица не опознана")
	}
	if HasCyrillic("what is a goroutine?") || HasCyrillic("") {
		t.Error("кириллица найдена там, где её нет")
	}
}

// Выключенный перевод (по умолчанию) — nil, и вызовы на nil безопасны:
// поиск зовёт Translate, не проверяя, включён ли он.
func TestOffByDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.KB.Translate {
		t.Fatal("kb.translate включён по умолчанию — ждали выключенным")
	}
	tr := New(cfg, "http://198.51.100.10:11434", nil)
	if tr != nil {
		t.Fatalf("при kb.translate = false переводчик создан: %v", tr)
	}
	en, err := tr.Translate(context.Background(), "что такое горутина?")
	if en != "" || err != nil || tr.Model() != "" {
		t.Errorf("nil-переводчик ответил %q, %v, модель %q", en, err, tr.Model())
	}
}

// Включённый: модель — kb.translate_model, а без неё graph.summary_model.
// Английский вопрос не переводится и сервер не трогается (адрес заведомо
// несуществующий: дошло бы до сети — тест упал бы по сроку).
func TestModelChoiceAndSkip(t *testing.T) {
	cfg := config.Default()
	cfg.KB.Translate = true
	cfg.Graph.Model = "extract-model"
	cfg.Graph.SummaryModel = "summary-model"
	tr := New(cfg, "http://198.51.100.10:11434", nil)
	if tr == nil || tr.Model() != "summary-model" {
		t.Fatalf("без translate_model ждали summary-model, получили %v", tr.Model())
	}
	cfg.KB.TranslateModel = "translate-model"
	if tr := New(cfg, "http://198.51.100.10:11434", nil); tr.Model() != "translate-model" {
		t.Errorf("ждали translate-model, получили %q", tr.Model())
	}
	en, err := tr.Translate(context.Background(), "what is a goroutine?")
	if en != "" || err != nil {
		t.Errorf("английский вопрос ушёл в перевод: %q, %v", en, err)
	}
}
