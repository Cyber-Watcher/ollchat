package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
)

// Интерфейс с общей библиотекой (kb.server_url, этап 121): свой граф не
// открывается — карту считает служба, свои файлы — только запасным путём.

// remoteTestModel — модель с коллекцией go и графом при ней на диске:
// без библиотеки интерфейсу было бы что открыть.
func remoteTestModel(t *testing.T) (*Model, *kb.Collection) {
	t.Helper()
	m, books := kbTestModel(t)
	m.cfg.KB.EmbedModel = "" // без эмбеддера подмес считается без сети
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	m.runCommand("/kb use go")
	coll, err := m.kbCollection("go")
	if err != nil {
		t.Fatal(err)
	}
	buildTestGraph(t, coll)
	m.cfg.Mix.Graph, m.cfg.Graph.Cache = true, true
	m.gr.autoOn = true
	m.modelCaps, m.modelRealTools = []string{"completion", "tools"}, true
	return m, coll
}

// fakeService — служба общей библиотеки: книги той же коллекции, карта по
// графу, который она открыла у себя (в тесте — с того же диска).
type fakeService struct {
	t     *testing.T
	coll  *kb.Collection
	down  bool
	calls int
	asked []string // какие коллекции у службы просили открыть
}

var errServiceDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", kb.ErrUnreachable)

func (s *fakeService) Names() ([]string, error) {
	if s.down {
		return nil, errServiceDown
	}
	return []string{s.coll.Name()}, nil
}

func (s *fakeService) Source(name string) (kb.Source, error) {
	s.asked = append(s.asked, name)
	if s.down {
		return nil, errServiceDown
	}
	return s.coll, nil
}

func (s *fakeService) GraphSearch(_ context.Context, _, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error) {
	s.calls++
	if s.down {
		return graph.SearchResult{}, nil, errServiceDown
	}
	g, err := graph.Open(s.coll.Dir(), s.coll.ChunkCount(), graph.Rules{})
	if err != nil {
		s.t.Fatalf("служба не открыла свой граф: %v", err)
	}
	defer g.Close()
	res := g.Search(query, graph.SearchOpts{TopEntities: entities, TopNeighbors: neighbors, TopChunks: 1, MinMentions: 2})
	return res, find.Chain(g, res.Entities, res.Relations, find.Opts{}), nil
}

var _ mixer.MapSource = (*fakeService)(nil)

// Прогрев при запуске: без библиотеки открывает свой граф, с библиотекой —
// нет: граф уже держит служба (А1).
func TestWarmGraphSkippedWithLibrary(t *testing.T) {
	m, coll := remoteTestModel(t)
	if m.warmGraphCmd(make(chan graph.OpenProgress, 8)) == nil {
		t.Fatal("подготовка: без библиотеки прогрев своего графа не запущен")
	}
	m.SetLibrary(&fakeService{t: t, coll: coll})
	if m.warmGraphCmd(make(chan graph.OpenProgress, 8)) != nil {
		t.Error("с общей библиотекой интерфейс прогревает свой граф")
	}
}

// Проверка здоровья графа при запуске открывает свою коллекцию; с библиотекой
// её нет — замер 22.10.2026: TUI без своего графа держал коллекцию books,
// 1 332 296 КБ.
func TestGraphHealthSkippedWithLibrary(t *testing.T) {
	m, coll := remoteTestModel(t)
	if m.checkGraphHealthCmd() == nil {
		t.Fatal("подготовка: без библиотеки проверка здоровья не запущена")
	}
	m.SetLibrary(&fakeService{t: t, coll: coll})
	if m.checkGraphHealthCmd() != nil {
		t.Error("с общей библиотекой проверка здоровья открывает свою коллекцию")
	}
}

// openFilesUnder — сколько файлов внутри dir держит открытыми этот процесс.
// Тот же прибор, что у замера 22.10.2026: файлы коллекций в /proc/<pid>/fd.
func openFilesUnder(t *testing.T, dir string) int {
	t.Helper()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("нет /proc/self/fd, открытые файлы не сосчитать: %v", err)
	}
	n := 0
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err == nil && strings.HasPrefix(target, dir+string(filepath.Separator)) {
			n++
		}
	}
	return n
}

