package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Строки окна собираются из журнала связываний: очередь — «?», судейский
// режим — «ДА» арбитра; вес узлов — по реестру, кандидат — по имени.
func TestReviewItemsFromLinksJournal(t *testing.T) {
	g, err := graph.Create(t.TempDir(), "books", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	gc, _, _ := g.Entities().Add("Garbage collection", graph.TypeConcept)
	sm, _, _ := g.Entities().Add("сборщик мусора", graph.TypeConcept)
	if err := g.Links().Add(graph.LinkRec{Norm: "сборщик мусора", Name: "сборщик мусора", From: sm, Cand: gc,
		CandName: "Garbage collection", Cos: 0.91, Verdict: graph.LinkDoubt, By: graph.LinkByJudge, Source: graph.LinkFromDoubles}); err != nil {
		t.Fatal(err)
	}
	if err := g.Links().Add(graph.LinkRec{Norm: "gc", Name: "GC", To: gc, ToName: "Garbage collection",
		Cos: 0.97, Verdict: graph.LinkYes, By: graph.LinkByJudge, Source: graph.LinkFromBuild}); err != nil {
		t.Fatal(err)
	}

	q := reviewItems(g, nil, false)
	if len(q) != 1 || !strings.Contains(q[0].from, "сборщик мусора") || !strings.Contains(q[0].to, "Garbage collection") {
		t.Fatalf("очередь: %+v", q)
	}
	j := reviewItems(g, nil, true)
	if len(j) != 1 || j[0].rec.Name != "GC" || !strings.Contains(j[0].to, "Garbage collection") {
		t.Fatalf("судейский режим: %+v", j)
	}

	// Окно рисуется и клавиши ходят по списку; Enter без куска-источника
	// не читает ничего, а говорит об этом.
	m := &Model{review: &reviewPanel{coll: "books", g: g, items: q}}
	out := m.review.view(100)
	if !strings.Contains(out, "разбор пар") || !strings.Contains(out, "←?→") {
		t.Fatalf("окно: %q", out)
	}
	if taken, id := m.handleReviewKey("enter"); !taken || id != "" || m.statusMsg == "" {
		t.Fatalf("Enter без куска: taken=%v id=%q status=%q", taken, id, m.statusMsg)
	}
	// Решение «ДА» склеивает узлы и убирает пару из списка.
	m.decideReview(graph.LinkYes)
	if len(m.review.items) != 0 || m.review.done != 1 {
		t.Fatalf("после решения: %d пар, решено %d", len(m.review.items), m.review.done)
	}
	if to := g.Merges().Resolve(sm); to != gc {
		t.Fatalf("склейка не записана: %d → %d", sm, to)
	}
	if len(reviewItems(g, nil, false)) != 0 {
		t.Fatal("очередь не опустела после решения")
	}
}
