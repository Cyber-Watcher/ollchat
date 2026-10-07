package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// manyTopics — n тем нижнего уровня по пять понятий, без описаний и с ними:
// годятся и для резюме, и для выводов.
func manyTopics(n int, described bool) *Communities {
	c := &Communities{}
	for i := 0; i < n; i++ {
		com := Community{ID: i + 1, Level: 0, Members: members(uint32(i*25+1), uint32(i*25+25))}
		if described {
			com.Title, com.Summary, com.Rating = "Тема", "описание", 9
		}
		c.List = append(c.List, com)
	}
	return c
}

// breakSave делает запись разбиения невозможной: на месте файла и его
// прежней копии — непустые каталоги (под root права не остановили бы).
func breakSave(t *testing.T, g *Graph) {
	t.Helper()
	for _, name := range []string{CommunityFile, PrevCommunityFile} {
		dir := filepath.Join(g.Dir(), name)
		must(t, os.MkdirAll(dir, 0o755))
		must(t, os.WriteFile(filepath.Join(dir, "занято"), []byte("x"), 0o644))
	}
}

// within ждёт завершения fn не дольше d; зависание — провал теста, а не
// вечный прогон.
func within(t *testing.T, d time.Duration, what string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("%s не вернулся за %s — раздатчик повис на канале без читателей", what, d)
		return nil
	}
}

// Рабочие ушли после неудачной записи на диск — раздача кончается, а не
// висит на канале, который больше некому читать (аудит 07.10.2026, №12).
func TestSummarizeReturnsWhenWorkersGone(t *testing.T) {
	g, _ := graph(t)
	breakSave(t, g)
	m := &model{answer: okAnswer("Название")}
	err := within(t, 5*time.Second, "Summarize", func() error {
		return g.Summarize(context.Background(), m, manyTopics(25, false), SummaryOpts{Workers: 1}, nil)
	})
	if err == nil {
		t.Fatal("неудачная запись разбиения не превратилась в ошибку")
	}
}

// Ctrl+C посреди описаний: работа возвращается сразу, сделанное сохраняется.
func TestSummarizeCancelReturnsPromptly(t *testing.T) {
	g, _ := graph(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &model{answer: func(n int) (string, error) {
		// Пока модель думает, раздатчик успевает встать на отправке
		// следующей темы; затем приходит Ctrl+C.
		time.Sleep(50 * time.Millisecond)
		cancel()
		return "", ctx.Err()
	}}
	err := within(t, 5*time.Second, "Summarize", func() error {
		return g.Summarize(ctx, m, manyTopics(10, false), SummaryOpts{Workers: 1}, nil)
	})
	if err == nil {
		t.Fatal("отмена не превратилась в ошибку")
	}
}

// То же у выводов по темам: раздача не переживает ушедших рабочих.
func TestFindingsReturnsWhenWorkersGone(t *testing.T) {
	g, _ := graph(t)
	breakSave(t, g)
	m := &model{answer: func(int) (string, error) {
		return `{"findings":[{"title":"Вывод","text":"Пояснение."}]}`, nil
	}}
	err := within(t, 5*time.Second, "Findings", func() error {
		return g.Findings(context.Background(), m, manyTopics(25, true), FindingsOpts{Workers: 1}, nil)
	})
	if err == nil {
		t.Fatal("неудачная запись разбиения не превратилась в ошибку")
	}
}
