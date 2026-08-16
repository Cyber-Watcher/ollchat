package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/itpro/ollchat/internal/agent"
)

// stuckTurn приводит модель в состояние «идёт генерация, и она зависла»:
// канал событий существует, но из него никто ничего не пришлёт. Ровно так
// выглядело приложение, простоявшее ночь на команде `dotnet run`.
func stuckTurn(m *Model) chan agent.Event {
	ch := make(chan agent.Event)
	m.events = ch
	m.streaming = true
	m.runGen++
	m.cancel = func() {}
	return ch
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestCtrlCTwiceQuitsFromStuckTurn — главный сценарий: из зависшего хода
// обязательно можно выйти двумя нажатиями Ctrl+C.
//
// Раньше при активной генерации Ctrl+C уходил в ветку прерывания и никогда
// не доходил до выхода: сколько ни нажимай, приложение оставалось на экране,
// и закрыть его можно было только вместе с терминалом.
func TestCtrlCTwiceQuitsFromStuckTurn(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)

	_, cmd := m.Update(pressCtrl('c'))
	if isQuit(cmd) {
		t.Fatal("первое нажатие не должно закрывать приложение без предупреждения")
	}
	if m.streaming {
		t.Error("после первого Ctrl+C генерация должна считаться прерванной")
	}
	if !strings.Contains(m.statusMsg, "ещё раз") {
		t.Errorf("пользователю нужно сказать, что делать дальше: %q", m.statusMsg)
	}

	_, cmd = m.Update(pressCtrl('c'))
	if !isQuit(cmd) {
		t.Fatal("второе нажатие Ctrl+C обязано закрывать приложение")
	}
}

// Даже если ход почему-то остался помеченным как активный, выход должен работать.
func TestCtrlCQuitsEvenIfTurnStaysActive(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)

	m.Update(pressCtrl('c'))
	m.streaming = true // имитируем, что состояние не сбросилось

	_, cmd := m.Update(pressCtrl('c'))
	if !isQuit(cmd) {
		t.Error("выход не должен зависеть от состояния генерации")
	}
}

// TestEscReleasesStuckTurn — Esc обязан вернуть управление сразу, не дожидаясь,
// пока зависший инструмент соизволит ответить.
func TestEscReleasesStuckTurn(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)

	m.Update(pressKey(tea.KeyEscape))

	if m.streaming {
		t.Fatal("после Esc интерфейс должен быть свободен")
	}
	if m.events != nil {
		t.Error("канал прерванного хода нужно отцепить")
	}

	// И сразу можно набирать дальше: ввод больше не отвечает «дождитесь ответа».
	before := len(m.blocks)
	m.ta.SetValue("/help")
	m.Update(pressKey(tea.KeyEnter))

	if strings.Contains(m.statusMsg, "дождитесь") {
		t.Errorf("интерфейс всё ещё считает, что идёт генерация: %q", m.statusMsg)
	}
	if len(m.blocks) == before {
		t.Error("после прерывания команды должны выполняться — справка не появилась")
	}
	if m.ta.Value() != "" {
		t.Error("поле ввода должно очищаться после отправки")
	}
}

// TestStaleEventsIgnored — события прерванного хода не должны портить экран,
// даже если брошенная горутина всё-таки что-то прислала.
func TestStaleEventsIgnored(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)
	staleGen := m.runGen

	m.Update(pressKey(tea.KeyEscape))
	blocksAfterEsc := len(m.blocks)

	// Запоздалые события брошенного хода.
	m.Update(agentEventMsg{gen: staleGen, ev: agent.Event{
		Kind: agent.EventContent, Text: "текст из прерванного хода"}})
	m.Update(streamClosedMsg{gen: staleGen})

	if len(m.blocks) != blocksAfterEsc {
		t.Errorf("лента изменилась от событий прерванного хода: было %d, стало %d",
			blocksAfterEsc, len(m.blocks))
	}
	if m.streaming {
		t.Error("запоздалое событие не должно возвращать состояние генерации")
	}
	for _, b := range m.blocks {
		if strings.Contains(b.text, "прерванного хода") {
			t.Error("текст прерванного хода попал на экран")
		}
	}
}

// TestEscClosesPendingConfirmation — если прерывание случилось на запросе
// подтверждения, панель должна закрыться вместе с ходом.
func TestEscClosesPendingConfirmation(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)
	m.confirm = &agent.ConfirmRequest{
		Tool:  "bash",
		Title: "bash(dotnet run)",
		Reply: make(chan agent.Answer, 1),
	}

	// При открытой панели Esc означает «отклонить», поэтому прерываем Ctrl+C.
	m.Update(pressCtrl('c'))

	if m.confirm != nil {
		t.Error("панель подтверждения должна закрываться вместе с ходом")
	}
	if m.streaming {
		t.Error("после прерывания генерация должна считаться завершённой")
	}
}
