package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
)

// Подмешивание знаний к вопросу: устройство и привратник — в пакете mixer.
// Здесь только то, что зависит от состояния диалога: какая коллекция выбрана,
// включены ли карта и выдержки, умеет ли нынешняя модель инструменты, — и
// показ подмеса человеку.
//
// Политика вынесена в отдельный пакет потому, что теперь её зовут двое: диалог
// и команда `--ask`. Две копии одной политики разошлись бы в первую же правку.

// mixResult — что подмешано к вопросу.
type mixResult = mixer.Result

// mixJob — всё нужное подмешиванию, снятое с модели в цикле событий.
//
// Ни одного указателя на Model здесь быть не должно: job считает горутина
// команды, а Model принадлежит циклу Bubble Tea. Запись в него из горутины —
// гонка, которую детектор поймает не сразу, а на чужой машине.
type mixJob struct {
	deps       mixer.Deps // Graph заполняет сама команда: его ещё надо открыть
	set        mixer.Settings
	graphDir   string
	graphStamp int64
	graphOpen  *graph.Graph // открыт и не устарел — открывать нечего
	chunks     int
	graphCfg   config.Graph
}

// needsOpen сообщает, что команде придётся открывать граф — это десятки секунд.
func (j mixJob) needsOpen() bool {
	return j.graphOpen == nil && j.graphStamp >= 0 && j.deps.GraphOn
}

// mixReadyMsg — подмешивание посчитано в стороне от цикла событий.
type mixReadyMsg struct {
	gen      int    // поколение вопроса: ответ на отменённый выбрасываем
	question string // сам вопрос: держать его в модели незачем
	mix      mixResult
	// Граф, открытый этой же командой. Пустой — значит обошлись уже открытым.
	graph      *graph.Graph
	graphDir   string
	graphStamp int64
	note       string

	// show — это /mix show: подмес показать, а не отправлять модели.
	show   bool
	report string // снимок настроек отбора на момент команды
}

// mixPlan снимает с модели всё, что нужно подмешиванию. Быстрое: чтение полей.
// Второе значение — есть ли что подмешивать вообще.
func (m *Model) mixPlan() (mixJob, bool) {
	if m.kb.use == "" || m.kb.base == nil {
		return mixJob{}, false
	}
	if !m.kb.autoOn && !m.gr.autoOn {
		return mixJob{}, false
	}
	coll, err := m.kbCollection(m.kb.use)
	if err != nil {
		return mixJob{}, false
	}
	topK, maxPerBook, minCos, semWeight := m.live.KB()
	dir := coll.Dir()
	stamp := graphStamp(dir, m.cfg.Graph.Name)
	job := mixJob{
		deps: mixer.Deps{
			Coll:     coll,
			Embedder: m.embedder(),
			Reranker: kbrerank.New(m.cfg.KB.RerankOptions()),
			BooksOn:  m.kb.autoOn,
			GraphOn:  m.gr.autoOn,
			NoTools:  m.toolsUnsupported(),
		},
		set: mixer.Settings{
			TableBoost:         m.cfg.KB.TableBoost,
			Entities:           m.cfg.Mix.Entities,
			Neighbors:          m.cfg.Mix.Neighbors,
			Rank:               m.live.Rank(),
			TopK:               topK,
			MaxPerBook:         maxPerBook,
			MinCosine:          minCos,
			SemanticWeight:     semWeight,
			Semantic:           m.cfg.KB.Semantic,
			QueryTimeout:       m.cfg.KB.QueryTimeoutDuration(),
			AnswerStyle:        m.cfg.KB.AnswerStyle,
			QuotesWithoutTools: m.cfg.Mix.QuotesWithoutTools,
			RerankOpts: kb.RerankOpts{
				Candidates: m.cfg.KB.RerankCandidates,
				Snippet:    m.cfg.KB.RerankSnippet,
			},
			Collection: m.kb.use,
		},
		graphDir:   dir,
		graphStamp: stamp,
		chunks:     coll.ChunkCount(),
		graphCfg:   m.cfg.Graph,
	}
	// Отпечаток тот же — граф в памяти годен. Реестр дозаписывается идущей
	// рядом сборкой, поэтому сверяется размер файла, а не сам факт открытия.
	if m.gr.open != nil && m.gr.dir == dir && m.gr.stamp == stamp {
		job.graphOpen = m.gr.open
	}
	return job, true
}

