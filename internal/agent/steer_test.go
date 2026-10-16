package agent

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Этап 112, Х1: реплика, набранная посреди хода, ложится в историю после
// результатов инструментов — следующий запрос к модели кончается цепочкой
// assistant{tool_calls} → tool → user, и вызов не остаётся без ответа.
func TestSteerInjectedAfterToolResults(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{bashCall("echo привет"), answer("учёл")}}
	r := fakeRunner(t, f)
	r.Steer = &SteerQueue{}
	r.Steer.Push("и проверь заодно права на файл")

	text, _, _, err := runAll(t, r, AnswerYes)
	if err != nil || text != "учёл" {
		t.Fatalf("ответ %q, ошибка %v", text, err)
	}
	if len(f.seen) != 2 {
		t.Fatalf("запросов к модели %d, ждали 2", len(f.seen))
	}
	first := f.seen[0].Messages
	if last := first[len(first)-1]; last.Content == "и проверь заодно права на файл" {
		t.Error("реплика ушла в первый запрос — до результатов инструментов")
	}
	msgs := f.seen[1].Messages
	n := len(msgs)
	if n < 3 || len(msgs[n-3].ToolCalls) == 0 || msgs[n-2].Role != ollama.RoleTool ||
		msgs[n-1].Role != ollama.RoleUser || msgs[n-1].Content != "и проверь заодно права на файл" {
		t.Fatalf("хвост второго запроса не assistant{tool_calls} → tool → user: %+v", msgs[max(0, n-3):])
	}
	if r.Steer.Len() != 0 {
		t.Errorf("в очереди осталось %d — реплика не забрана", r.Steer.Len())
	}
}

// Ход без инструментов кончается сам — реплику забирать некуда: она остаётся
// в очереди, и интерфейс отправит её следующим вопросом (follow-up).
func TestSteerLeftForFollowUpWhenNoTools(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{answer("готово")}}
	r := plainRunner(f, 0)
	r.Steer = &SteerQueue{}
	r.Steer.Push("а ещё?")
	if _, _, _, err := runAll(t, r, AnswerNo); err != nil {
		t.Fatal(err)
	}
	if got := r.Steer.Drain(); len(got) != 1 || got[0] != "а ещё?" {
		t.Errorf("в очереди %v, ждали реплику для follow-up", got)
	}
}
