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

// touchGraph сдвигает время правки журнала связей — то, что кэш видит после
// дозаписи сборкой. Сдвиг разный у разных вызовов, иначе отпечаток совпал бы.
func touchGraph(t *testing.T, dir string, shift time.Duration) {
	t.Helper()
	at := time.Now().Add(shift)
	if err := os.Chtimes(filepath.Join(dir, DirName, edgesFile), at, at); err != nil {
		t.Fatal(err)
	}
}

// waitNewGraph спрашивает кэш, пока он не отдаст экземпляр, отличный от old.
func waitNewGraph(t *testing.T, c *Cache, dir string, old *Graph) *Graph {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g, rel, err := c.Get(dir, 100)
		if err != nil {
			t.Fatal(err)
		}
		rel()
		if g != old {
			return g
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("свежий граф так и не подменил прежний")
	return nil
}

// Фоновый режим: граф с изменившимися файлами отдаётся сразу и помечен,
// свежий открывается в фоне и подменяет прежний.
//
// Ради этого режим и заведён: пока идёт сборка, файлы меняются каждые
// несколько секунд, и открытие на каждый вызов (77 с на рабочей машине) не давало
// службе ответить ни разу.
func TestCacheBackgroundServesOpenGraphAndRefreshes(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{}).RefreshInBackground()
	defer c.Close()

	g1, rel1, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	rel1()
	if _, refreshing := g1.Freshness(); refreshing {
		t.Error("только что открытый граф помечен отставшим")
	}

	touchGraph(t, dir, time.Hour)
	g2, rel2, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	if g2 != g1 {
		t.Fatal("файлы изменились, а кэш открыл граф заново и заставил ждать — фоновый режим не работает")
	}
	if opened, refreshing := g2.Freshness(); !refreshing || opened.IsZero() {
		t.Errorf("отданный отставший граф не помечен (открыт %v, обновляется %v): служба не скажет, что ответ отстаёт",
			opened, refreshing)
	}

	g3 := waitNewGraph(t, c, dir, g1)
	if _, refreshing := g3.Freshness(); refreshing {
		t.Error("свежий граф помечен отставшим")
	}
	if c.Len() != 1 {
		t.Errorf("после подмены держится %d графов, ожидался 1", c.Len())
	}
}

// Фоновая подмена не закрывает граф, которым ещё пользуются.
func TestCacheBackgroundKeepsBusyGraphAlive(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{}).RefreshInBackground()
	defer c.Close()

	g1, rel1, err := c.Get(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	touchGraph(t, dir, 2*time.Hour)
	waitNewGraph(t, c, dir, g1)

	// Прежний экземпляр занят — чтение по нему обязано пройти.
	if st := g1.Stats(100); st.Entities != 0 {
		t.Errorf("в пустом графе понятий %d", st.Entities)
	}
	rel1()
}

// Холодный кэш и много вопросов разом: открытие одно, экземпляр у всех общий.
func TestCacheBackgroundParallelColdGet(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{}).RefreshInBackground()
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

// Прогрев открывает граф в фоне; открытый прогревом граф живёт по сроку простоя.
func TestCacheWarmAndIdle(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(100*time.Millisecond, Rules{}).RefreshInBackground()
	defer c.Close()

	c.Warm(dir, 100)
	deadline := time.Now().Add(5 * time.Second)
	for c.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Len() != 1 {
		t.Fatal("прогрев не открыл граф")
	}
	// Никто не спрашивает — граф, открытый прогревом, закрывается по сроку простоя.
	deadline = time.Now().Add(5 * time.Second)
	for c.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Len() != 0 {
		t.Error("граф, открытый прогревом, не закрылся после простоя")
	}
}

// Close дожидается фонового открытия, а после Close кэш графов не выдаёт.
func TestCacheBackgroundCloseWaitsForLoad(t *testing.T) {
	dir := cacheFixture(t)
	c := NewCache(0, Rules{}).RefreshInBackground()

	c.Warm(dir, 100)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Errorf("после Close держится %d графов", c.Len())
	}
	if _, _, err := c.Get(dir, 100); err == nil {
		t.Error("Get после Close обязан отказать, а не открыть граф мимо кэша")
	}
}
