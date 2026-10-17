package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// journaled гоняет ход с журналом шагов и возвращает его записи, события
// уведомлений и ошибку хода.
func journaled(t *testing.T, r *Runner, reply Answer) (steps []steplog.Step, notices []string, err error) {
	t.Helper()
	pat, perr := chatlog.ParsePattern("steps.jsonl")
	if perr != nil {
		t.Fatal(perr)
	}
	r.Steps = steplog.New(t.TempDir(), pat, time.Now(), "test", true)
	r.Turn = "t1"
	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			ev.Confirm.Reply <- reply
		case EventNotice:
			notices = append(notices, ev.Text)
		case EventError:
			err = ev.Err
		}
	}
	r.Steps.Close()
	data, rerr := os.ReadFile(r.Steps.Path())
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		s, perr := steplog.Parse([]byte(line))
		if perr != nil {
			t.Fatalf("строка %q: %v", line, perr)
		}
		steps = append(steps, s)
	}
	return steps, notices, err
}

// ofKind — записи одного вида.
func ofKind(steps []steplog.Step, kind string) []steplog.Step {
	var out []steplog.Step
	for _, s := range steps {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// turnOf — единственная запись конца хода; две или ни одной — ошибка.
func turnOf(t *testing.T, steps []steplog.Step) steplog.Step {
	t.Helper()
	turns := ofKind(steps, steplog.KindTurn)
	if len(turns) != 1 {
		t.Fatalf("записей конца хода %d, ожидалась одна: %+v", len(turns), steps)
	}
	return turns[0]
}

// Конец хода пишется с причиной при каждом исходе: ответ, потолок шагов,
// сбой сервера (этап 112, Ф2). Без этого ход, упёршийся в потолок, отличался
// от прочих только числом шагов.
func TestTurnRecordReasons(t *testing.T) {
	t.Run("ответ", func(t *testing.T) {
		f := &fakeChat{turns: [][]ollama.Event{bashCall("echo x"), answer("готово")}}
		steps, _, err := journaled(t, fakeRunner(t, f), AnswerYes)
		if err != nil {
			t.Fatal(err)
		}
		turn := turnOf(t, steps)
		if turn.Outcome != steplog.TurnAnswer || turn.Turn != "t1" {
			t.Fatalf("конец хода: %+v", turn)
		}
		// Числа приходят из JSON, поэтому float64.
		if turn.Extra["steps"] != float64(3) || turn.Extra["tool_calls"] != float64(1) || turn.Extra["exchanges"] != float64(2) {
			t.Fatalf("счёт хода: %+v", turn.Extra)
		}
	})
	t.Run("потолок", func(t *testing.T) {
		turns := make([][]ollama.Event, 0, 5)
		for i := 0; i < 5; i++ {
			turns = append(turns, bashCall("echo x"))
		}
		r := fakeRunner(t, &fakeChat{turns: turns})
		r.MaxIterations = 2
		steps, _, err := journaled(t, r, AnswerYes)
		if err == nil {
			t.Fatal("ожидалась ошибка потолка")
		}
		if turn := turnOf(t, steps); turn.Outcome != steplog.TurnLimit {
			t.Fatalf("причина %q, ожидалась %q", turn.Outcome, steplog.TurnLimit)
		}
	})
	t.Run("сбой", func(t *testing.T) {
		f := &fakeChat{turns: [][]ollama.Event{{{Kind: ollama.EventError, Err: errors.New("400 плохой запрос")}}}}
		steps, _, err := journaled(t, plainRunner(f, 2), AnswerNo)
		if err == nil {
			t.Fatal("ожидалась ошибка")
		}
		if turn := turnOf(t, steps); turn.Outcome != steplog.TurnError {
			t.Fatalf("причина %q, ожидалась %q", turn.Outcome, steplog.TurnError)
		}
	})
}

// Ответ человека на подтверждение и время раздумья — в записи вызова (этап 112,
// Н14, О16): без них «пролистывание» подтверждений не измерить.
func TestToolStepRecordsConfirmation(t *testing.T) {
	for _, tc := range []struct {
		reply Answer
		want  string
	}{{AnswerYes, "yes"}, {AnswerNo, "no"}} {
		f := &fakeChat{turns: [][]ollama.Event{bashCall("echo x"), answer("готово")}}
		steps, _, err := journaled(t, fakeRunner(t, f), tc.reply)
		if err != nil {
			t.Fatal(err)
		}
		tools := ofKind(steps, steplog.KindTool)
		if len(tools) != 1 {
			t.Fatalf("записей вызова %d, ожидалась одна", len(tools))
		}
		if tools[0].Extra["confirm"] != tc.want {
			t.Fatalf("ответ человека в записи: %+v, ждали %q", tools[0].Extra, tc.want)
		}
		if ms, ok := tools[0].Extra["confirm_ms"].(float64); !ok || ms < 0 {
			t.Fatalf("время раздумья: %+v", tools[0].Extra)
		}
	}
}

// Вид и цель действия и решение правил до человека — в записи вызова (этап 112,
// И6): «разрешено правилом» и «одобрено человеком» больше не сливаются в один ok.
func TestToolStepRecordsDecision(t *testing.T) {
	for _, tc := range []struct {
		cmd, allow, decision, confirm, outcome string
	}{
		{"echo x", "", "ask", "yes", steplog.OutcomeOK},
		{"rm x", "", "deny", "", steplog.OutcomeDenied},
		{"echo x", "Bash(echo:*)", "allow", "", steplog.OutcomeOK},
	} {
		f := &fakeChat{turns: [][]ollama.Event{bashCall(tc.cmd), answer("готово")}}
		r := fakeRunner(t, f)
		if tc.allow != "" {
			sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
			if err != nil {
				t.Fatal(err)
			}
			set, err := permissions.Compile([]string{tc.allow}, nil, nil, sb.Root())
			if err != nil {
				t.Fatal(err)
			}
			r.Guard = permissions.NewGuard(set, sb, permissions.ModeSafe)
		}
		steps, _, err := journaled(t, r, AnswerYes)
		if err != nil {
			t.Fatal(err)
		}
		tools := ofKind(steps, steplog.KindTool)
		if len(tools) != 1 {
			t.Fatalf("%s: записей вызова %d, ожидалась одна", tc.cmd, len(tools))
		}
		x := tools[0].Extra
		if tools[0].Outcome != tc.outcome || x["decision"] != tc.decision || x["kind"] != "Bash" || x["target"] != tc.cmd {
			t.Errorf("%s (%s): исход %q, extra %+v", tc.cmd, tc.decision, tools[0].Outcome, x)
		}
		if got, _ := x["confirm"].(string); got != tc.confirm {
			t.Errorf("%s (%s): ответ человека %q, ждали %q", tc.cmd, tc.decision, got, tc.confirm)
		}
	}
}

// Что ушло модели — в записи вызова (этап 112, С2): размер всегда, начало — у
// запрета и ошибки. Успешный bash до 17.10.2026 тоже писал начало («Код
// возврата: 0» и вывод команды); по выбору владельца (находка 16, «В») вывод
// команды — чужое содержимое (`cat .env`), и у успеха пишется только размер.
func TestToolStepRecordsOutput(t *testing.T) {
	for _, tc := range []struct {
		cmd, allow, head string
	}{
		{"echo x", "Bash(echo:*)", ""},
		{"rm x", "", "Действие запрещено настройками"},
	} {
		f := &fakeChat{turns: [][]ollama.Event{bashCall(tc.cmd), answer("готово")}}
		r := fakeRunner(t, f)
		if tc.allow != "" {
			sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
			if err != nil {
				t.Fatal(err)
			}
			set, err := permissions.Compile([]string{tc.allow}, nil, nil, sb.Root())
			if err != nil {
				t.Fatal(err)
			}
			r.Guard = permissions.NewGuard(set, sb, permissions.ModeSafe)
		}
		steps, _, err := journaled(t, r, AnswerYes)
		if err != nil {
			t.Fatal(err)
		}
		tools := ofKind(steps, steplog.KindTool)
		if len(tools) != 1 {
			t.Fatalf("%s: записей вызова %d, ожидалась одна", tc.cmd, len(tools))
		}
		x := tools[0].Extra
		head, has := x["out_head"].(string)
		size, _ := x["out_bytes"].(float64) // числа приходят из JSON
		if tc.head == "" {
			if has || size == 0 {
				t.Errorf("%s: успешный bash — начало %q, размер %v; ждали только размер", tc.cmd, head, size)
			}
			continue
		}
		if !strings.HasPrefix(head, tc.head) || size < float64(len(tc.head)) {
			t.Errorf("%s: начало %q, размер %v; ждали начало %q", tc.cmd, head, size, tc.head)
		}
	}
}

// Находка 16 (аудит 16.10.2026): начало вывода успешного вызова пишется только
// у инструментов, чей вывод составил наш код; у read_file — только размер, хотя
// бы файл и прочитался. Runner.OutHead < 0 не пишет начала никогда, > 0 режет
// до стольких рун.
func TestToolStepOutHeadPolicy(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		outHead int
		head    string // "" — начала быть не должно
	}{
		{tools.NameReadFile, 0, ""},
		{tools.NameListDir, 0, "вывод list_dir"},
		{tools.NameListDir, 5, "выво…"}, // textx.Shorten: 5 рун вместе с «…»
		{tools.NameListDir, -1, ""},
	} {
		r, _ := gateRunner(t, &fakeChat{turns: [][]ollama.Event{
			callWith(tc.tool, map[string]any{"path": "a.txt"}, 10), answer("ок")}}, permissions.ModeNoAsk)
		r.OutHead = tc.outHead
		steps, _, err := journaled(t, r, AnswerYes)
		if err != nil {
			t.Fatal(err)
		}
		calls := ofKind(steps, steplog.KindTool)
		if len(calls) != 1 || calls[0].Outcome != steplog.OutcomeOK {
			t.Fatalf("%s: записей вызова %d — ждали один успешный", tc.tool, len(calls))
		}
		head, has := calls[0].Extra["out_head"].(string)
		if size, _ := calls[0].Extra["out_bytes"].(float64); size == 0 {
			t.Errorf("%s (%d): нет размера вывода", tc.tool, tc.outHead)
		}
		if tc.head == "" && has {
			t.Errorf("%s (%d): начало %q — ждали только размер", tc.tool, tc.outHead, head)
		}
		if tc.head != "" && head != tc.head {
			t.Errorf("%s (%d): начало %q — ждали %q", tc.tool, tc.outHead, head, tc.head)
		}
	}
}

// Запись обмена несёт время чтения промпта и загрузки модели отдельно от общего
// (этап 112, У10, Ц12): по ним делятся попадание в кеш префикса и холодный старт.
func TestChatStepTimings(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{{
		{Kind: ollama.EventContent, Text: "готово"},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "stop", PromptEvalCount: 900,
			PromptEvalDuration: 750e6, LoadDuration: 7800e6, TotalDuration: 9000e6}},
	}}}
	steps, _, err := journaled(t, plainRunner(f, 2), AnswerNo)
	if err != nil {
		t.Fatal(err)
	}
	chats := ofKind(steps, steplog.KindChat)
	if len(chats) != 1 {
		t.Fatalf("записей обмена %d, ожидалась одна", len(chats))
	}
	// Числа приходят из JSON, поэтому float64.
	if chats[0].Extra["prompt_eval_ms"] != float64(750) || chats[0].Extra["load_ms"] != float64(7800) {
		t.Fatalf("времена обмена: %+v", chats[0].Extra)
	}
	if chats[0].MS != 9000 {
		t.Fatalf("общее время %d, ожидалось 9000", chats[0].MS)
	}
}

