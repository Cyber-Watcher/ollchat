package graph

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Открытый граф, живущий между вызовами.
//
// **Зачем.** Открытие графа коллекции books — 11.5 с; граф удерживает 160 МБ,
// а пик процесса при открытии доходит до 1.03 ГБ
// (замер 28.08.2026: 750 тыс. понятий, 343 тыс. связей, векторы). В командной
// строке это платится один раз за запуск и незаметно. А `ollmcp` — служба:
// она живёт неделями, и каждый вопрос ассистента открывал тот же самый граф
// заново, платя одиннадцать секунд там, где нужно ноль.
//
// **Почему не просто открыть один раз навсегда.** Гигабайт памяти в службе,
// которая ради этого и работает, — честная цена; тот же гигабайт в TUI, где
// граф спрашивают раз в час, — нет. Поэтому у кэша есть срок простоя: после
// него граф закрывается сам, а следующий вопрос откроет его снова.
//
// **Почему отпечаток файлов, а не «открыли и держим».** Сборка графа идёт
// другим процессом и дописывает те же файлы. Кэш обязан это замечать, иначе
// служба неделями отвечала бы по графу недельной давности. Отпечаток — имена,
// размеры и времена правки файлов каталога графа; изменился — открываем заново.
//
// Читать открытый граф можно из нескольких горутин: всё лежит в памяти под
// `sync.RWMutex`, файлы открыты только на дозапись.

// Cache держит открытые графы по каталогам коллекций.
type Cache struct {
	rules Rules         // по каким правилам открывать графы
	ttl   time.Duration // сколько граф живёт без обращений; 0 — вечно

	mu   sync.Mutex
	open map[string]*cachedGraph
}

type cachedGraph struct {
	g     *Graph
	stamp string
	refs  int
	stale bool // файлы изменились: закрыть, как только отпустят
	timer *time.Timer
}

// NewCache заводит кэш. ttl — срок простоя, 0 — держать, пока не закроют.
func NewCache(ttl time.Duration, rules Rules) *Cache {
	return &Cache{ttl: ttl, rules: rules, open: map[string]*cachedGraph{}}
}

// Get отдаёт открытый граф и возврат: **вызывать возврат обязательно**,
// иначе граф не закроется никогда.
//
// Пока граф кем-то занят, он не закрывается — даже если файлы изменились
// и открыт уже новый. Иначе долгий поиск читал бы закрытые под ним файлы.
func (c *Cache) Get(collDir string, chunks int) (*Graph, func(), error) {
	dir := filepath.Join(collDir, DirFor(c.rules.Name))
	stamp := dirStamp(dir)

	c.mu.Lock()
	if e, ok := c.open[collDir]; ok {
		if e.stamp == stamp {
			c.hold(e)
			c.mu.Unlock()
			return e.g, func() { c.release(collDir, e) }, nil
		}
		// Файлы изменились: этот экземпляр больше никому не выдаём.
		e.stale = true
		c.stopTimer(e)
		if e.refs == 0 {
			e.g.Close()
		}
		delete(c.open, collDir)
	}
	c.mu.Unlock()

	// Открываем без замка: это секунды, и держать на них общий замок значило бы
	// выстроить в очередь все коллекции разом.
	g, err := Open(collDir, chunks, c.rules)
	if err != nil {
		return nil, nil, err
	}

	c.mu.Lock()
	// Пока открывали, мог успеть другой: тогда его и отдаём, а свой закрываем.
	if e, ok := c.open[collDir]; ok && e.stamp == stamp {
		c.hold(e)
		c.mu.Unlock()
		g.Close()
		return e.g, func() { c.release(collDir, e) }, nil
	}
	e := &cachedGraph{g: g, stamp: stamp, refs: 1}
	c.open[collDir] = e
	c.mu.Unlock()
	return g, func() { c.release(collDir, e) }, nil
}

// Close закрывает всё, что держит кэш. Занятые графы закроются, когда их
// отпустят: закрывать файлы из-под работающего поиска нельзя.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for dir, e := range c.open {
		e.stale = true
		c.stopTimer(e)
		if e.refs == 0 {
			if err := e.g.Close(); err != nil && first == nil {
				first = err
			}
		}
		delete(c.open, dir)
	}
	return first
}

// Len — сколько графов держится открытыми. Для проверок и отчёта о службе.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.open)
}

func (c *Cache) hold(e *cachedGraph) {
	e.refs++
	c.stopTimer(e)
}

func (c *Cache) release(collDir string, e *cachedGraph) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.refs--
	if e.refs > 0 {
		return
	}
	if e.stale {
		e.g.Close()
		return
	}
	if c.ttl <= 0 {
		return // держим, пока не позовут Close
	}
	e.timer = time.AfterFunc(c.ttl, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if e.refs > 0 || e.stale {
			return // успели снова взять или уже закрыт
		}
		e.stale = true
		e.g.Close()
		if cur, ok := c.open[collDir]; ok && cur == e {
			delete(c.open, collDir)
		}
	})
}

func (c *Cache) stopTimer(e *cachedGraph) {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// dirStamp — отпечаток каталога графа: имена, размеры и времена правки.
//
// Считается на каждое обращение, поэтому дешевизна важнее точности: восемь
// файлов, восемь `Stat`. Содержимое не читается — сборка только дозаписывает,
// и размер меняется всегда.
func dirStamp(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "нет каталога"
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(fi.Size(), 10))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		b.WriteByte('\n')
	}
	return b.String()
}
