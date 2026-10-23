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
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
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

func (s *fakeService) GraphMap(_ context.Context, _, query string, entities, neighbors int) (kbremote.GraphMap, error) {
	s.calls++
	if s.down {
		return kbremote.GraphMap{}, errServiceDown
	}
	g, err := graph.Open(s.coll.Dir(), s.coll.ChunkCount(), graph.Rules{})
	if err != nil {
		s.t.Fatalf("служба не открыла свой граф: %v", err)
	}
	defer g.Close()
	res := g.Search(query, graph.SearchOpts{TopEntities: entities, TopNeighbors: neighbors, TopChunks: 1, MinMentions: 2})
	return kbremote.GraphMap{Result: res, Chain: find.Chain(g, res.Entities, res.Relations, find.Opts{})}, nil
}

var _ mixer.MapSource = (*fakeService)(nil)

// findService — служба, которая ещё и ищет /search у себя (вход
// /api/v1/find, kbremote.Client.Find): тем же find.Search по своей коллекции
// и своему графу. queued — «ждал в очереди» в каждом ответе.
type findService struct {
	fakeService
	finds  []kbremote.FindRequest
	queued time.Duration
}

func (s *findService) Find(ctx context.Context, req kbremote.FindRequest) (kbremote.FindReply, error) {
	s.finds = append(s.finds, req)
	if s.down {
		return kbremote.FindReply{}, errServiceDown
	}
	deps := find.Deps{Coll: s.coll}
	if req.Graph {
		g, err := graph.Open(s.coll.Dir(), s.coll.ChunkCount(), graph.Rules{})
		if err != nil {
			s.t.Fatalf("служба не открыла свой граф: %v", err)
		}
		defer g.Close()
		deps.Graph = g
	}
	res, err := find.Search(ctx, deps, req.Query, find.Opts{Collection: s.coll.Name(), TopK: req.TopK,
		Entities: req.Entities, Neighbors: req.Neighbors, Full: req.Full})
	if err != nil {
		return kbremote.FindReply{}, err
	}
	return kbremote.FindReply{Result: res, Text: "СЛУЖБА: " + find.Render(res, req.Full, s.coll),
		Collection: s.coll.Name(), Queued: s.queued}, nil
}

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

	runKBUse(t, m, "/kb use go")
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

// runSearch выполняет команду поиска и отдаёт выдачу в модель, как Update.
func runSearch(t *testing.T, m *Model, line string) searchDoneMsg {
	t.Helper()
	cmd := m.runCommand(line)
	if cmd == nil {
		t.Fatalf("%s: команда не запущена: %q", line, m.blocks[len(m.blocks)-1].text)
	}
	done, ok := cmd().(searchDoneMsg)
	if !ok {
		t.Fatalf("%s: не выдача поиска", line)
	}
	m.applySearchResult(done)
	return done
}

// Команды человека при kb.server_url идут в службу, как модель и подмешивание
// (решение владельца 23.10.2026: «Команды в службу»): /search и /kb search —
// её поиском по её графу, своя коллекция и свой граф не открываются; /read N
// читает кусок у службы. Прежде все четыре команды шли по своим файлам и
// открывали свой граф через кэш (аудит 23.10.2026, находка 1).
func TestSearchCommandsGoToService(t *testing.T) {
	m, coll := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	m.kb.coll = nil
	svc := &findService{fakeService: fakeService{t: t, coll: coll}, queued: 1200 * time.Millisecond}
	m.SetLibrary(svc)
	before := openFilesUnder(t, coll.Dir())

	done := runSearch(t, m, "/search goroutines channels")
	if done.err != nil {
		t.Fatalf("/search через службу: %v", done.err)
	}
	if len(svc.finds) != 1 || !svc.finds[0].Graph || svc.finds[0].Collection != "go" {
		t.Fatalf("/search службе не ушёл или ушёл не тот: %+v", svc.finds)
	}
	if cache.Len() != 0 || m.gr.open != nil {
		t.Errorf("/search при службе открыл свой граф: в кэше %d, в модели %v", cache.Len(), m.gr.open)
	}
	last := m.blocks[len(m.blocks)-1].text
	if !strings.HasPrefix(last, "СЛУЖБА: ") {
		t.Errorf("/search показал не выдачу службы:\n%s", last)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-2].text, "ждал в очереди 1200 мс") {
		t.Errorf("/search молчит об очереди службы: %q", m.blocks[len(m.blocks)-2].text)
	}
	if len(m.finds) == 0 {
		t.Fatal("выдача службы не запомнена для /read N")
	}

	done = runSearch(t, m, "/kb search goroutines")
	if done.err != nil || len(svc.finds) != 2 || svc.finds[1].Graph {
		t.Errorf("/kb search через службу: err %v, запросы %+v", done.err, svc.finds)
	}

	svc.asked = nil
	cmd := m.runCommand("/read 1")
	if cmd == nil {
		t.Fatalf("/read 1 при службе не ушёл в службу: %q", m.blocks[len(m.blocks)-1].text)
	}
	msg, ok := cmd().(noticeMsg)
	if !ok || !strings.Contains(msg.text, "goroutines") {
		t.Errorf("/read 1 через службу: %#v", msg)
	}
	if len(svc.asked) != 1 || svc.asked[0] != "go" {
		t.Errorf("/read 1 не спросил коллекцию у службы: %q", svc.asked)
	}
	if n := openFilesUnder(t, coll.Dir()); n > before {
		t.Errorf("команды при службе открыли свои файлы коллекции: было %d, стало %d", before, n)
	}
}

