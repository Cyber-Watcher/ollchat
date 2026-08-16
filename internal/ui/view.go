package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// View рисует весь экран.
//
// Вместе с содержимым Bubble Tea отдаёт терминалу и настоящий курсор: его
// положение, форму, мигание и цвет. Поле ввода поэтому не подмешивает
// поддельный курсор в текст — символ под курсором остаётся виден, а ширина
// строки не меняется, как в обычной командной строке.
//
// Здесь же задаётся режим мыши: когда отчёт о мыши выключен, терминал сам
// выделяет и копирует текст ленты; ценой этого не работают колесо и бегунок.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = m.mouseMode()

	if !m.ready {
		v.SetContent("Запуск ollchat…")
		return v
	}

	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(m.transcriptView())
	b.WriteString("\n")
	// Панели стоят над разделителем, вместе с лентой: они относятся к тому,
	// что показано выше, и не должны разрывать поле ввода.
	if m.picker == nil {
		switch {
		case m.images != nil:
			b.WriteString(m.images.view(m.width, m.pending))
			b.WriteString("\n")
		case m.files != nil:
			b.WriteString(m.files.view(m.width))
			b.WriteString("\n")
		}
	}
	b.WriteString(m.separatorView())
	b.WriteString("\n")

	switch {
	case m.picker != nil:
		b.WriteString(m.picker.view(m.width))
	case m.confirm != nil:
		b.WriteString(m.confirmView())
		b.WriteString("\n")
		b.WriteString(m.ta.View())
	default:
		b.WriteString(m.ta.View())
	}

	b.WriteString("\n")
	b.WriteString(m.statusView())

	v.SetContent(b.String())
	v.Cursor = m.inputCursor()
	return v
}

// mouseMode сообщает Bubble Tea, запрашивать ли у терминала события мыши.
func (m *Model) mouseMode() tea.MouseMode {
	if !m.mouseOn {
		return tea.MouseModeNone
	}
	return tea.MouseModeCellMotion
}

// inputCursor переводит курсор поля ввода в координаты всего экрана.
// Пока открыт список выбора, поля ввода на экране нет — курсор прячем.
func (m *Model) inputCursor() *tea.Cursor {
	if m.picker != nil {
		return nil
	}
	c := m.ta.Cursor()
	if c == nil {
		return nil
	}
	c.Y += m.inputTop()
	return c
}

// inputTop — номер строки экрана, с которой начинается поле ввода:
// под шапкой, лентой, разделителем и (если открыта) панелью подтверждения.
func (m *Model) inputTop() int {
	top := transcriptTop + m.vp.Height() + 1
	// Панели стоят над разделителем, поэтому сдвигают поле ввода вниз.
	if m.files != nil {
		top += m.files.height()
	}
	top += m.imagesHeight()
	if m.confirm != nil {
		top += m.confirmHeight()
	}
	return top
}

// filesTop — номер строки экрана, с которой начинается список файлов.
func (m *Model) filesTop() int { return transcriptTop + m.vp.Height() }

// onFileMenu сообщает, попал ли указатель в панель списка файлов.
func (m *Model) onFileMenu(y int) bool {
	if m.files == nil {
		return false
	}
	top := m.filesTop()
	return y >= top && y < top+m.files.height()
}

// scrollbarState собирает геометрию полосы прокрутки по текущему состоянию ленты.
func (m *Model) scrollbarState() scrollbar {
	return scrollbar{
		height: m.vp.Height(),
		total:  m.vp.TotalLineCount(),
		offset: m.vp.YOffset(),
	}
}

// scrollbarColumn — номер колонки экрана, в которой нарисована полоса прокрутки.
func (m *Model) scrollbarColumn() int { return m.width - 1 }

// transcriptTop — номер строки экрана, с которой начинается лента (под шапкой).
const transcriptTop = 1

// transcriptView рисует ленту диалога вместе с полосой прокрутки справа.
//
// Когда мышь отдана терминалу, полосы нет вовсе, а строки не добиваются
// пробелами до края: терминал копирует ячейки экрана как есть, и в буфер обмена
// попали бы и колонка бегунка, и вся добивка. Без них копируется ровно ответ.
func (m *Model) transcriptView() string {
	view := m.vp.View()
	if !m.mouseOn {
		return trimLineEnds(view)
	}
	return attachScrollbar(view, m.scrollbarState().render())
}

