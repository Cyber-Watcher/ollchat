package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// fakeChat — сценарий событий вместо сервера Ollama: каждый вызов Chat отдаёт
// следующий список событий. Так цикл агента проверяется без HTTP (этап 91, R5.2).
type fakeChat struct {
	turns [][]ollama.Event
	calls int
	seen  []ollama.ChatRequest
}

func (f *fakeChat) Chat(_ context.Context, req ollama.ChatRequest) <-chan ollama.Event {
	f.seen = append(f.seen, req)
	var evs []ollama.Event
	if f.calls < len(f.turns) {
		evs = f.turns[f.calls]
	}
	f.calls++
	ch := make(chan ollama.Event, len(evs)+1)
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	return ch
}

func answer(text string) []ollama.Event {
	return []ollama.Event{
		{Kind: ollama.EventContent, Text: text},
		{Kind: ollama.EventDone, Stats: ollama.Stats{PromptEvalCount: 10, EvalCount: 3, DoneReason: "stop"}},
	}
}

func bashCall(cmd string) []ollama.Event {
	return []ollama.Event{
		{Kind: ollama.EventToolCalls, ToolCalls: []ollama.ToolCall{{
			ID:       "call_" + cmd,
			Function: ollama.ToolCallFunc{Name: tools.NameBash, Arguments: map[string]any{"command": cmd}},
		}}},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "stop"}},
	}
}

func runAll(t *testing.T, r *Runner, reply Answer) (text string, results []*ToolEvent, stats *ToolStats, err error) {
	t.Helper()
	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})
	var sb strings.Builder
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventContent:
			sb.WriteString(ev.Text)
		case EventToolConfirm:
			ev.Confirm.Reply <- reply
		case EventToolResult:
			results = append(results, ev.Tool)
		case EventTurnDone:
			stats = ev.Tools
		case EventError:
			err = ev.Err
		}
	}
	return sb.String(), results, stats, err
}

func TestFakePlainAnswer(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{answer("готово")}}
	r := plainRunner(f, 0)
	text, _, stats, err := runAll(t, r, AnswerNo)
	if err != nil || text != "готово" {
		t.Fatalf("ответ %q, ошибка %v", text, err)
	}
	if stats == nil || stats.Calls != 0 {
		t.Fatalf("счёт вызовов при простом ответе: %+v", stats)
	}
}

// Вызов инструмента: результат уходит модели сообщением role=tool, ответ
// после него — окончательный.
func TestFakeToolCallRoundTrip(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{bashCall("echo привет"), answer("сделано")}}
	r := fakeRunner(t, f)
	text, results, stats, err := runAll(t, r, AnswerYes)
	if err != nil || text != "сделано" {
		t.Fatalf("ответ %q, ошибка %v", text, err)
	}
	if len(results) != 1 || !results[0].OK || !strings.Contains(results[0].Output, "привет") {
		t.Fatalf("результат инструмента: %+v", results)
	}
	if stats.Calls != 1 || stats.Rejected != 0 {
		t.Fatalf("счёт: %+v", *stats)
	}
	// Второй запрос к модели несёт ответ инструмента.
	last := f.seen[1].Messages[len(f.seen[1].Messages)-1]
	if last.Role != ollama.RoleTool || !strings.Contains(last.Content, "привет") {
		t.Fatalf("в истории нет ответа инструмента: %+v", last)
	}
}

// Правило deny: вопрос не задаётся, вызов считается отклонённым, модель
// получает объяснение и продолжает.
func TestFakeDeniedCall(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{bashCall("rm -rf /"), answer("не стал")}}
	r := fakeRunner(t, f)
	_, results, stats, err := runAll(t, r, AnswerYes)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Skipped {
		t.Fatalf("запрещённый вызов должен быть пропущен: %+v", results)
	}
	if stats.Rejected != 1 {
		t.Fatalf("счёт: %+v", *stats)
	}
}

// Ответ «нет» на подтверждение: команда не выполняется, модели уходит отказ.
func TestFakeUserRejects(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{bashCall("echo нет"), answer("понял")}}
	r := fakeRunner(t, f)
	_, results, stats, err := runAll(t, r, AnswerNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].OK || !results[0].Skipped {
		t.Fatalf("отклонённый вызов: %+v", results)
	}
	if stats.Rejected != 1 {
		t.Fatalf("счёт: %+v", *stats)
	}
	last := f.seen[1].Messages[len(f.seen[1].Messages)-1]
	if last.Role != ollama.RoleTool || !strings.Contains(last.Content, "отклонил") {
		t.Fatalf("модель не узнала об отказе: %+v", last)
	}
}

// Повторяемый сбой до первого куска — повтор; неповторяемый — ошибка сразу.
func TestFakeRetryPolicy(t *testing.T) {
	boom := ollama.MarkRetryable(errors.New("сервер сорвал генерацию"))
	f := &fakeChat{turns: [][]ollama.Event{{{Kind: ollama.EventError, Err: boom}}, answer("со второй")}}
	text, _, _, err := runAll(t, plainRunner(f, 1), AnswerNo)
	if err != nil || text != "со второй" {
		t.Fatalf("повтор не сработал: %q, %v", text, err)
	}
	if f.calls != 2 {
		t.Fatalf("запросов %d, ожидалось 2", f.calls)
	}

	plain := &fakeChat{turns: [][]ollama.Event{{{Kind: ollama.EventError, Err: errors.New("400 плохой запрос")}}, answer("нет")}}
	if _, _, _, err := runAll(t, plainRunner(plain, 3), AnswerNo); err == nil || plain.calls != 1 {
		t.Fatalf("неповторяемая ошибка должна отдаваться сразу: err=%v, запросов %d", err, plain.calls)
	}

	// Кусок текста уже показан — повторять нельзя, даже если сбой повторяемый.
	shown := &fakeChat{turns: [][]ollama.Event{{{Kind: ollama.EventContent, Text: "нача"}, {Kind: ollama.EventError, Err: boom}}, answer("нет")}}
	if _, _, _, err := runAll(t, plainRunner(shown, 3), AnswerNo); err == nil || shown.calls != 1 {
		t.Fatalf("после показанного текста повтора быть не должно: err=%v, запросов %d", err, shown.calls)
	}
}

// Поток без завершающего чанка — тот же повтор.
func TestFakeIncompleteStreamRetried(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{{}, answer("дошло")}}
	text, _, _, err := runAll(t, plainRunner(f, 1), AnswerNo)
	if err != nil || text != "дошло" || f.calls != 2 {
		t.Fatalf("незавершённый поток: %q, %v, запросов %d", text, err, f.calls)
	}
}

// Предел итераций: модель просит инструмент без конца — ход завершается ошибкой
// с подсказкой, как поднять предел.
func TestFakeIterationLimit(t *testing.T) {
	turns := make([][]ollama.Event, 0, 10)
	for i := 0; i < 10; i++ {
		turns = append(turns, bashCall("echo x"))
	}
	f := &fakeChat{turns: turns}
	r := fakeRunner(t, f)
	r.MaxIterations = 3
	_, _, _, err := runAll(t, r, AnswerYes)
	if err == nil || !strings.Contains(err.Error(), "max_iterations") {
		t.Fatalf("ожидалась ошибка предела итераций: %v", err)
	}
	if f.calls != 3 {
		t.Fatalf("запросов %d, ожидалось 3", f.calls)
	}
}
