package oai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Записи этапа 129 (privatescripts/tuf/probe_ctx.py caps, A100, 17.11.2026):
// ответы vLLM 0.32.0 и SGLang 0.5.22 на те же три пробы, что шлёт ProbeCaps,
// напрямую и через LiteLLM v1.103.2, движок со всем включённым и со всем
// выключенным (vLLM: --language-model-only, без --enable-auto-tool-choice и
// --reasoning-parser; SGLang: --limit-mm-data-per-request '{"image": 0}', без
// разборщиков). Каталог — testdata/caps/<вариант>/caps-<проба>.raw и .meta.

// recorded читает запись пробы: код из .meta и тело байт в байт.
func recorded(t *testing.T, variant, probe string) probeResult {
	t.Helper()
	dir := filepath.Join("testdata", "caps", variant)
	meta, err := os.ReadFile(filepath.Join(dir, "caps-"+probe+".meta"))
	if err != nil {
		t.Fatalf("нет записи %s/%s: %v", variant, probe, err)
	}
	var m struct {
		Status json.Number `json:"status"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("%s/%s.meta: %v", variant, probe, err)
	}
	code, err := m.Status.Int64()
	if err != nil {
		t.Fatalf("%s/%s.meta: код %q", variant, probe, m.Status)
	}
	body, err := os.ReadFile(filepath.Join(dir, "caps-"+probe+".raw"))
	if err != nil {
		t.Fatal(err)
	}
	return probeResult{code: int(code), body: body}
}

func stateName(s CapState) string {
	return [...]string{"unknown", "on", "off", "absent"}[s]
}

// TestCapsFromRecordings: что движок включил и что выключил, узнаётся по его
// же ответам — для обоих движков, напрямую и через шлюз.
func TestCapsFromRecordings(t *testing.T) {
	all := func(s CapState) Caps { return Caps{Vision: s, Tools: s, Thinking: s} }
	cases := []struct {
		variant string
		want    Caps
	}{
		{"vllm-on", all(CapOn)},
		{"litellm-vllm-on", all(CapOn)},
		{"vllm-off", all(CapOff)},
		{"litellm-vllm-off", all(CapOff)},
		{"sglang-on", all(CapOn)},
		{"litellm-sglang-on", all(CapOn)},
		{"sglang-off", all(CapOff)},
		{"litellm-sglang-off", all(CapOff)},
	}
	for _, c := range cases {
		got := Caps{
			Vision:   visionState(recorded(t, c.variant, "image")),
			Tools:    toolsState(recorded(t, c.variant, "tools")),
			Thinking: thinkState(recorded(t, c.variant, "think")),
		}
		if got != c.want {
			t.Errorf("%s: зрение %s, инструменты %s, мышление %s; ждали %s, %s, %s", c.variant,
				stateName(got.Vision), stateName(got.Tools), stateName(got.Thinking),
				stateName(c.want.Vision), stateName(c.want.Tools), stateName(c.want.Thinking))
		}
	}
}

// TestCapsUnclearIsUnknown: сбой, 5xx и незнакомый отказ — «не знаю», а не
// «выключено»: запрещать из-за незнания хуже, чем дать серверу ответить самому.
func TestCapsUnclearIsUnknown(t *testing.T) {
	for _, r := range []probeResult{
		{err: context.DeadlineExceeded},
		{code: 500, body: []byte(`{"error":{"message":"internal"}}`)},
		{code: 400, body: []byte(`{"error":{"message":"что-то другое"}}`)},
		{code: 200, body: []byte(`не JSON`)},
	} {
		if s := visionState(r); s != CapUnknown {
			t.Errorf("зрение на %d %q: %s, ждали unknown", r.code, r.body, stateName(s))
		}
		if s := toolsState(r); s != CapUnknown {
			t.Errorf("инструменты на %d %q: %s, ждали unknown", r.code, r.body, stateName(s))
		}
		if s := thinkState(r); s != CapUnknown {
			t.Errorf("мышление на %d %q: %s, ждали unknown", r.code, r.body, stateName(s))
		}
	}
	// Модель ответила сразу и остановилась — мышления у неё нет.
	r := probeResult{code: 200, body: []byte(`{"choices":[{"message":{"content":"4"},"finish_reason":"stop"}]}`)}
	if s := thinkState(r); s != CapAbsent {
		t.Errorf("ответ без мышления: %s, ждали absent", stateName(s))
	}
}

// TestProbeCapsSendsThreeProbes: ProbeCaps шлёт ровно три пробы тем путём, каким
// пойдёт работа, с ключом и полем user, и раскладывает ответы.
func TestProbeCapsSendsThreeProbes(t *testing.T) {
	// Три пробы идут разом — обработчик пишет под замком.
	var mu sync.Mutex
	var auth, users []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		u, _ := b["user"].(string)
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		users = append(users, u)
		mu.Unlock()
		raw, _ := json.Marshal(b)
		switch {
		case strings.Contains(string(raw), "image_url"):
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"The"},"finish_reason":"length"}]}`))
		case b["tools"] != nil:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"4","reasoning_content":"2+2=4"},"finish_reason":"stop"}]}`))
		}
	}))
	defer ts.Close()
	c := New(ts.URL, "sk-test", 0, 0, 0, nil).WithUser("ivanov")
	got := c.ProbeCaps(context.Background(), "qwen38")
	if got != (Caps{Vision: CapOn, Tools: CapOn, Thinking: CapOn}) {
		t.Errorf("ProbeCaps: %+v", got)
	}
	if len(auth) != 3 {
		t.Fatalf("проб %d, ждали 3", len(auth))
	}
	for i := range auth {
		if auth[i] != "Bearer sk-test" || users[i] != "ivanov" {
			t.Errorf("проба %d: Authorization %q, user %q", i, auth[i], users[i])
		}
	}
}
