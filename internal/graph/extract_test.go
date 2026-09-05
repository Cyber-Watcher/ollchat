package graph

import (
	"strings"
	"testing"
)

// Разбор ответа в ограде.
func TestAnswerInsideFenceParsed(t *testing.T) {
	answer := "Вот что я нашёл:\n```json\n" +
		`{"entities":[{"name":"KV-кэш","type":"понятие","aliases":["KV cache"]},` +
		`{"name":"контекстное окно","type":"понятие"}],` +
		`"relations":[{"src":"контекстное окно","dst":"KV-кэш","type":"влияет"}]}` +
		"\n```\nНадеюсь, помог."
	f, err := ParseFacts(answer, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entities) != 2 || len(f.Relations) != 1 {
		t.Fatalf("разобрано %d сущностей, %d связей", len(f.Entities), len(f.Relations))
	}
	if f.Entities[0].Aliases[0] != "KV cache" {
		t.Errorf("синоним потерян: %+v", f.Entities[0])
	}
}

// Ответ без JSON это ошибка.
func TestAnswerWithoutJSONIsError(t *testing.T) {
	if _, err := ParseFacts("Извините, я не могу выполнить эту задачу.", ""); err == nil {
		t.Error("ответ без JSON принят")
	}
}

// Скобка внутри строки не должна обрывать разбор на середине.
func TestBraceInsideStringKeepsParse(t *testing.T) {
	answer := `{"entities":[{"name":"map[string]any","type":"формат"}],"relations":[]}`
	f, err := ParseFacts(answer, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entities) != 1 {
		t.Errorf("сущностей = %d", len(f.Entities))
	}
}

// Связь, у которой конец не назван среди сущностей, проверить нечем —
// она отбрасывается.
func TestDanglingEdgeDropped(t *testing.T) {
	f := Clean(Facts{
		Entities: []FactEntity{{Name: "Go", Type: "технология"}},
		Relations: []FactRelation{
			{Src: "Go", Dst: "Rust", Type: "противопоставлено"},
			{Src: "Go", Dst: "Go", Type: "является"},
		},
	})
	if len(f.Relations) != 0 {
		t.Errorf("связи оставлены: %+v", f.Relations)
	}
}

// Отбраковка имён.
func TestNameRejection(t *testing.T) {
	badCases := []string{
		"",
		"а",
		"42",
		"— 137 —",
		"система",
		"data",
		"это очень длинное название которое на самом деле является предложением из текста",
		"имя\nс переводом строки",
	}
	for _, name := range badCases {
		f := Clean(Facts{Entities: []FactEntity{{Name: name, Type: "понятие"}}})
		if len(f.Entities) != 0 {
			t.Errorf("имя %q принято", name)
		}
	}
	goodCases := []string{"Go", "KV-кэш", "RFC 8446", "Kubernetes", "вытеснение контекста"}
	for _, name := range goodCases {
		f := Clean(Facts{Entities: []FactEntity{{Name: name, Type: "понятие"}}})
		if len(f.Entities) != 1 {
			t.Errorf("имя %q отброшено", name)
		}
	}
}

// Повторы сущностей схлопываются.
func TestDuplicateEntitiesCollapse(t *testing.T) {
	f := Clean(Facts{Entities: []FactEntity{
		{Name: "KV-кэш", Type: "понятие"},
		{Name: "kv кэш", Type: "понятие"},
		{Name: "KV-КЭШ", Type: "технология"},
	}})
	if len(f.Entities) != 1 {
		t.Errorf("сущностей = %d, ожидалась одна: %+v", len(f.Entities), f.Entities)
	}
}

// Неизвестный тип связи становится связано.
func TestUnknownRelationBecomesRelated(t *testing.T) {
	f := Clean(Facts{
		Entities:  []FactEntity{{Name: "Go", Type: "технология"}, {Name: "GC", Type: "понятие"}},
		Relations: []FactRelation{{Src: "Go", Dst: "GC", Type: "содержит в себе"}},
	})
	if len(f.Relations) != 1 || RelType(f.Relations[0].Type) != RelRelated {
		t.Errorf("связь разобрана неверно: %+v", f.Relations)
	}
}

// Пределы на кусок.
func TestPerChunkLimits(t *testing.T) {
	var many Facts
	for i := 0; i < 50; i++ {
		many.Entities = append(many.Entities, FactEntity{
			Name: "понятие" + string(rune('а'+i%30)) + string(rune('0'+i%10)), Type: "понятие"})
	}
	f := Clean(many)
	if len(f.Entities) > maxEntitiesPerChunk {
		t.Errorf("сущностей = %d, предел %d", len(f.Entities), maxEntitiesPerChunk)
	}
}

// Вопрос содержит книгу и страницу.
func TestPromptCarriesBookAndPage(t *testing.T) {
	q := UserPrompt("Building AI Agents — Raieli", "стр.", 40, 41, "текст фрагмента")
	if !strings.Contains(q, "Building AI Agents") || !strings.Contains(q, "стр. 40–41") {
		t.Errorf("вопрос собран неверно:\n%s", q)
	}
	if !strings.Contains(q, "текст фрагмента") {
		t.Error("в вопросе нет самого фрагмента")
	}
}

