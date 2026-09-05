package tools

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Без базы знаний и графа понятий для дополнения нет, и запрос обязан
// вернуться нетронутым: перевод улучшает выдачу, но поиск без него работает.
func TestExpandWithoutGraphKeepsQuery(t *testing.T) {
	ents := graphEntities(Options{}, "", "горутина", 3)
	if len(ents) != 0 {
		t.Fatalf("без графа понятий быть не должно: %v", ents)
	}
	if got := find.Expand("горутина", ents, find.Opts{ExpandLimit: 3}); got != "горутина" {
		t.Fatalf("запрос изменился без графа: %q", got)
	}
}

// graphWithVectors — пустой граф с паспортом векторов под заданную модель:
// ровно столько, сколько нужно, чтобы вход по смыслу считался возможным.
func graphWithVectors(t *testing.T, model string, dim int) *graph.Graph {
	t.Helper()
	g, err := graph.Create(t.TempDir(), "t", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	if err := g.SetVectorsForTest(model, dim); err != nil {
		t.Fatal(err)
	}
	return g
}
