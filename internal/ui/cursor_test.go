package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/itpro/ollchat/internal/config"
)

// Курсор рисует терминал, поэтому проверять надо не картинку, а то, что
// приложение сообщает Bubble Tea: форму, мигание, цвет и координату.

func TestCursorShapeFromConfig(t *testing.T) {
	cases := map[string]tea.CursorShape{
		config.CursorBlock:     tea.CursorBlock,
		config.CursorUnderline: tea.CursorUnderline,
		config.CursorBar:       tea.CursorBar,
	}
	for name, want := range cases {
		m := newTestModelWith(t, func(cfg *config.Config) {
			cfg.Input.Cursor.Shape = name
		})
		c := m.View().Cursor
		if c == nil {
			t.Fatalf("форма %q: курсор не передан Bubble Tea", name)
		}
		if c.Shape != want {
			t.Errorf("форма %q: получено %v, ожидалось %v", name, c.Shape, want)
		}
	}
}

func TestCursorBlinkAndColorFromConfig(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.Input.Cursor.Blink = false
		cfg.Input.Cursor.Color = "212"
	})
	c := m.View().Cursor
	if c == nil {
		t.Fatal("курсор не передан Bubble Tea")
	}
	if c.Blink {
		t.Error("мигание выключено в конфиге, а курсор мигает")
	}
	if c.Color != lipgloss.Color("212") {
		t.Errorf("цвет курсора = %v, ожидался цвет 212", c.Color)
	}
}

// Пустой цвет означает «как решит терминал» — своего цвета не навязываем.
func TestCursorColorEmptyKeepsTerminalDefault(t *testing.T) {
	m := newTestModel(t)
	c := m.View().Cursor
	if c == nil {
		t.Fatal("курсор не передан Bubble Tea")
	}
	if c.Color != nil {
		t.Errorf("при пустой настройке цвет должен остаться за терминалом, получено %v", c.Color)
	}
}

// Координата курсора отсчитывается от верхнего края экрана, а поле ввода
// стоит под шапкой, лентой и разделителем.
func TestCursorPositionFollowsInput(t *testing.T) {
	m := newTestModel(t)

	c := m.View().Cursor
	if c == nil {
		t.Fatal("курсор не передан Bubble Tea")
	}
	if want := m.inputTop(); c.Y != want {
		t.Errorf("строка курсора = %d, ожидалась %d", c.Y, want)
	}
	emptyX := c.X

	m.ta.SetValue("привет")
	c = m.View().Cursor
	if c == nil {
		t.Fatal("после ввода текста курсор пропал")
	}
	if c.X != emptyX+len([]rune("привет")) {
		t.Errorf("колонка курсора = %d, ожидалась %d", c.X, emptyX+len([]rune("привет")))
	}
}

// Пока открыт список выбора, поля ввода на экране нет — курсору там не место.
func TestCursorHiddenWhilePickerOpen(t *testing.T) {
	m := newTestModel(t)
	m.openServerPicker()

	if m.picker == nil {
		t.Fatal("подготовка: список выбора не открылся")
	}
	if c := m.View().Cursor; c != nil {
		t.Errorf("при открытом списке выбора курсор должен быть скрыт, получено %+v", c)
	}
}