// linesBelow — сколько строк ленты осталось ниже видимой области.
func (m *Model) linesBelow() int {
	n := m.vp.TotalLineCount() - m.vp.YOffset() - m.vp.Height()
	if n < 0 {
		return 0
	}
	return n
}

// separatorView рисует разделитель между лентой и полем ввода. Когда снизу
// осталось непрочитанное, в разделитель встраивается счётчик строк — так
// подсказка не занимает отдельную строку и не меняет высоту зон.
func (m *Model) separatorView() string {
	width := max(1, m.width)
	below := m.linesBelow()
	if below <= 0 || m.vp.AtBottom() {
		return styBorder.Render(strings.Repeat("─", width))
	}

	label := fmt.Sprintf(" ↓ ещё %d %s · Ctrl+G — в конец ",
		below, plural(below, "строка", "строки", "строк"))
	if m.streaming {
		label = fmt.Sprintf(" ↓ ещё %d %s · автопрокрутка на паузе · Ctrl+G — в конец ",
			below, plural(below, "строка", "строки", "строк"))
	}

	labelWidth := lipgloss.Width(label)
	if labelWidth+4 > width {
		// В узком окне подсказку сокращаем до счётчика.
		label = fmt.Sprintf(" ↓ %d ", below)
		labelWidth = lipgloss.Width(label)
		if labelWidth+4 > width {
			return styBorder.Render(strings.Repeat("─", width))
		}
	}

	left := 2
	right := width - labelWidth - left
	if right < 0 {
		right = 0
	}
	return styBorder.Render(strings.Repeat("─", left)) +
		styScrollHint.Render(label) +
		styBorder.Render(strings.Repeat("─", right))
}

