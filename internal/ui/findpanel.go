package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/viewer"
)

// Панель найденного: Ctrl+F открывает и закрывает её, стрелки водят по списку,
// Enter показывает выбранный кусок целиком, Esc закрывает.
//
// **Зачем она.** Выдача /search — это десяток кусков со ссылками вида
// `books/12#37`. Прочитать один целиком можно командой `/read books/12#37`,
// но набирать идентификатор руками — работа для машины. Панель показывает
// тот же список и читает выбранное одним нажатием.
//
// Стоит там же, где список файлов и панель вложений, и по тем же причинам
// взаимно исключает их: три панели сразу сжали бы ленту до огрызка.
//
// Сам список живёт в Model.finds — панель только показывает его, поэтому
// расходиться им негде.

// defaultFindRows — сколько строк списка видно по умолчанию.
// Настраивается `input.find_rows`.
const defaultFindRows = 5

// findPanel — состояние панели найденного.
type findPanel struct {
	listCursor
	rows int // сколько строк показывать; 0 — defaultFindRows
}

func (p *findPanel) visibleRows() int {
	if p.rows > 0 {
		return p.rows
	}
	return defaultFindRows
}

// height — высота панели вместе с рамкой и заголовком.
func (p *findPanel) height(count int) int {
	rows := count
	if max := p.visibleRows(); rows > max {
		rows = max
	}
	if rows == 0 {
		rows = 1 // строка «ничего не найдено»
	}
	return rows + 3 // рамка (2) + заголовок
}

// clampCursor удерживает выбор внутри списка после новой выдачи.

// view рисует список найденного.
func (p *findPanel) move(delta, count int) { p.step(delta, count, p.visibleRows()) }
func (p *findPanel) clampCursor(count int) { p.clamp(count, p.visibleRows()) }

func (p *findPanel) view(width int, items []findRow, query string) string {
	title := "найдено"
	if query != "" {
		title += " по «" + query + "»"
	}
	if len(items) > p.visibleRows() {
		title += fmt.Sprintf("  %d/%d", p.cursor+1, len(items))
	}
	title += " — Enter прочитать, Ctrl+O открыть книгу, Ctrl+F закрыть"

	var b strings.Builder
	b.WriteString(styPickerTitle.Render(truncateLine(title, width-6)) + "\n")

	inner := panelInner(width)

	if len(items) == 0 {
		b.WriteString(styPickerHint.Render("ничего не найдено — сперва /search <текст>"))
		return styPickerBox.Width(width - 2).Render(b.String())
	}

	end := p.offset + p.visibleRows()
	if end > len(items) {
		end = len(items)
	}
	rows := make([]string, 0, end-p.offset)
	for i := p.offset; i < end; i++ {
		style, marker := styPickerName, "  "
		if i == p.cursor {
			style, marker = styPickerNameSel, styPickerMarker.Render("▸ ")
		}
		line := fmt.Sprintf("%d. %s", i+1, items[i].head)
		row := marker + style.Render(truncateLine(line, inner))
		if space := inner - lipgloss.Width(line) - 2; space > 6 && items[i].tail != "" {
			row += "  " + styPickerHint.Render(truncateLine(items[i].tail, space))
		}
		rows = append(rows, clip(row, inner+2))
	}
	b.WriteString(strings.Join(rows, "\n"))

	return styPickerBox.Width(width - 2).Render(b.String())
}

// findRow — одна строка списка: чем она названа и что показать справа.
type findRow struct {
	head string // книга и страница
	tail string // начало цитаты
}

// findRows собирает строки списка из последней выдачи.
func (m *Model) findRows() []findRow {
	out := make([]findRow, 0, len(m.finds))
	for _, e := range m.finds {
		head := e.Book
		if e.Unit != "" && e.From > 0 {
			head += fmt.Sprintf(" · %s %d", e.Unit, e.From)
		}
		out = append(out, findRow{head: head, tail: oneLine(e.Snippet)})
	}
	return out
}

// oneLine — начало цитаты одной строкой: в списке многострочное не помещается.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// ── Связь с моделью ──────────────────────────────────────────────────────────

// findHeight — сколько строк занимает панель найденного сейчас.
func (m *Model) findHeight() int {
	if m.findPane == nil {
		return 0
	}
	return m.findPane.height(len(m.finds))
}

// toggleFindPanel открывает и закрывает панель по Ctrl+F.
func (m *Model) toggleFindPanel() {
	if m.findPane != nil {
		m.closeFindPanel()
		return
	}
	// Три панели над разделителем сразу не показываем.
	m.files, m.cmds, m.images = nil, nil, nil
	m.findPane = &findPanel{rows: m.cfg.Input.FindRows}
	m.findPane.clampCursor(len(m.finds))
	m.relayout()
}

// closeFindPanel убирает панель и возвращает ленте отданные ей строки.
func (m *Model) closeFindPanel() {
	if m.findPane == nil {
		return
	}
	m.findPane = nil
	m.relayout()
}

// handleFindPanelKey обрабатывает клавиши, пока панель открыта.
// Второе значение — взяли ли клавишу себе.
func (m *Model) handleFindPanelKey(key string) (bool, string) {
	switch key {
	case "up", "ctrl+p":
		m.findPane.move(-1, len(m.finds))
		return true, ""
	case "down", "ctrl+n":
		m.findPane.move(1, len(m.finds))
		return true, ""
	case "enter":
		if m.findPane.cursor < len(m.finds) {
			return true, m.finds[m.findPane.cursor].ID
		}
		return true, ""
	case "ctrl+o":
		m.openSelectedBook()
		return true, ""
	case "esc", "ctrl+f":
		m.closeFindPanel()
		return true, ""
	}
	return false, ""
}

// openSelectedBook открывает книгу выбранного куска во внешней программе.
//
// Страница берётся из той же выдержки: в настройке `[viewers]` её место
// помечено `{page}`, и просмотрщик открывается сразу на нужном месте.
// У книги EPUB единица ссылки — раздел, а не страница, поэтому туда страница
// не передаётся вовсе (см. viewer.Open).
func (m *Model) openSelectedBook() {
	if m.findPane == nil || m.findPane.cursor >= len(m.finds) {
		return
	}
	e := m.finds[m.findPane.cursor]
	page := 0
	if e.Unit == "стр." {
		page = e.From
	}
	if err := viewer.Open(e.Path, page, viewer.Commands{
		PDF:  m.cfg.Viewers.PDF,
		EPUB: m.cfg.Viewers.EPUB,
		MD:   m.cfg.Viewers.MD,
	}); err != nil {
		m.addBlockAndShow(block{kind: blockError, text: err.Error()})
		return
	}
	// Строка состояния, а не блок в ленте: открытие книги — не событие
	// разговора, и засорять им ленту незачем.
	m.statusMsg = "открываю: " + filepath.Base(e.Path)
}
