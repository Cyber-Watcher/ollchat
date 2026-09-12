package graph

import (
	"errors"
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
// **Обновление в фоне (RefreshInBackground, служба ollmcp, 11.09.2026).** Пока
// идёт сборка, отпечаток меняется между любыми двумя вызовами, и кэш, честно
// открывая граф заново на каждый, не помогал вовсе: на рабочей машине открытие графа
// books — 77 с, три подряд kb_status заняли 78/77/77 с, клиент MCP не дождался
// ни одного. В фоновом режиме устаревший граф отдаётся сразу, а свежий
// открывается в фоне — не больше одного открытия на коллекцию — и подменяет
// прежний, когда готов. Ответ отстаёт от диска на одно открытие; экземпляр
// графа об этом знает (Graph.Freshness), и служба пишет это в ответе. Без
// фонового режима кэш ведёт себя как прежде: интерфейсу ollchat отставание
// ни к чему, там граф спрашивают редко.
//
// Читать открытый граф можно из нескольких горутин: всё лежит в памяти под
// `sync.RWMutex`, файлы открыты только на дозапись.

// Cache держит открытые графы по каталогам коллекций.
type Cache struct {
	rules Rules         // по каким правилам открывать графы
	ttl   time.Duration // сколько граф живёт без обращений; 0 — вечно

	mu         sync.Mutex
	open       map[string]*cachedGraph
	background bool                  // устаревший отдаётся сразу, свежий — в фоне
	loading    map[string]*cacheLoad // идущие фоновые открытия, по одному на коллекцию
	closed     bool                  // Close позван: фоновые открытия свой граф закрывают
	loads      sync.WaitGroup        // Close дожидается фоновых открытий
}

type cachedGraph struct {
	g     *Graph
	stamp string
	refs  int
	stale bool // файлы изменились: закрыть, как только отпустят
	timer *time.Timer
}

// cacheLoad — одно фоновое открытие графа; done закрывается по его окончании.
type cacheLoad struct {
	done chan struct{}
	err  error
}

// errCacheClosed — кэш закрыли, пока ждали открытия графа.
var errCacheClosed = errors.New("кэш графа закрыт")

// NewCache заводит кэш. ttl — срок простоя, 0 — держать, пока не закроют.
func NewCache(ttl time.Duration, rules Rules) *Cache {
	return &Cache{ttl: ttl, rules: rules,
		open: map[string]*cachedGraph{}, loading: map[string]*cacheLoad{}}
}

// RefreshInBackground включает фоновое обновление: граф, чьи файлы изменились,
// отдаётся сразу, а свежий открывается в фоне и подменяет его, когда готов.
// Звать до первого Get. Возвращает тот же кэш — для записи в одну строку.
func (c *Cache) RefreshInBackground() *Cache {
	c.mu.Lock()
	c.background = true
	c.mu.Unlock()
	return c
}

// Get отдаёт открытый граф и возврат: **вызывать возврат обязательно**,
// иначе граф не закроется никогда.
//
// Пока граф кем-то занят, он не закрывается — даже если файлы изменились
// и открыт уже новый. Иначе долгий поиск читал бы закрытые под ним файлы.
//
// В фоновом режиме (RefreshInBackground) граф с изменившимися файлами отдаётся
// сразу, с пометкой в Graph.Freshness; ждать приходится, только если отдать
// нечего — тогда ждут одного общего открытия, а не открывают каждый своё.
func (c *Cache) Get(collDir string, chunks int) (*Graph, func(), error) {
	c.mu.Lock()
	background := c.background
	c.mu.Unlock()
	if background {
		return c.getBackground(collDir, chunks)
	}
	return c.getSync(collDir, chunks)
}

// getSync — прежний порядок: изменились файлы — открыть заново и дождаться.
func (c *Cache) getSync(collDir string, chunks int) (*Graph, func(), error) {
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

// getBackground — фоновый режим: есть что отдать — отдаём сразу.
func (c *Cache) getBackground(collDir string, chunks int) (*Graph, func(), error) {
	dir := filepath.Join(collDir, DirFor(c.rules.Name))
	stamp := dirStamp(dir)

	c.mu.Lock()
	for {
		if c.closed {
			c.mu.Unlock()
			return nil, nil, errCacheClosed
		}
		if e, ok := c.open[collDir]; ok {
			if e.stamp != stamp {
				// Сборка дописала файлы: отдаём то, что открыто, и открываем свежий.
				e.g.refreshing.Store(true)
				c.startLoad(collDir, chunks)
			}
			c.hold(e)
			c.mu.Unlock()
			return e.g, func() { c.release(collDir, e) }, nil
		}
		// Отдать нечего: ждём открытия — своего или уже идущего.
		l := c.startLoad(collDir, chunks)
		c.mu.Unlock()
		<-l.done
		if l.err != nil {
			return nil, nil, l.err
		}
		// Сверяем с диском заново: пока ждали, сборка могла дописать ещё.
		stamp = dirStamp(dir)
		c.mu.Lock()
	}
}

// Warm открывает граф коллекции в фоне, если он ещё не открыт и не открывается:
// чтобы первый вопрос после запуска службы не ждал открытия графа.
func (c *Cache) Warm(collDir string, chunks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if _, ok := c.open[collDir]; ok {
		return
	}
	c.startLoad(collDir, chunks)
}

// startLoad заводит фоновое открытие графа коллекции или отдаёт уже идущее.
// Звать под c.mu.
func (c *Cache) startLoad(collDir string, chunks int) *cacheLoad {
	if l := c.loading[collDir]; l != nil {
		return l
	}
	l := &cacheLoad{done: make(chan struct{})}
	c.loading[collDir] = l
	c.loads.Add(1)
	go func() {
		defer c.loads.Done()
		// Отпечаток снимается ДО открытия: правка, пришедшая во время открытия,
		// даст расхождение и следующее обновление, а не потеряется.
		stamp := dirStamp(filepath.Join(collDir, DirFor(c.rules.Name)))
		g, err := Open(collDir, chunks, c.rules)

		c.mu.Lock()
		delete(c.loading, collDir)
		l.err = err
		switch {
		case err != nil:
			// Свежий не открылся — прежний остаётся, и следующий вызов попробует снова.
			if old, ok := c.open[collDir]; ok {
				old.g.refreshing.Store(false)
			}
		case c.closed:
			g.Close()
		default:
			if old, ok := c.open[collDir]; ok {
				old.stale = true
				c.stopTimer(old)
				if old.refs == 0 {
					old.g.Close()
				}
			}
			e := &cachedGraph{g: g, stamp: stamp}
			c.open[collDir] = e
			c.arm(collDir, e) // его ещё никто не взял — пошёл срок простоя
		}
		c.mu.Unlock()
		close(l.done)
	}()
	return l
}

// Close закрывает всё, что держит кэш. Занятые графы закроются, когда их
// отпустят: закрывать файлы из-под работающего поиска нельзя. Идущие фоновые
// открытия Close дожидается — они закрывают свой граф сами.
func (c *Cache) Close() error {
	c.mu.Lock()
	c.closed = true
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
	c.mu.Unlock()
	c.loads.Wait()
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
	c.arm(collDir, e)
}

// arm заводит срок простоя графа, которым сейчас никто не пользуется.
// Звать под c.mu.
func (c *Cache) arm(collDir string, e *cachedGraph) {
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