// Каждая неудавшаяся попытка обмена — запись в журнале (этап 112, М5): до
// правки повтор уходил только в интерфейс, и обрывов не видел ни один подсчёт.
func TestRetryRecords(t *testing.T) {
	boom := ollama.MarkRetryable(errors.New("сервер сорвал генерацию"))
	fail := []ollama.Event{{Kind: ollama.EventError, Err: boom}}

	t.Run("со второй попытки", func(t *testing.T) {
		f := &fakeChat{turns: [][]ollama.Event{fail, fail, answer("дошло")}}
		steps, _, err := journaled(t, plainRunner(f, 2), AnswerNo)
		if err != nil {
			t.Fatal(err)
		}
		retries := ofKind(steps, steplog.KindRetry)
		if len(retries) != 2 {
			t.Fatalf("записей повтора %d, ожидалось 2: %+v", len(retries), steps)
		}
		for i, s := range retries {
			if s.Outcome != steplog.RetryAgain || s.Extra["attempt"] != float64(i+1) ||
				s.Extra["of"] != float64(3) || s.Extra["retryable"] != true || !strings.Contains(s.Note, "сорвал") {
				t.Fatalf("запись повтора %d: %+v", i, s)
			}
		}
		if turn := turnOf(t, steps); turn.Outcome != steplog.TurnAnswer {
			t.Fatalf("причина %q", turn.Outcome)
		}
	})
	t.Run("повторы кончились", func(t *testing.T) {
		f := &fakeChat{turns: [][]ollama.Event{fail, fail, fail}}
		steps, _, err := journaled(t, plainRunner(f, 1), AnswerNo)
		if err == nil {
			t.Fatal("ожидалась ошибка")
		}
		retries := ofKind(steps, steplog.KindRetry)
		if len(retries) != 2 || retries[0].Outcome != steplog.RetryAgain || retries[1].Outcome != steplog.RetryGaveUp {
			t.Fatalf("записи повтора: %+v", retries)
		}
		if retries[1].Extra["pause_ms"] != float64(0) {
			t.Fatalf("у последней попытки паузы нет: %+v", retries[1].Extra)
		}
		if turn := turnOf(t, steps); turn.Outcome != steplog.TurnError {
			t.Fatalf("причина %q", turn.Outcome)
		}
	})
}

