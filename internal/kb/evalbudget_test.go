package kb

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Третий исход замера: кусок нашёлся, но не поместился (этап 105, Ж2).
//
// Набор строится так, чтобы промах был заведомо **не** промахом поиска:
// нужная книга находится по слову, но стоит за пределом выдачи. Чужой поиск
// тут не подставляется — ищет настоящая коллекция настоящим кодом, меняются
// только два предела, как и в работе.
func TestEvalBudgetSeparatesDroppedFromMissed(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)

	// Нужная книга упоминает термины ОДИН раз в длинном тексте, а шесть чужих
	// состоят почти из одной этой фразы. BM25 нормирует по длине документа,
	// поэтому короткие и точные честно встают выше — нужная книга оказывается
	// за пределом выдачи не из-за подставленного списка, а по обычному счёту.
	makeBook(t, books, "wanted.pdf",
		longPage("cilium ebpf datapath explained once")+
			strings.Repeat(" unrelated filler sentence about other matters entirely. ", 40))
	// Книги обязаны отличаться побайтово: одинаковые доливка отбрасывает
	// как повторы, и вместо шести конкурентов в индексе оказывался один
	// (поймано этим же тестом).
	for i := 0; i < 6; i++ {
		letter := string(rune('a' + i))
		makeBook(t, books, "noisy"+letter+".pdf", "cilium ebpf datapath, note "+letter+".")
	}

	c, _ := base.Create("test", "")
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	cases := []EvalCase{{Query: "cilium ebpf datapath", Book: "wanted"}}

	// Рабочий бюджет тесный: нужная книга в первые два места не попадает.
	tight := EvalOpts{K: 2, MaxPerDoc: 1}
	rep, err := c.Eval(context.Background(), cases, EvalLexical, tight, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missed) != 1 {
		t.Fatalf("при тесном бюджете ожидался промах, получено Missed=%v recall=%.2f", rep.Missed, rep.Recall)
	}
	if rep.Dropped != nil {
		t.Errorf("без WideK третий исход мериться не должен, получено %v", rep.Dropped)
	}

	// Тот же бюджет плюс щедрый прогон: исход обязан назваться «выпало».
	withWide := tight
	withWide.WideK = 50
	rep, err = c.Eval(context.Background(), cases, EvalLexical, withWide, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missed) != 1 {
		t.Fatalf("recall от щедрого прогона меняться не должен: Missed=%v", rep.Missed)
	}
	if len(rep.Dropped) != 1 {
		t.Fatalf("кусок достижим при щедром бюджете — ожидался Dropped=1, получено %v", rep.Dropped)
	}
	if rep.Dropped[0] != cases[0].Query {
		t.Errorf("в Dropped не тот вопрос: %q", rep.Dropped[0])
	}

	// Щедрый бюджет в работе — и промаха нет вовсе: значит дело было в пределах,
	// а не в поиске. Это контрольная проверка самого теста.
	loose := EvalOpts{K: 50, MaxPerDoc: 0}
	rep, err = c.Eval(context.Background(), cases, EvalLexical, loose, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missed) != 0 {
		t.Fatalf("при щедром бюджете книга обязана находиться, иначе тест мерит не то: %v", rep.Missed)
	}
}

// Щедрый прогон снимает предел на книгу ОТРИЦАТЕЛЬНЫМ значением, а не нулём:
// путь работы (find.Books) читает ноль как «умолчание коллекции», и с нулём
// первый замер Ж2 (24.09.2026) мерил щедрый бюджет при прежнем пределе 3.
func TestEvalWideRunLiftsPerBookLimit(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "only.pdf", longPage("goroutines and channels in practice"))

	c, _ := base.Create("test", "")
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	wideLimit, wideRuns := 0, 0
	opt := EvalOpts{K: 5, MaxPerDoc: 3, WideK: 50}
	opt.Search = func(ctx context.Context, q string, so SearchOpts, want int) ([]Result, error) {
		if want > 5 {
			wideRuns++
			wideLimit = so.MaxPerDoc
			return nil, nil
		}
		return nil, nil // рабочий прогон промахивается — щедрый обязан состояться
	}
	if _, err := c.Eval(context.Background(), []EvalCase{{Query: "goroutines channels", Book: "only"}}, EvalLexical, opt, nil); err != nil {
		t.Fatal(err)
	}
	if wideRuns != 1 {
		t.Fatalf("щедрых прогонов %d, ожидался один", wideRuns)
	}
	if wideLimit >= 0 {
		t.Errorf("щедрый прогон ушёл с MaxPerDoc=%d — ноль путь работы прочтёт как умолчание 3; нужно отрицательное", wideLimit)
	}
}

// Найденный вопрос щедрым прогоном не тревожится: спрашивать о месте
// в широком списке у того, кто уже попал в выдачу, нечего.
func TestEvalBudgetSkipsFoundCases(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "only.pdf", longPage("goroutines and channels in practice"))

	c, _ := base.Create("test", "")
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	var extra int
	opt := EvalOpts{K: 5, WideK: 50}
	opt.Search = func(ctx context.Context, q string, so SearchOpts, want int) ([]Result, error) {
		if want > 5 {
			extra++ // щедрый прогон
		}
		return c.SearchWith(ctx, q, so, nil)
	}
	rep, err := c.Eval(context.Background(), []EvalCase{{Query: "goroutines channels", Book: "only"}}, EvalLexical, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missed) != 0 {
		t.Fatalf("книга должна находиться: %v", rep.Missed)
	}
	if extra != 0 {
		t.Errorf("щедрый прогон сделан %d раз для найденного вопроса — лишняя работа", extra)
	}
}
