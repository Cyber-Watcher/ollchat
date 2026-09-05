package graph

import (
	"testing"
)

// Отброшенная книга исчезает из выдачи, но реестр и журналы целы; откат
// возвращает её. Всё через журнал dropped-books.jsonl, дозаписью.
func TestDropAndRestoreBook(t *testing.T) {
	coll := t.TempDir()
	g, err := Create(coll, "t", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if g.Dropped().Dropped(10) {
		t.Fatal("книга 10 не должна быть отброшена изначально")
	}

	if err := g.DropBook(10, "/path/bad.pdf", "скан"); err != nil {
		t.Fatal(err)
	}
	if !g.Dropped().Dropped(10) || g.Dropped().Count() != 1 {
		t.Fatalf("книга 10 не отброшена: %v", g.Dropped().Books())
	}

	// Переоткрытие графа: решение переживает закрытие — оно в журнале.
	_ = g.Close()
	g2, err := Open(coll, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if !g2.Dropped().Dropped(10) {
		t.Fatal("отбрасывание не пережило переоткрытие графа")
	}

	// Откат — обратная запись; последняя по книге побеждает.
	if err := g2.RestoreBook(10, "/path/bad.pdf"); err != nil {
		t.Fatal(err)
	}
	if g2.Dropped().Dropped(10) || g2.Dropped().Count() != 0 {
		t.Fatalf("возврат книги не сработал: %v", g2.Dropped().Books())
	}
}

// Куски отброшенной книги не попадают в подтверждения выдачи.
func TestDroppedBookHiddenFromEvidence(t *testing.T) {
	coll := t.TempDir()
	g, _ := Create(coll, "t", 100, Rules{})
	defer g.Close()
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(g.Mentions().Add(1, ChunkKey{Doc: 10, Ord: 1})) // книга 10
	must(g.Mentions().Add(1, ChunkKey{Doc: 20, Ord: 1})) // книга 20

	seeds := []FoundEntity{{Entity: Entity{ID: 1}}}
	ev := g.evidence(seeds, 10)
	if len(ev) != 2 {
		t.Fatalf("до отбрасывания подтверждений %d, ожидалось 2", len(ev))
	}
	must(g.DropBook(10, "", ""))
	ev = g.evidence(seeds, 10)
	if len(ev) != 1 || ev[0].Doc != 20 {
		t.Fatalf("после отбрасывания книги 10 подтверждения: %v", ev)
	}
}