// startupTestModel — модель с коллекцией go и графом, у которой своя база
// только что открыта и ни одна коллекция ещё не прочитана, как сразу после
// запуска. Настройки — те, при которых запуск без библиотеки коллекцию
// открывает: сверка папок, эмбеддер, подмес графа с прогревом.
func startupTestModel(t *testing.T) (*Model, *kb.Collection) {
	t.Helper()
	m, coll := remoteTestModel(t)
	// Коллекцию открыли /kb add и постройка графа: закрываем, базу берём новую.
	if err := m.kb.base.Close(); err != nil {
		t.Fatal(err)
	}
	base, err := kb.OpenBase(m.kb.base.Dir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	m.kb.base = base
	m.cfg.KB.SyncOnStart = true
	m.cfg.KB.EmbedModel = "bge-m3"
	m.cfg.KB.RerankURL = "" // проверка службы переранжирования ушла бы в сеть
	if n := openFilesUnder(t, coll.Dir()); n != 0 {
		t.Fatalf("подготовка: до запуска открыто файлов коллекции %d", n)
	}
	return m, coll
}

// runStartupKB выполняет всё, что читает базу знаний при запуске и в простое:
// команды запуска и проверки по таймерам здоровья, эмбеддера и реранкера.
func runStartupKB(m *Model) {
	cmds := m.kbStartupCmds(make(chan graph.OpenProgress, 64))
	for _, tick := range []tea.Msg{healthTickMsg{}, embedTickMsg{}, rerankTickMsg{}} {
		_, cmd := m.Update(tick)
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		// Прогретый граф без библиотеки — закрыть: его держала бы модель.
		if msg, ok := cmd().(mixReadyMsg); ok && msg.graph != nil {
			defer msg.graph.Close()
		}
	}
}

// С библиотекой запуск и проверки в простое не открывают ни одной своей
// коллекции: замер 22.10.2026 — TUI с kb.server_url держал открытыми все
// четыре коллекции (282 файла, 1 329 976 КБ), их открывала сверка папок
// при запуске и проверка эмбеддера.
func TestStartupOpensNoOwnCollectionWithLibrary(t *testing.T) {
	m, coll := startupTestModel(t)
	m.SetLibrary(&fakeService{t: t, coll: coll})
	runStartupKB(m)
	if n := openFilesUnder(t, coll.Dir()); n != 0 {
		t.Errorf("с общей библиотекой запуск открыл своих файлов коллекции: %d", n)
	}

	// Без библиотеки те же команды коллекцию открывают — иначе счёт выше
	// ничего бы не доказывал.
	direct, dcoll := startupTestModel(t)
	runStartupKB(direct)
	if n := openFilesUnder(t, dcoll.Dir()); n == 0 {
		t.Fatal("подготовка: без библиотеки запуск не открыл ни одного файла коллекции — прибор слеп")
	}
}

// Подмес с библиотекой: карта — от службы, свой граф не открыт, значка «GR»
// нет; задача подмеса не собирается ничего открывать (А1, А2).
func TestRemoteMixDoesNotOpenOwnGraph(t *testing.T) {
	m, coll := remoteTestModel(t)
	svc := &fakeService{t: t, coll: coll}
	m.SetLibrary(svc)

	job, ok := m.mixPlan()
	if !ok {
		t.Fatal("с библиотекой подмес не планируется")
	}
	if job.needsOpen() || job.graphOpen != nil {
		t.Errorf("задача подмеса собирается открыть свой граф: needsOpen %v, graphOpen %v", job.needsOpen(), job.graphOpen)
	}

	mix := m.autoMix("как связаны goroutine и channel")
	if mix.Entities == 0 || !strings.Contains(mix.Text, "goroutine") {
		t.Fatalf("карта от службы не подмешалась: %+v", mix)
	}
	if svc.calls != 1 {
		t.Errorf("карта у службы спрошена %d раз, ждали один", svc.calls)
	}
	if mix.Remote != "" {
		t.Errorf("служба ответила, а заметка о ней есть: %q", mix.Remote)
	}
	if m.gr.open != nil || m.graphMemoryStatus() != "" {
		t.Errorf("свой граф лёг в модель: open %v, значок %q", m.gr.open, m.graphMemoryStatus())
	}
}

// Служба не отвечает — подмес по своим файлам: граф из общего кэша (его
// закроет простой), в модель он не ложится, а под вопросом строка (А4).
func TestRemoteMixFallsBackThroughCache(t *testing.T) {
	m, coll := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	m.SetLibrary(&fakeService{t: t, coll: coll, down: true})

	mix := m.autoMix("как связаны goroutine и channel")
	if mix.Entities == 0 {
		t.Fatalf("при недоступной службе карта по своему графу не подмешалась: %+v", mix)
	}
	if mix.Remote != kb.FallbackNote {
		t.Errorf("заметка %q, ждали %q", mix.Remote, kb.FallbackNote)
	}
	if m.gr.open != nil {
		t.Error("граф запасного пути лёг в модель до выхода")
	}
	if cache.Len() != 1 {
		t.Errorf("граф запасного пути не в общем кэше: графов в кэше %d", cache.Len())
	}

	// Строка под вопросом — и в /mix show.
	if text := mixShowText("вопрос", mix, ""); !strings.Contains(text, kb.FallbackNote) {
		t.Errorf("/mix show молчит о переходе на свои файлы:\n%s", text)
	}
}

// Своя единственная коллекция, выбранная сама при пустом kb.default, службе
// не уходит: у неё такой может не быть, а инструменты модели идут в её
// умолчание. Службе — пустое имя; явный /kb use — уже выбор человека.
func TestPickedCollectionNotSentToService(t *testing.T) {
	first, coll := remoteTestModel(t)
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.KB.Dir, cfg.KB.Roots = first.cfg.KB.Dir, first.cfg.KB.Roots
		cfg.KB.Default, cfg.KB.EmbedModel = "", ""
	})
	if !m.kb.picked || m.kb.use != "go" {
		t.Fatalf("подготовка: единственная коллекция не выбрана сама: use %q, picked %v", m.kb.use, m.kb.picked)
	}
	svc := &fakeService{t: t, coll: coll}
	m.SetLibrary(svc)
	m.kb.autoOn = true

	m.autoMix("как связаны goroutine и channel")
	if len(svc.asked) != 1 || svc.asked[0] != "" {
		t.Errorf("службе ушло имя %q, ждали пустое (её умолчание)", svc.asked)
	}

	m.runCommand("/kb use go")
	svc.asked = nil
	m.autoMix("как связаны goroutine и channel")
	if len(svc.asked) != 1 || svc.asked[0] != "go" {
		t.Errorf("после /kb use go службе ушло %q, ждали go", svc.asked)
	}
}

