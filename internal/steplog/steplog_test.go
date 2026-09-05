package steplog

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
)

func TestWriteAndParse(t *testing.T) {
	dir := t.TempDir()
	pat, err := chatlog.ParsePattern("steps-%Y-%m-%d_%H-%M-%S.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := New(dir, pat, time.Date(2026, 9, 3, 22, 0, 0, 0, time.Local), "test", true)
	long := strings.Repeat("я", 600)
	w.Write(Step{Turn: "k7f3-01", Step: 2, Kind: KindTool, Tool: "kb_search",
		Args: long, Outcome: OutcomeRejected, MS: 12, Extra: map[string]any{"field": "top_k"}})
	w.Write(Step{Kind: KindChat, Model: "m", TokensIn: 10, TokensOut: 5, MS: 300})
	if err := w.LastError(); err != nil {
		t.Fatalf("ошибка записи: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(w.Path(), "steps-2026-09-03_22-00-00.jsonl") {
		t.Fatalf("имя файла: %s", w.Path())
	}

	f, err := os.Open(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var steps []Step
	for sc.Scan() {
		s, err := Parse(sc.Bytes())
		if err != nil {
			t.Fatalf("разбор строки: %v", err)
		}
		steps = append(steps, s)
	}
	if len(steps) != 2 {
		t.Fatalf("строк %d, ожидалось 2", len(steps))
	}
	if steps[0].Tool != "kb_search" || steps[0].Outcome != OutcomeRejected || steps[0].Src != "test" {
		t.Fatalf("первая строка: %+v", steps[0])
	}
	if len(steps[0].Args) > MaxArgs+4 || !strings.HasSuffix(steps[0].Args, "…") {
		t.Fatalf("аргументы не обрезаны: %d байт", len(steps[0].Args))
	}
	if steps[0].TS.IsZero() {
		t.Fatal("время не проставлено")
	}
	if steps[1].TokensIn != 10 || steps[1].Kind != KindChat {
		t.Fatalf("вторая строка: %+v", steps[1])
	}
}

func TestNilWriterIsSafe(t *testing.T) {
	var w *Writer
	w.Write(Step{Kind: KindChat})
	if w.LastError() != nil || w.Path() != "" || w.Close() != nil {
		t.Fatal("nil-журнал должен молчать")
	}
	if New("", nil, time.Now(), "x", true) != nil {
		t.Fatal("без шаблона журнал не заводится")
	}
}

func TestParseRejectsNoKind(t *testing.T) {
	if _, err := Parse([]byte(`{"ts":"2026-09-03T22:00:00Z"}`)); err == nil {
		t.Fatal("строка без kind должна отклоняться")
	}
}
