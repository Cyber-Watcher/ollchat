package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Окно разбора пар: /graph review.
//
// **Откуда пары.** Связывание новых имён при сборке (`--graph-link-new`) и ночной
// разбор двойников (`graphdoubles.sh`) отдают спорное не в корзину, а человеку:
// вердикт «?» ложится в links.jsonl рядом с графом. Здесь эти пары показываются
// списком, и решение принимается одной клавишей.
//
// **Что делает решение.** Пока пара ждала, узел нового имени уже заведён —
// сборка человека не ждёт. Поэтому «одно и то же» это склейка двух узлов
// (merges.jsonl, обратимо, как все склейки) плюс запись в журнал связываний,
// чтобы имя дальше шло к узлу без арбитра; «разные» — только журнал, арбитра
// об этой паре больше не спрашивают. Решения пишутся сразу, по одному: окно
// можно закрыть на середине.
//
// **Судейский режим** (`/graph review --judge`): те же клавиши по решениям
// арбитра «ДА» при сборке — выборочная проверка глазами в первые ночи опытного
// графа. «Разные» здесь означает отмену: дальше имя заведёт свой узел, а уже
// отданные чужому узлу упоминания назад не переносятся — окно об этом говорит.
//
// Панель стоит там же, где список найденного, и по тем же причинам взаимно
// исключает его.

const defaultReviewRows = 6

// reviewItem — одна пара в окне.
type reviewItem struct {
	rec      graph.LinkRec
	from     string // новое имя и его вес
	to       string // существующее понятие и его вес
	source   string // книга и страница куска-источника; пусто у пар двойников
	snippet  string
	chunkID  string // для чтения куска целиком: books/12#37
	fromNode uint32

	// Выдержки по стороне пары: книга, страница и кусок текста, где понятие
	// встретилось. Заводятся для ОБЕИХ сторон (этап 101, A2).
	//
	// **Зачем.** Человек решал «одно это или разное» по двум именам — самой
	// бедной части данных. Книги советуют обратное: «Knowledge Graphs and LLMs
	// in Action» (Negro, 2025, стр. 149) — держать доступ к тексту, из которого
	// сущность извлечена, именно ради донастройки склейки. У пар двойников
	// (r.Chunk пуст) выдержек не было вовсе — а это основной поток разбора.
	fromEv evidence
	toEv   evidence
	// toNode и evLoaded — для ленивой загрузки выдержек: читаются куски
	// только у выделенной пары, когда до неё дошёл курсор. Список бывает
	// в сотни пар, а чтение куска — обращение к диску; грузить все сразу
	// значило бы держать окно закрытым секунды ради двух строк на экране.
	toNode   uint32
	evLoaded bool
}

// evidence — одна выдержка: откуда и что там написано.
type evidence struct {
	source  string // «Название книги, стр. 42»
	snippet string
	chunkID string // books/12#37 — чтобы открыть кусок целиком
}

// reviewPanel — состояние окна.
type reviewPanel struct {
	listCursor
	judge  bool
	coll   string
	g      *graph.Graph
	kbc    *kb.Collection // для выдержек по сторонам пары; nil — без них
	items  []reviewItem
	done   int    // решено за это открытие
	last   string // последнее решение — для строки заголовка
	rows   int
	undo   []reviewItem // что убрано из списка последним решением (для u)
	undoAt []int
}

func (p *reviewPanel) visibleRows() int {
	if p.rows > 0 {
		return p.rows
	}
	return defaultReviewRows
}

func (p *reviewPanel) height() int {
	rows := len(p.items)
	if max := p.visibleRows(); rows > max {
		rows = max
	}
	if rows == 0 {
		rows = 1
	}
	// По две строки на пару, плюс две строки выдержек у выделенной пары
	// (этап 101, A2): показывать выдержки у всех сразу — панель на пол-экрана,
	// а решают всегда одну пару.
	extra := 0
	if len(p.items) > 0 {
		extra = 2
	}
	return rows*2 + 3 + extra // рамка и заголовок
}

func (p *reviewPanel) move(delta int) { p.step(delta, len(p.items), p.visibleRows()) }
func (p *reviewPanel) clampCursor()   { p.clamp(len(p.items), p.visibleRows()) }
func (p *reviewPanel) current() *reviewItem {
	if p.cursor < len(p.items) {
		return &p.items[p.cursor]
	}
	return nil
}

