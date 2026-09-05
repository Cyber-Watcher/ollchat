package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Меню команд: список подсказок над строкой ввода, пока набирается «/…».
//
// Устроено по образцу меню файлов (`filemenu.go`) намеренно: те же клавиши,
// та же рамка, та же логика прокрутки. Человеку не надо запоминать два разных
// поведения, а мне — держать в голове два разных устройства.
//
// **Открывается только когда «/» — первый знак строки.** Косая черта посреди
// вопроса («что делает /api/chat») командой не является, и подсказка там
// мешала бы. Проверяется по всему значению поля, а не по последней строке:
// многострочный вопрос, начинающийся с «/», всё равно уйдёт как команда только
// первой строкой, но подсказку показывать в нём незачем.
//
// **Подсказка-призрак.** Пока набранное однозначно продолжается до команды,
// хвост дописывается затемнёнными знаками прямо в строке ввода — как в fish
// и zsh. Это не текст поля: значение не меняется, дорисовка идёт при отрисовке
// (`ghostOverlay`), поэтому отправить случайно недонабранное нельзя.

// defaultCmdMenuRows — сколько команд видно в меню по умолчанию.
// Настраивается `input.command_rows`.
const defaultCmdMenuRows = 4

// cmdMenu — список команд под набранное начало.
type cmdMenu struct {
	prefix  string // набранное целиком, вместе с косой чертой
	entries []cmdEntry
	listCursor
	rows int // сколько строк показывать
}

func (c *cmdMenu) visibleRows() int {
	if c.rows > 0 {
		return c.rows
	}
	return defaultCmdMenuRows
}

// height — высота панели вместе с рамкой и заголовком.
func (c *cmdMenu) height() int {
	rows := len(c.entries)
	if max := c.visibleRows(); rows > max {
		rows = max
	}
	if rows == 0 {
		rows = 1 // строка «нет такой команды»
	}
	return rows + 3 // рамка (2) + заголовок
}

// move двигает выбор по кругу.
func (c *cmdMenu) move(delta int) { c.step(delta, len(c.entries), c.visibleRows()) }

func (c *cmdMenu) selected() (cmdEntry, bool) {
	if c.cursor < 0 || c.cursor >= len(c.entries) {
		return cmdEntry{}, false
	}
	return c.entries[c.cursor], true
}

// ghost — хвост, который дорисовывается затемнённым к набранному.
//
// Показывается только когда выбранная строка продолжает набранное. После
// перемещения стрелками по списку выбранное может начинаться иначе — тогда
// призрака нет, иначе он врал бы о том, что получится по Enter.
func (c *cmdMenu) ghost() string {
	e, ok := c.selected()
	if !ok {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(e.Full), strings.ToLower(c.prefix)) {
		return ""
	}
	return e.Full[len(c.prefix):]
}

// view рисует список над разделителем.
func (c *cmdMenu) view(width int) string {
	title := "команды"
	if max := c.visibleRows(); len(c.entries) > max {
		title += fmt.Sprintf("  %d/%d  ↑↓ или колесо", c.cursor+1, len(c.entries))
	}

	var b strings.Builder
	b.WriteString(styPickerTitle.Render(title) + "\n")

	inner := panelInner(width)

	if len(c.entries) == 0 {
		b.WriteString(styPickerHint.Render("нет такой команды"))
		return styPickerBox.Width(width - 2).Render(b.String())
	}

	// Ширина колонки имени — по самой длинной из видимых, а не из всех:
	// иначе одна длинная команда в конце списка сдвигала бы описания вправо
	// на всех остальных экранах.
	end := c.offset + c.visibleRows()
	if end > len(c.entries) {
		end = len(c.entries)
	}
	nameWidth := 0
	for i := c.offset; i < end; i++ {
		if w := lipgloss.Width(nameOf(c.entries[i])); w > nameWidth {
			nameWidth = w
		}
	}
	if maxName := inner / 2; nameWidth > maxName {
		nameWidth = maxName
	}

	rows := make([]string, 0, end-c.offset)
	for i := c.offset; i < end; i++ {
		e := c.entries[i]

		style := styPickerName
		marker := "  "
		if i == c.cursor {
			style, marker = styPickerNameSel, styPickerMarker.Render("▸ ")
		}

		name := truncateLine(nameOf(e), nameWidth)
		pad := nameWidth - lipgloss.Width(name)
		if pad < 0 {
			pad = 0
		}
		line := marker + style.Render(name) + strings.Repeat(" ", pad)

		// Описание занимает остаток строки; не поместившееся сворачивается
		// многоточием — читать половину слова смысла нет.
		if space := inner - nameWidth - 2; space > 3 && e.Desc != "" {
			line += "  " + styPickerHint.Render(truncateLine(e.Desc, space))
		}
		rows = append(rows, clip(line, inner+2))
	}
	b.WriteString(strings.Join(rows, "\n"))

	return styPickerBox.Width(width - 2).Render(b.String())
}

// nameOf — имя команды вместе с подсказкой о доводах.
func nameOf(e cmdEntry) string {
	if e.Args == "" {
		return e.Full
	}
	return e.Full + " " + e.Args
}

