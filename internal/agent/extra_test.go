package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Имена из подсказок модели (clients, doctors) не попадают ни в журнал шагов,
// ни в события интерфейса (окно подтверждения, журнал диалога): туда идёт
// Plan.LogArgs. До 03.10.2026 steps-*.jsonl хранил их открытым текстом.
// Имена выдуманы.
func TestLogArgsHideNamesFromJournals(t *testing.T) {
	checkNamesHidden(t, `"path":"scan.pdf","clients":"Adaline Quorrow","doctors":"Bertrand Vexholm"`)
}

// То же — когда вызов отклонён ещё на разборе аргументов (Plan вернул ошибку).
// До 03.10.2026 на этой ветке в событие и в журнал шагов уходили сырые
// аргументы модели: LogArgs подставлялся только после удачного Plan.
func TestLogArgsHideNamesWhenPlanFails(t *testing.T) {
	checkNamesHidden(t, `"path":"scan.pdf","formats":"docx","clients":"Adaline Quorrow","doctors":"Bertrand Vexholm"`)
}

func checkNamesHidden(t *testing.T, callArgs string) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if atomic.AddInt32(&calls, 1) == 1 {
			fmt.Fprint(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"scan_redact",`+
				`"arguments":{`+callArgs+`}}}]},"done":false}`+"\n")
			fmt.Fprint(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
			return
		}
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"готово"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	defer srv.Close()

	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "scan.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile([]string{"Read(./**)", "Write(./**)"}, nil, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameScanRedact}, tools.Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{
		Client: ollama.New(srv.URL, 0, 0, nil), Model: "test", Tools: reg,
		Guard:         permissions.NewGuard(set, sb, permissions.ModeSafe),
		MaxIterations: 5, ToolsSupported: true,
		Steps: steplog.New(t.TempDir(), pat, time.Now(), "test", true),
	}
	defer r.Steps.Close()

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "обезличь скан"})
	events := 0
	for ev := range r.Run(context.Background(), conv) {
		if ev.Kind == EventToolConfirm {
			ev.Confirm.Reply <- AnswerYes
		}
		if ev.Tool != nil {
			events++
			for _, name := range []string{"Adaline", "Vexholm"} {
				if strings.Contains(ev.Tool.Args, name) || strings.Contains(ev.Tool.Title, name) {
					t.Errorf("имя %q в событии интерфейса: %q", name, ev.Tool.Args)
				}
			}
		}
	}
	if events == 0 {
		t.Fatal("событий инструмента не было — проверять нечего")
	}
	data, err := os.ReadFile(r.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Adaline", "Vexholm"} {
		if strings.Contains(string(data), name) {
			t.Errorf("имя %q в журнале шагов: %s", name, data)
		}
	}
	if !strings.Contains(string(data), `\"clients\":1`) {
		t.Errorf("в журнале нет числа подсказок: %s", data)
	}
}

// Запрет на любую цель действия запрещает всё действие (Plan.Extra).
//
// scan_redact читает один файл и пишет другие; главная цель плана — запись,
// и она разрешена. Если бы проверялась только она, запрет чтения каталога
// с тайной обходился бы: инструмент прочитал бы скан и выложил его текст
// рядом. Действие обязано отклоняться без вопроса человеку и без записи.
func TestExtraTargetDenyStopsWholeAction(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if atomic.AddInt32(&calls, 1) == 1 {
			fmt.Fprint(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"scan_redact",`+
				`"arguments":{"path":"secret/scan.pdf","out_pdf":"public/scan.pdf","out_md":"public/scan.md"}}}]},"done":false}`+"\n")
			fmt.Fprint(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
			return
		}
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"готово"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	defer srv.Close()

	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret", "scan.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile([]string{"Write(./**)"}, nil, []string{"Read(./secret/**)"}, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameScanRedact}, tools.Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{
		Client: ollama.New(srv.URL, 0, 0, nil), Model: "test", Tools: reg,
		Guard:         permissions.NewGuard(set, sb, permissions.ModeSafe),
		MaxIterations: 5, ToolsSupported: true,
	}

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "обезличь скан"})
	skipped := false
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			t.Error("запрещённое действие не должно спрашивать подтверждения")
			ev.Confirm.Reply <- AnswerNo
		case EventToolResult:
			if ev.Tool.OK {
				t.Error("запрещённое действие выполнилось")
			}
			if ev.Tool.Skipped {
				skipped = true
			}
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}
	if !skipped {
		t.Error("действие должно отмечаться как пропущенное")
	}
	for _, p := range []string{"public/scan.pdf", "public/scan.md"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("записан %s, хотя чтение исходника запрещено", p)
		}
	}
}
