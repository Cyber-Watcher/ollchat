package mcp

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Этап 112, Ц1 (а): что служба отвечает клиентам разных ревизий MCP, и (б) что
// рукопожатие ложится в журнал шагов — запрошенная версия, отданная, клиент.
// Сегодня ответ один на всех — «2024-11-05», в том числе на несуществующую
// версию; тест это фиксирует, чтобы смена поведения была замечена.
func TestInitializeVersionsLogged(t *testing.T) {
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s := server(probe())
	s.Steps = steplog.New(t.TempDir(), pat, time.Now(), "ollmcp", true)
	versions := []string{"2024-11-05", "2025-06-18", "2025-11-25", "2026-07-28", "1999-01-01"}
	for _, v := range versions {
		d := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+v+
			`","capabilities":{},"clientInfo":{"name":"проба","version":"7"}}}`)
		got := d["result"].(map[string]any)["protocolVersion"]
		if got != protocolVersion {
			t.Errorf("на %s служба ответила %v, ждали %s", v, got, protocolVersion)
		}
	}
	s.Steps.Close()
	data, err := os.ReadFile(s.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	var inits []steplog.Step
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		st, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка %q: %v", line, err)
		}
		if st.Kind == steplog.KindInit {
			inits = append(inits, st)
		}
	}
	if len(inits) != len(versions) {
		t.Fatalf("записей рукопожатия %d, ждали %d", len(inits), len(versions))
	}
	for i, st := range inits {
		if st.Extra["requested"] != versions[i] || st.Extra["answered"] != protocolVersion ||
			st.Extra["client"] != "проба" || st.Extra["client_version"] != "7" {
			t.Errorf("запись %d: %+v", i, st.Extra)
		}
	}
}
