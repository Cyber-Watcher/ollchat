package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"github.com/Cyber-Watcher/ollchat/internal/mcphub"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// /mcp (этап 121, В7) против настоящего сервера internal/mcp: состояние до
// подключения, переподключение, инструменты сервера, их число в /tools.
func TestMCPCommand(t *testing.T) {
	const token = "ключ-wiki-77"
	mux := http.NewServeMux()
	mcp.MountHTTP(mux, mcp.NewServer(nil, mcp.Tool{
		Spec: ollama.ToolSpec{Name: "echo", Description: "повторяет текст",
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{"text": {Type: "string"}}}},
		Run: func(_ context.Context, a map[string]any) (string, error) { return fmt.Sprint(a["text"]), nil },
	}), token, false)
	ts := httptest.NewServer(kbserve.Protect(mux, true))
	t.Cleanup(ts.Close)
	t.Setenv("OLLCHAT_TEST_UI_WIKI", token)

	m := newTestModelWith(t, func(c *config.Config) {
		c.MCP.Servers = map[string]config.MCPServer{"wiki": {URL: ts.URL + "/mcp",
			TokenEnv: "OLLCHAT_TEST_UI_WIKI", Allow: []string{"echo"}}}
	})
	hub := mcphub.New(m.cfg, "test")
	if err := hub.Attach(m.registry); err != nil {
		t.Fatal(err)
	}
	m.SetMCP(hub)

	last := func() block { return m.blocks[len(m.blocks)-1] }
	m.runCommand("/mcp")
	if b := last(); b.kind != blockNotice || !strings.Contains(b.text, "wiki") || !strings.Contains(b.text, "не подключался") {
		t.Fatalf("/mcp до подключения: %+v", b)
	}
	cmd := m.runCommand("/mcp reconnect wiki")
	if cmd == nil {
		t.Fatal("/mcp reconnect не вернул работы")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok || !strings.Contains(msg.text, "подключён") || strings.Contains(msg.text, token) {
		t.Fatalf("/mcp reconnect: %+v", msg)
	}
	if !m.registry.Has("mcp__wiki__echo") {
		t.Fatalf("инструмент сервера не попал в реестр: %v", m.registry.ExternalNames())
	}
	m.runCommand("/mcp tools wiki")
	if b := last(); !strings.Contains(b.text, "mcp__wiki__echo") || !strings.Contains(b.text, "Без вопроса (allow, 1)") {
		t.Errorf("/mcp tools: %q", b.text)
	}
	m.runCommand("/mcp tools нет")
	if b := last(); b.kind != blockError {
		t.Errorf("неизвестный сервер: %+v", b)
	}
	m.runCommand("/mcp что-то")
	if b := last(); b.kind != blockError || !strings.Contains(b.text, "reconnect") {
		t.Errorf("неизвестная подкоманда: %+v", b)
	}
	m.runCommand("/tools")
	if b := last(); !strings.Contains(b.text, "Инструментов сторонних серверов MCP: 1") || strings.Contains(b.text, "mcp__wiki__echo") {
		t.Errorf("/tools: внешние числом, без имён:\n%s", b.text)
	}
	if !strings.Contains(commandsHelp(m.sections()), "/mcp") {
		t.Error("/mcp нет в справке")
	}
}

// Сервер отказывает в рукопожатии текстом с ESC, OSC и C1 (U+009B — CSI одним
// знаком): /mcp reconnect кладёт в ленту причину, но ни одного управляющего
// знака — иначе сервер красил бы и стирал терминал человека (аудит 23.10.2026).
func TestMCPReconnectErrorWithoutControls(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if len(m.ID) == 0 {
			m.ID = json.RawMessage("null")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"\u001b]0;evil\u0007\u001b[2J стёр экран \u009b31m красный"}}`, m.ID)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("OLLCHAT_TEST_UI_WIKI", "ключ")
	m := newTestModelWith(t, func(c *config.Config) {
		c.MCP.Servers = map[string]config.MCPServer{"wiki": {URL: ts.URL + "/mcp", TokenEnv: "OLLCHAT_TEST_UI_WIKI"}}
	})
	hub := mcphub.New(m.cfg, "test")
	if err := hub.Attach(m.registry); err != nil {
		t.Fatal(err)
	}
	m.SetMCP(hub)
	cmd := m.runCommand("/mcp reconnect wiki")
	if cmd == nil {
		t.Fatal("/mcp reconnect не вернул работы")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("/mcp reconnect: %T", msg)
	}
	for _, r := range msg.text {
		if r != '\n' && unicode.IsControl(r) {
			t.Fatalf("в ленту ушёл управляющий знак %U: %q", r, msg.text)
		}
	}
	if !strings.Contains(msg.text, "стёр экран") {
		t.Errorf("причина отказа пропала: %q", msg.text)
	}
}

// Без серверов — внятный ответ, а не паника.
func TestMCPCommandWithoutHub(t *testing.T) {
	m := newTestModel(t)
	m.runCommand("/mcp")
	if b := m.blocks[len(m.blocks)-1]; !strings.Contains(b.text, "не подключены") {
		t.Errorf("/mcp без серверов: %+v", b)
	}
}
