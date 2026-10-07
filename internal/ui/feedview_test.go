package ui

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// joinParts — склейка ленты, какой её делал refreshViewport до feedView:
// непустые блоки через пустую строку.
func joinParts(parts []string) string {
	keep := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n\n")
}

// feedTokens — из чего собираются строки проверки: кириллица, широкие знаки,
// раскраска, табуляция, возврат каретки и слова длиннее окна.
var feedTokens = []string{
	"слово", "word", "日本語", "🙂", "\t", "\r", "x", " ",
	"\x1b[31mкрасный\x1b[0m",
	lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render("цвет"),
	strings.Repeat("ш", 45),
}

func randomFeedPart(rng *rand.Rand) string {
	switch rng.IntN(10) {
	case 0:
		return ""
	case 1:
		return []string{"   ", "\n", " \n ", "\x1b[0m"}[rng.IntN(4)]
	}
	lines := make([]string, 1+rng.IntN(8))
	for i := range lines {
		var b strings.Builder
		for n := rng.IntN(12); n > 0; n-- {
			b.WriteString(feedTokens[rng.IntN(len(feedTokens))])
			b.WriteString(" ")
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// Окно ленты обязано вести себя ровно как viewport из bubbles: тот же экран,
// та же прокрутка. Оба окна гоняются одной случайной (но повторяемой)
// последовательностью действий — тех, что делает интерфейс, — и сверяются
// после каждого шага.
func TestFeedViewMatchesViewport(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 2026))
	for round := 0; round < 40; round++ {
		w, h := 1+rng.IntN(100), rng.IntN(30)
		feed := newFeedView(w, h)
		ref := viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))
		var parts []string
		setBoth := func() {
			feed.setParts(parts)
			ref.SetContent(joinParts(parts))
		}
		for step := 0; step < 150; step++ {
			var what string
			switch rng.IntN(12) {
			case 0:
				what = "новый блок"
				parts = append(parts, randomFeedPart(rng))
				setBoth()
			case 1:
				what = "кусок потока в последний блок"
				if len(parts) == 0 {
					parts = append(parts, "")
				}
				parts[len(parts)-1] += feedTokens[rng.IntN(len(feedTokens))]
				setBoth()
			case 2:
				what = "перерисован блок в середине"
				if len(parts) > 0 {
					parts[rng.IntN(len(parts))] = randomFeedPart(rng)
				}
				setBoth()
			case 3:
				what = "выброшены блоки хвоста"
				if len(parts) > 0 {
					parts = parts[:rng.IntN(len(parts))]
				}
				setBoth()
			case 4:
				what = "лента собрана заново"
				parts = nil
				for n := rng.IntN(6); n > 0; n-- {
					parts = append(parts, randomFeedPart(rng))
				}
				setBoth()
			case 5:
				what = "высота окна"
				h = rng.IntN(30)
				feed.SetHeight(h)
				ref.SetHeight(h)
				if rng.IntN(2) == 0 {
					setBoth() // relayout: высота, потом та же лента
				}
			case 6:
				what = "ширина окна"
				w = 1 + rng.IntN(100)
				feed.SetWidth(w)
				ref.SetWidth(w)
			case 7:
				what = "в начало или в конец"
				if rng.IntN(2) == 0 {
					feed.GotoTop()
					ref.GotoTop()
				} else {
					feed.GotoBottom()
					ref.GotoBottom()
				}
			case 8:
				what = "страница или полстраницы"
				switch rng.IntN(4) {
				case 0:
					feed.PageUp()
					ref.PageUp()
				case 1:
					feed.PageDown()
					ref.PageDown()
				case 2:
					feed.HalfPageUp()
					ref.HalfPageUp()
				default:
					feed.HalfPageDown()
					ref.HalfPageDown()
				}
			case 9:
				what = "бегунок"
				y := rng.IntN(ref.TotalLineCount()+10) - 5
				feed.SetYOffset(y)
				ref.SetYOffset(y)
			case 10:
				what = "колесо"
				btn := []tea.MouseButton{tea.MouseWheelUp, tea.MouseWheelDown,
					tea.MouseWheelLeft, tea.MouseWheelRight}[rng.IntN(4)]
				var mod tea.KeyMod
				if rng.IntN(3) == 0 {
					mod = tea.ModShift
				}
				msg := tea.MouseWheelMsg{Button: btn, Mod: mod}
				feed, _ = feed.Update(msg)
				ref, _ = ref.Update(msg)
			default:
				what = "лента без изменений, следим за концом"
				atFeed, atRef := feed.AtBottom(), ref.AtBottom()
				setBoth()
				if atFeed {
					feed.GotoBottom()
				}
				if atRef {
					ref.GotoBottom()
				}
			}
			where := fmt.Sprintf("круг %d, шаг %d (%s), окно %d×%d", round, step, what, w, h)
			if feed.View() != ref.View() {
				t.Fatalf("%s: экран разошёлся\nсвоё:\n%q\nviewport:\n%q", where, feed.View(), ref.View())
			}
			if feed.YOffset() != ref.YOffset() || feed.xOffset != ref.XOffset() {
				t.Fatalf("%s: сдвиг %d/%d, у viewport %d/%d", where,
					feed.YOffset(), feed.xOffset, ref.YOffset(), ref.XOffset())
			}
			if feed.AtTop() != ref.AtTop() || feed.AtBottom() != ref.AtBottom() {
				t.Fatalf("%s: края разошлись", where)
			}
			if feed.TotalLineCount() != ref.TotalLineCount() || feed.GetContent() != ref.GetContent() {
				t.Fatalf("%s: содержимое разошлось: %d строк против %d", where,
					feed.TotalLineCount(), ref.TotalLineCount())
			}
		}
	}
}

// Отрисовка блоков зависит от ширины, а не от высоты: изменение одной высоты
// окна не прогоняет историю через markdown заново (2.7 с при 500 обменах).
func TestHeightOnlyResizeKeepsRendering(t *testing.T) {
	m := newTestModel(t)
	m.addBlock(block{kind: blockAssistant, text: "ответ модели"})
	i := len(m.rendered) - 1
	m.rendered[i] = "сторожок" // такой отрисовки Render не даёт

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.rendered[i] != "сторожок" {
		t.Fatal("изменение одной высоты перерисовало историю")
	}
	if !strings.Contains(m.vp.GetContent(), "сторожок") {
		t.Error("лента не обновлена после изменения высоты")
	}
	if m.vp.Height() != m.viewportHeight() {
		t.Errorf("высота ленты %d, ожидалась %d", m.vp.Height(), m.viewportHeight())
	}

	m.Update(tea.WindowSizeMsg{Width: 90, Height: 40})
	if m.rendered[i] == "сторожок" {
		t.Fatal("после изменения ширины история обязана перерисоваться")
	}
}

// Неизменная история не перемеряется: кусок потока трогает только живой блок.
func TestFeedViewReusesUnchangedParts(t *testing.T) {
	feed := newFeedView(80, 10)
	parts := []string{"первый блок", "второй блок\nв две строки", "живой"}
	feed.setParts(parts)
	first := feed.parts[0].lines

	parts[2] += " ответ"
	feed.setParts(parts)
	if &feed.parts[0].lines[0] != &first[0] {
		t.Error("строки неизменного блока разобраны заново")
	}
	if got := feed.GetContent(); got != joinParts(parts) {
		t.Errorf("лента после куска потока:\n%q\nожидалось:\n%q", got, joinParts(parts))
	}
}
