package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Номер понятия, выброшенного уплотнением с хвоста реестра, новому понятию
// не достаётся: иначе оно унаследовало бы упоминания, вектор, синонимы,
// описание и запреты прежнего (аудит 07.10.2026, №12).
func TestCompactDropDoesNotReuseTailIDs(t *testing.T) {
	g := newGraphWith(t, "живое", "связанное", "мёртвое")
	live, linked, dead := uint32(1), uint32(2), uint32(3)
	must(t, g.Mentions().Add(live, ChunkKey{Doc: 1, Ord: 1}))
	must(t, g.Edges().Add(Edge{Src: live, Dst: linked, Type: RelUses, Evidence: ChunkKey{Doc: 1, Ord: 1}}))
	if got := g.DeadEntities(); len(got) != 1 || got[0] != dead {
		t.Fatalf("мёртвыми названы %v", got)
	}
	collDir := filepath.Dir(g.Dir())
	must(t, g.Close())

	st, err := CompactDrop(collDir, "", false, false, map[uint32]bool{dead: true})
	if err != nil || !st.Applied || st.Dropped != 1 {
		t.Fatalf("уплотнение: %+v, %v", st, err)
	}
	g2, err := Open(collDir, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	id, isNew, err := g2.Entities().Add("новое", TypeConcept)
	if err != nil || !isNew {
		t.Fatalf("новое понятие: %d, %v, %v", id, isNew, err)
	}
	if id == dead {
		t.Fatalf("новому понятию выдан номер выброшенного №%d", dead)
	}
	if id != dead+1 {
		t.Errorf("новому понятию выдан №%d, ожидался №%d", id, dead+1)
	}
}

// Отметки уплотнения может не быть (граф уплотняли прежней программой):
// тогда граница номеров выводится из данных — здесь из числа векторов,
// посчитанных и для выброшенного понятия.
func TestNewIDsAboveVectors(t *testing.T) {
	g := newGraphWith(t, "живое", "связанное", "мёртвое")
	ctx := context.Background()
	must(t, g.Mentions().Add(1, ChunkKey{Doc: 1, Ord: 1}))
	must(t, g.Edges().Add(Edge{Src: 1, Dst: 2, Type: RelUses, Evidence: ChunkKey{Doc: 1, Ord: 1}}))
	emb := &countingEmbedder{model: "проба", dim: 4}
	if _, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil); err != nil {
		t.Fatal(err)
	}
	collDir := filepath.Dir(g.Dir())
	must(t, g.Close())
	if _, err := CompactDrop(collDir, "", false, false, map[uint32]bool{3: true}); err != nil {
		t.Fatal(err)
	}
	must(t, os.Remove(filepath.Join(collDir, DirName, entMaxIDFile))) // как у прежней программы

	g2, err := Open(collDir, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if id, _, _ := g2.Entities().Add("новое", TypeConcept); id != 4 {
		t.Fatalf("новому понятию выдан №%d, ожидался №4 — вектор №3 принадлежит выброшенному", id)
	}
	// Досчёт векторов видит выброшенный номер местом в файле, а не поводом
	// для полного пересчёта: дописывается ровно новое понятие.
	res, err := g2.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil)
	if err != nil {
		t.Fatalf("досчёт после уплотнения: %v", err)
	}
	if res.Before != 3 || res.Added != 1 {
		t.Errorf("досчёт: было %d, добавлено %d — ожидалось 3 и 1", res.Before, res.Added)
	}
}

// Упоминание понятия, чья запись не дошла до реестра (обрыв), держит номер:
// новое понятие его не займёт. А мусорный номер из журнала, прочитанного
// со сдвигом, границу не двигает — иначе реестр разросся бы под него.
func TestNewIDsSkipReferencedNumbers(t *testing.T) {
	g, coll := graph(t)
	a, _, _ := g.Entities().Add("Альфа", TypeConcept)
	must(t, g.Mentions().Add(a, ChunkKey{Doc: 1, Ord: 1}))
	must(t, g.Mentions().Add(5, ChunkKey{Doc: 1, Ord: 2}))           // записи №5 в реестре нет
	must(t, g.Mentions().Add(200_000_000, ChunkKey{Doc: 1, Ord: 3})) // мусор сдвига
	must(t, g.Close())

	g2, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	id, _, err := g2.Entities().Add("Бета", TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	if id != 6 {
		t.Fatalf("новому понятию выдан №%d, ожидался №6: №5 занят упоминанием, мусор не в счёт", id)
	}
}
