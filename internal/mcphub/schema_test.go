package mcphub

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
)

// applyList принимает список инструментов, как после чтения у сервера, и
// отдаёт отчёт /mcp tools по нему.
func applyList(t *testing.T, list ...mcpclient.Tool) (*Hub, *server, string) {
	t.Helper()
	h := New(testConfig(t, "http://mcp.example/mcp", nil), "test")
	s := h.servers["wiki"]
	h.mu.Lock()
	h.apply(s, list)
	s.state = stateUp
	h.mu.Unlock()
	rep, err := h.ToolsReport("wiki")
	if err != nil {
		t.Fatal(err)
	}
	return h, s, rep
}

func schemaTool(name, schema string) mcpclient.Tool {
	return mcpclient.Tool{Name: name, Description: "инструмент " + name, InputSchema: json.RawMessage(schema)}
}

func toolNames(s *server) string {
	var names []string
	for _, e := range s.tools {
		names = append(names, e.remote)
	}
	return strings.Join(names, ",")
}

// Схема, которую Ollama не разберёт в свои типы, отбрасывается с причиной:
// иначе отказ получал бы весь /api/chat — ломались бы все обмены, а не один
// инструмент. Остальные инструменты сервера живы.
func TestApplyDropsSchemaOllamaRejects(t *testing.T) {
	_, s, rep := applyList(t,
		schemaTool("good", `{"type":"object","properties":{"q":{"type":"string","description":"вопрос"}}}`),
		schemaTool("badreq", `{"type":"object","properties":{"q":{"type":"string","required":true}}}`),
		schemaTool("baddesc", `{"type":"object","properties":{"q":{"type":"string","description":{"text":"x"}}}}`),
		schemaTool("also", `{"type":"object","properties":{"description":{"type":"string"}}}`),
	)
	if got := toolNames(s); got != "good,also" {
		t.Errorf("приняты инструменты %q, ждали good,also", got)
	}
	for _, want := range []string{"badreq — схема параметров не ложится в разбор Ollama", "baddesc — схема параметров не ложится в разбор Ollama"} {
		if !strings.Contains(rep, want) {
			t.Errorf("в /mcp tools нет причины %q:\n%s", want, rep)
		}
	}
}

// Описания параметров внутри схемы — без управляющих и невидимых знаков, тем
// же правилом, что описание инструмента; порядок ключей и данные (enum,
// default) — как прислал сервер, свойство по имени default чистится как все.
func TestApplyCleansNestedDescriptions(t *testing.T) {
	raw := `{"type":"object","properties":{` +
		`"q":{"type":"string","description":"вопрос\u202e\u200b\u001b[31m\nвторая строка","enum":["a\u200b"]},` +
		`"list":{"type":"array","items":{"type":"object","properties":{"x":{"description":"x\u2066y"}}}},` +
		`"default":{"anyOf":[{"type":"string","description":"\u200dпервый"}],"default":{"description":"d\u200b"}}},` +
		`"required":["q"]}`
	_, s, _ := applyList(t, schemaTool("find", raw))
	if len(s.tools) != 1 {
		t.Fatalf("инструмент не принят: %v", s.skipped)
	}
	got := string(s.tools[0].params.Raw)
	for _, want := range []string{
		`"description":"вопрос[31m вторая строка"`, `"description":"xy"`, `"description":"первый"`,
		`"enum":["a\u200b"]`, `"default":{"description":"d\u200b"}`,
	} {
		w := want
		if strings.Contains(want, `\u200b`) {
			w = strings.ReplaceAll(want, `\u200b`, "\u200b")
		}
		if !strings.Contains(got, w) {
			t.Errorf("в схеме нет %s:\n%s", want, got)
		}
	}
	// Порядок ключей — как у сервера.
	if i, j := strings.Index(got, `"q":`), strings.Index(got, `"list":`); i < 0 || j < i {
		t.Errorf("порядок свойств переставлен:\n%s", got)
	}
	// Вне данных невидимых знаков не осталось.
	var v map[string]any
	if err := json.Unmarshal(s.tools[0].params.Raw, &v); err != nil {
		t.Fatal(err)
	}
	props := v["properties"].(map[string]any)
	for _, d := range []string{
		props["q"].(map[string]any)["description"].(string),
		props["default"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)["description"].(string),
	} {
		for _, r := range d {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Errorf("в описании остался знак %U: %q", r, d)
			}
		}
	}
	// Чистить нечего — схема та же, байт в байт.
	plain := `{"type":"object","properties":{"z":{"type":"string","description":"<b> & </b>"},"a":{"type":"integer"}}}`
	_, s2, _ := applyList(t, schemaTool("plain", plain))
	if len(s2.tools) != 1 || string(s2.tools[0].params.Raw) != plain {
		t.Errorf("чистая схема изменилась: %v %v", s2.tools, s2.skipped)
	}
}

// Схемы одного сервера вместе — не больше maxServerSchemas: что не влезло,
// отброшено с причиной, а не ушло в каждый запрос к модели.
func TestApplyServerSchemaBudget(t *testing.T) {
	big := func(name string) mcpclient.Tool {
		pad := strings.Repeat("п", (maxSchemaBytes-200)/2) // кириллица — два байта на знак
		return schemaTool(name, `{"type":"object","properties":{"q":{"type":"string","description":"`+pad+`"}}}`)
	}
	_, s, rep := applyList(t, big("one"), big("two"), big("three"),
		schemaTool("small", `{"type":"object","properties":{}}`))
	if got := toolNames(s); got != "one,two,small" {
		t.Errorf("приняты %q, ждали one,two,small", got)
	}
	total := 0
	for _, e := range s.tools {
		total += len(e.params.Raw)
	}
	if total > maxServerSchemas {
		t.Errorf("схем принято %d байт > %d", total, maxServerSchemas)
	}
	if !strings.Contains(rep, "three — схемы инструментов сервера вместе больше") {
		t.Errorf("в /mcp tools нет причины для three:\n%s", rep)
	}
}