// ── Связь со строкой ввода ───────────────────────────────────────────────────

// commandPrefix возвращает набранное, если строка ввода — это команда.
//
// Требования жёсткие нарочно: «/» первым знаком и ни одного перевода строки.
// Пробелы внутри допустимы — ими разделяются команда и подкоманда («/kb ad»),
// а вот как только пошёл довод («/model qwen3.8»), подсказка уже не нужна:
// его меню не знает. Отличаются они тем, что довод не продолжает ни одной
// известной команды, и список тогда пуст — панель закрывается сама.
func commandPrefix(value string) (string, bool) {
	if !strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\n') {
		return "", false
	}
	return value, true
}

// syncCmdMenu пересобирает меню под текущую строку ввода.
func (m *Model) syncCmdMenu() {
	prefix, ok := commandPrefix(m.ta.Value())
	if !ok {
		m.closeCmdMenu()
		return
	}
	if m.cmds != nil && m.cmds.prefix == prefix {
		return // список уже собран под это начало
	}

	entries := matchCommands(prefix, m.sections())
	if len(entries) == 0 && strings.Contains(strings.TrimSpace(prefix), " ") {
		// Пошли доводы команды — подсказывать нечего, панель только мешает.
		m.closeCmdMenu()
		return
	}

	m.findPane = nil // две панели над разделителем сразу не показываем
	before := m.cmdsHeight()
	m.cmds = &cmdMenu{prefix: prefix, entries: entries, rows: m.cfg.Input.CommandRows}
	if m.cmdsHeight() != before {
		m.relayout()
	}
}

// cmdsHeight — сколько строк занимает меню команд сейчас.
func (m *Model) cmdsHeight() int {
	if m.cmds == nil {
		return 0
	}
	return m.cmds.height()
}

// closeCmdMenu убирает меню и возвращает ленте отданные ему строки.
func (m *Model) closeCmdMenu() {
	if m.cmds == nil {
		return
	}
	m.cmds = nil
	m.relayout()
}

// acceptCmdMenu подставляет выбранную команду в строку ввода.
//
// Именно подставляет, а не выполняет: у половины команд есть доводы, а `/clear`
// и `/quit` по одному нажатию — верный способ потерять работу. Отправляет
// человек, вторым Enter.
func (m *Model) acceptCmdMenu() {
	if m.cmds == nil {
		return
	}
	entry, ok := m.cmds.selected()
	if !ok {
		return
	}
	m.ta.SetValue(entry.insert())
	m.ta.CursorEnd()
	m.syncCmdMenu()
}

// ── Подсказка-призрак в строке ввода ─────────────────────────────────────────

// ghostOverlay дорисовывает затемнённый хвост команды поверх строки ввода.
//
// Работает наложением на **готовую** отрисовку поля: значение textarea не
// меняется. Иначе призрак попал бы в отправляемый текст, а курсор уехал бы
// за него — обе беды тихие.
func (m *Model) ghostOverlay(view string) string {
	if m.cmds == nil {
		return view
	}
	ghost := m.cmds.ghost()
	if ghost == "" {
		return view
	}
	// Призрак имеет смысл только когда курсор в конце набранного: иначе
	// дорисовка встанет не там, где человек печатает.
	cur := m.ta.Cursor()
	if cur == nil || cur.Y != 0 {
		return view
	}

	lines := strings.SplitN(view, "\n", 2)
	overlaid := overlayAt(lines[0], cur.X, styGhost.Render(ghost))
	if len(lines) == 1 {
		return overlaid
	}
	return overlaid + "\n" + lines[1]
}

// overlayAt подменяет часть строки, начиная с видимой колонки col.
//
// Строка приходит уже со стилями, поэтому считать знаки нельзя — только
// видимые ячейки. Заменяется ровно столько ячеек, сколько занимает вставка;
// хвост строки остаётся на месте, чтобы рамка и добивка не поехали.
func overlayAt(line string, col int, ins string) string {
	insWidth := lipgloss.Width(ins)
	if insWidth == 0 {
		return line
	}

	var head, tail strings.Builder
	visible := 0
	skipped := 0
	inEscape := false

	for _, r := range line {
		switch {
		case inEscape:
			// Управляющая последовательность не занимает ячеек и обязана
			// уехать в ту же часть строки, где началась.
			if visible <= col {
				head.WriteRune(r)
			} else {
				tail.WriteRune(r)
			}
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		case r == 0x1b:
			inEscape = true
			if visible <= col {
				head.WriteRune(r)
			} else {
				tail.WriteRune(r)
			}
		case visible < col:
			head.WriteRune(r)
			visible++
		case skipped < insWidth:
			skipped++ // ячейка под вставкой — старый знак выбрасывается
			visible++
		default:
			tail.WriteRune(r)
			visible++
		}
	}

	if visible < col {
		// Строка короче нужной колонки — добиваем пробелами.
		head.WriteString(strings.Repeat(" ", col-visible))
	}
	return head.String() + ins + tail.String()
}
