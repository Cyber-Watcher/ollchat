package graph

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// cacheFixture: коллекция с готовым графом из одной связи.
func cacheFixture(t *testing.T) string {
	t.Helper()
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Второе обращение отдаёт тот же экземпляр: ради этого кэш и заведён.
func TestCacheReusesOpenGraph(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{})
	defer c.Close()

	g1, rel1, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	g2, rel2, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if g1 != g2 {
		t.Error("второе обращение открыло граф заново — кэш не работает")
	}
	rel1()
	rel2()
	if c.Len() != 1 {
		t.Errorf("после возврата держится %d графов, ожидался 1", c.Len())
	}
}

// Файлы изменились — кэш обязан это заметить.
//
// Сборка идёт другим процессом и дописывает те же файлы; не заметив, служба
// неделями отвечала бы по старому графу.
func TestCacheNoticesChangedFiles(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{})
	defer c.Close()

	g1, rel1, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	rel1()

	// Дозапись в журнал связей — ровно то, что делает сборка.
	path := filepath.Join(dir, DirName, edgesFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, edgeSize)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	g2, rel2, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if g1 == g2 {
		t.Error("файлы изменились, а кэш отдал прежний граф")
	}
}

// Пока граф занят, его не закрывают — даже если он уже устарел.
//
// Иначе долгий поиск читал бы файлы, закрытые под ним.
func TestCacheKeepsBusyGraphAlive(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{})
	defer c.Close()

	g1, rel1, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Устареваем, не отпуская.
	if err := os.Chtimes(filepath.Join(dir, DirName, edgesFile),
		time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, rel2, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()

	// Прежний экземпляр обязан оставаться живым: им ещё пользуются. Чтение
	// по нему после устаревания и есть проверка — на закрытых файлах оно
	// не прошло бы.
	if st := g1.Stats(100); st.Entities != 0 {
		t.Errorf("в пустом графе понятий %d", st.Entities)
	}
	rel1()
}

// Срок простоя: никто не спрашивал — граф закрылся сам.
func TestCacheClosesAfterIdle(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(50*time.Millisecond, Rules{})
	defer c.Close()

	_, rel, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	rel()

	deadline := time.Now().Add(2 * time.Second)
	for c.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Len() != 0 {
		t.Error("граф не закрылся после простоя — служба держала бы гигабайт впустую")
	}
}

// Одновременные обращения: граф открывается один раз, лишние закрываются.
func TestCacheParallelGet(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{})
	defer c.Close()

	var wg sync.WaitGroup
	got := make([]*Graph, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g, rel, err := c.Get(dir, 100)
			if err != nil {
				t.Error(err)
				return
			}
			defer rel()
			got[i] = g
		}(i)
	}
	wg.Wait()
	for i, g := range got {
		if g != got[0] {
			t.Fatalf("горутина %d получила другой экземпляр графа", i)
		}
	}
	if c.Len() != 1 {
		t.Errorf("держится %d графов, ожидался 1", c.Len())
	}
}
