package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Забытый кусок уносит свои упоминания и подтверждения, отметка становится
// «пропущен», счётчики понятий пересчитываются, номера не меняются.
func TestForgetChunksRemovesTracesAndKeepsIDs(t *testing.T) {
	g, collDir := graph(t)
	dir := g.Dir()
	a, _, _ := g.Entities().Add("CoreDNS", TypeTech)
	b, _, _ := g.Entities().Add("Corefile", TypeTech)
	toc := ChunkKey{Doc: 104, Ord: 21}
	prose := ChunkKey{Doc: 104, Ord: 682}
	other := ChunkKey{Doc: 105, Ord: 732}
	for _, k := range []ChunkKey{toc, prose, other} {
		must(t, g.Mentions().Add(a, k))
		must(t, g.Progress().Mark(k, MarkDone))
	}
	must(t, g.Mentions().Add(b, toc)) // Corefile — только в оглавлении
	g.Entities().Touch(a, true)
	g.Entities().Touch(a, true)
	g.Entities().Touch(a, false)
	g.Entities().Touch(b, true)
	must(t, g.Edges().Add(Edge{Src: a, Dst: b, Type: RelRelated, Weight: 1, Evidence: toc}))
	must(t, g.Edges().Add(Edge{Src: a, Dst: b, Type: RelRelated, Weight: 1, Evidence: prose}))
	must(t, g.Entities().SaveCounters())
	must(t, g.Close())

	drop := func(k ChunkKey) bool { return k == toc }
	dry, err := ForgetChunks(dir, drop, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Mentions != 2 || dry.Edges != 1 || dry.Marks != 1 || len(dry.Backups) != 0 {
		t.Fatalf("сухой прогон: %+v", dry)
	}
	st, err := ForgetChunks(dir, drop, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Chunks != 1 || st.Mentions != 2 || st.Edges != 1 || st.Marks != 1 || st.Orphans != 1 {
		t.Fatalf("чистка: %+v", st)
	}
	if len(st.Backups) != 4 {
		t.Fatalf("резервных копий %d, ожидалось 4: %v", len(st.Backups), st.Backups)
	}
	for _, bk := range st.Backups {
		if _, err := os.Stat(bk); err != nil || !strings.Contains(filepath.Base(bk), ".bak-") {
			t.Errorf("резервная копия %s: %v", bk, err)
		}
	}

	g2, err := Open(collDir, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if n := len(g2.Mentions().Of(a)); n != 2 {
		t.Errorf("у CoreDNS осталось %d упоминаний, ожидалось 2", n)
	}
	if n := len(g2.Mentions().Of(b)); n != 0 {
		t.Errorf("у Corefile осталось %d упоминаний, ожидалось 0", n)
	}
	if g2.Edges().Count() != 1 {
		t.Errorf("связей %d, ожидалась 1", g2.Edges().Count())
	}
	if m, ok := g2.Progress().MarkOf(toc); !ok || m != MarkService {
		t.Errorf("отметка оглавления %v %v, ожидался MarkService", m, ok)
	}
	if m, _ := g2.Progress().MarkOf(prose); m != MarkDone {
		t.Errorf("отметка обычного куска сбита: %v", m)
	}
	ea, _ := g2.Entities().Get(a)
	eb, _ := g2.Entities().Get(b)
	if ea.ID != a || eb.ID != b {
		t.Fatalf("номера понятий сдвинулись: %d %d", ea.ID, eb.ID)
	}
	// count — по оставшимся упоминаниям; docs не трогается (сборка его не ведёт).
	if ea.Count != 2 || ea.Docs != 2 {
		t.Errorf("CoreDNS: упоминаний %d, книг %d — ожидалось 2 и 2", ea.Count, ea.Docs)
	}
	if eb.Count != 0 || eb.Docs != 1 {
		t.Errorf("Corefile: упоминаний %d, книг %d — ожидалось 0 и 1", eb.Count, eb.Docs)
	}
	// Повторная чистка ничего не находит и файлов не трогает.
	again, err := ForgetChunks(dir, drop, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Mentions != 0 || again.Edges != 0 || again.Marks != 0 || len(again.Backups) != 0 {
		t.Fatalf("повтор: %+v", again)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Судьба забытого куска выбирается: «разобрать заново» или «не разбирать».
func TestForgetChunksAsChoosesFate(t *testing.T) {
	g, _ := graph(t)
	dir := g.Dir()
	a, _, _ := g.Entities().Add("Relation extraction", TypeConcept)
	junk := ChunkKey{Doc: 7, Ord: 1}
	redo := ChunkKey{Doc: 7, Ord: 2}
	for _, k := range []ChunkKey{junk, redo} {
		must(t, g.Mentions().Add(a, k))
		must(t, g.Progress().Mark(k, MarkDone))
	}
	must(t, g.Entities().SaveCounters())
	must(t, g.Close())

	if _, err := ForgetChunksAs(dir, func(k ChunkKey) bool { return k == junk }, MarkSkipped, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ForgetChunksAs(dir, func(k ChunkKey) bool { return k == redo }, MarkService, false); err != nil {
		t.Fatal(err)
	}
	// «Разобран» и «пусто» при забытом содержимом — ложь о состоянии графа.
	for _, bad := range []uint32{MarkDone, MarkEmpty, 0} {
		if _, err := ForgetChunksAs(dir, func(ChunkKey) bool { return false }, bad, true); err == nil {
			t.Errorf("отметка %d принята, а должна быть отвергнута", bad)
		}
	}

	p, err := openProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if m, _ := p.MarkOf(junk); m != MarkSkipped {
		t.Errorf("мусорный кусок: отметка %d, ожидалась «пропущен»", m)
	}
	if m, _ := p.MarkOf(redo); m != MarkService {
		t.Errorf("кусок к повтору: отметка %d, ожидалась «служебный»", m)
	}
}

func TestParseChunkKey(t *testing.T) {
	k, err := ParseChunkKey(" 12#37 ")
	if err != nil || k != (ChunkKey{Doc: 12, Ord: 37}) || k.String() != "12#37" {
		t.Fatalf("разбор: %+v %v", k, err)
	}
	for _, bad := range []string{"", "12", "12#", "#37", "a#b", "12#37#1", "-1#2"} {
		if _, err := ParseChunkKey(bad); err == nil {
			t.Errorf("%q принято, а должно быть отвергнуто", bad)
		}
	}
}