// loadEvidence читает выдержки по сторонам выделенной пары, один раз.
func (p *reviewPanel) loadEvidence() {
	it := p.current()
	if it == nil || it.evLoaded {
		return
	}
	it.evLoaded = true
	if it.fromEv.snippet == "" {
		it.fromEv, _ = firstEvidence(p.g, p.kbc, it.fromNode)
	}
	it.toEv, _ = firstEvidence(p.g, p.kbc, it.toNode)
}

func (p *reviewPanel) view(width int) string {
	p.loadEvidence()
	title := "разбор пар"
	if p.judge {
		title = "проверка решений арбитра"
	}
	title += " · " + p.coll
	if n := len(p.items); n > 0 {
		title += fmt.Sprintf("  %d/%d", p.cursor+1, n)
	}
	if p.done > 0 {
		title += fmt.Sprintf("  решено %d", p.done)
	}
	if p.judge {
		title += " — y верно, n отменить, пробел позже, Enter кусок, u вернуть, Esc"
	} else {
		title += " — y одно и то же, n разные, пробел позже, Enter кусок, u вернуть, Esc"
	}
	var b strings.Builder
	b.WriteString(styPickerTitle.Render(truncateLine(title, width-6)) + "\n")
	inner := panelInner(width)
	if len(p.items) == 0 {
		if p.judge {
			b.WriteString(styPickerHint.Render("решений арбитра «ДА» нет — или все уже проверены"))
		} else {
			b.WriteString(styPickerHint.Render("очередь пуста: машина ни в чём не усомнилась, или всё уже решено"))
		}
		return styPickerBox.Width(width - 2).Render(b.String())
	}
	end := p.offset + p.visibleRows()
	if end > len(p.items) {
		end = len(p.items)
	}
	var rows []string
	for i := p.offset; i < end; i++ {
		it := p.items[i]
		style, marker := styPickerName, "  "
		if i == p.cursor {
			style, marker = styPickerNameSel, styPickerMarker.Render("▸ ")
		}
		arrow := "←?→"
		if p.judge {
			arrow = "═"
		}
		head := fmt.Sprintf("%d. %s %s %s  %.2f  %s: %s", i+1, it.from, arrow, it.to, it.rec.Cos, it.rec.By, oneLine(it.rec.Why))
		rows = append(rows, clip(marker+style.Render(truncateLine(head, inner)), inner+2))
		tail := it.source
		if it.snippet != "" {
			tail += " «" + it.snippet + "»"
		}
		if tail == "" {
			tail = "источник: " + it.rec.Source
		}
		rows = append(rows, clip("     "+styPickerHint.Render(truncateLine(tail, inner-5)), inner+2))
		if i == p.cursor {
			rows = append(rows,
				evidenceRow("◂", it.fromEv, inner),
				evidenceRow("▸", it.toEv, inner))
		}
	}
	b.WriteString(strings.Join(rows, "\n"))
	return styPickerBox.Width(width - 2).Render(b.String())
}

// ── Связь с моделью ──────────────────────────────────────────────────────────

// reviewReadyMsg — граф открыт, пары собраны.
type reviewReadyMsg struct {
	panel *reviewPanel
	err   error
}

func (m *Model) reviewHeight() int {
	if m.review == nil {
		return 0
	}
	return m.review.height()
}

// graphReviewCmd — /graph review [--judge] [коллекция].
func (m *Model) graphReviewCmd(arg string) tea.Cmd {
	judge := false
	var rest []string
	for _, f := range strings.Fields(arg) {
		switch strings.ToLower(f) {
		case "--judge", "судья", "арбитр":
			judge = true
		default:
			rest = append(rest, f)
		}
	}
	if m.review != nil {
		m.closeReviewPanel()
	}
	name, err := m.resolveKBName(strings.Join(rest, " "))
	if err != nil {
		m.fail("/graph review", err)
		return nil
	}
	rules := m.cfg.Graph.Rules()
	base := m.kb.base
	m.addBlock(block{kind: blockNotice, text: "открываю граф для разбора пар — это десятки секунд…"})
	return func() tea.Msg {
		coll, err := base.Open(name)
		if err != nil {
			return reviewReadyMsg{err: fmt.Errorf("/graph review: %w", err)}
		}
		g, err := graph.Open(coll.Dir(), coll.ChunkCount(), rules)
		if err != nil {
			return reviewReadyMsg{err: fmt.Errorf("/graph review: граф коллекции %s: %w", name, err)}
		}
		p := &reviewPanel{judge: judge, coll: name, g: g, kbc: coll, rows: m.cfg.Input.FindRows}
		p.items = reviewItems(g, coll, judge)
		return reviewReadyMsg{panel: p}
	}
}

