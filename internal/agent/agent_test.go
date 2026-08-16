package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// fakeServer изображает Ollama: на первые len(toolCmds) запросов отвечает
// вызовом инструмента bash, затем — обычным текстом.
func fakeServer(t *testing.T, toolCmds []string) (*ollama.Client, *int32) {
	t.Helper()
	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&calls, 1)) - 1
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)

		if n < len(toolCmds) {
			_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{
				Role: ollama.RoleAssistant,
				ToolCalls: []ollama.ToolCall{{
					ID: "call_" + toolCmds[n],
					Function: ollama.ToolCallFunc{
						Name:      tools.NameBash,
						Arguments: map[string]any{"command": toolCmds[n]},
					},
				}},
			}})
		} else {
			_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{
				Role: ollama.RoleAssistant, Content: "готово",
			}})
		}
		_ = enc.Encode(ollama.ChatResponse{
			Done: true, DoneReason: "stop", PromptEvalCount: 10, EvalCount: 5,
		})
	}))
	t.Cleanup(srv.Close)

	return ollama.New(srv.URL, 10*time.Second, nil), &calls
}

func fakeRunner(t *testing.T, client *ollama.Client) *Runner {
	t.Helper()
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)

	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	set, err := permissions.Compile(nil, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, sb.Root())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameBash}, tools.Options{
		Sandbox: sb, BashTimeout: 10 * time.Second, MaxOutputKB: 64,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return &Runner{
		Client: client, Model: "test", Tools: reg,
		Guard:         permissions.NewGuard(set, sb, permissions.ModeSafe),
		MaxIterations: 10, ToolsSupported: true,
	}
}

// TestAnswerAlwaysToolStopsAsking — ответ «разрешить весь инструмент» должен
// снимать вопросы по всем последующим вызовам этого инструмента, а не только
// по повторению того же самого действия.
func TestAnswerAlwaysToolStopsAsking(t *testing.T) {
	client, _ := fakeServer(t, []string{"echo раз", "echo два", "echo три"})
	r := fakeRunner(t, client)

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "выполни три команды"})

	asked := 0
	executed := 0
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			asked++
			ev.Confirm.Reply <- AnswerAlwaysTool
		case EventToolResult:
			if ev.Tool.OK {
				executed++
			}
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if asked != 1 {
		t.Errorf("подтверждение должно запрашиваться один раз, запрошено %d", asked)
	}
	if executed != 3 {
		t.Errorf("должны выполниться все три команды, выполнено %d", executed)
	}
}

// TestAnswerAlwaysAsksForDifferentCommand — узкое разрешение «это действие»
// не должно распространяться на другую команду того же инструмента.
func TestAnswerAlwaysAsksForDifferentCommand(t *testing.T) {
	client, _ := fakeServer(t, []string{"echo раз", "true", "echo два"})
	r := fakeRunner(t, client)

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "выполни команды"})

	asked := 0
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			asked++
			ev.Confirm.Reply <- AnswerAlways
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	// echo разрешается после первого ответа, true — отдельная команда: ещё вопрос.
	if asked != 2 {
		t.Errorf("ожидалось 2 запроса подтверждения (echo и true), получено %d", asked)
	}
}

// TestDeniedToolCallNeverAsks — запрещённая команда не должна доходить
// до пользователя и не должна выполняться.
func TestDeniedToolCallNeverAsks(t *testing.T) {
	client, _ := fakeServer(t, []string{"rm -rf /"})
	r := fakeRunner(t, client)

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "удали всё"})

	skipped := false
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			t.Error("запрещённая команда не должна запрашивать подтверждение")
			ev.Confirm.Reply <- AnswerNo
		case EventToolResult:
			if ev.Tool.Skipped {
				skipped = true
			}
			if ev.Tool.OK {
				t.Error("запрещённая команда не должна выполняться")
			}
		}
	}
	if !skipped {
		t.Error("запрещённая команда должна отмечаться как пропущенная")
	}
}

// TestMaxIterationsGuard — цикл не должен уходить в бесконечность,
// если модель всё время просит инструменты.
func TestMaxIterationsGuard(t *testing.T) {
	cmds := make([]string, 50)
	for i := range cmds {
		cmds[i] = "true"
	}
	client, calls := fakeServer(t, cmds)
	r := fakeRunner(t, client)
	r.MaxIterations = 4

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "работай"})

	var gotErr error
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			ev.Confirm.Reply <- AnswerAlwaysTool
		case EventError:
			gotErr = ev.Err
		}
	}

	if gotErr == nil {
		t.Fatal("превышение max_iterations должно давать ошибку")
	}
	if n := atomic.LoadInt32(calls); n != 4 {
		t.Errorf("к модели должно быть ровно 4 обращения, было %d", n)
	}
}
