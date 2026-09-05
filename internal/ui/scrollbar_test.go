package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestScrollbarHiddenForShortContent(t *testing.T) {
	s := scrollbar{height: 20, total: 5, offset: 0}
	if s.visible() {
		t.Error("при коротком тексте полоса не нужна")
	}
	// Колонка всё равно занимает место, чтобы лента не дёргалась.
	col := s.render()
	if len(col) != 20 {
		t.Fatalf("высота колонки = %d, ожидалось 20", len(col))
	}
	for _, c := range col {
		if c != " " {
			t.Errorf("без прокрутки колонка должна быть пустой, получено %q", c)
			break
		}
	}
}

func TestScrollbarThumbReachesBothEnds(t *testing.T) {
	height, total := 20, 200
	top := scrollbar{height: height, total: total, offset: 0}
	start, size := top.thumb()
	if start != 0 {
		t.Errorf("вверху бегунок должен стоять в начале, получено %d", start)
	}
	if size < 1 || size > height {
		t.Fatalf("размер бегунка = %d, ожидалось от 1 до %d", size, height)
	}

	bottom := scrollbar{height: height, total: total, offset: total - height}
	start, size = bottom.thumb()
	if start+size != height {
		t.Errorf("внизу бегунок должен доходить до конца: start=%d size=%d height=%d",
			start, size, height)
	}
}

func TestScrollbarThumbSizeReflectsContent(t *testing.T) {
	// Чем длиннее текст, тем короче бегунок.
	_, small := scrollbar{height: 20, total: 40, offset: 0}.thumb()
	_, tiny := scrollbar{height: 20, total: 400, offset: 0}.thumb()
	if small <= tiny {
		t.Errorf("для длинного текста бегунок должен быть короче: %d против %d", tiny, small)
	}
	if tiny < 1 {
		t.Error("бегунок не должен исчезать даже на очень длинном тексте")
	}
}

func TestScrollbarThumbMovesMonotonically(t *testing.T) {
	height, total := 15, 300
	prev := -1
	for offset := 0; offset <= total-height; offset++ {
		start, _ := scrollbar{height: height, total: total, offset: offset}.thumb()
		if start < prev {
			t.Fatalf("бегунок поехал назад при offset=%d: %d после %d", offset, start, prev)
		}
		prev = start
	}
}

func TestScrollbarOffsetForRowRoundTrip(t *testing.T) {
	height, total := 20, 200
	s := scrollbar{height: height, total: total}

	if got := s.offsetForRow(0); got != 0 {
		t.Errorf("щелчок по верху должен давать offset 0, получено %d", got)
	}
	if got := s.offsetForRow(height - 1); got != total-height {
		t.Errorf("щелчок по низу должен давать offset %d, получено %d", total-height, got)
	}

	// Перенос в строку и обратно не должен уводить бегунок далеко.
	for row := 0; row < height; row++ {
		off := s.offsetForRow(row)
		start, size := scrollbar{height: height, total: total, offset: off}.thumb()
		center := start + size/2
		if diff := center - row; diff > 1 || diff < -1 {
			t.Errorf("строка %d: центр бегунка оказался на %d (offset=%d)", row, center, off)
		}
	}
}

func TestScrollbarOffsetForRowClamped(t *testing.T) {
	s := scrollbar{height: 10, total: 100}
	if got := s.offsetForRow(-5); got != 0 {
		t.Errorf("отрицательная строка должна давать 0, получено %d", got)
	}
	if got := s.offsetForRow(999); got != 90 {
		t.Errorf("строка за пределами должна давать максимум 90, получено %d", got)
	}
}

func TestAttachScrollbarKeepsLineCount(t *testing.T) {
	view := "первая\nвторая\nтретья"
	bar := []string{"a", "b", "c"}
	got := attachScrollbar(view, bar)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("строк = %d, ожидалось 3", len(lines))
	}
	if lines[0] != "перваяa" || lines[2] != "третьяc" {
		t.Errorf("полоса приклеена неверно: %q", lines)
	}
}

// ── Поведение в модели ───────────────────────────────────────────────────────

func TestScrollbarDragMovesViewport(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 300)

	col := m.scrollbarColumn()
	if !m.scrollbarState().visible() {
		t.Fatal("подготовка: полоса прокрутки должна быть видна")
	}

	// Захватываем бегунок в верхней части полосы.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft,
		X: col, Y: transcriptTop})
	if !m.draggingBar {
		t.Fatal("щелчок по полосе должен захватывать бегунок")
	}
	if m.vp.YOffset() != 0 {
		t.Errorf("щелчок по верху полосы должен прокручивать в начало, получено %d", m.vp.YOffset())
	}

	// Тянем вниз — лента следует за указателем.
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft,
		X: col, Y: transcriptTop + m.vp.Height() - 1})
	if !m.vp.AtBottom() {
		t.Errorf("перетаскивание вниз должно доводить до конца, offset=%d", m.vp.YOffset())
	}

	// Указатель ушёл в сторону, кнопка ещё нажата — захват сохраняется.
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft,
		X: 0, Y: transcriptTop})
	if m.vp.YOffset() != 0 {
		t.Errorf("при удержании бегунка важна только строка, offset=%d", m.vp.YOffset())
	}

	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft,
		X: col, Y: transcriptTop})
	if m.draggingBar {
		t.Error("после отпускания кнопки захват должен сниматься")
	}

	// Без захвата движение мыши ленту не двигает.
	before := m.vp.YOffset()
	m.Update(tea.MouseMotionMsg{X: 0, Y: transcriptTop + 5})
	if m.vp.YOffset() != before {
		t.Error("без захвата бегунка движение мыши не должно прокручивать ленту")
	}
}

func TestClickOutsideScrollbarDoesNotDrag(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 300)

	// Щелчок по тексту, а не по полосе.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft,
		X: 5, Y: transcriptTop + 2})
	if m.draggingBar {
		t.Error("щелчок по тексту не должен захватывать бегунок")
	}
}

func TestScrollbarRendersInTranscript(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 300)

	view := m.transcriptView()
	lines := strings.Split(view, "\n")
	if len(lines) != m.vp.Height() {
		t.Fatalf("строк в ленте = %d, ожидалось %d", len(lines), m.vp.Height())
	}
	if !strings.Contains(view, scrollThumbChar) {
		t.Error("в ленте должен быть виден бегунок")
	}
	if !strings.Contains(view, scrollTrackChar) {
		t.Error("в ленте должна быть видна дорожка полосы прокрутки")
	}
	// Ширина ленты вместе с полосой равна ширине окна.
	if w := visibleWidth(lines[0]); w != m.width {
		t.Errorf("ширина строки ленты = %d, ожидалась %d", w, m.width)
	}
}
