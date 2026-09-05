package maint

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Остаток печатается по-человечески, а не машинной записью длительности.
func TestHumanLeft(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{40 * time.Second, "40 с"},
		{7 * time.Minute, "7 мин"},
		{95 * time.Minute, "1 ч 35 мин"},
		{3 * time.Hour, "3 ч 0 мин"},
	}
	for _, c := range cases {
		if got := humanLeft(c.d); got != c.want {
			t.Errorf("humanLeft(%v) = %q, ожидалось %q", c.d, got, c.want)
		}
	}
}

// Скорость появляется по окну, а не сразу и не от начала работы.
//
// Средняя от старта врёт после любой заминки: сервер задумался на минуту —
// и оценка остатка ещё долго тянет эту минуту за собой.
func TestProgressRateFromWindow(t *testing.T) {
	p := &progressLine{start: time.Now()}

	// Первое сообщение только ставит отметку — скорости ещё нет.
	p.update(kb.Progress{Phase: "векторы", DocsDone: 0, DocsTotal: 1000})
	if p.rate != 0 {
		t.Errorf("после первого сообщения скорость = %v, ожидался 0", p.rate)
	}

	// Отматываем отметку назад: как будто прошло четыре секунды.
	p.mu.Lock()
	p.markAt = time.Now().Add(-4 * time.Second)
	p.mu.Unlock()

	p.update(kb.Progress{Phase: "векторы", DocsDone: 200, DocsTotal: 1000})
	if p.rate < 40 || p.rate > 60 {
		t.Errorf("скорость = %.1f/с, ожидалось около 50", p.rate)
	}
	p.mu.Lock()
	p.stop()
	p.mu.Unlock()
}

// Строка перерисовывается по таймеру, даже когда новых сообщений нет.
//
// Ради этого всё и затевалось: одна пачка счёта векторов — полторы тысячи
// кусков, и между сообщениями проходят десятки секунд. Неподвижная строка
// читается как зависание.
func TestProgressRedrawsWithoutNewMessages(t *testing.T) {
	p := &progressLine{start: time.Now()}
	p.tick()
	defer func() { p.mu.Lock(); p.stop(); p.mu.Unlock() }()

	p.update(kb.Progress{Phase: "векторы", DocsDone: 10, DocsTotal: 100})
	// Ждём два тика: перерисовка обязана случиться сама.
	time.Sleep(2200 * time.Millisecond)

	p.mu.Lock()
	stopped := p.stopped
	p.mu.Unlock()
	if stopped {
		t.Error("таймер перерисовки остановился сам")
	}
}

// Done останавливает таймер: строка не должна дописываться после конца работы.
func TestProgressStopsOnDone(t *testing.T) {
	p := &progressLine{start: time.Now()}
	p.tick()
	p.update(kb.Progress{Phase: "векторы", DocsDone: 1, DocsTotal: 10})
	p.update(kb.Progress{Done: true})

	p.mu.Lock()
	stopped := p.stopped
	p.mu.Unlock()
	if !stopped {
		t.Error("после Done таймер обязан быть остановлен")
	}
}

// Список добавленных книг показывается, а длинный — сворачивается.
func TestPrintAddedIsSafe(t *testing.T) {
	// Пустой список ничего не печатает и не падает.
	printAdded(io.Discard, nil)

	many := make([]kb.AddedBook, 100)
	for i := range many {
		many[i] = kb.AddedBook{Title: strings.Repeat("к", 10), Year: 2024, Chunks: 5}
	}
	printAdded(io.Discard, many) // не должно ни падать, ни печатать сотню строк
}

// Полоса заполняется пропорционально и не выходит за края.
func TestBar(t *testing.T) {
	cases := []struct{ pct, full int }{
		{0, 0}, {50, 10}, {100, 20}, {-5, 0}, {150, 20}, {7, 1},
	}
	for _, c := range cases {
		got := bar(c.pct)
		full := strings.Count(got, "█")
		if full != c.full {
			t.Errorf("bar(%d): заполнено %d, ожидалось %d — %q", c.pct, full, c.full, got)
		}
		if n := len([]rune(got)); n != 22 {
			t.Errorf("bar(%d): ширина %d знаков, ожидалось 22", c.pct, n)
		}
	}
}

// По завершении полоса дорисовывается до конца, а не остаётся где застало.
//
// Последнее сообщение о ходе приходит раньше конца работы, и строка оставалась
// на экране с «75%» у законченного дела — читается как оборванная работа.
func TestProgressFinishesFull(t *testing.T) {
	p := &progressLine{start: time.Now()}
	p.update(kb.Progress{Phase: "индекс", DocsDone: 3000, DocsTotal: 3951})
	p.update(kb.Progress{Done: true})

	p.mu.Lock()
	done, total, stopped := p.done, p.total, p.stopped
	p.mu.Unlock()
	if done != total {
		t.Errorf("после завершения %d/%d — полоса осталась незакрытой", done, total)
	}
	if !stopped {
		t.Error("после завершения таймер должен быть остановлен")
	}
}
