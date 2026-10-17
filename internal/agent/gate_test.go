package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// callWith — один вызов инструмента с аргументами и счётчиком входных токенов.
func callWith(name string, args map[string]any, promptTokens int) []ollama.Event {
	return []ollama.Event{
		{Kind: ollama.EventToolCalls, ToolCalls: []ollama.ToolCall{{
			ID: "call_" + name, Function: ollama.ToolCallFunc{Name: name, Arguments: args},
		}}},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "stop", PromptEvalCount: promptTokens}},
	}
}

// gateRunner — агент с подставными инструментами (шов planTool), правилами,
// которые всё разрешают, и заданным режимом подтверждений.
func gateRunner(t *testing.T, f *fakeChat, mode string) (*Runner, *int) {
	t.Helper()
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile([]string{"Bash(*)", "Fetch(*)", "Write(./**)", "Read(./**)"}, nil, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameReadFile}, tools.Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	prev := planTool
	t.Cleanup(func() { planTool = prev })
	planTool = func(_ *tools.Registry, name string, args map[string]any) (*tools.Plan, error) {
		req := permissions.Request{Tool: name, Fixed: true}
		switch name {
		case tools.NameReadFile:
			req.Kind, req.Target = permissions.KindRead, filepath.Join(realRoot, "a.txt")
		case tools.NameWebSearch, tools.NameHTTPFetch:
			req.Kind, req.Target = permissions.KindFetch, "https://searx.example"
		case tools.NameWriteFile:
			req.Kind, req.Target = permissions.KindWrite, filepath.Join(realRoot, "a.txt")
		default:
			req.Kind, req.Target = permissions.KindBash, "echo"
		}
		return &tools.Plan{Tool: name, Req: req, Title: name + "()",
			Run: func(context.Context) (string, error) { ran++; return "вывод " + name, nil }}, nil
	}
	return &Runner{Client: f, Model: "test", Tools: reg, Guard: permissions.NewGuard(set, sb, mode),
		MaxIterations: 10, ToolsSupported: true}, &ran
}

// drive гоняет ход и возвращает заданные человеку вопросы, результаты и ошибку.
func drive(r *Runner, conv *session.Conversation, reply Answer) (asked []string, results []*ToolEvent, err error) {
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			asked = append(asked, ev.Confirm.Tool)
			ev.Confirm.Reply <- reply
		case EventToolResult:
			results = append(results, ev.Tool)
		case EventError:
			err = ev.Err
		}
	}
	return
}

func newConv() *session.Conversation {
	c := session.New("")
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})
	return c
}

// Т7, О15: после чужого текста (прочитанный файл) web_search в noask — с вопросом.
func TestGateUntrustedAsksAfterForeign(t *testing.T) {
	for _, on := range []bool{true, false} {
		f := &fakeChat{turns: [][]ollama.Event{
			callWith(tools.NameReadFile, map[string]any{"path": "a.txt"}, 10),
			callWith(tools.NameWebSearch, map[string]any{"query": "go 1.26"}, 10),
			answer("готово"),
		}}
		r, ran := gateRunner(t, f, permissions.ModeNoAsk)
		r.UntrustedGate = on
		asked, _, err := drive(r, newConv(), AnswerNo)
		if err != nil {
			t.Fatal(err)
		}
		if on && (len(asked) != 1 || asked[0] != tools.NameWebSearch || *ran != 1) {
			t.Fatalf("включено: вопросы %q, исполнено %d — ждали вопрос про web_search и 1 исполнение", asked, *ran)
		}
		if !on && (len(asked) != 0 || *ran != 2) {
			t.Fatalf("выключено: вопросы %q, исполнено %d — ждали 0 вопросов и 2 исполнения", asked, *ran)
		}
	}
	// Чужой текст из прошлых ходов тоже в силе: он остался в разговоре.
	conv := newConv()
	conv.Append(ollama.Message{Role: ollama.RoleTool, ToolName: tools.NameHTTPFetch, Content: "страница"})
	r, _ := gateRunner(t, &fakeChat{turns: [][]ollama.Event{
		callWith(tools.NameWebSearch, map[string]any{"query": "x"}, 10), answer("ок")}}, permissions.ModeNoAsk)
	r.UntrustedGate = true
	if asked, _, _ := drive(r, conv, AnswerNo); len(asked) != 1 {
		t.Fatalf("чужой текст прошлого хода не учтён: вопросы %q", asked)
	}
	if tainted([]ollama.Message{{Role: ollama.RoleTool, ToolName: tools.NameReadFile, Content: "Пользователь отклонил выполнение этого действия."}}) {
		t.Error("отклонённый вызов посчитан чужим текстом")
	}
}

