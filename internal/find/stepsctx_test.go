package find

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Журнал шагов, пришедший с контекстом вызова, получает запись поиска с ходом
// (этап 112, Ч17). До 16.10.2026 инструмент поиска журнала в Deps не нёс, и
// записей `search` в журналах не было ни одной — отчёт о пробелах библиотеки
// (`stepstat.py --gaps`) считать было не из чего.
func TestSearchWritesStepFromContext(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Create("probe", "проба журнала")
	if err != nil {
		t.Fatal(err)
	}
	book := filepath.Join(t.TempDir(), "book.md")
	if err := os.WriteFile(book, []byte("# Глава\n\nСемафор ограничивает число одновременных вызовов.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{book}, kb.IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := steplog.New(t.TempDir(), pat, time.Now(), "test", true)
	defer w.Close()

	ctx := steplog.NewContext(context.Background(), w, "k7f3-02")
	if _, err := Search(ctx, Deps{Coll: coll}, "семафор", Opts{Collection: "probe"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		s, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind == steplog.KindSearch {
			found = true
			if s.Turn != "k7f3-02" || s.Args != "семафор" {
				t.Errorf("запись поиска: ход %q, запрос %q", s.Turn, s.Args)
			}
			if _, ok := s.Extra["top1"]; !ok {
				t.Errorf("в записи поиска нет top1: %+v", s.Extra)
			}
		}
	}
	if !found {
		t.Fatalf("записи search в журнале нет: %s", data)
	}

	// Без журнала в контексте и в Deps — молча ничего, как раньше.
	if got, _ := steplog.FromContext(context.Background()); got != nil {
		t.Error("пустой контекст вернул журнал")
	}
	if got, _ := steplog.FromContext(steplog.NewContext(context.Background(), nil, "x")); got != nil {
		t.Error("выключенный журнал (nil) попал в контекст")
	}
}
