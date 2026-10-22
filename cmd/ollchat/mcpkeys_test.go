package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"github.com/Cyber-Watcher/ollchat/internal/mcphub"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Ключи --mcp-* и --kb-remote-check (этап 121, А5, В1–В5) против настоящего
// сервера internal/mcp на петлевом httptest.

const keysToken = "ключ-ключей-3a9d"

func keysServer(t *testing.T) *httptest.Server {
	t.Helper()
	tool := func(name string, run func(map[string]any) (string, error)) mcp.Tool {
		return mcp.Tool{Spec: ollama.ToolSpec{Name: name, Description: "инструмент " + name,
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{"text": {Type: "string"}}}},
			Run: func(_ context.Context, a map[string]any) (string, error) { return run(a) }}
	}
	echo := func(a map[string]any) (string, error) { return fmt.Sprintf("эхо: %v", a["text"]), nil }
	mux := http.NewServeMux()
	mcp.MountHTTP(mux, mcp.NewServer(nil, tool("echo", echo), tool("other", echo), tool("denied", echo),
		tool("fail", func(map[string]any) (string, error) { return "", errors.New("сломан") })), keysToken, false)
	ts := httptest.NewServer(kbserve.Protect(mux, true))
	t.Cleanup(ts.Close)
	return ts
}

// keysSetup — конфиг с сервером wiki (allow — echo), охранник как у запуска:
// «спросить» по Fetch, запрет инструмента denied, allow сервера.
func keysSetup(t *testing.T, mode string) (*config.Config, *mcphub.Hub, *permissions.Guard) {
	t.Helper()
	ts := keysServer(t)
	t.Setenv("OLLCHAT_TEST_KEYS_TOKEN", keysToken)
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServer{"wiki": {URL: ts.URL + "/mcp",
		TokenEnv: "OLLCHAT_TEST_KEYS_TOKEN", Allow: []string{"echo"}}}
	hub := mcphub.New(cfg, "test")
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 64)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile(nil, []string{"Fetch(*)"}, []string{"Fetch(mcp://wiki/denied)"}, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range hub.AllowRules() {
		rule, err := permissions.ParseRule(r, sb.Root())
		if err != nil {
			t.Fatal(err)
		}
		set.AddAllow(rule)
	}
	return cfg, hub, permissions.NewGuard(set, sb, mode)
}

// --mcp-call идёт через охранник TUI: allow сервера — сразу; «спросить» —
// отказ без --mcp-yes (и в noask); deny — отказ и с --mcp-yes.
func TestMCPCallKey(t *testing.T) {
	cfg, hub, guard := keysSetup(t, permissions.ModeNoAsk)
	call := func(tool, args string, yes bool) (string, string, error) {
		var out, errOut bytes.Buffer
		a := []string{tool}
		if args != "" {
			a = append(a, args)
		}
		err := runMCPCall(context.Background(), hub, guard, cfg.Agent.PrivateEgressCheck, "wiki", a, yes, &out, &errOut)
		return out.String(), errOut.String(), err
	}
	out, _, err := call("echo", `{"text":"привет"}`, false)
	if err != nil || strings.TrimSpace(out) != "эхо: привет" {
		t.Fatalf("echo из allow: %q, %v", out, err)
	}
	if _, _, err := call("other", `{"text":"x"}`, false); err == nil || !strings.Contains(err.Error(), "--mcp-yes") {
		t.Errorf("other вне allow в noask без --mcp-yes: %v", err)
	}
	if out, _, err := call("other", `{"text":"x"}`, true); err != nil || !strings.Contains(out, "эхо: x") {
		t.Errorf("other с --mcp-yes: %q, %v", out, err)
	}
	if _, _, err := call("denied", "", true); err == nil || !strings.Contains(err.Error(), "запрещено") {
		t.Errorf("denied: %v", err)
	}
	if _, _, err := call("fail", "", true); err == nil {
		t.Error("ошибка инструмента не дала ошибки ключа")
	}
	if _, _, err := call("echo", `[1,2]`, false); err == nil || !strings.Contains(err.Error(), "объектом JSON") {
		t.Errorf("доводы не объект: %v", err)
	}
	var out2, errOut2 bytes.Buffer
	if err := runMCPCall(context.Background(), hub, guard, true, "wiki", nil, false, &out2, &errOut2); err == nil {
		t.Error("без инструмента — не отказ")
	}
}

// --mcp-call и частные адреса (agent.private_egress_check, Н16): та же
// проверка, что у модели, — запрет даже инструменту из allow и с --mcp-yes;
// сервер вызова не получает. Выключена настройкой — вызов идёт.
// Адреса — петля и link-local метаданных облака: частных данных в них нет.
func TestMCPCallKeyPrivateEgress(t *testing.T) {
	cfg, hub, guard := keysSetup(t, permissions.ModeNoAsk)
	if !cfg.Agent.PrivateEgressCheck {
		t.Fatal("подготовка: agent.private_egress_check по умолчанию выключен")
	}
	call := func(check bool, tool, args string, yes bool) (string, error) {
		var out, errOut bytes.Buffer
		err := runMCPCall(context.Background(), hub, guard, check, "wiki", []string{tool, args}, yes, &out, &errOut)
		return out.String(), err
	}
	for _, args := range []string{`{"text":"http://169.254.169.254/latest/meta-data"}`, `{"text":"открой localhost:8080"}`} {
		for _, tool := range []string{"echo", "other"} {
			out, err := call(true, tool, args, true)
			if err == nil || !strings.Contains(err.Error(), "запрещено") || !strings.Contains(err.Error(), "private_egress_check") {
				t.Errorf("%s %s: ждали запрет по private_egress_check, получили %v", tool, args, err)
			}
			if out != "" {
				t.Errorf("%s %s: вызов дошёл до сервера: %q", tool, args, out)
			}
		}
	}
	out, err := call(false, "echo", `{"text":"http://169.254.169.254/latest/meta-data"}`, false)
	if err != nil || !strings.Contains(out, "эхо: http://169.254.169.254") {
		t.Errorf("проверка выключена, а вызов не прошёл: %q, %v", out, err)
	}
}

