package graph

import (
	"context"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// tripleGraph — граф для индекса троек: «горутина —использует→ канал»
// подтверждена двумя источниками (куски 1 и 5 одной книги), «канал —часть→
// рантайм» — одним (куски 7 и 8 подряд — один источник, зона перекрытия).
func tripleGraph(t *testing.T) (*Graph, uint32, uint32, uint32) {
	t.Helper()
	g := newGraphWith(t, "горутина", "канал", "рантайм")
	for _, e := range []Edge{
		{Src: 1, Dst: 2, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}},
		{Src: 1, Dst: 2, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 5}},
		{Src: 2, Dst: 3, Type: RelPart, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 7}},
		{Src: 2, Dst: 3, Type: RelPart, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 8}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for id := uint32(1); id <= 3; id++ {
		for ord := uint32(1); ord <= 3; ord++ {
			if err := g.Mentions().Add(id, ChunkKey{Doc: 1, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return g, 1, 2, 3
}

// В индекс попадают только тройки с числом источников не ниже порога;
// соседние куски одной книги — один источник.
func TestTriplesByOrigins(t *testing.T) {
	g, a, b, _ := tripleGraph(t)
	tr := g.Triples(2)
	if len(tr) != 1 || tr[0].Key != (EdgeKey{Src: a, Dst: b, Type: RelUses}) || tr[0].Origins != 2 {
		t.Fatalf("ожидалась одна тройка с двумя источниками, получено %+v", tr)
	}
	if want := "горутина —использует→ канал"; tr[0].Text != want {
		t.Errorf("текст тройки %q, ожидался %q", tr[0].Text, want)
	}
	if all := g.Triples(1); len(all) != 2 {
		t.Errorf("с порогом 1 ожидались обе тройки, получено %d", len(all))
	}
}

// Счёт пишет индекс на диск, повторное открытие его читает, повторный счёт
// ничего не пересчитывает, а изменившийся текст конца (новый синоним)
// делает тройку устаревшей и пересчитывается одной записью поверх.
func TestEmbedTriplesPersistsAndTracksStale(t *testing.T) {
	g, a, b, _ := tripleGraph(t)
	emb := &fakeEmbedder{model: "проба"}
	ctx := context.Background()
	if err := g.EmbedTriples(ctx, emb, 2, EmbedOpts{Batch: 8, Checkpoint: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 1 || len(emb.seen) != 1 {
		t.Fatalf("ожидался один вызов на одну тройку, вызовов %d, текстов %v", emb.calls, emb.seen)
	}
	info := g.EdgeVectorsInfo()
	if !info.Ready || info.Count != 1 || info.Dim != 4 {
		t.Fatalf("индекс после счёта: %+v", info)
	}

	// Перечитанный с диска индекс тот же.
	re := openEdgeVectors(g.dir)
	if !re.Ready() || re.Count() != 1 || re.Problem() != "" {
		t.Fatalf("индекс с диска: готов=%v, троек %d, беда %q", re.Ready(), re.Count(), re.Problem())
	}
	if _, ok := re.index[EdgeKey{Src: a, Dst: b, Type: RelUses}]; !ok {
		t.Fatalf("в индексе с диска нет ключа тройки: %+v", re.index)
	}

	// Повторный счёт — ничего не считает.
	if err := g.EmbedTriples(ctx, emb, 2, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 1 {
		t.Fatalf("повторный счёт позвал эмбеддер: вызовов %d", emb.calls)
	}

	// Новый синоним у конца меняет текст тройки — она устарела.
	if err := g.Entities().AddAliases(a, "goroutine"); err != nil {
		t.Fatal(err)
	}
	if want, missing, stale := g.StaleTriples(2); want != 1 || missing != 0 || stale != 1 {
		t.Fatalf("после синонима: троек %d, нет %d, устарели %d", want, missing, stale)
	}
	if err := g.EmbedTriples(ctx, emb, 2, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 2 || emb.seen[1] != "горутина, goroutine —использует→ канал" {
		t.Fatalf("устаревшая тройка не пересчитана: вызовов %d, тексты %v", emb.calls, emb.seen)
	}
	if _, _, stale := g.StaleTriples(2); stale != 0 {
		t.Fatalf("после пересчёта тройка всё ещё устаревшая")
	}
	// Перекрытая запись перепакована: в файле одна запись, не две.
	if info := g.EdgeVectorsInfo(); info.Count != 1 || info.Rows != 1 {
		t.Fatalf("после перепаковки: троек %d, записей %d", info.Count, info.Rows)
	}
	if re := openEdgeVectors(g.dir); re.Count() != 1 || re.Problem() != "" {
		t.Fatalf("индекс с диска после перепаковки: троек %d, беда %q", re.Count(), re.Problem())
	}
}

// Склейка конца переводит тройку к выжившему: прежний ключ уходит из отбора,
// новый — недостающий; счёт дописывает его, а старый вычищает.
func TestTriplesFollowMerges(t *testing.T) {
	g, a, b, c := tripleGraph(t)
	_ = c
	emb := &fakeEmbedder{model: "проба"}
	ctx := context.Background()
	if err := g.EmbedTriples(ctx, emb, 2, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	// «канал» поглощён «рантаймом»: тройка становится «горутина → рантайм».
	if _, err := g.Merges().Add([]MergeRec{{From: b, To: c}}); err != nil {
		t.Fatal(err)
	}
	want, missing, _ := g.StaleTriples(2)
	if want != 1 || missing != 1 {
		t.Fatalf("после склейки: троек %d, недостающих %d", want, missing)
	}
	if err := g.EmbedTriples(ctx, emb, 2, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	if emb.seen[len(emb.seen)-1] != "горутина —использует→ рантайм" {
		t.Fatalf("тройка после склейки: %q", emb.seen[len(emb.seen)-1])
	}
	if info := g.EdgeVectorsInfo(); info.Count != 1 {
		t.Fatalf("после склейки и перепаковки ожидалась одна тройка, %+v", info)
	}
	if _, ok := g.evecs.index[EdgeKey{Src: a, Dst: c, Type: RelUses}]; !ok {
		t.Fatalf("в индексе нет тройки к выжившему: %+v", g.evecs.index)
	}
}

// Вход по тройкам: при TripleLimit > 0 концы ближайшей тройки попадают
// в выдачу с пометкой «по связи»; при 0 индекс не читается вовсе.
func TestSearchTripleEntry(t *testing.T) {
	g, _, _, _ := tripleGraph(t)
	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedTriples(context.Background(), emb, 2, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	// Вектор вопроса — «такой же», как у тройки (fakeEmbedder кодирует длину).
	vec, _ := emb.Embed(context.Background(), []string{"горутина —использует→ канал"})
	q := kb.Quantize(vec[0])

	off := g.Search("вопрос ни о чём", SearchOpts{TopEntities: 6, QueryVector: q})
	if len(off.Entities) != 0 {
		t.Fatalf("при выключенном входе по тройкам выдача не пуста: %+v", off.Entities)
	}

	g.rules.TripleLimit = 2
	on := g.Search("вопрос ни о чём", SearchOpts{TopEntities: 6, QueryVector: q})
	if len(on.Entities) != 2 {
		t.Fatalf("ожидались два конца тройки, получено %d: %+v", len(on.Entities), on.Entities)
	}
	for _, e := range on.Entities {
		if e.Matched != "по связи" {
			t.Errorf("понятие %s помечено %q, ожидалось «по связи»", e.Name, e.Matched)
		}
	}
	names := on.Entities[0].Name + "/" + on.Entities[1].Name
	if names != "горутина/канал" {
		t.Errorf("концы тройки: %s", names)
	}

	// Без индекса на диске вход молчит и ничего не ломает.
	g2 := newGraphWith(t, "одно")
	g2.rules.TripleLimit = 2
	if res := g2.Search("одно", SearchOpts{TopEntities: 6, QueryVector: q}); len(res.Entities) != 1 {
		t.Fatalf("граф без индекса троек: %+v", res.Entities)
	}
}
