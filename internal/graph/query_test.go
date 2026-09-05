package graph

import (
	"strings"
	"testing"
)

// TestExpandQueryTranslatesToLibraryLanguage — главное свойство: к русскому
// слову приписывается английский синоним понятия, иначе английские книги
// не получают второго голоса при слиянии списков.
func TestExpandQueryTranslatesToLibraryLanguage(t *testing.T) {
	// Синонимы берутся из поля FoundEntity, а не из записи понятия: они
	// проходят проверку (Entities.TrustedAliases) там, где виден реестр,
	// и в выдачу попадают уже отсеянными и упорядоченными.
	ents := []FoundEntity{
		{Entity: Entity{Name: "горутина", Norm: Normalize("горутина")},
			AliasesSafe: []string{"goroutine", "goroutines"}},
	}
	got := ExpandQuery("горутина", ents, 0)
	for _, want := range []string{"горутина", "goroutine", "goroutines"} {
		if !strings.Contains(got, want) {
			t.Fatalf("в запросе нет %q: %q", want, got)
		}
	}
}

// Без понятий запрос остаётся вопросом: граф собран не по всей библиотеке,
// и вопрос, ни с чем не связавшийся, не должен меняться вовсе.
func TestExpandQueryKeepsQuestionWithoutEntities(t *testing.T) {
	if got := ExpandQuery("вопрос", nil, 0); got != "вопрос" {
		t.Fatalf("пустой список понятий изменил запрос: %q", got)
	}
}

// Больше трёх понятий не берётся: дальше идут случайные попутчики вопроса.
func TestExpandQueryStopsAtMax(t *testing.T) {
	ents := make([]FoundEntity, 0, 5)
	for _, n := range []string{"один", "два", "три", "четыре", "пять"} {
		ents = append(ents, FoundEntity{Entity: Entity{Name: n, Norm: Normalize(n)}})
	}
	got := ExpandQuery("вопрос", ents, 0)
	if !strings.Contains(got, "три") {
		t.Fatalf("третье понятие потеряно: %q", got)
	}
	if strings.Contains(got, "четыре") || strings.Contains(got, "пять") {
		t.Fatalf("взято больше трёх понятий: %q", got)
	}
	if got := ExpandQuery("вопрос", ents, 1); strings.Contains(got, "два") {
		t.Fatalf("предел не соблюдён: %q", got)
	}
}