// --mcp-servers, --mcp-ping, --mcp-tools — общие с /mcp отчёты; ключ доступа
// в вывод не попадает.
func TestMCPInfoKeys(t *testing.T) {
	cfg, hub, guard := keysSetup(t, permissions.ModeSafe)
	run := func(args ...string) (string, error) {
		f := newFlags(t, args...)
		var out, errOut bytes.Buffer
		err := runMCPKey(f, hub, guard, true, &out, &errOut)
		if strings.Contains(out.String()+errOut.String(), keysToken) {
			t.Errorf("%v: ключ доступа в выводе", args)
		}
		return out.String(), err
	}
	out, err := run("--mcp-servers")
	if err != nil || !strings.Contains(out, "wiki") || !strings.Contains(out, cfg.MCP.Servers["wiki"].URL) ||
		!strings.Contains(out, "allow: 1") {
		t.Errorf("--mcp-servers: %q, %v", out, err)
	}
	out, err = run("--mcp-ping", "wiki")
	if err != nil || !strings.Contains(out, "рукопожатие за") || !strings.Contains(out, "ревизия") {
		t.Errorf("--mcp-ping: %q, %v", out, err)
	}
	if _, err := run("--mcp-ping", "нет"); err == nil {
		t.Error("--mcp-ping неизвестного сервера — не ошибка")
	}
	out, err = run("--mcp-tools", "wiki")
	if err != nil || !strings.Contains(out, "mcp__wiki__echo") || !strings.Contains(out, "Без вопроса (allow, 1)") {
		t.Errorf("--mcp-tools: %q, %v", out, err)
	}
}

// --mcp-registry — весь набор модели строкой JSON: встроенные и внешние,
// имена не пересекаются, описания внешних помечены.
func TestMCPRegistryKey(t *testing.T) {
	_, hub, _ := keysSetup(t, permissions.ModeSafe)
	reg, err := tools.NewRegistry([]string{tools.NameWebSearch}, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.Attach(reg); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runMCPRegistry(hub, reg, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
		t.Errorf("не одна строка: %q", out.String())
	}
	var specs []ollama.Tool
	if err := json.Unmarshal(out.Bytes(), &specs); err != nil {
		t.Fatalf("не JSON: %v\n%s", err, out.String())
	}
	seen := map[string]bool{}
	for _, s := range specs {
		if seen[s.Function.Name] {
			t.Errorf("имя %s дважды", s.Function.Name)
		}
		seen[s.Function.Name] = true
		if tools.IsExternalName(s.Function.Name) && !strings.HasPrefix(s.Function.Description, "[внешний сервер wiki] ") {
			t.Errorf("описание %s не помечено: %q", s.Function.Name, s.Function.Description)
		}
	}
	if !seen[tools.NameWebSearch] || !seen["mcp__wiki__echo"] || !seen["mcp__wiki__fail"] {
		t.Errorf("набор: %v", seen)
	}
}

// checkService — общая библиотека для --kb-remote-check: книги и карта —
// подставные (askChainService), MCP — настоящий сервер на kb.server_url.
type checkService struct {
	*askService
	tool string
	fail bool
}

func (s *checkService) Names() ([]string, error) {
	if s.fail {
		return nil, errAskServiceDown
	}
	return s.askService.Names()
}

func (s *checkService) GraphTool(_ context.Context, _, name string, _ map[string]any) (string, error) {
	s.tool = name
	return "ответ " + name, nil
}

// --kb-remote-check: семь шагов со временем; отказ шага — ошибка ключа, но
// остальные шаги идут; ключ доступа в выводе не появляется.
func TestKBRemoteCheck(t *testing.T) {
	ts := keysServer(t)
	t.Setenv("OLLCHAT_TEST_KEYS_KB", keysToken)
	cfg := config.Default()
	cfg.KB.ServerURL, cfg.KB.ServerTokenEnv, cfg.KB.Default = ts.URL, "OLLCHAT_TEST_KEYS_KB", "books"
	hub := mcphub.New(cfg, "test")
	svc := &checkService{askService: askChainService(t)}

	var out bytes.Buffer
	if err := runKBRemoteCheck(cfg, svc, hub, []string{"горутина", "канал"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"коллекции", "книги", "поиск", "карта для подмешивания", "инструмент графа",
		"MCP: рукопожатие", "MCP: tools/list", "все 7 шагов прошли", " мс"} {
		if !strings.Contains(text, want) {
			t.Errorf("в выводе нет %q:\n%s", want, text)
		}
	}
	if svc.tool != tools.NameGraphEntity {
		t.Errorf("инструмент графа %q: понятие из карты не взято", svc.tool)
	}
	if strings.Contains(text, keysToken) {
		t.Error("ключ доступа в выводе")
	}

	svc.fail = true
	out.Reset()
	err := runKBRemoteCheck(cfg, svc, hub, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "отказов 1") || !strings.Contains(out.String(), "MCP: tools/list") {
		t.Errorf("отказ одного шага: %v\n%s", err, out.String())
	}
	if err := runKBRemoteCheck(config.Default(), nil, hub, nil, &out); err == nil {
		t.Error("без kb.server_url — не ошибка")
	}
}