// Аудит 16.10.2026, находка 14: метка чужого текста переживает /compact и
// сохранение с восстановлением сессии; снимает её только /clear. До правки
// метка пересчитывалась из истории, и после сжатия web_search шёл без вопроса.
func TestGateUntrustedSurvivesCompact(t *testing.T) {
	ask := func(conv *session.Conversation) []string {
		t.Helper()
		r, _ := gateRunner(t, &fakeChat{turns: [][]ollama.Event{
			callWith(tools.NameWebSearch, map[string]any{"query": "x"}, 10), answer("ок")}}, permissions.ModeNoAsk)
		r.UntrustedGate = true
		asked, _, err := drive(r, conv, AnswerNo)
		if err != nil {
			t.Fatal(err)
		}
		return asked
	}
	// Первый ход читает файл — чужой текст входит в разговор.
	conv := newConv()
	r, _ := gateRunner(t, &fakeChat{turns: [][]ollama.Event{
		callWith(tools.NameReadFile, map[string]any{"path": "a.txt"}, 10), answer("прочитал")}}, permissions.ModeNoAsk)
	r.UntrustedGate = true
	if _, _, err := drive(r, conv, AnswerNo); err != nil {
		t.Fatal(err)
	}
	if !conv.Foreign() {
		t.Fatal("после read_file разговор не помечен чужим текстом")
	}
	// /compact: всё, кроме последнего сообщения, уходит в сводку — ответа
	// инструмента в истории больше нет.
	if n := conv.CompactWith(1, "читали файл a.txt"); n == 0 {
		t.Fatal("сжатие ничего не сжало — тест не проверяет того, что заявлено")
	}
	if tainted(conv.Messages()) {
		t.Fatal("после сжатия ответ инструмента остался в истории — тест не проверяет того, что заявлено")
	}
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "поищи"})
	if asked := ask(conv); len(asked) != 1 || asked[0] != tools.NameWebSearch {
		t.Fatalf("после /compact: вопросы %q — ждали вопрос про web_search", asked)
	}
	// Сохранение и восстановление сжатой сессии: метка едет в файле.
	store := session.NewStore(t.TempDir())
	_, path, err := store.SaveAs("", conv, "srv", "m")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Load(strings.TrimSuffix(filepath.Base(path), ".json"))
	if err != nil {
		t.Fatal(err)
	}
	restored := session.New("")
	restored.Restore(rec)
	if !restored.Foreign() {
		t.Fatal("метка чужого текста потерялась при сохранении и восстановлении")
	}
	// /clear снимает метку: разговор пуст, чужого текста в нём нет.
	conv.Clear()
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "поищи"})
	if asked := ask(conv); len(asked) != 0 {
		t.Fatalf("после /clear: вопросы %q — ждали 0", asked)
	}
}