// cutCall — вызов инструмента в ответе, оборванном пределом вывода.
func cutCall(cmd string) []ollama.Event {
	return []ollama.Event{
		{Kind: ollama.EventToolCalls, ToolCalls: []ollama.ToolCall{{
			ID:       "call_" + cmd,
			Function: ollama.ToolCallFunc{Name: tools.NameBash, Arguments: map[string]any{"command": cmd}},
		}}},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "length"}},
	}
}

// Вызов из оборванного ответа не исполняется: модель получает результат-пояснение
// и может повторить (этап 112, Х2).
func TestLengthCutToolCallNotExecuted(t *testing.T) {
	marker := t.TempDir() + "/ran"
	f := &fakeChat{turns: [][]ollama.Event{cutCall("touch " + marker), answer("понял")}}
	steps, notices, err := journaled(t, fakeRunner(t, f), AnswerYes)
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("вызов из оборванного ответа исполнен")
	}
	if f.calls != 2 {
		t.Fatalf("запросов %d, ожидалось 2: модель должна получить пояснение и ответить", f.calls)
	}
	last := f.seen[1].Messages[len(f.seen[1].Messages)-1]
	if last.Role != ollama.RoleTool || last.Content != lengthToolResult {
		t.Fatalf("модель не получила пояснения: %+v", last)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "оборван") {
		t.Fatalf("человеку не сказано об обрыве: %v", notices)
	}
	toolSteps := ofKind(steps, steplog.KindTool)
	if len(toolSteps) != 1 || toolSteps[0].Outcome != steplog.OutcomeInvalid {
		t.Fatalf("запись вызова: %+v", toolSteps)
	}
	if turn := turnOf(t, steps); turn.Outcome != steplog.TurnAnswer {
		t.Fatalf("причина %q", turn.Outcome)
	}
}

// Второй обрыв подряд заканчивает ход: предел вывода не даёт дописать вызов.
func TestLengthCutTwiceStops(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{cutCall("echo 1"), cutCall("echo 2"), answer("не дойдёт")}}
	steps, _, err := journaled(t, fakeRunner(t, f), AnswerYes)
	if err == nil || !strings.Contains(err.Error(), "num_predict") {
		t.Fatalf("ожидалась ошибка о пределе вывода: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("запросов %d, ожидалось 2", f.calls)
	}
	if turn := turnOf(t, steps); turn.Outcome != steplog.TurnLength {
		t.Fatalf("причина %q", turn.Outcome)
	}
}

// Оборванный ответ без вызовов — ответ остаётся, человеку сказано, причина
// хода — length.
func TestLengthCutAnswer(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{{
		{Kind: ollama.EventContent, Text: "начало ответа"},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "length"}},
	}}}
	steps, notices, err := journaled(t, plainRunner(f, 0), AnswerNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "num_predict") {
		t.Fatalf("уведомление: %v", notices)
	}
	if turn := turnOf(t, steps); turn.Outcome != steplog.TurnLength {
		t.Fatalf("причина %q", turn.Outcome)
	}
}
