package mixer

import (
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

func TestBooksQueryAddsEntityNames(t *testing.T) {
	// Синонимы лежат в поле FoundEntity: в выдачу они попадают уже проверенными
	// (graph.Entities.TrustedAliases) — без чужих собственных имён, переводы
	// впереди. Брать их из записи понятия нельзя: там лежит всё подряд.
	ents := []graph.FoundEntity{
		{Entity: graph.Entity{Name: "RAG", Norm: graph.Normalize("RAG")},
			AliasesSafe: []string{"Retrieval-Augmented Generation"}},
		{Entity: graph.Entity{Name: "дообучение", Norm: graph.Normalize("дообучение")}},
	}
	got := find.Expand("чем RAG отличается от дообучения", ents, find.Opts{ExpandLimit: 3})
	for _, want := range []string{"чем RAG отличается", "RAG", "Retrieval-Augmented Generation", "дообучение"} {
		if !strings.Contains(got, want) {
			t.Fatalf("в запросе нет %q: %q", want, got)
		}
	}
	// Без понятий запрос остаётся вопросом.
	if got := find.Expand("вопрос", nil, find.Opts{ExpandLimit: 3}); got != "вопрос" {
		t.Fatalf("пустой список понятий изменил запрос: %q", got)
	}
}

// TestMixGatekeeper — главное свойство подмешивания: вопрос, не связанный
// ни с одним понятием графа, не стоит ни одного токена.