// Н16: частный адрес или имя хоста из ~/.ssh/config в исходящем вызове — отказ.
// Адреса частных сетей ниже ВЫДУМАНЫ (разрешение владельца 15.10.2026, строки
// в .claude/skills/never-clobber-files/privacy-allow.tsv), имена хостов — тоже.
func TestGatePrivateEgressDenied(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfg, []byte("Host stand-alpha\n  HostName stand-alpha.example\nHost *\n  User nobody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prevPath := sshConfigPath
	t.Cleanup(func() { sshConfigPath = prevPath; sshOnce = sync.Once{}; sshNames = nil })
	sshConfigPath = func() string { return cfg }
	sshOnce, sshNames = sync.Once{}, nil

	cases := map[string]bool{
		"статус 10.0.0.1":             true,
		"http://192.168.0.1/x":        true,
		"сервер 172.16.0.1 молчит":    true,
		"проверь stand-alpha.example": true,
		"go 1.26 release":             false,
		"alpha version":               false,
		"172.40.0.5 и 198.51.100.10":  false,
	}
	for q, deny := range cases {
		f := &fakeChat{turns: [][]ollama.Event{callWith(tools.NameWebSearch, map[string]any{"query": q}, 10), answer("ок")}}
		r, ran := gateRunner(t, f, permissions.ModeNoAsk)
		r.PrivateEgressCheck = true
		_, results, err := drive(r, newConv(), AnswerYes)
		if err != nil {
			t.Fatal(err)
		}
		if deny && (*ran != 0 || len(results) != 1 || !results[0].Skipped || !strings.Contains(results[0].Reason, "private_egress_check")) {
			t.Errorf("%q: ждали отказ, исполнено %d, результаты %+v", q, *ran, results)
		}
		if !deny && *ran != 1 {
			t.Errorf("%q: ждали исполнение, исполнено %d", q, *ran)
		}
	}
}

// Н6: журнал шагов не пишется — bash не исполняется.
func TestGateJournalFailClosed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Каталог журнала — внутри обычного файла: запись невозможна.
	steps := steplog.New(filepath.Join(file, "sub"), pat, time.Now(), "test", true)
	steps.Write(steplog.Step{Kind: steplog.KindSession})
	if steps.LastError() == nil {
		t.Fatal("журнал в невозможном каталоге не дал ошибки — проверять нечего")
	}
	for _, on := range []bool{true, false} {
		f := &fakeChat{turns: [][]ollama.Event{callWith("bash", map[string]any{"command": "echo x"}, 10), answer("ок")}}
		r, ran := gateRunner(t, f, permissions.ModeNoAsk)
		r.Steps, r.JournalFailClosed = steps, on
		_, results, _ := drive(r, newConv(), AnswerYes)
		if on && (*ran != 0 || len(results) != 1 || !strings.Contains(results[0].Reason, "journal_fail_closed")) {
			t.Fatalf("включено: исполнено %d, результаты %+v", *ran, results)
		}
		if !on && *ran != 1 {
			t.Fatalf("выключено: исполнено %d, ждали 1", *ran)
		}
	}
}

// Ч3: в noask после предела правок за ход следующая — с вопросом.
func TestGateMaxWritesNoask(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{
		callWith(tools.NameWriteFile, map[string]any{"path": "a.txt"}, 10),
		callWith(tools.NameWriteFile, map[string]any{"path": "a.txt"}, 10),
		callWith(tools.NameWriteFile, map[string]any{"path": "a.txt"}, 10),
		answer("ок"),
	}}
	r, ran := gateRunner(t, f, permissions.ModeNoAsk)
	r.MaxWritesNoask = 2
	asked, _, err := drive(r, newConv(), AnswerNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || *ran != 2 {
		t.Fatalf("вопросы %q, исполнено %d — ждали 1 вопрос на третью правку и 2 исполнения", asked, *ran)
	}
}

// Т11: потолок входных токенов за ход — ход останавливается с отчётом.
func TestTurnTokenCap(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{
		callWith("bash", map[string]any{"command": "echo 1"}, 600),
		callWith("bash", map[string]any{"command": "echo 2"}, 600),
		answer("ок"),
	}}
	r, ran := gateRunner(t, f, permissions.ModeNoAsk)
	r.MaxTurnTokens = 1000
	_, _, err := drive(r, newConv(), AnswerYes)
	if err == nil || !strings.Contains(err.Error(), "max_turn_tokens") || !strings.Contains(err.Error(), "Сделано за ход: вызовов 1") {
		t.Fatalf("ждали остановку по потолку с отчётом: %v", err)
	}
	if *ran != 1 {
		t.Fatalf("исполнено %d, ждали 1: второй вызов после потолка не исполняется", *ran)
	}
}

// В yolo не спрашивается ничего: проверка «чужой текст» молчит (слово владельца
// 16.10.2026); в noask она по-прежнему спрашивает — это проверяет тест выше.
func TestUntrustedGateSilentInYolo(t *testing.T) {
	f := &fakeChat{turns: [][]ollama.Event{
		callWith(tools.NameReadFile, map[string]any{"path": "a.txt"}, 10),
		callWith(tools.NameWebSearch, map[string]any{"query": "go 1.26"}, 10),
		answer("готово"),
	}}
	r, ran := gateRunner(t, f, permissions.ModeYolo)
	r.UntrustedGate = true
	asked, _, err := drive(r, newConv(), AnswerNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 || *ran != 2 {
		t.Fatalf("yolo: вопросы %q, исполнено %d — ждали 0 вопросов и 2 исполнения", asked, *ran)
	}
}
