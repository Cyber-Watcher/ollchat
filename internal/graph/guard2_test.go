package graph

import (
	"context"
	"testing"
	"time"
)

// Заслон опытного графа (этап 104, Ж4): чтобы ошибки рабочего графа не перешли
// в опытный. Все правила действуют только в формате 2.

// Промпты заморожены значениями. Правка любого файла промпта меняет версию,
// и досборка графа после неё откажется идти — это намеренно, но случайной
// такая правка быть не должна. Рабочий граф собран промптом 1a2fa975: тест
// падает раньше, чем ночная сборка откажется продолжать.
func TestPromptIDsFrozen(t *testing.T) {
	if PromptID != "1a2fa975" {
		t.Fatalf("промпт РАБОЧЕГО графа изменился: %s. Рабочий граф собран 1a2fa975, "+
			"и досборка другим промптом откажется идти. Если правка намеренная — "+
			"она требует --graph-allow-prompt-change и записи в этапе.", PromptID)
	}
	if PromptIDV2 != "a1ba9f39" {
		t.Fatalf("промпт формата 2 изменился: %s. Если опытный граф уже собирается, "+
			"досборка откажется идти; обновите значение здесь вместе с записью в паспорте.", PromptIDV2)
	}
}

func labGraph(t *testing.T) *Graph {
	t.Helper()
	g, err := CreateKind(collection(t), "books", 1000, Rules{Name: "lab", Format: FormatV2},
		CreateOpts{Kind: KindExperimental, Note: "заслон"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// Отношение обязано быть названо: «связано», пустой и выдуманный тип —
// отказ модели, и в формате 2 такая связь не пишется. В формате 1 — как было.
func TestFormat2RequiresNamedRelation(t *testing.T) {
	facts := Facts{
		Entities: []FactEntity{{Name: "Go", Type: TypeTech}, {Name: "goroutine", Type: TypeConcept}, {Name: "канал", Type: TypeConcept}},
		Relations: []FactRelation{
			{Src: "Go", Dst: "goroutine", Type: "использует"},
			{Src: "Go", Dst: "канал", Type: "связано"},
			{Src: "goroutine", Dst: "канал", Type: "коррелирует"},
			{Src: "канал", Dst: "Go", Type: ""},
		},
	}
	lab := labGraph(t)
	if _, err := writeFacts(context.Background(), lab, ChunkKey{Doc: 1, Ord: 5}, facts, nil); err != nil {
		t.Fatal(err)
	}
	if n := lab.Edges().Count(); n != 1 {
		t.Fatalf("в формате 2 записано связей %d, ожидалась одна — с названным отношением", n)
	}
	if untyped, _ := lab.SkippedRelations(); untyped != 3 {
		t.Fatalf("не названо отношений %d, ожидалось 3: правило обязано быть видно в итоге захода", untyped)
	}
	work, _ := graph(t)
	if _, err := writeFacts(context.Background(), work, ChunkKey{Doc: 1, Ord: 5}, facts, nil); err != nil {
		t.Fatal(err)
	}
	if n := work.Edges().Count(); n != 4 {
		t.Fatalf("рабочий формат обязан писать как раньше: связей %d, ожидалось 4", n)
	}
}

// Одна фраза из зоны перекрытия кусков подтверждает связь один раз.
func TestFormat2SkipsOverlapDuplicate(t *testing.T) {
	facts := Facts{
		Entities:  []FactEntity{{Name: "Go", Type: TypeTech}, {Name: "goroutine", Type: TypeConcept}},
		Relations: []FactRelation{{Src: "Go", Dst: "goroutine", Type: "использует"}},
	}
	back := Facts{Entities: facts.Entities, Relations: []FactRelation{{Src: "goroutine", Dst: "Go", Type: "часть"}}}
	lab := labGraph(t)
	ctx := context.Background()
	for _, step := range []struct {
		key ChunkKey
		f   Facts
	}{
		{ChunkKey{Doc: 1, Ord: 5}, facts}, // первая запись
		{ChunkKey{Doc: 1, Ord: 6}, facts}, // сосед — та же фраза из перекрытия
		{ChunkKey{Doc: 1, Ord: 7}, back},  // сосед соседа, обратное направление: всё та же пара
		{ChunkKey{Doc: 1, Ord: 9}, facts}, // далёкий кусок — новый источник
		{ChunkKey{Doc: 2, Ord: 6}, facts}, // другая книга — новый источник
	} {
		if _, err := writeFacts(ctx, lab, step.key, step.f, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := lab.Edges().Count(); n != 4 {
		t.Fatalf("записей связей %d, ожидалось 4: 1#5, 1#7 (у 1#6 записи нет, цепочка прервана), 1#9, 2#6", n)
	}
	if _, overlap := lab.SkippedRelations(); overlap != 1 {
		t.Fatalf("повторов из перекрытия %d, ожидался 1", overlap)
	}
}

// Аббревиатура в формате 2 остаётся в карточке, но ключом не служит.
func TestFormat2AcronymIsNotAKey(t *testing.T) {
	lab := labGraph(t)
	re, _, _ := lab.Entities().Add("Relation extraction", TypeConcept, "RE", "извлечение отношений")
	// Кусок про регулярные выражения называет «RE» — и получает СВОЁ понятие.
	other, created, _ := lab.Entities().Add("RE", TypeTool, "regular expressions")
	if !created || other == re {
		t.Fatalf("«RE» попало в Relation extraction (id %d): аббревиатура сработала ключом", other)
	}
	ent, _ := lab.Entities().Get(re)
	if len(ent.Aliases) == 0 || ent.Aliases[0] != "RE" {
		t.Fatalf("сокращение пропало из карточки: %v", ent.Aliases)
	}
	// В рабочем формате — как было: сокращение ведёт к понятию.
	work, _ := graph(t)
	w, _, _ := work.Entities().Add("Relation extraction", TypeConcept, "RE")
	if got, created, _ := work.Entities().Add("RE", TypeTool); created || got != w {
		t.Fatalf("рабочий формат изменился: «RE» → id %d, новое %v", got, created)
	}
}

// Заход сборки и формат записываются в паспорт.
func TestNoteRunKeepsHistory(t *testing.T) {
	lab := labGraph(t)
	if h := lab.Meta().FormatHistory; len(h) != 1 || h[0].Version != FormatV2 || h[0].At.IsZero() {
		t.Fatalf("история формата при создании: %+v", h)
	}
	if err := lab.NoteRun(RunStamp{At: time.Now(), Model: "пусто", Done: 0}); err != nil || len(lab.Meta().Runs) != 0 {
		t.Fatalf("пустой заход записан: %v %+v", err, lab.Meta().Runs)
	}
	for i := 0; i < maxRunStamps+3; i++ {
		if err := lab.NoteRun(RunStamp{At: time.Now(), Model: "qwen3.8:latest", PromptID: PromptIDV2, Format: FormatV2, Done: 10, Covered: 10 * (i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	runs := lab.Meta().Runs
	if len(runs) != maxRunStamps || runs[len(runs)-1].Covered != 10*(maxRunStamps+3) {
		t.Fatalf("заходов в паспорте %d, последний %+v", len(runs), runs[len(runs)-1])
	}
	// Граф, заведённый до правки: история формата начинается с первого захода.
	work, _ := graph(t)
	work.meta.FormatHistory = nil
	if err := work.NoteRun(RunStamp{At: time.Now(), Model: "qwen3.8:latest", Done: 50, Covered: 1050}); err != nil {
		t.Fatal(err)
	}
	if h := work.Meta().FormatHistory; len(h) != 1 || h[0].Covered != 1000 {
		t.Fatalf("история формата у прежнего графа: %+v", h)
	}
}
