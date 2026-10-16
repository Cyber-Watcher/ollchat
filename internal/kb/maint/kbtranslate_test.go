package maint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Кэш переводов: нет файла — пустой кэш; битые и неполные строки пропускаются.
func TestLoadTranslations(t *testing.T) {
	dir := t.TempDir()
	got, err := loadTranslations(filepath.Join(dir, "нет.jsonl"))
	if err != nil || len(got) != 0 {
		t.Fatalf("нет файла: %v, %v — ждали пустой кэш без ошибки", got, err)
	}
	path := filepath.Join(dir, "en.jsonl")
	data := `{"q":"Что такое горутина?","en":"What is a goroutine?"}
не json
{"q":"без перевода","en":""}
{"q":"Как работает канал?","en":"How does a channel work?"}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = loadTranslations(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["Что такое горутина?"] != "What is a goroutine?" {
		t.Errorf("кэш %v, ждали два перевода", got)
	}
}

// Всё уже в кэше — модель не нужна: переводить нечего, и ни конфиг модели,
// ни сервер не трогаются.
func TestTranslateMissingNothingToDo(t *testing.T) {
	have := map[string]string{"вопрос": "question"}
	cases := []kb.EvalCase{{Query: "вопрос"}, {Query: "вопрос"}}
	cfg := config.Default()
	if err := translateMissing(context.Background(), cfg, filepath.Join(t.TempDir(), "en.jsonl"), cases, have, 0); err != nil {
		t.Errorf("при полном кэше ошибка: %v", err)
	}
}
