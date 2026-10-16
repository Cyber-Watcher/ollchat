package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Этап 112, Х4: /retry убирает последний обмен целиком — вопрос, вызовы
// инструментов и их ответы — и задаёт тот же вопрос заново. В истории не
// остаётся «сирот» assistant{tool_calls} без ответов.
func TestRetryDropsLastExchangeAndResends(t *testing.T) {
	m := newTestModel(t)
	m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "первый"})
	m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ на первый"})

	if cmd := m.retryCmd(""); cmd != nil || !strings.Contains(lastBlockText(t, m), "повторять нечего") {
		t.Fatalf("без вопроса в этом сеансе /retry должен отказать: %q", lastBlockText(t, m))
	}

	m.startExchange("второй", nil, mixResult{})
	m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{{}}})
	m.conv.Append(ollama.Message{Role: ollama.RoleTool, Content: "вывод"})
	m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ на второй"})

	if cmd := m.retryCmd(""); cmd != nil || !strings.Contains(lastBlockText(t, m), "идёт ход") {
		t.Fatalf("посреди хода /retry должен отказать: %q", lastBlockText(t, m))
	}
	m.finishTurn()

	m.retryCmd("")
	msgs := m.conv.Messages()
	if len(msgs) != 3 {
		t.Fatalf("после /retry сообщений %d, ждали 3 (первый, ответ, второй заново): %+v", len(msgs), msgs)
	}
	if msgs[2].Role != ollama.RoleUser || msgs[2].Content != "второй" {
		t.Errorf("вопрос не задан заново: %+v", msgs[2])
	}
	for _, msg := range msgs {
		if msg.Role == ollama.RoleTool || len(msg.ToolCalls) > 0 {
			t.Errorf("в истории остался след отменённого обмена: %+v", msg)
		}
	}
	m.finishTurn()
}

// /compact сдвигает номера сообщений — /retry после него откатил бы не то.
func TestRetryRefusedAfterCompact(t *testing.T) {
	m := newTestModel(t)
	fillHistory(m, 10)
	m.startExchange("вопрос", nil, mixResult{})
	m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ"})
	m.finishTurn()
	m.compactCmd("2")
	before := m.conv.Len()
	m.retryCmd("")
	if m.conv.Len() != before || !strings.Contains(lastBlockText(t, m), "переписана") {
		t.Errorf("после /compact /retry не отказал: сообщений %d → %d, %q", before, m.conv.Len(), lastBlockText(t, m))
	}
}

// Этап 112, Х3: сеанс сохраняется сам после каждого хода — один файл на сеанс,
// переписанный целиком; без ответа модели файла нет; /clear начинает новый файл.
func TestAutosaveAfterEachTurn(t *testing.T) {
	m := newTestModel(t)
	m.finishTurn()
	if list, _ := m.store.List(); len(list) != 0 {
		t.Fatalf("без ответа модели заведён файл сессии: %d", len(list))
	}

	for i, q := range []string{"раз", "два", "три"} {
		m.startExchange(q, nil, mixResult{})
		m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ " + q})
		runCmd(t, m, m.finishTurn())
		list, err := m.store.List()
		if err != nil || len(list) != 1 {
			t.Fatalf("ход %d: сессий на диске %d (%v), ждали 1", i+1, len(list), err)
		}
		if got := len(list[0].Messages); got != 2*(i+1) {
			t.Errorf("ход %d: в файле сообщений %d, ждали %d", i+1, got, 2*(i+1))
		}
	}

	m.clearCmd("")
	m.startExchange("после очистки", nil, mixResult{})
	m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ"})
	runCmd(t, m, m.finishTurn())
	if list, _ := m.store.List(); len(list) != 2 {
		t.Errorf("после /clear сессий %d, ждали 2: прежний диалог должен остаться своим файлом", len(list))
	}

	off := newTestModel(t)
	off.cfg.Agent.Autosave = false
	off.startExchange("вопрос", nil, mixResult{})
	off.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ"})
	if cmd := off.finishTurn(); cmd != nil {
		runCmd(t, off, cmd)
	}
	if list, _ := off.store.List(); len(list) != 0 {
		t.Errorf("agent.autosave = false, а сессий на диске %d", len(list))
	}
}
