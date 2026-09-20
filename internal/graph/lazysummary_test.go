package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Разбиение для ленивых описаний: две темы нижнего уровня по семь понятий
// и объединение над ними.
func lazyGraph(t *testing.T) *Graph {
	t.Helper()
	names := make([]string, 0, 14)
	for i := 1; i <= 14; i++ {
		names = append(names, "понятие"+string(rune('A'+i-1)))
	}
	g := newGraphWith(t, names...)
	c := &Communities{List: []Community{
		{ID: 1, Level: 0, Parent: 3, Members: members(1, 7)},
		{ID: 2, Level: 0, Parent: 3, Members: members(8, 14), Title: "Готовая", Summary: "уже описана"},
		{ID: 3, Level: 1, Parent: -1, Members: members(1, 14)},
	}}
	if err := g.saveCommunities(c); err != nil {
		t.Fatal(err)
	}
	return g
}

func okAnswer(title string) func(int) (string, error) {
	return func(int) (string, error) {
		return `{"title":"` + title + `","summary":"описание","key":["a"],"rating":7,"why":"важно"}`, nil
	}
}

// Ленивое описание пишет только запрошенные темы и не трогает описанные.
func TestDescribeTopicsOnlyRequested(t *testing.T) {
	g := lazyGraph(t)
	m := &model{answer: okAnswer("Новая")}
	n, err := g.DescribeTopics(context.Background(), m, []int{1, 2}, SummaryOpts{MinMembers: 5})
	if err != nil || n != 1 || m.calls != 1 {
		t.Fatalf("описано %d, вызовов %d, ошибка %v", n, m.calls, err)
	}
	c, err := g.LoadCommunities()
	if err != nil {
		t.Fatal(err)
	}
	if c.List[0].Title != "Новая" || c.List[1].Title != "Готовая" || c.List[2].Title != "" {
		t.Fatalf("описания после ленивого счёта: %q, %q, %q", c.List[0].Title, c.List[1].Title, c.List[2].Title)
	}
	// Тема меньше порога не описывается и при прямой просьбе.
	c.List[0].Title = ""
	c.List[0].Members = members(1, 3)
	if err := g.saveCommunities(c); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.DescribeTopics(context.Background(), m, []int{1}, SummaryOpts{MinMembers: 5}); n != 0 {
		t.Fatalf("тема из трёх понятий описана при пороге 5")
	}
}

// Верхняя тема описывается по вложенным: их названия и описания уходят
// модели вместо голого списка понятий.
func TestDescribeUpperTopicFromChildren(t *testing.T) {
	g := lazyGraph(t)
	var mu sync.Mutex
	var seen string
	m := &model{answer: okAnswer("Объединение"), spy: func(u string) { mu.Lock(); seen = u; mu.Unlock() }}
	if n, err := g.DescribeTopics(context.Background(), m, []int{3}, SummaryOpts{MinMembers: 5}); err != nil || n != 1 {
		t.Fatalf("описано %d, %v", n, err)
	}
	if !strings.Contains(seen, "Вложенные темы") || !strings.Contains(seen, "Готовая — уже описана") {
		t.Fatalf("в запросе для верхней темы нет вложенных:\n%s", seen)
	}
}

// Охрана записи: разбиение переписали, пока шло описание, — ленивая запись
// не затирает новое разбиение.
func TestDescribeTopicsGuardsAgainstRepartition(t *testing.T) {
	g := lazyGraph(t)
	path := filepath.Join(g.dir, CommunityFile)
	m := &model{answer: func(int) (string, error) {
		// Пока модель «думает», докатка переписала разбиение.
		fresh := &Communities{List: []Community{{ID: 9, Level: 0, Parent: -1, Members: members(1, 14)}}}
		if err := g.saveCommunities(fresh); err != nil {
			return "", err
		}
		// Отпечаток — размер и время: время в тесте могло не сдвинуться,
		// размер другой файл меняет наверняка.
		return okAnswer("Поздно")(0)
	}}
	_, err := g.DescribeTopics(context.Background(), m, []int{1}, SummaryOpts{MinMembers: 5})
	if !errors.Is(err, ErrCommunitiesChanged) {
		t.Fatalf("ожидалась ErrCommunitiesChanged, получено %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "Поздно") || !strings.Contains(string(raw), `"id": 9`) {
		t.Fatalf("ленивая запись затёрла новое разбиение:\n%s", raw)
	}
}