// Модель зацикливается и упирается в потолок длины: JSON обрывается на середине.
// Начало ответа при этом целое — терять его нельзя, иначе пропадает каждый
// пятый кусок (замерено на живой сборке 23.08.2026).
func TestTruncatedJSONRepaired(t *testing.T) {
	cut := `{"entities":[{"name":"GraphRAG","type":"технология"},` +
		`{"name":"Leiden","type":"инструмент"},{"name":"Algorithm","type":"поня`
	f, err := ParseFacts(cut, "")
	if err != nil {
		t.Fatalf("оборванный ответ не починился: %v", err)
	}
	if len(f.Entities) != 2 {
		t.Fatalf("сущностей = %d, ожидалось 2 целых: %+v", len(f.Entities), f.Entities)
	}
	if f.Entities[0].Name != "GraphRAG" || f.Entities[1].Name != "Leiden" {
		t.Errorf("сущности разобраны неверно: %+v", f.Entities)
	}
}

// Оборванный JSON со связями чинится.
func TestTruncatedJSONWithEdgesRepaired(t *testing.T) {
	cut := `{"entities":[{"name":"RAG","type":"технология"},{"name":"граф","type":"понятие"}],` +
		`"relations":[{"src":"RAG","dst":"граф","type":"использует"},{"src":"RAG","dst":"гр`
	f, err := ParseFacts(cut, "")
	if err != nil {
		t.Fatalf("не починился: %v", err)
	}
	if len(f.Entities) != 2 || len(f.Relations) != 1 {
		t.Errorf("разобрано %d сущностей и %d связей", len(f.Entities), len(f.Relations))
	}
}

// Повторы, которыми модель забивает ответ при зацикливании, схлопываются
// в одну сущность — ради этого и нужна нормализация имён.
func TestLoopedAnswerYieldsOneEntity(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"entities":[{"name":"GraphRAG","type":"технология"}`)
	for i := 0; i < 40; i++ {
		b.WriteString(`,{"name":"Algorithm","type":"понятие"}`)
	}
	b.WriteString(`],"relations":[]}`)
	f, err := ParseFacts(b.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entities) != 2 {
		t.Errorf("сущностей = %d, ожидалось 2 (GraphRAG и Algorithm): %+v", len(f.Entities), f.Entities)
	}
}

// Не JSON не чинится.
func TestNonJSONNotRepaired(t *testing.T) {
	if _, err := ParseFacts("Извините, не могу помочь.", ""); err == nil {
		t.Error("текст без JSON принят за починенный")
	}
}

// Синоним, которого нет в тексте куска, отбрасывается — это выдумка модели.
// Замер 03.09.2026: так модель приписывала «Knowledge base ← внешний источник».
func TestCleanDropsAliasNotInChunk(t *testing.T) {
	f := Facts{Entities: []FactEntity{{
		Name: "Knowledge base", Type: "понятие",
		Aliases: []string{"база знаний", "внешний источник"},
	}}}
	text := "База знаний хранит факты. Knowledge base is queried by the agent."

	got := clean(f, text)
	if len(got.Entities) != 1 {
		t.Fatalf("понятие потеряно: %+v", got)
	}
	al := got.Entities[0].Aliases
	// «база знаний» в тексте есть — остаётся; «внешний источник» нет — уходит.
	if len(al) != 1 || al[0] != "база знаний" {
		t.Fatalf("проверка по тексту сработала неверно: %v", al)
	}
}

// Синоним, разорванный переносом строки в тексте, всё равно засчитывается:
// «внешний\nисточник» это тот же «внешний источник».
func TestCleanAliasAcrossLineBreak(t *testing.T) {
	f := Facts{Entities: []FactEntity{{
		Name: "Config", Type: "понятие", Aliases: []string{"файл настроек"},
	}}}
	text := "Config — это файл\nнастроек программы."
	got := clean(f, text)
	if len(got.Entities[0].Aliases) != 1 {
		t.Fatalf("перенос строки съел синоним: %+v", got.Entities[0].Aliases)
	}
}

// Без текста куска проверка не делается — формат 1 и тесты ведут себя как раньше.
func TestCleanWithoutTextKeepsAliases(t *testing.T) {
	f := Facts{Entities: []FactEntity{{
		Name: "Alpha", Type: "понятие", Aliases: []string{"beta gamma"},
	}}}
	got := clean(f, "")
	if len(got.Entities) != 1 || len(got.Entities[0].Aliases) != 1 {
		t.Fatalf("без текста синонимы трогать нельзя: %+v", got)
	}
}

// Граница слова: синоним в скобках и кавычках засчитывается, а вложенный
// в другое слово — нет.
func TestContainsWordBoundary(t *testing.T) {
	// clean() приводит и текст, и синоним к нижнему регистру — так и здесь.
	cases := []struct {
		h, a string
		want bool
	}{
		{"это (kv cache) кэш", "kv cache", true},   // в скобках — да
		{"вызов net.dial здесь", "net.dial", true}, // точка внутри — да
		{"работает goroutine тут", "go", false},    // внутри слова — нет
		{"язык go быстрый", "go", true},            // отдельным словом — да
		{"«внешний источник»", "внешний источник", true},
		{"конецстрокиkv cache", "kv cache", false}, // слева буква — не граница
	}
	for _, c := range cases {
		if got := containsWord(c.h, c.a); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v, ожидалось %v", c.h, c.a, got, c.want)
		}
	}
}
