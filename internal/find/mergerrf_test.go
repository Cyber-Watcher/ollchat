package find

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// TestMergeRRF — слияние выдержек книг с подтверждениями графа (этап 112, П7):
// без MergeRRFK граф дописывается в хвост, с ним оба списка голосуют на равных,
// и кусок, найденный обоими, поднимается вверх.
func TestMergeRRF(t *testing.T) {
	hits := []kb.Result{{ID: "b/1#1", Text: "A"}, {ID: "b/1#2", Text: "B"}, {ID: "b/1#3", Text: "C"}}
	fromG := []Excerpt{{ID: "b/9#1", Graph: true}, {ID: "b/1#2", Graph: true}, {ID: "b/9#2", Graph: true}}
	ids := func(ex []Excerpt) []string {
		var out []string
		for _, e := range ex {
			out = append(out, e.ID)
		}
		return out
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, ждали %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, ждали %v", name, got, want)
			}
		}
	}

	eq("дописывание", ids(merge(hits, fromG, Opts{TopK: 4})), []string{"b/1#1", "b/1#2", "b/1#3", "b/9#1"})

	// k = 60: B — 2/62; A и X — по 1/61 (ничья, книги впереди); C и Y — по 1/63.
	got := merge(hits, fromG, Opts{TopK: 4, MergeRRFK: 60})
	eq("RRF k=60", ids(got), []string{"b/1#2", "b/1#1", "b/9#1", "b/1#3"})
	if got[0].Graph || got[0].Text != "B" {
		t.Fatalf("общий кусок должен остаться выдержкой книги: %+v", got[0])
	}
}
