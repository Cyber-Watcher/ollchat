package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// Ответ и ошибка стороннего сервера в --mcp-call — чужой текст для терминала
// (аудит 23.10.2026): ESC-последовательности (CSI, OSC — заголовок окна),
// C1 и возврат каретки печатаются видимо, перевод строки и табуляция — как есть.
func TestMCPCallOutputEscaped(t *testing.T) {
	const tok = "ключ-эскейпов-5c1e"
	esc := "\x1b]0;PWNED\x07\x1b[2J\u009b31m\rстрока\tстолбец\nвторая"
	mux := http.NewServeMux()
	mcp.MountHTTP(mux, mcp.NewServer(nil, mcp.Tool{
		Spec: ollama.ToolSpec{Name: "esc", Description: "e", Parameters: ollama.ToolParams{Type: "object"}},
		Run:  func(context.Context, map[string]any) (string, error) { return esc, nil },
	}, mcp.Tool{
		Spec: ollama.ToolSpec{Name: "boom", Description: "b", Parameters: ollama.ToolParams{Type: "object"}},
		Run:  func(context.Context, map[string]any) (string, error) { return "", errors.New(esc) },
	}), tok, false)
	ts := httptest.NewServer(kbserve.Protect(mux, true))
	defer ts.Close()
	t.Setenv("OLLCHAT_TEST_ESC_TOKEN", tok)
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServer{"wiki": {URL: ts.URL + "/mcp", TokenEnv: "OLLCHAT_TEST_ESC_TOKEN",
		Allow: []string{"esc", "boom"}}}
	hub := mcphub.New(cfg, "test")
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 64)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile(nil, []string{"Fetch(*)"}, nil, sb.Root())
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
	guard := permissions.NewGuard(set, sb, permissions.ModeNoAsk)
	raw := func(s string) bool { return strings.ContainsAny(s, "\x1b\x07\r\u009b") }

	var out, errOut bytes.Buffer
	if err := runMCPKey(newFlags(t, "--mcp-call", "wiki", "esc"), hub, guard, false, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if raw(out.String()) || raw(errOut.String()) {
		t.Errorf("управляющие знаки в выводе: %q / %q", out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), `\x1b]0;PWNED`) || !strings.Contains(out.String(), "строка\tстолбец\nвторая") {
		t.Errorf("текст сервера искажён сверх нужного: %q", out.String())
	}
	out.Reset()
	err = runMCPKey(newFlags(t, "--mcp-call", "wiki", "boom"), hub, guard, false, &out, &errOut)
	if err == nil || raw(err.Error()) || raw(out.String()) {
		t.Errorf("ошибка сервера с управляющими знаками: %q", fmt.Sprint(err))
	}
}

// Ключи --mcp-* и шаги MCP в --kb-remote-check ждут ключ доступа
// (token_cmd) и подключение не дольше срока (аудит 23.10.2026): с
// context.Background() зависшая команда держала ключ бесконечно.
func TestMCPKeysBoundedTokenCmd(t *testing.T) {
	old := mcpKeyTimeout
	mcpKeyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { mcpKeyTimeout = old })
	ts := keysServer(t)
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServer{"wiki": {URL: ts.URL + "/mcp", TokenCmd: "exec sleep 5"}}
	cfg.KB.ServerURL, cfg.KB.ServerTokenCmd, cfg.KB.Default = ts.URL, "exec sleep 5", "books"
	hub := mcphub.New(cfg, "test")
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 64)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile(nil, []string{"Fetch(*)"}, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	guard := permissions.NewGuard(set, sb, permissions.ModeNoAsk)
	for _, args := range [][]string{{"--mcp-ping", "wiki"}, {"--mcp-tools", "wiki"}, {"--mcp-call", "wiki", "echo"}} {
		start := time.Now()
		var out, errOut bytes.Buffer
		err := runMCPKey(newFlags(t, args...), hub, guard, false, &out, &errOut)
		if took := time.Since(start); err == nil || took > 3*time.Second {
			t.Errorf("%q: %v за %s — срок на ключ доступа не действует", args, err, took)
		}
	}
	start := time.Now()
	var out bytes.Buffer
	svc := &checkService{askService: askChainService(t)}
	if err := runKBRemoteCheck(cfg, svc, hub, nil, &out); err == nil {
		t.Errorf("шаги MCP без ключа прошли:\n%s", out.String())
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("--kb-remote-check шёл %s — срок на kb.server_token_cmd не действует:\n%s", took, out.String())
	}
}