// Служба не отвечает, а карта выключена: свой граф не открывается и в общий
// кэш не ложится — подмешиваются только книги (рецензия этапа 121).
func TestRemoteMixMapOffKeepsGraphCacheEmpty(t *testing.T) {
	m, coll := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	svc := &fakeService{t: t, coll: coll, down: true}
	m.SetLibrary(svc)
	m.cfg.Mix.Graph, m.gr.autoOn = false, false
	m.kb.autoOn = true

	mix := m.autoMix("как связаны goroutine и channel")
	if cache.Len() != 0 {
		t.Errorf("карта выключена, а свой граф открыт: графов в кэше %d", cache.Len())
	}
	if svc.calls != 0 {
		t.Errorf("карта выключена, а у службы спрошена %d раз", svc.calls)
	}
	if mix.Entities != 0 {
		t.Errorf("карта подмешана при выключенной карте: %+v", mix)
	}
}

// Служба не отвечает, а своих файлов нет — подмешивать нечего, и человек
// видит почему: адрес службы.
func TestRemoteMixServiceDownWithoutOwnFiles(t *testing.T) {
	m := newTestModelWith(t, nil)
	m.kb.base = nil
	m.SetLibrary(&fakeService{t: t, down: true})
	m.kb.autoOn = true
	mix := m.autoMix("как связаны goroutine и channel")
	if !mix.Empty() || !strings.Contains(mix.Remote, "kb.example") {
		t.Errorf("подмес без службы и своих файлов: %+v", mix)
	}
}
