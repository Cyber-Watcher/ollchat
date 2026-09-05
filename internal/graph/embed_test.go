package graph

import (
	"context"
	"strings"
	"testing"
)

// fakeEmbedder отдаёт вектор, по которому видно, какому тексту он принадлежит.
//
// Так проверяется главное свойство досчёта: старые векторы остаются на своих
// местах, а новые дописываются в хвост. Если досчёт перепутает порядок, поиск
// начнёт находить не то, и заметить это по числам будет невозможно.
type fakeEmbedder struct {
	model string
	calls int
	seen  []string
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.seen = append(f.seen, texts...)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 4)
		v[0] = float32(len(t)) // по длине текста узнаём, что именно посчитали
		v[1] = 1
		out[i] = v
	}
	return out, nil
}

// newGraphWith создаёт граф с заданными именами понятий.
func newGraphWith(t *testing.T, names ...string) *Graph {
	t.Helper()
	g, err := Create(t.TempDir(), "проба", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, _, err := g.Entities().Add(n, TypeConcept); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// Досчёт считает только новые понятия, а прежние берёт как есть.
//
// Ради этого он и написан: граф растёт заходами по два часа, и пересчитывать
// при каждом все 63 тысячи понятий — двадцать минут карты на две минуты новой
// работы.
func TestEmbedEntitiesIncremental(t *testing.T) {
	g := newGraphWith(t, "первое", "второе")
	defer g.Close()

	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := g.VectorsInfo().Count; got != 2 {
		t.Fatalf("посчитано %d векторов, ожидалось 2", got)
	}
	first := emb.calls

	// Добавляем третье понятие и считаем снова.
	if _, _, err := g.Entities().Add("третье", TypeConcept); err != nil {
		t.Fatal(err)
	}
	emb.seen = nil
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := g.VectorsInfo().Count; got != 3 {
		t.Fatalf("после досчёта векторов %d, ожидалось 3", got)
	}
	if len(emb.seen) != 1 || !strings.Contains(emb.seen[0], "третье") {
		t.Fatalf("досчёт отправил в модель %v, ожидалось только новое понятие", emb.seen)
	}
	if emb.calls <= first {
		t.Fatal("досчёт не сделал ни одного запроса")
	}
}

// Когда считать нечего, досчёт не трогает ни модель, ни файлы.
func TestEmbedEntitiesNothingToDo(t *testing.T) {
	g := newGraphWith(t, "одно")
	defer g.Close()

	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	before := emb.calls
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if emb.calls != before {
		t.Fatalf("повторный досчёт сделал %d лишних запросов", emb.calls-before)
	}
}

// Recount считает всё заново — нужен, когда прежние векторы негодны.
func TestEmbedEntitiesRecount(t *testing.T) {
	g := newGraphWith(t, "первое", "второе")
	defer g.Close()

	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	emb.seen = nil
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{Recount: true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(emb.seen) != 2 {
		t.Fatalf("пересчёт отправил %d текстов, ожидалось 2", len(emb.seen))
	}
}

// Смена модели отменяет досчёт: векторы разных моделей живут в разных
// пространствах, и склеивать их — то же, что складывать метры с килограммами.
func TestEmbedEntitiesModelChangeForcesFull(t *testing.T) {
	g := newGraphWith(t, "первое", "второе")
	defer g.Close()

	if err := g.EmbedEntities(context.Background(), &fakeEmbedder{model: "старая"}, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	other := &fakeEmbedder{model: "новая"}
	if err := g.EmbedEntities(context.Background(), other, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(other.seen) != 2 {
		t.Fatalf("при смене модели отправлено %d текстов, ожидалось 2 (всё заново)", len(other.seen))
	}
	if got := g.VectorsInfo().Model; got != "новая" {
		t.Fatalf("в паспорте осталась модель %q", got)
	}
}
