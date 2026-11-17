package chatclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// offEngine — сервер в повадках vLLM 0.32.0, запущенного без зрения, без
// разборщика вызовов и без разборщика мышления (тексты отказов — из записей
// этапа 129, probe_ctx.py caps). chats считает запросы чата.
func offEngine(t *testing.T, chats *atomic.Int32) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen38","object":"model","max_model_len":32768}]}`))
		case "/v1/chat/completions":
			chats.Add(1)
			b, _ := io.ReadAll(r.Body)
			switch {
			case strings.Contains(string(b), "image_url"):
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"At most 0 image(s) may be provided in one prompt. (parameter=image)","type":"BadRequestError","param":"image","code":400}}`))
			case strings.Contains(string(b), `"tools"`):
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"\"auto\" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set","type":"BadRequestError","param":null,"code":400}}`))
			default:
				_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"The user asks 2+2. Answer 4.\n</think>\n\n4","reasoning":null},"finish_reason":"stop"}]}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestShowProbesWhatEngineTurnedOff: всё, что движок выключил, уходит в
// CapabilitiesOff и пропадает из Capabilities (интерфейс не шлёт картинку,
// которую движок отвергнет); пробы — раз на модель.
func TestShowProbesWhatEngineTurnedOff(t *testing.T) {
	var chats atomic.Int32
	ts := offEngine(t, &chats)
	c := New(loadServer(t, "[[servers]]\nname = \"v\"\nurl = \""+ts.URL+"\"\napi = \"openai\"\n"))

	show, err := c.Show(context.Background(), "qwen38")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !reflect.DeepEqual(show.Capabilities, []string{"completion"}) {
		t.Errorf("Capabilities %v, ждали только completion", show.Capabilities)
	}
	if !reflect.DeepEqual(show.CapabilitiesOff, []string{"tools", "thinking", "vision"}) {
		t.Errorf("CapabilitiesOff %v, ждали tools thinking vision", show.CapabilitiesOff)
	}
	if len(show.CapabilitiesUnknown) != 0 {
		t.Errorf("CapabilitiesUnknown %v, ждали пусто", show.CapabilitiesUnknown)
	}
	if got := chats.Load(); got != 3 {
		t.Errorf("проб %d, ждали 3", got)
	}
	if _, err := c.Show(context.Background(), "qwen38"); err != nil {
		t.Fatalf("второй Show: %v", err)
	}
	if got := chats.Load(); got != 3 {
		t.Errorf("второй Show повторил пробы: запросов чата %d, ждали 3", got)
	}
}

// TestShowWithoutProbes: probe_caps = false — ни одного запроса чата, всё
// объявлено (как до этапа 129) и помечено непроверенным.
func TestShowWithoutProbes(t *testing.T) {
	var chats atomic.Int32
	ts := offEngine(t, &chats)
	c := New(loadServer(t, "[[servers]]\nname = \"v\"\nurl = \""+ts.URL+"\"\napi = \"openai\"\nprobe_caps = false\n"))

	show, err := c.Show(context.Background(), "qwen38")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got := chats.Load(); got != 0 {
		t.Errorf("при probe_caps = false запросов чата %d, ждали 0", got)
	}
	if !reflect.DeepEqual(show.Capabilities, []string{"completion", "tools", "thinking", "vision"}) {
		t.Errorf("Capabilities %v", show.Capabilities)
	}
	if !reflect.DeepEqual(show.CapabilitiesUnknown, []string{"tools", "thinking", "vision"}) {
		t.Errorf("CapabilitiesUnknown %v", show.CapabilitiesUnknown)
	}
	if len(show.CapabilitiesOff) != 0 {
		t.Errorf("CapabilitiesOff %v, ждали пусто", show.CapabilitiesOff)
	}
}
