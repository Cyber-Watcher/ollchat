package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Отпечаток текста ловит изменение и не срабатывает на совпадении.
func TestTextStampChangesWithText(t *testing.T) {
	a := textStamp("горутина")
	if a != textStamp("горутина") {
		t.Fatal("один и тот же текст дал разные отпечатки")
	}
	if a == textStamp("горутина, goroutine") {
		t.Fatal("добавленный синоним не изменил отпечаток")
	}
}

// Устаревшим считается понятие, чей текст изменился после счёта вектора.
func TestStaleEntitiesFindsChangedText(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	for _, n := range []string{"горутина", "канал", "рантайм"} {
		if _, _, err := g.Entities().Add(n, TypeConcept); err != nil {
			t.Fatal(err)
		}
	}

	// Векторы «посчитаны»: три понятия по два числа, отпечатки записаны.
	if err := g.SaveEntityVectors("проба", "", 2, []int8{1, 0, 0, 1, 1, 1}); err != nil {
		t.Fatal(err)
	}
	if ids, _, have := g.StaleEntities(); !have || len(ids) != 0 {
		t.Fatalf("сразу после счёта устаревших быть не может: %v (отпечатки: %v)", ids, have)
	}

	// У второго понятия появился синоним — текст изменился.
	if _, _, err := g.Entities().Add("канал", TypeConcept, "channel"); err != nil {
		t.Fatal(err)
	}
	ids, _, have := g.StaleEntities()
	if !have {
		t.Fatal("отпечатки пропали")
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("ожидалось устаревшее понятие №2, получено %v", ids)
	}
}

// Без отпечатков вопрос «что устарело» ответа не имеет — и это надо отличать
// от «всё в порядке», иначе пересчёт молча не сделает ничего.
func TestStaleEntitiesWithoutStamps(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, _, err := g.Entities().Add("горутина", TypeConcept); err != nil {
		t.Fatal(err)
	}
	if ids, _, have := g.StaleEntities(); have || len(ids) != 0 {
		t.Fatalf("без векторов и отпечатков ответа быть не должно: %v %v", ids, have)
	}
}

// Досчёт хвостом не стирает признак устаревания у головы.
//
// Поймано ревизией 10.09.2026: отпечатки переписывались по всем понятиям при
// каждой записи векторов, и ночная докатка, зовущая --graph-embed перед
// --graph-embed-stale, каждую ночь находила ноль устаревших.
func TestTopUpKeepsStaleStamps(t *testing.T) {
	g := newGraphWith(t, "горутина", "канал")
	defer g.Close()
	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Entities().Add("канал", TypeConcept, "channel"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Entities().Add("рантайм", TypeConcept); err != nil {
		t.Fatal(err)
	}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(emb.seen) != 3 || emb.seen[2] != "рантайм" {
		t.Fatalf("досчёт должен был посчитать только хвост, считал %q", emb.seen)
	}
	ids, unknown, have := g.StaleEntities()
	if !have || unknown != 0 || len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("после досчёта устаревшим должно остаться №2: ids=%v unknown=%d have=%v", ids, unknown, have)
	}
	// Пересчёт устаревших закрывает вопрос.
	if n, err := g.EmbedStale(context.Background(), emb, EmbedOpts{}, nil); err != nil || n != 1 {
		t.Fatalf("EmbedStale: n=%d err=%v", n, err)
	}
	if ids, _, _ := g.StaleEntities(); len(ids) != 0 {
		t.Fatalf("после пересчёта устаревших быть не должно: %v", ids)
	}
}

// Векторы, посчитанные до появления отпечатков, считаются неизвестными,
// а не свежими: досчёт хвостом ставит им ноль, а не отпечаток нынешнего текста.
func TestTopUpMarksPreStampVectorsUnknown(t *testing.T) {
	g := newGraphWith(t, "горутина", "канал")
	defer g.Close()
	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	// Граф из времён без отпечатков: файла нет.
	if err := os.Remove(filepath.Join(g.dir, entVecStampFile)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Entities().Add("рантайм", TypeConcept); err != nil {
		t.Fatal(err)
	}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	ids, unknown, have := g.StaleEntities()
	if !have || unknown != 2 || len(ids) != 0 {
		t.Fatalf("ожидалось два неизвестных и ни одного устаревшего: ids=%v unknown=%d have=%v", ids, unknown, have)
	}
}