// plural подбирает форму русского существительного для числа n.
func plural(n int, one, few, many string) string {
	mod100 := n % 100
	if mod100 >= 11 && mod100 <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

// headerView рисует шапку: сервер, модель, рабочий каталог.
func (m *Model) headerView() string {
	model := m.modelName
	if model == "" {
		model = "не выбрана"
	}
	// В шапке показываем только отличительные возможности: completion есть у всех.
	marks := make([]string, 0, 3)
	for _, c := range m.modelCaps {
		switch c {
		case "tools", "thinking", "vision":
			marks = append(marks, c)
		}
	}
	caps := ""
	if len(marks) > 0 {
		caps = " [" + strings.Join(marks, ",") + "]"
	}

	parts := []string{
		styHeader.Render("ollchat"),
		styHeaderDim.Render("сервер: ") + styHeader.Render(m.server.Name),
		styHeaderDim.Render(trimScheme(m.server.URL)),
		styHeaderDim.Render("модель: ") + lipgloss.NewStyle().Foreground(colModel).Render(model+caps),
	}
	line := strings.Join(parts, styBorder.Render(" · "))

	root := m.guard.Sandbox().Root()
	right := styHeaderDim.Render("📁 " + shortenPath(root, 30))
	if m.srvVersion != "" {
		right = styHeaderDim.Render("ollama "+m.srvVersion) + styBorder.Render(" · ") + right
	}

	return joinEnds(line, right, m.width)
}

// joinEnds разносит две части строки по краям заданной ширины.
// Если места не хватает, правая часть отбрасывается, а левая обрезается
// с учётом управляющих последовательностей, а не длины строки в байтах.
func joinEnds(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap >= 1 {
		return left + strings.Repeat(" ", gap) + right
	}
	return clip(left, width)
}

// clip обрезает строку до ширины width, не ломая ANSI-последовательности.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// statusView рисует нижнюю строку состояния.
func (m *Model) statusView() string {
	mode := m.guard.Mode()
	segments := []string{modeStyle(mode).Render(mode)}

	// Индикатор заполнения контекстного окна.
	ctxText := m.meter.String(10)
	segments = append(segments, ctxStyle(m.meter.Percent()).Render(ctxText))

	if m.job != nil {
		segments = append(segments, m.spin.View()+" "+styStatusWarn.Render(m.jobStatus()))
	}
	if m.streaming {
		spin := m.spin.View()
		info := "думает…"
		if m.iterations > 0 {
			info = fmt.Sprintf("инструментов: %d", m.iterations)
		}
		segments = append(segments, spin+" "+styStatus.Render(info)+styStatus.Render("  Esc — прервать"))
	} else if tps := m.stats.TokensPerSecond(); tps > 0 {
		segments = append(segments, styStatus.Render(fmt.Sprintf("%.0f ток/с", tps)))
	}

	if m.logger.Enabled() {
		segments = append(segments, styStatus.Render("лог: "+filepath.Base(m.logger.CurrentPath())))
	} else {
		segments = append(segments, styStatus.Render("лог: выкл"))
	}

	// Вложенные изображения: одно показываем подробно, несколько — общей
	// пометкой, подробности смотрятся в панели по F3.
	if s := m.imagesStatus(); s != "" {
		segments = append(segments, styStatusWarn.Render(s))
	}

	line := strings.Join(segments, styBorder.Render(" · "))

	right := styStatus.Render("^C выход · /help")
	if m.statusMsg != "" {
		right = styStatusWarn.Render(m.statusMsg)
	}

	return joinEnds(line, right, m.width)
}

// confirmHeight — сколько строк занимает панель подтверждения.
func (m *Model) confirmHeight() int {
	if m.confirm == nil {
		return 0
	}
	return lipgloss.Height(m.confirmView())
}

// confirmView рисует запрос подтверждения действия.
func (m *Model) confirmView() string {
	c := m.confirm
	if c == nil {
		return ""
	}

	icon := "⚠"
	switch c.Kind {
	case permissions.KindWrite:
		icon = "✎"
	case permissions.KindBash:
		icon = "$"
	case permissions.KindFetch:
		icon = "🌐"
	}

	var b strings.Builder
	b.WriteString(styConfirmTitle.Render(icon+" "+c.Title) + "\n")
	if c.Reason != "" {
		b.WriteString(styNotice.Render(c.Reason) + "\n")
	}

	if strings.TrimSpace(c.Preview) != "" {
		inner := m.width - 6
		lines := strings.Split(strings.TrimRight(c.Preview, "\n"), "\n")

		const maxShow = 14
		start := m.confirmScroll
		if start > len(lines)-1 {
			start = len(lines) - 1
		}
		if start < 0 {
			start = 0
		}
		end := start + maxShow
		if end > len(lines) {
			end = len(lines)
		}
		shown := strings.Join(lines[start:end], "\n")

		if c.Kind == permissions.KindWrite {
			b.WriteString(renderDiff(shown, inner) + "\n")
		} else {
			b.WriteString(styToolOutput.Render(truncateBlock(shown, inner)) + "\n")
		}
		if len(lines) > end {
			b.WriteString(styNotice.Render(fmt.Sprintf("… ещё строк: %d (↑↓ — прокрутка)", len(lines)-end)) + "\n")
		}
	}

	b.WriteString(styConfirmKeys.Render("[y] выполнить   [n] отклонить") + "\n")
	b.WriteString(styConfirmKeys.Render("[a] это действие до конца сеанса   [t] весь инструмент " +
		c.Tool + " до конца сеанса"))
	return styConfirmBox.Width(m.width - 2).Render(b.String())
}

func truncateBlock(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = truncateLine(l, width)
	}
	return strings.Join(lines, "\n")
}

func trimScheme(u string) string {
	u = strings.TrimPrefix(u, "http://")
	return strings.TrimPrefix(u, "https://")
}

// shortenPath сокращает длинный путь до вида …/parent/dir.
func shortenPath(p string, width int) string {
	if len([]rune(p)) <= width {
		return p
	}
	parts := strings.Split(p, string(filepath.Separator))
	for i := 1; i < len(parts); i++ {
		candidate := "…/" + strings.Join(parts[i:], "/")
		if len([]rune(candidate)) <= width {
			return candidate
		}
	}
	r := []rune(p)
	return "…" + string(r[len(r)-width+1:])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
