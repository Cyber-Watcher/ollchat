package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Окно ленты диалога.
//
// **Зачем своё, а не viewport из bubbles.** viewport принимает содержимое
// только целиком: SetContent режет его на строки и меряет ширину каждой
// строки (ansi.StringWidth — разбор управляющих последовательностей и графем).
// А лента меняется на каждый кусок потока ответа, и каждый кусок обходился
// заново всей историей: 13.6 мс на кусок при 50 обменах, 138 мс при 500
// (аудит 07.10.2026, находка 16) — при 30–80 токенах в секунду интерфейс
// упирался в процессор. Две трети этого времени уходило на замер ширины
// строк, которые не менялись с прошлого куска.
//
// Здесь лента хранится частями — по части на блок, — и каждая часть помнит
// свои строки и их ширину. Изменился живой блок в хвосте — пересобирается
// хвост; ширина меряется только у изменившихся частей.
//
// **Поведение — ровно как у viewport bubbles v2.1.1** в той его части,
// которой пользуется интерфейс: те же правила прокрутки и тот же вывод View,
// собранный теми же вызовами lipgloss и ansi. Совпадение стережёт
// TestFeedViewMatchesViewport: он гоняет оба окна одними действиями
// и сверяет экран после каждого.

const (
	// feedWheelDelta — строк на щелчок колеса, как MouseWheelDelta у viewport.
	feedWheelDelta = 3
	// feedHorizontalStep — колонок на шаг вбок, как у viewport.
	feedHorizontalStep = 6
)

// feedView — окно ленты.
type feedView struct {
	width, height    int
	yOffset, xOffset int

	parts   []feedPart
	all     []string // строки всех частей подряд, с пустой строкой между частями
	lines   []string // что показывается: all либо ничего
	longest int      // ширина самой широкой строки
}

// feedPart — одна часть ленты: отрисованный блок.
type feedPart struct {
	src    string   // отрисовка, по которой собраны строки
	lines  []string // её строки; пусто — часть в ленту не попадает
	width  int      // ширина самой широкой из них
	top    int      // с какого места all начинается часть (вместе с разделителем)
	widest int      // самая широкая строка частей с первой по эту
}

func newFeedView(width, height int) feedView {
	return feedView{width: width, height: height}
}

// setParts кладёт в окно ленту: непустые части через пустую строку — та же
// склейка, что прежде делал refreshViewport через strings.Join.
//
// Пересобирается всё, начиная с первой изменившейся части, а ширина меряется
// только у частей с новой отрисовкой: неизменная история стоит одного
// сравнения строк на блок.
func (v *feedView) setParts(src []string) {
	k := 0
	for k < len(src) && k < len(v.parts) && v.parts[k].src == src[k] {
		k++
	}
	if k < len(src) || k < len(v.parts) {
		old := v.parts
		if k < len(old) {
			v.all = v.all[:old[k].top]
		}
		// Части пишутся поверх прежнего массива: old[i] читается раньше,
		// чем на его место ляжет новая часть.
		v.parts = old[:k]
		for i := k; i < len(src); i++ {
			p := feedPart{src: src[i], top: len(v.all)}
			switch {
			case i < len(old) && old[i].src == src[i]:
				p.lines, p.width = old[i].lines, old[i].width
			case strings.TrimSpace(src[i]) != "":
				p.lines = strings.Split(src[i], "\n")
				p.width = lipgloss.Width(src[i])
			}
			if len(p.lines) > 0 {
				if len(v.all) > 0 {
					v.all = append(v.all, "")
				}
				v.all = append(v.all, p.lines...)
			}
			p.widest = p.width
			if i > 0 {
				p.widest = max(p.widest, v.parts[i-1].widest)
			}
			v.parts = append(v.parts, p)
		}
	}

	// Дальше — то же, что viewport.SetContentLines делает с готовыми строками.
	v.lines, v.longest = v.all, 0
	if n := len(v.parts); n > 0 {
		v.longest = v.parts[n-1].widest
	}
	if len(v.lines) == 1 && v.longest == 0 {
		v.lines = nil
	}
	if v.yOffset > v.maxYOffset() {
		v.GotoBottom()
	}
}

// GetContent — вся лента одной строкой, как у viewport.
func (v feedView) GetContent() string { return strings.Join(v.lines, "\n") }

func (v feedView) Height() int      { return v.height }
func (v *feedView) SetHeight(h int) { v.height = h }
func (v *feedView) SetWidth(w int)  { v.width = w }