// runMixCmd считает подмешивание вне цикла событий.
//
// Здесь живёт всё дорогое: открытие графа (41 с на библиотеке из 465 тысяч
// кусков, замер 02.09.2026), поиск по книгам, запрос к эмбеддеру и вторая
// ступень. Пока это считается, интерфейс обязан оставаться живым.
func runMixCmd(gen int, question string, job mixJob, prog chan<- graph.OpenProgress) tea.Cmd {
	return func() tea.Msg {
		defer close(prog) // закрытый канал говорит ленте, что полосу пора убрать
		msg := mixReadyMsg{gen: gen, question: question}
		g := job.graphOpen
		if job.needsOpen() {
			opened, err := graph.OpenWithProgress(job.graphDir, job.chunks, job.graphCfg.Rules(), sendProgress(prog))
			if err == nil {
				g = opened
				msg.graph, msg.graphDir, msg.graphStamp = opened, job.graphDir, job.graphStamp
				msg.note = GraphOpenNote(opened.Opened(), &job.graphCfg)
			}
			// Молча: отсутствие графа не ошибка, а несовпадение с уплотнённой
			// коллекцией объяснено там, где графом пользуются осознанно.
		}
		job.deps.Graph = g
		msg.mix = mixer.Build(question, job.deps, job.set)
		return msg
	}
}

// runMixShowCmd — то же вычисление, что у вопроса, но ответ помечен show:
// интерфейс покажет материал и ни к какой модели не пойдёт (этап 91, R6.1).
// Поколение — прогрев: полоса открытия графа рисуется, а ход вопроса не трогается.
func runMixShowCmd(question string, job mixJob, report string, prog chan<- graph.OpenProgress) tea.Cmd {
	inner := runMixCmd(genWarm, question, job, prog)
	return func() tea.Msg {
		msg := inner().(mixReadyMsg)
		msg.show, msg.report = true, report
		return msg
	}
}

// warmGraphCmd открывает граф заранее, сразу после запуска.
//
// Без прогрева за открытие платит первый же вопрос: человек задаёт его через
// секунду после старта и ждёт минуту неизвестно чего. С прогревом то же время
// тратится, пока он читает приветствие и набирает вопрос, и к моменту Enter
// граф чаще всего уже в памяти.
func (m *Model) warmGraphCmd(prog chan<- graph.OpenProgress) tea.Cmd {
	if !m.cfg.Mix.Graph || !m.cfg.Graph.Cache || m.kb.base == nil || m.kb.use == "" {
		return nil
	}
	job, ok := m.mixPlan()
	if !ok || !job.needsOpen() {
		return nil
	}
	dir, stamp, chunks, gcfg := job.graphDir, job.graphStamp, job.chunks, job.graphCfg
	return func() tea.Msg {
		defer close(prog)
		g, err := graph.OpenWithProgress(dir, chunks, gcfg.Rules(), sendProgress(prog))
		if err != nil {
			return mixReadyMsg{gen: genWarm}
		}
		return mixReadyMsg{
			gen: genWarm, graph: g, graphDir: dir, graphStamp: stamp,
			note: GraphOpenNote(g.Opened(), &gcfg),
		}
	}
}

// sendProgress отдаёт обратный вызов, который кладёт ход открытия в канал
// и **никогда не ждёт**: обратный вызов зовётся из горутины чтения графа,
// и заблокировать его значит замедлить само открытие ровно на то время,
// пока лента занята чем-то другим. Пропущенное сообщение о ходе не потеря —
// следующее придёт через восемь мегабайт.
func sendProgress(prog chan<- graph.OpenProgress) func(graph.OpenProgress) {
	return func(p graph.OpenProgress) {
		select {
		case prog <- p:
		default:
		}
	}
}

// autoMix собирает то, что уйдёт модели вместе с вопросом.
func (m *Model) autoMix(question string) mixResult {
	if m.kb.use == "" || m.kb.base == nil {
		return mixResult{}
	}
	coll, err := m.kbCollection(m.kb.use)
	if err != nil {
		return mixResult{}
	}
	defer m.releaseGraph()

	topK, maxPerBook, minCos, semWeight := m.live.KB()
	return mixer.Build(question, mixer.Deps{
		Coll:     coll,
		Graph:    m.graphOf(coll),
		Embedder: m.embedder(),
		Reranker: kbrerank.New(m.cfg.KB.RerankOptions()),
		BooksOn:  m.kb.autoOn,
		GraphOn:  m.gr.autoOn,
		NoTools:  m.toolsUnsupported(),
	}, mixer.Settings{
		TableBoost:         m.cfg.KB.TableBoost,
		Entities:           m.cfg.Mix.Entities,
		Neighbors:          m.cfg.Mix.Neighbors,
		Rank:               m.live.Rank(),
		TopK:               topK,
		MaxPerBook:         maxPerBook,
		MinCosine:          minCos,
		SemanticWeight:     semWeight,
		Semantic:           m.cfg.KB.Semantic,
		QueryTimeout:       m.cfg.KB.QueryTimeoutDuration(),
		AnswerStyle:        m.cfg.KB.AnswerStyle,
		QuotesWithoutTools: m.cfg.Mix.QuotesWithoutTools,
		RerankOpts: kb.RerankOpts{
			Candidates: m.cfg.KB.RerankCandidates,
			Snippet:    m.cfg.KB.RerankSnippet,
		},
		Collection: m.kb.use,
	})
}

