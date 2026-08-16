package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
)

// pickerKind — что именно выбирается.
type pickerKind int

const (
	pickServer pickerKind = iota
	pickModel
	pickSession
)

// pickerItem — строка списка.
//
// Строка выводится колонками: label выравнивается по самому длинному значению
// в списке, за ним идут columns. Так подписи вроде "tools,thinking,vision"
// выстраиваются друг под другом, а не убегают к правому краю.
type pickerItem struct {
	label   string   // основной текст (имя сервера, модели, метка сессии)
	columns []string // дополнительные колонки
	value   string   // значение, возвращаемое при выборе
}

// picker — простой список с выбором стрелками.
type picker struct {
	kind    pickerKind
	title   string
	items   []pickerItem
	cursor  int
	offset  int
	maxRows int
}

func newPicker(kind pickerKind, title string, items []pickerItem, current string) *picker {
	p := &picker{kind: kind, title: title, items: items, maxRows: 10}
	for i, it := range items {
		if it.value == current {
			p.cursor = i
		}
	}
	p.ensureVisible()
	return p
}

// height возвращает высоту панели вместе с рамкой и заголовком.
func (p *picker) height() int {
	rows := len(p.items)
	if rows > p.maxRows {
		rows = p.maxRows
	}
	return rows + 4 // рамка (2) + заголовок + подсказка
}