// reviewItems собирает строки окна из журнала связываний.
func reviewItems(g *graph.Graph, coll *kb.Collection, judge bool) []reviewItem {
	l := g.Links()
	if l == nil {
		return nil
	}
	var recs []graph.LinkRec
	if judge {
		recs = l.Judged()
	} else {
		recs = l.Queue()
	}
	weight := func(id uint32, name string) (string, uint32) {
		if id == 0 {
			if ent, ok := g.Entities().Lookup(name); ok {
				id = ent.ID
			}
		}
		if ent, ok := g.Entities().Get(id); ok {
			return fmt.Sprintf("%s (%d упом.)", ent.Name, ent.Count), id
		}
		if name == "" {
			name = "?"
		}
		return name, id
	}
	out := make([]reviewItem, 0, len(recs))
	for _, r := range recs {
		it := reviewItem{rec: r}
		it.from, it.fromNode = weight(r.From, r.Name)
		other, otherName := r.Cand, r.CandName
		if r.To != 0 {
			other, otherName = r.To, r.ToName
		}
		it.to, it.toNode = weight(other, otherName)
		if r.Chunk.Doc != 0 && coll != nil {
			if ev, ok := evidenceOf(coll, r.Chunk); ok {
				it.source, it.snippet, it.chunkID = ev.source, ev.snippet, ev.chunkID
				it.fromEv = ev
			}
		}
		// Выдержка по каждой стороне (этап 101, A2) читается лениво,
		// когда пара выделена: reviewPanel.loadEvidence.
		out = append(out, it)
	}
	return out
}

// evidenceRow — строка выдержки одной стороны выделенной пары.
//
// Пусто бывает честно: понятие пришло из книги, которой больше нет на диске,
// или его упоминания ещё не перенесены (backfill выдержек). Тогда так и
// говорим, а не оставляем пустое место, которое читается как «данных нет».
func evidenceRow(mark string, ev evidence, inner int) string {
	text := ev.source
	if ev.snippet != "" {
		text += " «" + ev.snippet + "»"
	}
	if text == "" {
		text = "выдержки нет"
	}
	return clip("     "+styPickerHint.Render(truncateLine(mark+" "+text, inner-5)), inner+2)
}

// evidenceOf — выдержка по ключу куска.
func evidenceOf(coll *kb.Collection, key graph.ChunkKey) (evidence, bool) {
	if coll == nil || key.Doc == 0 {
		return evidence{}, false
	}
	ci, ok := coll.ChunkByRef(key.Doc, key.Ord)
	if !ok {
		return evidence{}, false
	}
	src := ci.Book.Title
	if ci.Unit != "" && ci.UnitFrom > 0 {
		src += fmt.Sprintf(", %s %d", ci.Unit, ci.UnitFrom)
	}
	return evidence{
		source:  src,
		snippet: cutRunes(oneLine(ci.Text), 120),
		chunkID: fmt.Sprintf("%s/%d#%d", coll.Name(), key.Doc, key.Ord),
	}, true
}

