package kb

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
)

// Поиск идёт, пока в той же коллекции доливают и удаляют книги.
//
// Так устроена служба и сам ollchat: один объект коллекции на процесс,
// поиск из нескольких потоков, а рядом /kb add или --kb-sync. До 07.10.2026
// доливка переоткрывала индекс без замка коллекции — закрывала файлы
// и обнуляла хранилище посреди чужого поиска: под -race сотни отчётов,
// без него паника на nil и «file already closed». Тест ловит это под -race,
// а панику и ошибки чтения — и без него.
func TestSearchDuringAddAndForget(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		makeBook(t, books, fmt.Sprintf("b%d.pdf", i), longPage(fmt.Sprintf("goroutines topic%d", i)))
	}
	c, err := base.Create("race", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Add(ctx, []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := c.Search("goroutines", DefaultSearchOpts()); err != nil {
					errs <- err
					return
				}
				c.Stats()
				c.ChunkByRef(1, 0)
				c.ChunkCount()
			}
		}()
	}
	func() {
		defer func() { close(stop); wg.Wait() }()
		for i := 3; i < 9; i++ {
			p := makeBook(t, books, fmt.Sprintf("b%d.pdf", i), longPage(fmt.Sprintf("goroutines topic%d", i)))
			if _, err := c.Add(ctx, []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
				t.Errorf("доливка %d: %v", i, err)
				return
			}
			if err := c.Forget(p); err != nil {
				t.Errorf("удаление %d: %v", i, err)
				return
			}
		}
	}()
	close(errs)
	for err := range errs {
		t.Errorf("поиск во время доливки: %v", err)
	}
	if hits, _ := c.Search("topic0", DefaultSearchOpts()); len(hits) == 0 {
		t.Fatal("после доливок и удалений первая книга не находится")
	}
}
