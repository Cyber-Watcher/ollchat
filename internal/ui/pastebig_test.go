package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"

	tea "charm.land/bubbletea/v2"
)

// Порог — место в поле, а не число знаков.
//
// Сто коротких строк не помещаются в трёхстрочное поле, сколько бы знаков
// в них ни было: перевод строки съедает остаток строки целиком.
func TestPasteFits(t *testing.T) {
	cases := []struct {
		name          string
		text          string
		width, height int
		want          bool
	}{
		{"короткая строка", "привет", 40, 3, true},
		{"ровно три строки", "а\nб\nв", 40, 3, true},
		{"четыре строки", "а\nб\nв\nг", 40, 3, false},
		{"длинная строка переносом", strings.Repeat("x", 121), 40, 3, false},
		{"длинная строка впритык", strings.Repeat("x", 80), 40, 3, true},
		{"размеров ещё нет", strings.Repeat("x\n", 100), 0, 0, true},
		{"кириллица считается знаками, не байтами", strings.Repeat("я", 39), 40, 1, true},
		// Широкий терминал не должен отменять свёртку: длинную вставку в 1–3
		// строки на окне в 200 колонок всё равно надо свернуть (регресс 03.09.2026).
		{"широкое окно, длинная строка", strings.Repeat("y", 350), 200, 3, false},
		{"широкое окно, две длинные строки", strings.Repeat("y", 190) + "\n" + strings.Repeat("z", 190), 200, 3, false},
	}
	for _, c := range cases {
		if got := pasteFits(c.text, c.width, c.height); got != c.want {
			t.Errorf("%s: pasteFits = %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

// Метка называет число знаков и склоняет слово: её читает человек.
func TestPastedTextLabel(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{strings.Repeat("я", 1), "[Текст01: 1 знак]"},
		{strings.Repeat("я", 3), "[Текст01: 3 знака]"},
		{strings.Repeat("я", 11), "[Текст01: 11 знаков]"},
		{strings.Repeat("я", 21), "[Текст01: 21 знак]"},
		{strings.Repeat("я", 5), "[Текст01: 5 знаков]"},
	}
	for _, c := range cases {
		p := pastedText{num: 1, text: c.text}
		if got := p.label(); got != c.want {
			t.Errorf("label = %q, ожидалось %q", got, c.want)
		}
	}
}

// Разворот идёт по метке, а не по порядку: метки можно переставить местами
// и дописать между ними текст.
func TestExpandPastesByLabel(t *testing.T) {
	m := &Model{pastes: []pastedText{
		{num: 1, text: "ПЕРВЫЙ"},
		{num: 2, text: "ВТОРОЙ"},
	}}
	in := "смотри " + m.pastes[1].label() + " и потом " + m.pastes[0].label() + " конец"
	got := m.expandPastes(in)
	want := "смотри ВТОРОЙ и потом ПЕРВЫЙ конец"
	if got != want {
		t.Errorf("expandPastes = %q, ожидалось %q", got, want)
	}
}

// Стёр метку — отменил вставку. Метка в тексте единственный источник правды.
func TestSyncPastesDropsErasedMarks(t *testing.T) {
	m := newTestModel(t)
	m.pastes = []pastedText{{num: 1, text: "ПЕРВЫЙ"}, {num: 2, text: "ВТОРОЙ"}}
	m.ta.SetValue("осталась только " + m.pastes[1].label())

	m.syncPastes()
	if len(m.pastes) != 1 || m.pastes[0].num != 2 {
		t.Fatalf("после стирания метки осталось %v", m.pastes)
	}
	// Метка без вставки — обычный текст: человек мог написать её руками.
	m.ta.SetValue("[Текст07: 100 знаков]")
	m.syncPastes()
	if len(m.pastes) != 0 {
		t.Errorf("чужая метка не должна удерживать вставку: %v", m.pastes)
	}
}

// Целиком через Update: сто строк не попадают в поле, вместо них метка,
// а текст ждёт отправки. Ради этого всё и делалось.
func TestBigPasteCollapsesToMark(t *testing.T) {
	m := newTestModel(t)
	big := strings.Repeat("строка текста\n", 100)

	m.Update(tea.PasteMsg{Content: big})

	value := m.ta.Value()
	if strings.Contains(value, "строка текста") {
		t.Fatalf("большая вставка попала в поле ввода целиком: %d знаков", len(value))
	}
	if len(m.pastes) != 1 {
		t.Fatalf("вставка не сохранена: %v", m.pastes)
	}
	if !strings.Contains(value, m.pastes[0].label()) {
		t.Errorf("в поле нет метки вставки: %q", value)
	}
	if got := m.expandPastes(value); got != big {
		t.Errorf("разворот не вернул исходный текст (%d знаков против %d)", len(got), len(big))
	}
}

// Обычная вставка ведёт себя как прежде: никаких меток.
func TestSmallPasteGoesToInput(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.PasteMsg{Content: "короткая вставка"})

	if got := m.ta.Value(); got != "короткая вставка" {
		t.Errorf("короткая вставка должна попадать в поле как есть: %q", got)
	}
	if len(m.pastes) != 0 {
		t.Errorf("короткая вставка не должна сворачиваться: %v", m.pastes)
	}
}

// input.collapse_paste = false: текст идёт в поле как есть, но вставка всё
// равно известна — иначе Shift+F6 нечего было бы сворачивать.
func TestPasteNotCollapsedWhenSettingOff(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) { c.Input.CollapsePaste = false })
	big := strings.Repeat("строка текста\n", 100)

	m.Update(tea.PasteMsg{Content: big})

	if !strings.Contains(m.ta.Value(), "строка текста") {
		t.Fatalf("при collapse_paste=false текст обязан попасть в поле целиком: %q", m.ta.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("свёрнутых вставок быть не должно: %v", m.pastes)
	}
	if len(m.pasteVault) != 1 {
		t.Fatalf("вставка не запомнена: %v", m.pasteVault)
	}

	// Shift+F6 сворачивает её в метку.
	if !m.collapseInPrompt() {
		t.Fatal("свернуть вставку не удалось")
	}
	if strings.Contains(m.ta.Value(), "строка текста") {
		t.Errorf("после сворачивания текста в поле быть не должно: %q", m.ta.Value())
	}
	if len(m.pastes) != 1 {
		t.Errorf("свёрнутая вставка не поднялась в список: %v", m.pastes)
	}
	if got := m.expandPastes(m.ta.Value()); got != big {
		t.Errorf("метка не разворачивается в исходный текст (%d знаков против %d)", len(got), len(big))
	}
}

// F6 разворачивает метку в поле, Shift+F6 сворачивает обратно — и так по кругу.
func TestExpandAndFoldPasteInPrompt(t *testing.T) {
	m := newTestModel(t)
	big := strings.Repeat("строка текста\n", 100)
	m.Update(tea.PasteMsg{Content: big})
	label := m.pastes[0].label()

	if !m.expandInPrompt() {
		t.Fatal("F6 не развернул вставку")
	}
	if !strings.Contains(m.ta.Value(), "строка текста") {
		t.Fatalf("после разворота в поле нет текста: %q", m.ta.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("развёрнутая вставка не должна ждать подстановки: %v", m.pastes)
	}
	if m.expandInPrompt() {
		t.Error("второй разворот менять ничего не должен")
	}

	if !m.collapseInPrompt() {
		t.Fatal("Shift+F6 не свернул вставку")
	}
	if got := m.ta.Value(); !strings.Contains(got, label) {
		t.Errorf("после сворачивания в поле нет метки: %q", got)
	}
	if m.collapseInPrompt() {
		t.Error("повторное сворачивание менять ничего не должно")
	}
}

// Вопрос со свёрнутой вставкой, возвращённый стрелкой вверх, обязан снова
// знать свой текст: иначе повторная отправка ушла бы модели с мёртвой меткой.
func TestHistoryKeepsPasteAlive(t *testing.T) {
	m := newTestModel(t)
	big := strings.Repeat("строка текста\n", 100)
	m.Update(tea.PasteMsg{Content: big})
	m.ta.InsertString(" разбери это")
	sent := m.ta.Value()

	// Отправка: список свёрнутых вставок сбрасывается, как в send().
	m.hist.add(sent)
	m.pastes = nil
	m.ta.Reset()

	back, ok := m.hist.back(m.ta.Value())
	if !ok {
		t.Fatal("история пуста")
	}
	m.setInput(back)

	if len(m.pastes) != 1 {
		t.Fatalf("вставка не поднялась из хранилища: %v", m.pastes)
	}
	if got := m.expandPastes(m.ta.Value()); !strings.HasPrefix(got, big) {
		t.Errorf("вопрос из истории потерял текст вставки: %d знаков", len(got))
	}
}

// Номера вставок не повторяются за сеанс: две метки с одним номером означали бы
// два разных текста под одним именем, и вопрос из истории ушёл бы с чужим.
func TestPasteNumbersAreUniquePerSession(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.PasteMsg{Content: strings.Repeat("первая вставка\n", 100)})
	first := m.pastes[0].label()

	m.pastes = nil // как после отправки
	m.ta.Reset()
	m.Update(tea.PasteMsg{Content: strings.Repeat("вторая вставка\n", 100)})
	second := m.pastes[0].label()

	if first == second {
		t.Errorf("две вставки получили одну метку: %q", first)
	}
	if len(m.pasteVault) != 2 {
		t.Errorf("в хранилище должны быть обе вставки: %v", len(m.pasteVault))
	}
}