// Служба библиотеки при занятом имени kb (аудит 23.10.2026, находка 9):
// --kb-remote-check не находит её среди серверов и отказывает на шагах MCP,
// а не подключает вместо неё ВСЕ сторонние серверы (Reconnect с пустым
// именем). Конфиг из файла такое уже не пропускает (config.TestLibraryNameReserved);
// здесь — конфиг, собранный кодом.
func TestKBRemoteCheckLibraryNameTaken(t *testing.T) {
	ts := keysServer(t)
	t.Setenv("OLLCHAT_TEST_KEYS_KB", keysToken)
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "не сюда", http.StatusInternalServerError)
	}))
	defer other.Close()
	cfg := config.Default()
	cfg.KB.ServerURL, cfg.KB.ServerTokenEnv, cfg.KB.Default = ts.URL, "OLLCHAT_TEST_KEYS_KB", "books"
	cfg.MCP.Servers = map[string]config.MCPServer{
		"kb":   {URL: other.URL + "/kb/mcp"},
		"wiki": {URL: other.URL + "/mcp"},
	}
	hub := mcphub.New(cfg, "test")
	svc := &checkService{askService: askChainService(t)}
	var out bytes.Buffer
	err := runKBRemoteCheck(cfg, svc, hub, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "отказов 2") {
		t.Errorf("итог: %v\n%s", err, out.String())
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("при занятом имени kb ключ ходил к сторонним серверам: запросов %d\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "[mcp.servers.kb]") {
		t.Errorf("причина отказа не названа:\n%s", out.String())
	}
}

// closeHub на выходе заканчивает сеанс подключённого сервера (DELETE) и не
// ждёт дольше своего срока.
func TestCloseHubEndsSessions(t *testing.T) {
	var deletes atomic.Int32
	target, err := url.Parse(keysServer(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	t.Setenv("OLLCHAT_TEST_CLOSE_TOKEN", keysToken)
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServer{"wiki": {URL: front.URL + "/mcp", TokenEnv: "OLLCHAT_TEST_CLOSE_TOKEN"}}
	hub := mcphub.New(cfg, "test")
	if err := hub.Reconnect(context.Background(), "wiki"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	closeHub(hub)
	if took := time.Since(start); took > hubCloseTimeout+time.Second {
		t.Errorf("closeHub шёл %s", took)
	}
	if n := deletes.Load(); n != 1 {
		t.Errorf("DELETE сеанса: %d, ждали 1", n)
	}
}

// Имя службы библиотеки у хаба и у проверки конфига — одно: иначе конфиг
// резервировал бы одно имя, а ключи искали бы службу под другим.
func TestLibraryNameAgrees(t *testing.T) {
	if mcphub.LibraryServer != config.LibraryMCPName {
		t.Errorf("mcphub.LibraryServer = %q, config.LibraryMCPName = %q", mcphub.LibraryServer, config.LibraryMCPName)
	}
}

// Службу библиотеки среди [mcp.servers] хаб и проверка имени kb в конфиге
// узнают одинаково: иначе конфиг отверг бы раздел kb, который хаб счёл бы
// службой, или пропустил бы тот, что спрячет службу от ключей.
func TestLibraryEndpointAgrees(t *testing.T) {
	for _, lib := range []string{"http://mcp.example:8377", "http://mcp.example", "https://mcp.example"} {
		for _, u := range []string{
			lib + "/mcp", lib + "/mcp/", strings.ToUpper(lib) + "/mcp", lib + "/mcp?x=1",
			"http://mcp.example/mcp", "http://mcp.example:80/mcp", "https://mcp.example:443/mcp",
			"https://mcp.example:8377/mcp", lib, lib + "/other", "http://198.51.100.10:8377/mcp",
		} {
			cfg := config.Default()
			cfg.KB.ServerURL = lib
			cfg.MCP.Servers = map[string]config.MCPServer{config.LibraryMCPName: {URL: u}}
			hubSays := mcphub.New(cfg, "test").LibraryName() == config.LibraryMCPName
			if cfgSays := config.SameEndpoint(u, cfg.KB.LibraryMCPURL()); hubSays != cfgSays {
				t.Errorf("kb.server_url %s, url %s: хаб считает службой — %v, конфиг — %v", lib, u, hubSays, cfgSays)
			}
		}
	}
	// Порт по умолчанию — тот же вход, что пропущенный.
	if !config.SameEndpoint("http://mcp.example:80/mcp", "http://mcp.example/mcp/") {
		t.Error("http://mcp.example:80/mcp и http://mcp.example/mcp — разные входы")
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