func (p *picker) ensureVisible() {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+p.maxRows {
		p.offset = p.cursor - p.maxRows + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// colWidths вычисляет ширину колонок по всем строкам списка.
func (p *picker) colWidths() (nameW int, colW []int) {
	for _, it := range p.items {
		if w := lenRunes(it.label); w > nameW {
			nameW = w
		}
		for c, v := range it.columns {
			for len(colW) <= c {
				colW = append(colW, 0)
			}
			if w := lenRunes(v); w > colW[c] {
				colW[c] = w
			}
		}
	}
	return nameW, colW
}

func (p *picker) view(width int) string {
	var b strings.Builder
	b.WriteString(styPickerTitle.Render(p.title) + "\n")

	end := p.offset + p.maxRows
	if end > len(p.items) {
		end = len(p.items)
	}

	// Рамка занимает 2 колонки, отступы стиля — ещё 2, маркер строки — 2.
	inner := width - 6
	if inner < 10 {
		inner = 10
	}
	nameW, colW := p.colWidths()
	if nameW > inner/2 {
		nameW = inner / 2
	}

	for i := p.offset; i < end; i++ {
		it := p.items[i]
		selected := i == p.cursor

		nameStyle, colStyle := styPickerName, styPickerCol
		if selected {
			nameStyle, colStyle = styPickerNameSel, styPickerColSel
		}

		name := padRunes(truncateLine(it.label, nameW), nameW)
		parts := []string{nameStyle.Render(name)}
		for c, v := range it.columns {
			w := 0
			if c < len(colW) {
				w = colW[c]
			}
			if c == len(it.columns)-1 {
				// Последнюю колонку не дополняем пробелами: хвост не нужен.
				parts = append(parts, colStyle.Render(v))
				continue
			}
			parts = append(parts, colStyle.Render(padRunes(v, w)))
		}
		line := strings.Join(parts, "  ")

		marker := "  "
		if selected {
			marker = styPickerMarker.Render("▸ ")
		}
		row := clip(marker+line, inner+2)
		if selected {
			// Подсветка всей строки фоном — так курсор виден, но текст читается.
			row = styPickerRow.Width(inner + 2).Render(row)
		}
		b.WriteString(row + "\n")
	}
	b.WriteString(styPickerHint.Render("↑↓ выбор · Enter применить · Esc отмена"))
	return styPickerBox.Width(width - 2).Render(b.String())
}

func lenRunes(s string) int { return len([]rune(s)) }

// padRunes дополняет строку пробелами до ширины w.
func padRunes(s string, w int) string {
	if n := lenRunes(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// ── Открытие списков ─────────────────────────────────────────────────────────

func (m *Model) openServerPicker() tea.Cmd {
	items := make([]pickerItem, 0, len(m.cfg.Servers))
	for i := range m.cfg.Servers {
		s := &m.cfg.Servers[i]
		items = append(items, pickerItem{
			label:   s.Name,
			columns: []string{trimScheme(s.URL), s.Model},
			value:   s.Name,
		})
	}
	m.picker = newPicker(pickServer, "Выбор сервера Ollama", items, m.server.Name)
	m.syncHeights()
	return nil
}

func (m *Model) openModelPicker() tea.Cmd {
	// Список к этому моменту уже обновлён с сервера, поэтому пусто здесь
	// означает именно отсутствие моделей, а не незавершённую загрузку.
	if len(m.models) == 0 {
		return func() tea.Msg {
			return noticeMsg{text: "на сервере " + m.server.Name + " нет ни одной модели"}
		}
	}
	items := make([]pickerItem, 0, len(m.models))
	for _, mi := range m.models {
		marks := make([]string, 0, 3)
		for _, c := range []string{"tools", "thinking", "vision"} {
			if mi.HasCapability(c) {
				marks = append(marks, c)
			}
		}
		ctxCol := "—"
		if mi.Details.ContextLength > 0 {
			ctxCol = "ctx " + ctxmeter.FormatTokens(mi.Details.ContextLength)
		}
		items = append(items, pickerItem{
			label:   mi.Name,
			columns: []string{mi.Details.ParameterSize, ctxCol, strings.Join(marks, ",")},
			value:   mi.Name,
		})
	}
	m.picker = newPicker(pickModel, "Выбор модели на сервере "+m.server.Name, items, m.modelName)
	m.syncHeights()
	return nil
}

func (m *Model) openSessionPicker() tea.Cmd {
	list, err := m.store.List()
	if err != nil {
		return func() tea.Msg { return errorMsg{err: err} }
	}
	if len(list) == 0 {
		return func() tea.Msg { return noticeMsg{text: "сохранённых сессий нет"} }
	}
	items := make([]pickerItem, 0, len(list))
	for _, s := range list {
		items = append(items, pickerItem{
			label:   s.SavedAt.Format("2006.01.02 15:04"),
			columns: []string{s.Server, s.Model, fmt.Sprintf("сообщений: %d", len(s.Messages))},
			value:   s.ID,
		})
	}
	m.picker = newPicker(pickSession, "Возобновление сессии", items, "")
	m.syncHeights()
	return nil
}

func (m *Model) syncHeights() {
	if !m.ready {
		return
	}
	m.vp.SetHeight(m.viewportHeight())
	m.refreshViewport(true)
}

// handlePickerKey обрабатывает клавиши в списке выбора.
func (m *Model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	switch msg.String() {
	case "esc":
		m.picker = nil
		m.syncHeights()
		return m, nil
	case "up", "k", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
			p.ensureVisible()
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if p.cursor < len(p.items)-1 {
			p.cursor++
			p.ensureVisible()
		}
		return m, nil
	case "enter":
		item := p.items[p.cursor]
		kind := p.kind
		m.picker = nil
		m.syncHeights()
		return m, m.applyPick(kind, item.value)
	}
	return m, nil
}

func (m *Model) applyPick(kind pickerKind, value string) tea.Cmd {
	switch kind {
	case pickServer:
		return m.switchServer(value)
	case pickModel:
		return m.switchModel(value)
	case pickSession:
		return m.resumeSession(value)
	}
	return nil
}

// switchServer переключает активный сервер Ollama.
func (m *Model) switchServer(name string) tea.Cmd {
	srv, ok := m.cfg.ServerByName(name)
	if !ok {
		return func() tea.Msg {
			return errorMsg{err: fmt.Errorf("сервер %q не описан в конфиге", name)}
		}
	}
	if m.streaming {
		m.stopStreaming()
	}
	// Ответы прежнего сервера с этого момента чужие: он мог быть недоступен,
	// и его запрос всё ещё висит в ожидании таймаута.
	m.srvGen++
	m.server = srv
	m.client = newClient(srv)
	m.modelName = srv.Model
	m.modelCaps = nil
	m.modelMaxCtx = 0
	m.models = nil
	m.srvVersion = ""
	m.think = srv.Think
	m.meter = ctxmeter.Meter{}
	if n, ok := srv.NumCtx(); ok {
		m.meter.SetCapacity(n, ctxmeter.SourceConfig)
	}
	if strings.TrimSpace(srv.SystemPrompt) != "" {
		m.conv.SetSystem(srv.SystemPrompt)
	}
	m.addBlock(block{kind: blockNotice, text: fmt.Sprintf("сервер переключён на %s (%s)", srv.Name, srv.URL)})
	_ = m.logger.Write("Система", fmt.Sprintf("Переключение на сервер %s (%s).", srv.Name, srv.URL))
	return m.loadServerInfoCmd()
}

// switchModel переключает модель на текущем сервере.
func (m *Model) switchModel(name string) tea.Cmd {
	if m.streaming {
		m.stopStreaming()
	}
	m.modelName = name
	m.modelCaps = nil
	m.modelMaxCtx = 0
	m.meter.Reset()
	m.addBlock(block{kind: blockNotice, text: "модель переключена на " + name})
	_ = m.logger.Write("Система", "Переключение на модель "+name+".")
	return m.loadModelInfoCmd()
}

// resumeSession восстанавливает сохранённый диалог.
func (m *Model) resumeSession(id string) tea.Cmd {
	rec, err := m.store.Load(id)
	if err != nil {
		return func() tea.Msg { return errorMsg{err: err} }
	}
	m.conv.Restore(rec)
	m.blocks = nil
	m.rendered = nil
	m.meter.Reset()
	m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
		"восстановлена сессия от %s (%s, %s), сообщений: %d",
		rec.SavedAt.Format("2006.01.02 15:04"), rec.Server, rec.Model, len(rec.Messages))})

	for _, msg := range rec.Messages {
		switch msg.Role {
		case "user":
			m.addBlock(block{kind: blockUser, text: msg.Content})
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				m.addBlock(block{kind: blockAssistant, text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				m.addBlock(block{kind: blockTool, status: "ok",
					title: fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.ArgumentsJSON())})
			}
		}
	}
	m.meter.Used = ctxmeter.EstimateChars(m.conv.EstimatedChars())
	m.meter.Exact = false
	return nil
}
