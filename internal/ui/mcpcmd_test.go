package ui

import (
	"context"
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

// Без серверов — внятный ответ, а не паника.
func TestMCPCommandWithoutHub(t *testing.T) {
	m := newTestModel(t)
	m.runCommand("/mcp")
	if b := m.blocks[len(m.blocks)-1]; !strings.Contains(b.text, "не подключены") {
		t.Errorf("/mcp без серверов: %+v", b)
	}
}
