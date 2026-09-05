package graph

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// communitiesFixture — разбиение из четырёх тем, различающихся ровно тем,
// по чему идёт отбор: оценкой, размером и наличием резюме.
func communitiesFixture() *Communities {
	big := make([]uint32, 25)
	for i := range big {
		big[i] = uint32(i + 1)
	}
	small := []uint32{1, 2, 3}
	return &Communities{List: []Community{
		{ID: 1, Level: 0, Members: big, Title: "Важная тема", Summary: "о важном", Rating: 9},
		{ID: 2, Level: 0, Members: big, Title: "Пустяковая тема", Summary: "о пустяке", Rating: 3},
		{ID: 3, Level: 0, Members: small, Title: "Мелкая тема", Summary: "мелкая", Rating: 10},
		{ID: 4, Level: 0, Members: big, Rating: 9}, // резюме не написано
		{ID: 5, Level: 1, Members: big, Title: "Объединение", Rating: 10},
	}}
}

// Отбор берёт только крупные важные темы с резюме.
func TestSelectForFindings(t *testing.T) {
	c := communitiesFixture()
	got := c.SelectForFindings(FindingsOpts{})
	if len(got) != 1 || c.List[got[0]].ID != 1 {
		var ids []int
		for _, i := range got {
			ids = append(ids, c.List[i].ID)
		}
		t.Fatalf("отобраны темы %v, ожидалась одна — #1", ids)
	}
}

// Уже разобранная тема второй раз не считается: прогон стоит времени карты,
// и повтор без нужды — это выброшенные минуты.
func TestSelectForFindingsSkipsDone(t *testing.T) {
	c := communitiesFixture()
	c.List[0].Findings = []Finding{{Title: "уже есть", Text: "и текст"}}
	if got := c.SelectForFindings(FindingsOpts{}); len(got) != 0 {
		t.Fatalf("разобранная тема снова отобрана: %v", got)
	}
	if got := c.SelectForFindings(FindingsOpts{Redo: true}); len(got) != 1 {
		t.Fatalf("с Redo тема должна отбираться заново: %v", got)
	}
}

// Разбор ответа терпит ограду и мусор вокруг JSON: замер на живой модели
// показал, что она отдаёт то одно, то другое на одном и том же промпте.
func TestParseFindingsTolerant(t *testing.T) {
	cases := []string{
		`{"findings":[{"title":"Вывод","text":"Пояснение."}]}`,
		"```json\n{\"findings\":[{\"title\":\"Вывод\",\"text\":\"Пояснение.\"}]}\n```",
		"Вот разбор:\n{\"findings\":[{\"title\":\"Вывод\",\"text\":\"Пояснение.\"}]}\nготово",
	}
	for _, raw := range cases {
		got, err := parseFindings(raw)
		if err != nil {
			t.Fatalf("не разобрано %q: %v", raw, err)
		}
		if len(got) != 1 || got[0].Title != "Вывод" {
			t.Fatalf("разобрано %+v из %q", got, raw)
		}
	}
}

// Половина вывода — не вывод: без пояснения он не отличается от заголовка.
func TestParseFindingsDropsHalf(t *testing.T) {
	got, err := parseFindings(`{"findings":[{"title":"Есть","text":"Текст."},{"title":"Без текста","text":"  "},{"title":"","text":"Без заголовка"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Есть" {
		t.Fatalf("оставлено %+v, ожидался один полный вывод", got)
	}
}

// Пустой список — допустимый ответ, а не ошибка: бывают темы, по которым
// выводов и правда нет.
func TestParseFindingsEmptyAllowed(t *testing.T) {
	got, err := parseFindings(`{"findings":[]}`)
	if err != nil || len(got) != 0 {
		t.Fatalf("пустой список должен приниматься: %+v, %v", got, err)
	}
}

// Сбой на одной теме не роняет прогон целиком: карта занята часами,
// и одна оборванная связь не должна стоить всей работы.
func TestFindingsSurvivesFailure(t *testing.T) {
	dir := t.TempDir()
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	c := communitiesFixture()
	c.List[1].Rating = 9 // сделаем вторую тему тоже подходящей
	m := &model{answer: func(n int) (string, error) {
		if n == 1 {
			return "", errors.New("сервер сорвал ответ")
		}
		return `{"findings":[{"title":"Вывод","text":"Пояснение."}]}`, nil
	}}

	err = g.Findings(context.Background(), m, c, FindingsOpts{Workers: 1}, nil)
	if err != nil {
		t.Fatalf("прогон вернул ошибку: %v", err)
	}
	got := 0
	for _, com := range c.List {
		if len(com.Findings) > 0 {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("разобранных тем %d, ожидалась одна (вторая после сбоя первой)", got)
	}
}

// Вопрос к модели содержит и название темы, и её понятия: без первого вывод
// теряет предмет, без второго — опору.
func TestFindingsPromptCarriesTopicAndEntities(t *testing.T) {
	dir := t.TempDir()
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, _, err := g.Entities().Add("контекстное окно", TypeConcept); err != nil {
		t.Fatal(err)
	}

	var seen string
	m := &model{answer: func(int) (string, error) { return `{"findings":[]}`, nil }}
	m.spy = func(user string) { seen = user }

	com := Community{ID: 1, Level: 0, Members: []uint32{1}, Title: "Окно контекста",
		Summary: "про окно", Rating: 9}
	if _, err := g.askFindings(context.Background(), m, &com, FindingsOpts{}.norm()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Окно контекста", "про окно", "контекстное окно"} {
		if !strings.Contains(seen, want) {
			t.Errorf("в вопросе нет %q:\n%s", want, seen)
		}
	}
}
