package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func arrowUp() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyUp} }
func arrowDown() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown} }
func enter() tea.KeyPressMsg     { return tea.KeyPressMsg{Code: tea.KeyEnter} }

// Перебор идёт от последнего к первому, а вниз возвращает обратно.
func TestHistoryWalksBothWays(t *testing.T) {
	var h inputHistory
	h.add("первый")
	h.add("второй")
	h.add("третий")

	for _, want := range []string{"третий", "второй", "первый"} {
		got, ok := h.back("")
		if !ok || got != want {
			t.Fatalf("шаг назад дал %q (%v), ожидалось %q", got, ok, want)
		}
	}
	if _, ok := h.back(""); ok {
		t.Error("за самым первым вопросом ничего быть не должно")
	}
	for _, want := range []string{"второй", "третий"} {
		got, ok := h.forward()
		if !ok || got != want {
			t.Fatalf("шаг вперёд дал %q (%v), ожидалось %q", got, ok, want)
		}
	}
}

// Набранное до захода в историю не теряется: возврат вниз до конца отдаёт его.
func TestHistoryKeepsDraft(t *testing.T) {
	var h inputHistory
	h.add("старый вопрос")

	if _, ok := h.back("недописанное"); !ok {
		t.Fatal("шаг назад не сработал")
	}
	got, ok := h.forward()
	if !ok || got != "недописанное" {
		t.Fatalf("возврат вниз дал %q (%v), а должен был вернуть набранное", got, ok)
	}
	if _, ok := h.forward(); ok {
		t.Error("ниже набранного идти некуда")
	}
}

// Пустое не запоминается, повтор подряд — одной записью: иначе перебор
// буксует там, где Enter нажали дважды.
func TestHistorySkipsEmptyAndRepeats(t *testing.T) {
	var h inputHistory
	h.add("   \n\t ")
	h.add("")
	if h.len() != 0 {
		t.Fatalf("пустое попало в историю: %d записей", h.len())
	}
	h.add("один и тот же")
	h.add("один и тот же")
	if h.len() != 1 {
		t.Fatalf("повтор подряд записан дважды: %d записей", h.len())
	}
	// Тот же вопрос после другого — запись законная.
	h.add("другой")
	h.add("один и тот же")
	if h.len() != 3 {
		t.Fatalf("записей %d, ожидалось 3", h.len())
	}
}

// Новая запись возвращает перебор в исходное положение: после отправки
// стрелка вверх обязана дать только что отправленное, а не то, на чём
// остановился прошлый перебор.
func TestHistoryResetsAfterAdd(t *testing.T) {
	var h inputHistory
	h.add("первый")
	h.add("второй")
	h.back("")
	h.back("") // смотрим на «первый»

	h.add("третий")
	got, ok := h.back("")
	if !ok || got != "третий" {
		t.Fatalf("после отправки шаг назад дал %q, ожидался «третий»", got)
	}
}

func TestHistoryHasLimit(t *testing.T) {
	var h inputHistory
	for i := 0; i < maxHistory+50; i++ {
		h.add(strings.Repeat("x", i%7+1) + string(rune('a'+i%26)) + string(rune(i)))
	}
	if h.len() != maxHistory {
		t.Fatalf("история выросла до %d записей при пределе %d", h.len(), maxHistory)
	}
}

// Стрелка вверх в интерфейсе возвращает отправленный вопрос.
func TestArrowUpRestoresSentQuestion(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/help")
	m.Update(enter())
	if v := m.ta.Value(); v != "" {
		t.Fatalf("после отправки поле должно опустеть, там %q", v)
	}

	m.Update(arrowUp())
	if got := m.ta.Value(); got != "/help" {
		t.Fatalf("стрелка вверх дала %q, ожидалось «/help»", got)
	}
	m.Update(arrowDown())
	if got := m.ta.Value(); got != "" {
		t.Fatalf("стрелка вниз должна вернуть пустое поле, там %q", got)
	}
}

// Внутри многострочного вопроса стрелка двигает курсор, а не листает историю.
// На самом верху — листает.
func TestArrowUpMovesCursorInsideMultiline(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/help")
	m.Update(enter())

	typeText(m, "первая")
	m.Update(altEnter())
	typeText(m, "вторая")

	// Курсор на второй строке: вверх обязан увести его на первую,
	// а текст оставить как есть.
	m.Update(arrowUp())
	if got := m.ta.Value(); got != "первая\nвторая" {
		t.Fatalf("текст подменён историей: %q", got)
	}
	if line := m.ta.Line(); line != 0 {
		t.Fatalf("курсор остался на строке %d, ожидалась 0", line)
	}
	// Теперь курсор наверху — вторая стрелка уходит в историю,
	// а набранное сохраняется в черновике.
	m.Update(arrowUp())
	if got := m.ta.Value(); got != "/help" {
		t.Fatalf("со верхней строки ожидалась история, получено %q", got)
	}
	m.Update(arrowDown())
	if got := m.ta.Value(); got != "первая\nвторая" {
		t.Fatalf("набранное не вернулось: %q", got)
	}
}

// Отклонённый вопрос остаётся в истории: ради этого случая всё и сделано.
func TestHistoryKeepsRejectedQuestion(t *testing.T) {
	m := newTestModel(t)

	// Модель без vision и вложенная картинка — отправка отклоняется.
	m.pending = []pendingImage{{num: 1, data: []byte("не картинка"), mime: "image/png"}}
	typeText(m, "что на картинке [Image01]")
	m.Update(enter())

	m.Update(arrowUp())
	if got := m.ta.Value(); !strings.Contains(got, "что на картинке") {
		t.Fatalf("отклонённый вопрос потерян, стрелка вверх дала %q", got)
	}
}

// Команда с токеном не попадает в историю ввода: стрелка вверх не должна
// показывать секрет тому, кто заглянул в экран.
func TestSecretCommandStaysOutOfHistory(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/help")
	m.Update(enter())
	typeText(m, "/confluencetoken MzQ0NzQyNjcyMDA0OnNlY3JldA")
	m.Update(enter())

	m.Update(arrowUp())
	if got := m.ta.Value(); strings.Contains(got, "MzQ0") {
		t.Fatalf("токен попал в историю: %q", got)
	}
	if got := m.ta.Value(); got != "/help" {
		t.Errorf("стрелка вверх должна дать предыдущую команду, дала %q", got)
	}
}

// Опознание секретных команд не зависит от регистра и слэша.
func TestSecretCommandRecognised(t *testing.T) {
	for _, s := range []string{"/confluencetoken abc", "/ConfluenceToken abc", "/token abc"} {
		if !secretCommand(s) {
			t.Errorf("%q должна считаться секретной", s)
		}
	}
	for _, s := range []string{"/help", "как связаны токен и сеанс", ""} {
		if secretCommand(s) {
			t.Errorf("%q секретной не является", s)
		}
	}
}
