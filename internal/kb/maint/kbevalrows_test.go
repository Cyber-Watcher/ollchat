package maint

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// --kb-eval-out: строка на вопрос, поля kb.EvalRow плюс режим; русский текст
// и ссылки «books/7#12» пишутся как есть, без экранирования.
func TestWriteEvalRows(t *testing.T) {
	rows := []kb.EvalRow{
		{Query: "что такое <goroutine> & канал", Rank: 1, RankNear: 1, Top1: 2.5, Gap: 0.4,
			IDs: []string{"books/7#12"}, Scores: []float64{2.5}},
		{Query: "борщ", Note: "далёкий", IDs: []string{}, Scores: []float64{}},
	}
	var buf bytes.Buffer
	if err := writeEvalRows(&buf, kb.EvalFusion, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(rows) {
		t.Fatalf("строк %d, ждали %d:\n%s", len(lines), len(rows), buf.String())
	}
	if !strings.Contains(lines[0], "<goroutine> & канал") {
		t.Errorf("текст вопроса экранирован или искажён: %s", lines[0])
	}
	for i, l := range lines {
		var got struct {
			Mode string `json:"mode"`
			kb.EvalRow
		}
		if err := json.Unmarshal([]byte(l), &got); err != nil {
			t.Fatalf("строка %d не разбирается: %v\n%s", i, err, l)
		}
		if got.Mode != string(kb.EvalFusion) {
			t.Errorf("строка %d: режим %q, ждали %q", i, got.Mode, kb.EvalFusion)
		}
		if got.Query != rows[i].Query || got.Rank != rows[i].Rank || got.Note != rows[i].Note {
			t.Errorf("строка %d разобрана как %+v, писали %+v", i, got.EvalRow, rows[i])
		}
	}
}
