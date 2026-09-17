package graph

import (
	"context"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
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
	if PromptIDV2 != "933cd735" {
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

// Имя понятия в формате 2 обязано стоять в тексте куска — на языке куска.
// Перевод принимается только синонимом, дословно стоящим в тексте; если имени
// в тексте нет, а такой синоним есть, именем становится он.
func TestFormat2GroundsNames(t *testing.T) {
	chunk := "Go uses garbage collection to reclaim memory. Каталогами управляет сервис.\n" +
		"Goroutines are cheap; the load-\nbalancing layer spreads them."
	answer := `{"entities":[
		{"name":"сборка мусора","type":"понятие","aliases":["garbage collection"]},
		{"name":"каталоги","type":"понятие"},
		{"name":"goroutine","type":"понятие"},
		{"name":"load balancing","type":"понятие"},
		{"name":"Kubernetes","type":"технология"},
		{"name":"Go","type":"технология"}],
	 "relations":[
		{"src":"Go","dst":"сборка мусора","type":"использует"},
		{"src":"Go","dst":"Kubernetes","type":"использует"},
		{"src":"Go","dst":"goroutine","type":"использует"}]}`

	f, err := ParseFactsFor(FormatV2, answer, chunk)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range f.Entities {
		names[e.Name] = true
	}
	for _, want := range []string{"garbage collection", "каталоги", "goroutine", "load balancing", "Go"} {
		if !names[want] {
			t.Errorf("понятие %q потеряно: %v", want, names)
		}
	}
	if names["сборка мусора"] || names["Kubernetes"] {
		t.Errorf("имя, которого нет в тексте, осталось: %v", names)
	}
	if f.DroppedNames != 1 || f.RenamedNames != 1 {
		t.Errorf("отброшено %d (ожидалось 1: Kubernetes), переименовано %d (ожидалось 1)", f.DroppedNames, f.RenamedNames)
	}
	// Связь переименованного понятия идёт за ним; связь отброшенного отпадает.
	rel := map[string]bool{}
	for _, r := range f.Relations {
		rel[r.Src+"→"+r.Dst] = true
	}
	if !rel["Go→garbage collection"] || !rel["Go→goroutine"] || rel["Go→Kubernetes"] || len(f.Relations) != 2 {
		t.Errorf("связи: %v", rel)
	}

	// Рабочий формат проверкой имён не затронут.
	work, err := ParseFacts(answer, chunk)
	if err != nil || len(work.Entities) != 6 || work.DroppedNames != 0 {
		t.Fatalf("формат 1 изменился: понятий %d, отброшено %d, %v", len(work.Entities), work.DroppedNames, err)
	}
}

// Счёт остатка и сборка берут куски одним правилом: «забытый» кусок без
// служебного признака снова в работе, служебный и разобранный — нет.
func TestTakesChunkOneRule(t *testing.T) {
	g, _ := graph(t)
	mk := func(ord uint32, mark uint32) kb.ChunkRef {
		if mark != 0 {
			must(t, g.Progress().Mark(ChunkKey{Doc: 1, Ord: ord}, mark))
		}
		return kb.ChunkRef{Doc: 1, Ord: ord}
	}
	cases := []struct {
		name  string
		c     kb.ChunkRef
		redo  bool
		takes bool
	}{
		{"нетронутый", mk(1, 0), false, true},
		{"разобранный", mk(2, MarkDone), false, false},
		{"забытый, без служебного признака", mk(3, MarkService), false, true},
		{"пропущенный как мусор", mk(4, MarkSkipped), false, false},
		{"пустой без просьбы", mk(5, MarkEmpty), false, false},
		{"пустой по просьбе", mk(6, MarkEmpty), true, true},
	}
	for _, c := range cases {
		if got := takesChunk(g, c.c, c.redo); got != c.takes {
			t.Errorf("%s: берёт=%v, ожидалось %v", c.name, got, c.takes)
		}
	}
	toc := mk(7, MarkService)
	toc.TOC = true
	if takesChunk(g, toc, false) {
		t.Error("оглавление со служебной отметкой взято в работу")
	}
}
