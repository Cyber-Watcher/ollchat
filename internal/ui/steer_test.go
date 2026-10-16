package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Этап 112, Х1: Enter посреди хода ставит реплику в очередь, а не просит
// подождать; Esc обрывает ход и возвращает очередь в поле ввода.
func TestEnterMidTurnQueuesAndEscRestores(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)

	m.ta.SetValue("и проверь права")
	m.Update(pressKey(tea.KeyEnter))
	if m.steer.Len() != 1 || m.ta.Value() != "" {
		t.Fatalf("реплика не в очереди: в очереди %d, в поле %q", m.steer.Len(), m.ta.Value())
	}
	if !strings.Contains(lastBlockText(t, m), "в очереди") {
		t.Errorf("человеку не сказано, что реплика ждёт: %q", lastBlockText(t, m))
	}

	m.ta.SetValue("/help")
	m.Update(pressKey(tea.KeyEnter))
	if m.steer.Len() != 1 || !strings.Contains(m.statusMsg, "дождитесь") {
		t.Errorf("команда посреди хода ушла в очередь: в очереди %d, строка %q", m.steer.Len(), m.statusMsg)
	}

	m.Update(pressKey(tea.KeyEscape))
	if m.streaming || m.steer.Len() != 0 {
		t.Fatalf("после Esc: ход %v, в очереди %d", m.streaming, m.steer.Len())
	}
	if got := m.ta.Value(); got != "и проверь права\n/help" {
		t.Errorf("в поле после Esc %q, ждали реплику из очереди и набранное", got)
	}
}

// Реплика, которую ход не забрал (модель ответила без новых вызовов), уходит
// следующим вопросом — follow-up; забранная (EventSteer) встаёт в ленту вопросом.
func TestSteerFollowUpAfterTurnDone(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)
	m.steerQueue().Push("а дальше?")

	m.handleAgentEvent(agent.Event{Kind: agent.EventSteer, Text: "взята посреди хода"})
	if b := m.blocks[len(m.blocks)-1]; b.kind != blockUser || b.text != "взята посреди хода" {
		t.Errorf("забранная реплика не в ленте вопросом: %+v", b)
	}

	if cmd := m.handleAgentEvent(agent.Event{Kind: agent.EventTurnDone}); cmd == nil {
		t.Fatal("follow-up не отправлен")
	}
	msgs := m.conv.Messages()
	if last := msgs[len(msgs)-1]; last.Role != ollama.RoleUser || last.Content != "а дальше?" {
		t.Errorf("последнее сообщение истории %+v, ждали вопрос «а дальше?»", last)
	}
	if m.steer.Len() != 0 {
		t.Errorf("очередь не опустела: %d", m.steer.Len())
	}
	m.finishTurn()
}

// Ход упал — реплика не уходит сама, а возвращается в поле: человек решит.
func TestSteerRestoredOnError(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)
	m.steerQueue().Push("уточнение")
	m.handleAgentEvent(agent.Event{Kind: agent.EventError, Err: errors.New("сервер упал")})
	if m.ta.Value() != "уточнение" || m.steer.Len() != 0 {
		t.Errorf("после ошибки хода: в поле %q, в очереди %d", m.ta.Value(), m.steer.Len())
	}
}