// firstEvidence — первое упоминание понятия, которое удалось прочитать.
//
// Первое, а не «лучшее»: выбор лучшего требует счёта, а человеку нужен пример
// употребления, чтобы отличить «Java (язык)» от «Java (остров)». Кусок, который
// не читается (книга уехала с диска), пропускается молча — выдержка не обещание.
func firstEvidence(g *graph.Graph, coll *kb.Collection, id uint32) (evidence, bool) {
	if g == nil || coll == nil || id == 0 {
		return evidence{}, false
	}
	keys := g.Mentions().Of(id)
	for i, key := range keys {
		if i >= 5 { // дальше не ищем: пять неудач подряд — беда крупнее выдержки
			break
		}
		if ev, ok := evidenceOf(coll, key); ok {
			return ev, true
		}
	}
	return evidence{}, false
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (m *Model) onReviewReady(msg reviewReadyMsg) {
	if msg.err != nil {
		m.addBlock(block{kind: blockError, text: msg.err.Error()})
		return
	}
	m.files, m.cmds, m.images = nil, nil, nil
	m.closeFindPanel()
	m.review = msg.panel
	m.review.clampCursor()
	m.relayout()
	n := len(m.review.items)
	what := "пар в очереди"
	if m.review.judge {
		what = "решений арбитра для проверки"
	}
	m.addBlock(block{kind: blockNotice, text: fmt.Sprintf("%s: %d", what, n)})
}

// closeReviewPanel закрывает окно и граф, открытый под него.
func (m *Model) closeReviewPanel() {
	if m.review == nil {
		return
	}
	if m.review.g != nil {
		_ = m.review.g.Close()
	}
	if m.review.done > 0 {
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf("разбор пар: решено %d, осталось %d",
			m.review.done, len(m.review.items))})
	}
	m.review = nil
	m.relayout()
}

// handleReviewKey — клавиши окна. Второе значение — кусок для чтения.
func (m *Model) handleReviewKey(key string) (bool, string) {
	p := m.review
	switch key {
	case "up", "ctrl+p":
		p.move(-1)
		return true, ""
	case "down", "ctrl+n":
		p.move(1)
		return true, ""
	case "y", "н":
		m.decideReview(graph.LinkYes)
		return true, ""
	case "n", "т":
		m.decideReview(graph.LinkNo)
		return true, ""
	case " ", "space":
		p.move(1)
		return true, ""
	case "u", "г":
		m.undoReview()
		return true, ""
	case "enter":
		if it := p.current(); it != nil && it.chunkID != "" {
			return true, it.chunkID
		}
		m.statusMsg = "у этой пары нет куска-источника"
		return true, ""
	case "esc":
		m.closeReviewPanel()
		return true, ""
	}
	return false, ""
}

// decideReview пишет решение по текущей паре и убирает её из списка.
func (m *Model) decideReview(verdict string) {
	p := m.review
	it := p.current()
	if it == nil {
		return
	}
	why := "окно разбора"
	if p.judge && verdict == graph.LinkNo {
		why = "отмена решения арбитра"
	}
	if err := p.g.DecideLink(it.rec, verdict, why); err != nil {
		m.addBlock(block{kind: blockError, text: "/graph review: " + err.Error()})
		return
	}
	p.undo = append(p.undo, *it)
	p.undoAt = append(p.undoAt, p.cursor)
	p.items = append(p.items[:p.cursor], p.items[p.cursor+1:]...)
	p.done++
	p.clampCursor()
	switch {
	case verdict == graph.LinkYes && !p.judge:
		m.statusMsg = "склеено: " + it.from + " → " + it.to
		m.closeGraph() // кэш графа откроется заново со свежими склейками
	case verdict == graph.LinkNo && p.judge:
		m.statusMsg = "отменено: дальше имя заведёт свой узел; уже отданные упоминания остались"
	default:
		m.statusMsg = "записано: " + verdict
	}
	if len(p.items) == 0 {
		m.relayout()
	}
}

// undoReview возвращает последнюю убранную пару в список. Записанное решение
// остаётся в журнале: отменить его — новым решением по той же паре.
func (m *Model) undoReview() {
	p := m.review
	if len(p.undo) == 0 {
		m.statusMsg = "возвращать нечего"
		return
	}
	it := p.undo[len(p.undo)-1]
	at := p.undoAt[len(p.undoAt)-1]
	p.undo, p.undoAt = p.undo[:len(p.undo)-1], p.undoAt[:len(p.undoAt)-1]
	if at > len(p.items) {
		at = len(p.items)
	}
	p.items = append(p.items[:at], append([]reviewItem{it}, p.items[at:]...)...)
	p.cursor = at
	if p.done > 0 {
		p.done--
	}
	p.clampCursor()
	m.statusMsg = "пара возвращена в список; решение в журнале остаётся — перерешите её"
	m.relayout()
}

var _ = lipgloss.Width
