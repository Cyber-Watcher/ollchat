package graph

import "testing"

// orphanFixture — три вида понятий сразу:
//   - «канал» и «горутина» связаны между собой (не одиночки);
//   - «сноска» извлечена без единой связи (случай 1: модель не назвала отношений);
//   - «GC» и «сборка мусора» — двойники, связанные только друг с другом:
//     после склейки связь между ними становится петлёй, и выживший остаётся
//     без связей (случай 2).
func orphanFixture(t *testing.T) (*Graph, uint32, uint32) {
	t.Helper()
	g := newGraphWith(t)
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ch := add("канал")
	gor := add("горутина")
	note := add("сноска")
	gc := add("GC")
	gcru := add("сборка мусора")

	for _, e := range []Edge{
		{Src: gor, Dst: ch, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}},
		{Src: gc, Dst: gcru, Type: RelRelated, Weight: 1, Evidence: ChunkKey{Doc: 2, Ord: 1}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	// «Сноска» упомянута один раз и не связана ни с чем.
	if err := g.Mentions().Add(note, ChunkKey{Doc: 1, Ord: 5}); err != nil {
		t.Fatal(err)
	}
	return g, note, gc
}

func TestOrphansFindsUnlinkedEntity(t *testing.T) {
	g, note, _ := orphanFixture(t)
	defer g.Close()

	o := g.Orphans(10)
	if o.Total != 1 {
		t.Fatalf("одиночек %d, ожидался 1 (сноска): %+v", o.Total, o.Sample)
	}
	if o.NoRawEdges != 1 || o.LostToMerge != 0 {
		t.Errorf("причина определена неверно: без записей %d, потеряно склейкой %d",
			o.NoRawEdges, o.LostToMerge)
	}
	if o.Sample[0].ID != note {
		t.Errorf("в примерах не то понятие: %+v", o.Sample[0])
	}
	if o.Mentioned != 1 || o.SingleMention != 1 {
		t.Errorf("упоминания посчитаны неверно: %d и %d", o.Mentioned, o.SingleMention)
	}
}

// После склейки двойников, связанных только друг с другом, выживший остаётся
// без связей — и разбор обязан назвать причиной склейку, а не пустое извлечение.
func TestOrphansSeparatesMergeLoss(t *testing.T) {
	g, _, gc := orphanFixture(t)
	defer g.Close()

	survivor := gc
	if _, err := g.Merges().Add([]MergeRec{{To: survivor, From: survivor + 1, Level: "mutual"}}); err != nil {
		t.Fatal(err)
	}

	o := g.Orphans(10)
	if o.LostToMerge != 1 {
		t.Fatalf("потерь на склейке %d, ожидалась 1: %+v", o.LostToMerge, o.Sample)
	}
	if o.Survivors != 1 {
		t.Errorf("выживших среди одиночек %d, ожидался 1", o.Survivors)
	}
	// «Сноска» никуда не делась и по-прежнему числится пустым извлечением.
	if o.NoRawEdges != 1 || o.Total != 2 {
		t.Errorf("разбор сбился: всего %d, без записей %d", o.Total, o.NoRawEdges)
	}
}

// Примеры идут по убыванию упоминаний, а их число ограничено запрошенным.
func TestOrphansSampleLimited(t *testing.T) {
	g, _, _ := orphanFixture(t)
	defer g.Close()

	if o := g.Orphans(0); len(o.Sample) > 20 {
		t.Fatalf("умолчание не ограничило примеры: %d", len(o.Sample))
	}
}

// Пустой узел — без упоминаний и без связей — смысловым входом не предлагается:
// показать по нему нечего. Понятие с одним упоминанием остаётся: /search ищет
// как раз редкое. По слову пустой узел по-прежнему находится — имя назвал человек.
func TestEmptyNodeHiddenFromSearch(t *testing.T) {
	g, note, _ := orphanFixture(t)
	defer g.Close()

	empty, _, err := g.Entities().Add("Adobe Minion Pro", TypeTool)
	if err != nil {
		t.Fatal(err)
	}
	if !g.emptyNode(empty, 0) {
		t.Error("понятие без упоминаний и связей не признано пустым")
	}
	// «Сноска» упомянута один раз — она не пустая, хоть и без связей.
	if g.emptyNode(note, 1) {
		t.Error("понятие с упоминанием признано пустым")
	}
	// Связанное понятие не пустое и без упоминаний: по нему есть сосед.
	ch, ok := g.Entities().Lookup("канал")
	if !ok {
		t.Fatal("понятие «канал» пропало из проверки")
	}
	if g.emptyNode(ch.ID, 0) {
		t.Error("связанное понятие признано пустым")
	}
}
