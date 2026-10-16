package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// second — ещё один инструмент, чтобы было что оставить включённым.
func second() Tool {
	t := probe()
	t.Spec.Name = "вторая"
	return t
}

// Этап 112, Ц6: аварийный выключатель инструмента — disabled_tools в ollmcp.toml.
// Выключенного нет в tools/list, а вызов по старому списку отвечает отказом
// с исходом denied в журнале: выключатель держит вызов, а не только список.
func TestDisabledToolHiddenAndRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ollmcp.toml")
	if err := os.WriteFile(path, []byte(`disabled_tools = ["проба"]`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := steplog.New(dir, pat, time.Now(), "test", true)
	s, err := NewService(nil, []Tool{probe(), second()}, ServiceOptions{Settings: path, Steps: w})
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, tl := range s.Tools() {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "вторая" {
		t.Errorf("в списке %v, ждали только «вторая»", names)
	}

	d := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"проба","arguments":{"что":"кот"}}}`)
	r, _ := d["result"].(map[string]any)
	if r == nil || r["isError"] != true || !strings.Contains(resultText(r), "выключен") {
		t.Errorf("вызов выключенного не отказан: %v", d)
	}
	d = call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"вторая","arguments":{"что":"кот"}}}`)
	if r, _ := d["result"].(map[string]any); r == nil || resultText(r) != "нашлось: кот" {
		t.Errorf("включённый сломался: %v", d)
	}

	w.Close()
	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var st steplog.Step
		if json.Unmarshal([]byte(line), &st) == nil && st.Kind == steplog.KindTool {
			outcomes[st.Tool] = st.Outcome
		}
	}
	if outcomes["проба"] != steplog.OutcomeDenied || outcomes["вторая"] != steplog.OutcomeOK {
		t.Errorf("исходы в журнале %v, ждали проба=denied, вторая=ok", outcomes)
	}
}

// Опечатка в имени не должна молча оставлять инструмент включённым: служба не
// поднимается, а правка на ходу отвергается проверкой (служба остаётся на прежних).
func TestDisabledToolUnknownNameRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ollmcp.toml")
	if err := os.WriteFile(path, []byte(`disabled_tools = ["пробаа"]`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(nil, []Tool{probe()}, ServiceOptions{Settings: path}); err == nil ||
		!strings.Contains(err.Error(), "пробаа") {
		t.Fatalf("неизвестное имя принято: %v", err)
	}

	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(nil, []Tool{probe()}, ServiceOptions{Settings: path})
	if err != nil {
		t.Fatal(err)
	}
	if s.Validate == nil {
		t.Fatal("проверки правки нет — за файлом не следят")
	}
	if err := os.WriteFile(path, []byte(`disabled_tools = ["пробаа"]`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err == nil {
		t.Error("правка с опечаткой прошла проверку")
	}
	if err := os.WriteFile(path, []byte(`disabled_tools = ["проба"]`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("верная правка отвергнута: %v", err)
	}
}

// Отпечаток набора меняется с выключением — по нему служба шлёт клиенту
// list_changed (этап 109), и клиент перечитывает список без перезапуска сеанса.
func TestDisabledToolChangesFingerprint(t *testing.T) {
	on := NewServer(nil, probe(), second())
	off := NewServer(nil, probe(), second())
	off.Policy.Disabled = map[string]bool{"проба": true}
	if on.Fingerprint() == off.Fingerprint() {
		t.Error("отпечаток набора не сменился — клиент не узнает о выключении")
	}
}