// Line — серая строка под вопросом о том, что реально ушло модели.
//
// Нужна затем, что подмешивание невидимо: человек видит свой вопрос, а модель
// получает вопрос плюс тысячи токенов, о которых он не знает. Догадаться об
// этом по ответу нельзя, а объяснять расхождение задним числом дорого.
func mixLine(r mixResult) string {
	if r.Empty() {
		return ""
	}
	var parts []string
	if r.Entities > 0 {
		parts = append(parts, fmt.Sprintf("граф: понятий %d, связей %d — /graph auto off отключает",
			r.Entities, r.Relations))
	}
	if r.Chunks > 0 {
		s := fmt.Sprintf("книги: фрагментов %d", r.Chunks)
		switch {
		case r.From > 0 && r.From != r.To:
			s += fmt.Sprintf(", %d–%d гг.", r.From, r.To)
		case r.From > 0:
			s += fmt.Sprintf(", %d г.", r.From)
		}
		if r.NoTools {
			s += " (модель без инструментов — сама не дозапросит)"
		} else {
			s += " — /kb auto off отключает"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "подмешано к вопросу ≈" + ctxmeter.FormatTokens(r.Tokens) + " токенов\n  " +
		strings.Join(parts, "\n  ")
}

// toolsUnsupported сообщает, что модель не умеет вызывать инструменты.
//
// Пустой список возможностей — не повод считать, что не умеет: описание могло
// не прийти, а лишняя пара фрагментов дешевле ложного вывода.
func (m *Model) toolsUnsupported() bool {
	if len(m.modelCaps) == 0 {
		return false
	}
	return !m.toolsUsable()
}

// toolsUsable — модель и объявляет инструменты, и умеет их разбирать.
func (m *Model) toolsUsable() bool {
	// Строка «tools» в описании модели бывает ложной: у сборки нет ни RENDERER,
	// ни PARSER, и вызвать инструмент она не сможет. Замерено на живой проверке
	// 24.08.2026: deepseek-r1:70b объявляет tools, получал одну карту понятий
	// без выдержек и отвечал по памяти, без единой ссылки на книгу.
	return hasCap(m.modelCaps, "tools") && m.modelRealTools
}

// graphOf отдаёт открытый граф коллекции или nil, если графа нет.
//
// Граф держится открытым между вопросами: чтение реестра понятий это десятки
// мегабайт и сотни миллисекунд, а вопросов за сеанс сотни. Но реестр
// дозаписывается — идущая рядом сборка добавляет понятия, — поэтому перед
// каждым обращением проверяется размер файла, и подросший граф перечитывается.
// Один stat против устаревшей карты понятий — сделка выгодная.
func (m *Model) graphOf(coll *kb.Collection) *graph.Graph {
	dir := coll.Dir()
	stamp := graphStamp(dir, m.cfg.Graph.Name)
	if m.gr.open != nil && m.gr.dir == dir && m.gr.stamp == stamp {
		return m.gr.open
	}
	m.closeGraph()
	if stamp < 0 {
		return nil
	}
	g, err := graph.Open(dir, coll.ChunkCount(), m.cfg.Graph.Rules())
	if err != nil {
		// Молча: отсутствие графа не ошибка, а несовпадение с уплотнённой
		// коллекцией уже объяснено там, где графом пользуются осознанно.
		return nil
	}
	m.gr.open, m.gr.dir, m.gr.stamp = g, dir, stamp
	// Во что обошлось открытие — видно сразу. Строка появляется только здесь,
	// на настоящем открытии: при попадании в кэш графа ничего не читалось,
	// и говорить не о чем.
	if note := GraphOpenNote(g.Opened(), &m.cfg.Graph); note != "" {
		m.addBlock(block{kind: blockRaw, text: note})
	}
	return g
}

// releaseGraph закрывает граф, если держать его в памяти запрещено настройкой.
//
// Зовётся после каждого обращения к графу: при graph.cache = false человек
// платит одиннадцатью секундами на каждый вопрос, зато не платит памятью.
// Это осмысленный выбор на машине, где памяти мало, и бессмысленный на всех
// остальных — поэтому по умолчанию кэш включён.
func (m *Model) releaseGraph() {
	if !m.cfg.Graph.Cache {
		m.closeGraph()
	}
}

// closeGraph закрывает открытый граф. Зовётся при смене коллекции и на выходе.
func (m *Model) closeGraph() {
	if m.gr.open != nil {
		_ = m.gr.open.Close()
	}
	m.gr.open, m.gr.dir, m.gr.stamp = nil, "", 0
}

// graphStamp — признак того, что реестр понятий не менялся: размер файла.
// Отрицательное значение означает, что графа нет вовсе.
func graphStamp(collDir, name string) int64 {
	fi, err := os.Stat(filepath.Join(collDir, graph.DirFor(name), "entities.jsonl"))
	if err != nil {
		return -1
	}
	return fi.Size()
}
