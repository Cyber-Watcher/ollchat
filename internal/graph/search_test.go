package graph

import (
	"testing"
)

// заполнить строит маленький граф с понятным устройством:
//
//	контекстное окно —влияет→ KV-кэш —часть→ видеопамять
//	вытеснение контекста —связано→ контекстное окно
//	Go (отдельно, ни с чем не связано)
func fill(t *testing.T, g *Graph) map[string]uint32 {
	t.Helper()
	ids := map[string]uint32{}
	addEntity := func(name, kind string, aliases ...string) uint32 {
		id, _, err := g.Entities().Add(name, kind, aliases...)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
		return id
	}
	window := addEntity("контекстное окно", TypeConcept, "context window")
	kv := addEntity("KV-кэш", TypeConcept, "KV cache")
	memory := addEntity("видеопамять", TypeConcept)
	eviction := addEntity("вытеснение контекста", TypeConcept)
	addEntity("Go", TypeTech)

	// Упоминания: окно и кэш часто встречаются вместе — это и делает связь
	// между ними самой убедительной.
	for ord := uint32(1); ord <= 5; ord++ {
		if err := g.Mentions().Add(window, ChunkKey{Doc: 1, Ord: ord}); err != nil {
			t.Fatal(err)
		}
		if err := g.Mentions().Add(kv, ChunkKey{Doc: 1, Ord: ord}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Mentions().Add(memory, ChunkKey{Doc: 2, Ord: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(kv, ChunkKey{Doc: 2, Ord: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(eviction, ChunkKey{Doc: 3, Ord: 7}); err != nil {
		t.Fatal(err)
	}

	addEdge := func(a, b uint32, kind uint8, weight float32, k ChunkKey) {
		if err := g.Edges().Add(Edge{Src: a, Dst: b, Type: kind, Weight: weight, Evidence: k}); err != nil {
			t.Fatal(err)
		}
	}
	addEdge(window, kv, RelAffects, 3, ChunkKey{1, 1})
	addEdge(kv, memory, RelPart, 2, ChunkKey{2, 1})
	addEdge(eviction, window, RelRelated, 1, ChunkKey{3, 7})
	return ids
}

// Поиск находит понятия вопроса.
func TestSearchFindsQueryEntities(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)

	res := g.Search("как контекстное окно влияет на KV-кэш", SearchOpts{})
	if len(res.Entities) < 2 {
		t.Fatalf("найдено понятий %d: %+v", len(res.Entities), res.Entities)
	}
	names := map[string]bool{}
	for _, e := range res.Entities {
		names[e.Name] = true
	}
	if !names["контекстное окно"] || !names["KV-кэш"] {
		t.Errorf("не нашлись оба понятия вопроса: %v", names)
	}
	if len(res.Relations) == 0 {
		t.Error("связи не собраны")
	}
	if len(res.Chunks) == 0 {
		t.Error("подтверждений нет — на что тогда ссылаться")
	}
}

// Длинное сочетание важнее коротких внутри него: «контекстное окно» это одно
// понятие, а не «контекст» и «окно» по отдельности.
func TestLongPhraseBeatsShort(t *testing.T) {
	g, _ := graph(t)
	e := g.Entities()
	if _, _, err := e.Add("контекстное окно", TypeConcept); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Add("окно", TypeConcept); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(1, ChunkKey{1, 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(2, ChunkKey{1, 2}); err != nil {
		t.Fatal(err)
	}

	res := g.Search("контекстное окно", SearchOpts{})
	if len(res.Entities) != 1 || res.Entities[0].Name != "контекстное окно" {
		t.Errorf("разобрано на части: %+v", res.Entities)
	}
}

// Поиск по синониму.
func TestSearchByAlias(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	res := g.Search("what is KV cache", SearchOpts{})
	if len(res.Entities) == 0 || res.Entities[0].Name != "KV-кэш" {
		t.Errorf("синоним не сработал: %+v", res.Entities)
	}
}

// Вопрос не о том, что есть в графе, — честный отказ, а не выдумка.
func TestUnknownQueryExplains(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	res := g.Search("рецепт борща", SearchOpts{})
	if len(res.Entities) != 0 {
		t.Errorf("нашлось лишнее: %+v", res.Entities)
	}
	if res.Note == "" {
		t.Error("нет пояснения, почему пусто")
	}
}

// Редкие понятия отсекаются.
func TestRareEntitiesCut(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	// «вытеснение контекста» встречается один раз, «KV-кэш» — шесть.
	res := g.Search("вытеснение контекста и KV-кэш", SearchOpts{MinMentions: 3})
	for _, e := range res.Entities {
		if e.Name == "вытеснение контекста" {
			t.Errorf("редкое понятие прошло порог: %+v", e)
		}
	}
	if len(res.Entities) == 0 {
		t.Error("порог выбросил вообще всё")
	}
}

// Карточка понятия.
func TestEntityCard(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	e, ok := g.Entity("KV cache", SearchOpts{})
	if !ok {
		t.Fatal("понятие не найдено по синониму")
	}
	if e.Name != "KV-кэш" {
		t.Errorf("имя = %q", e.Name)
	}
	if e.Mentions != 6 {
		t.Errorf("упоминаний = %d, ожидалось 6", e.Mentions)
	}
	if e.Books != 2 {
		t.Errorf("книг = %d, ожидалось 2", e.Books)
	}
	if len(e.Neighbors) == 0 {
		t.Error("соседей нет")
	}
}

// Путь между понятиями.
func TestPathBetweenEntities(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)

	steps, ok := g.Path("контекстное окно", "видеопамять", 4)
	if !ok {
		t.Fatal("путь не найден, хотя он есть")
	}
	if len(steps) != 2 {
		t.Fatalf("шагов = %d, ожидалось 2: %+v", len(steps), steps)
	}
	if steps[0].From != "контекстное окно" || steps[1].To != "видеопамять" {
		t.Errorf("путь собран неверно: %+v", steps)
	}
	// У каждого шага должна быть опора в книге.
	for _, st := range steps {
		if st.Evidence == (ChunkKey{}) {
			t.Errorf("шаг без подтверждения: %+v", st)
		}
	}
}

// Пути нет когда его нет.
func TestNoPathWhenNone(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	if _, ok := g.Path("Go", "видеопамять", 4); ok {
		t.Error("найден путь там, где связей нет")
	}
	if _, ok := g.Path("несуществующее", "видеопамять", 4); ok {
		t.Error("найден путь от несуществующего понятия")
	}
}

// Выдача не должна плясать от запуска к запуску: иначе один и тот же вопрос
// даёт разные ответы, и доверять им нельзя.
func TestResultsAreStable(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	first := g.Search("контекстное окно KV-кэш видеопамять", SearchOpts{})
	for i := 0; i < 5; i++ {
		again := g.Search("контекстное окно KV-кэш видеопамять", SearchOpts{})
		if len(again.Entities) != len(first.Entities) {
			t.Fatalf("разное число понятий: %d и %d", len(first.Entities), len(again.Entities))
		}
		for j := range first.Entities {
			if first.Entities[j].ID != again.Entities[j].ID {
				t.Fatalf("порядок понятий поплыл на повторе %d", i)
			}
		}
		for j := range first.Chunks {
			if first.Chunks[j] != again.Chunks[j] {
				t.Fatalf("порядок подтверждений поплыл на повторе %d", i)
			}
		}
	}
}

// Кусок, где названы сразу несколько искомых понятий, ценнее куска с одним:
// именно в нём, скорее всего, и написано, как они связаны.
func TestEvidenceWithManyEntitiesFirst(t *testing.T) {
	g, _ := graph(t)
	fill(t, g)
	res := g.Search("контекстное окно KV-кэш", SearchOpts{TopChunks: 3})
	if len(res.Chunks) == 0 {
		t.Fatal("подтверждений нет")
	}
	if res.Chunks[0].Doc != 1 {
		t.Errorf("первым идёт %v, а ожидался кусок книги 1, где названы оба понятия", res.Chunks[0])
	}
}