func (v feedView) YOffset() int        { return v.yOffset }
func (v feedView) TotalLineCount() int { return len(v.lines) }
func (v feedView) AtTop() bool         { return v.yOffset <= 0 }
func (v feedView) AtBottom() bool      { return v.yOffset >= v.maxYOffset() }

func (v feedView) maxYOffset() int { return max(0, len(v.lines)-v.height) }
func (v feedView) maxXOffset() int { return max(0, v.longest-v.width) }

func (v *feedView) SetYOffset(n int) { v.yOffset = clampOrdered(n, 0, v.maxYOffset()) }
func (v *feedView) setXOffset(n int) { v.xOffset = clampOrdered(n, 0, v.maxXOffset()) }

func (v *feedView) GotoTop() {
	if v.AtTop() {
		return
	}
	v.SetYOffset(0)
}

func (v *feedView) GotoBottom() { v.SetYOffset(v.maxYOffset()) }

func (v *feedView) PageDown() {
	if v.AtBottom() {
		return
	}
	v.scrollDown(v.height)
}

func (v *feedView) PageUp() {
	if v.AtTop() {
		return
	}
	v.scrollUp(v.height)
}

func (v *feedView) HalfPageDown() {
	if v.AtBottom() {
		return
	}
	v.scrollDown(v.height / 2)
}

func (v *feedView) HalfPageUp() {
	if v.AtTop() {
		return
	}
	v.scrollUp(v.height / 2)
}

func (v *feedView) scrollDown(n int) {
	if v.AtBottom() || n == 0 || len(v.lines) == 0 {
		return
	}
	v.SetYOffset(v.yOffset + n)
}

func (v *feedView) scrollUp(n int) {
	if v.AtTop() || n == 0 || len(v.lines) == 0 {
		return
	}
	v.SetYOffset(v.yOffset - n)
}

// Update — колесо мыши, как у viewport. Клавиши окну не передаются:
// прокрутку с клавиатуры ведёт handleKey.
func (v feedView) Update(msg tea.Msg) (feedView, tea.Cmd) {
	wheel, ok := msg.(tea.MouseWheelMsg)
	if !ok {
		return v, nil
	}
	switch wheel.Button {
	case tea.MouseWheelDown:
		// Некоторые терминалы не присылают Shift вместе с колесом.
		if wheel.Mod.Contains(tea.ModShift) {
			v.setXOffset(v.xOffset + feedHorizontalStep)
			break
		}
		v.scrollDown(feedWheelDelta)
	case tea.MouseWheelUp:
		if wheel.Mod.Contains(tea.ModShift) {
			v.setXOffset(v.xOffset - feedHorizontalStep)
			break
		}
		v.scrollUp(feedWheelDelta)
	case tea.MouseWheelLeft:
		v.setXOffset(v.xOffset - feedHorizontalStep)
	case tea.MouseWheelRight:
		v.setXOffset(v.xOffset + feedHorizontalStep)
	}
	return v, nil
}

// View рисует видимую часть ленты, добивая её до размеров окна.
func (v feedView) View() string {
	w, h := v.width, v.height
	if w == 0 || h == 0 {
		return ""
	}
	contents := lipgloss.NewStyle().Width(w).Height(h).Render(strings.Join(v.visibleLines(), "\n"))
	return lipgloss.NewStyle().UnsetWidth().UnsetHeight().Render(contents)
}

// visibleLines — строки в окне; длинные срезаются по ширине со сдвигом вбок.
func (v feedView) visibleLines() []string {
	maxHeight, maxWidth := max(0, v.height), max(0, v.width)
	if maxHeight == 0 || maxWidth == 0 {
		return nil
	}
	var lines []string
	if total := len(v.lines); total > 0 {
		top := min(v.yOffset, total)
		bottom := clampOrdered(top+maxHeight, top, total)
		lines = slices.Clone(v.lines[top:bottom])
	}
	if v.xOffset == 0 && v.longest <= maxWidth {
		return lines
	}
	for i := range lines {
		lines[i] = ansi.Cut(lines[i], v.xOffset, v.xOffset+maxWidth)
	}
	return lines
}

// clampOrdered — как clamp у viewport: границы, перепутанные местами,
// меняются местами, а не дают пустой промежуток.
func clampOrdered(v, low, high int) int {
	if high < low {
		low, high = high, low
	}
	return min(high, max(low, v))
}