// Служба не ответила, а свои файлы есть — /search ищет по ним с пометкой,
// как модель (kb.Fallback); своих нет — ошибка службы с её адресом.
func TestSearchCommandFallsBackToOwnFiles(t *testing.T) {
	m, coll := remoteTestModel(t)
	svc := &findService{fakeService: fakeService{t: t, coll: coll, down: true}}
	m.SetLibrary(svc)
	done := runSearch(t, m, "/search goroutines channels")
	if done.err != nil || !strings.Contains(done.note, kb.FallbackNote) || len(done.res.Excerpts) == 0 {
		t.Errorf("недоступная служба: err %v, оговорка %q, выдержек %d", done.err, done.note, len(done.res.Excerpts))
	}

	bare := newTestModelWith(t, nil)
	bare.kb.base = nil
	bare.SetLibrary(&findService{fakeService: fakeService{t: t, down: true}})
	done = runSearch(t, bare, "/search goroutines")
	if done.err == nil || !strings.Contains(done.err.Error(), "kb.example") {
		t.Errorf("без службы и своих файлов: %v", done.err)
	}
}

// Служба прежней сборки (входа /api/v1/find нет): выдержки её поиском по книгам
// и строка об этом; свой граф не открывается.
func TestSearchCommandOldService(t *testing.T) {
	m, coll := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	svc := &fakeService{t: t, coll: coll}
	m.SetLibrary(svc)
	done := runSearch(t, m, "/search goroutines channels")
	if done.err != nil || len(svc.asked) != 1 || cache.Len() != 0 {
		t.Errorf("служба прежней сборки: err %v, у службы спросили %q, графов в кэше %d", done.err, svc.asked, cache.Len())
	}
	if !strings.Contains(done.note, "прежней сборки") {
		t.Errorf("нет строки о службе прежней сборки: %q", done.note)
	}
}

// /kb use выбирает коллекцию у службы — в том числе ту, которой у себя нет
// (прежде отказ «нет такой коллекции»); нет и у службы — её отказ.
func TestKBUseServiceOnlyCollection(t *testing.T) {
	_, coll := remoteTestModel(t) // коллекция go живёт только «у службы»
	m := newTestModelWith(t, nil)
	if m.kb.base == nil {
		t.Fatal("подготовка: своей базы нет")
	}
	m.SetLibrary(&fakeService{t: t, coll: coll})
	runKBUse(t, m, "/kb use go")
	if last := m.blocks[len(m.blocks)-1]; m.kb.use != "go" || last.kind == blockError {
		t.Errorf("коллекцию службы go выбрать нельзя: use %q, %q", m.kb.use, last.text)
	}
}

// Своих файлов нет (m.kb.base == nil — режим, который SetLibrary допускает
// явно): команды над своими файлами отказывают словами, а не падают на пустом
// указателе; команды, которым свои файлы не нужны, работают через службу.
func TestCommandsWithoutOwnFiles(t *testing.T) {
	for _, line := range []string{"/kb", "/kb use go", "/kb add go /tmp", "/kb list", "/kb stats",
		"/search goroutines", "/kb search goroutines", "/read go/1#1"} {
		t.Run(line, func(t *testing.T) {
			m := newTestModelWith(t, nil)
			m.kb.base = nil
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s без своих файлов и без службы — паника: %v", line, r)
				}
			}()
			if cmd := m.runCommand(line); cmd != nil {
				t.Errorf("%s без своих файлов запустил работу", line)
			}
			if last := m.blocks[len(m.blocks)-1]; last.kind != blockError || !strings.Contains(last.text, "своих файлов") {
				t.Errorf("%s: %q", line, last.text)
			}
		})
	}

	// Со службой выбор коллекции и поиск идут в неё.
	_, coll := remoteTestModel(t)
	m := newTestModelWith(t, nil)
	m.kb.base = nil
	svc := &findService{fakeService: fakeService{t: t, coll: coll}}
	m.SetLibrary(svc)
	runKBUse(t, m, "/kb use go")
	if m.kb.use != "go" {
		t.Errorf("/kb use go при службе без своих файлов: %q", m.blocks[len(m.blocks)-1].text)
	}
	if done := runSearch(t, m, "/search goroutines"); done.err != nil || len(svc.finds) != 1 {
		t.Errorf("/search при службе без своих файлов: err %v, запросов %d", done.err, len(svc.finds))
	}
}
