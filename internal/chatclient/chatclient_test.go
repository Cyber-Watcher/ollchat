package chatclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// loadServer собирает сервер через настоящую загрузку конфига: таймауты и
// раскрытие ~ в api_key_file проставляет config, а не тест.
func loadServer(t *testing.T, body string) *config.Server {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	// Сервер тела — сервер чата по умолчанию: иначе загрузка ищет "local".
	name := ""
	if i := strings.Index(body, "name = \""); i >= 0 {
		rest := body[i+len("name = \""):]
		name = rest[:strings.Index(rest, "\"")]
	}
	body = "[general]\ndefault_server = \"" + name + "\"\n" + body
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatalf("конфиг: %v", err)
	}
	return &cfg.Servers[0]
}

// Сервер без api — по-прежнему клиент Ollama, как до этапа 127.
func TestNewOllamaByDefault(t *testing.T) {
	srv := loadServer(t, "[[servers]]\nname = \"local\"\nurl = \"http://ollama.example:11434\"\n")
	if _, ok := New(srv).(*ollama.Client); !ok {
		t.Fatalf("сервер без api: клиент %T, ждали *ollama.Client", New(srv))
	}
}

// Модели OpenAI-сервера приходят в форме /api/tags и /api/show: окно из
// max_model_len, context_length конфига его перекрывает, возможности объявлены,
// RealTools верит им (шаблона нет), PS пуст без ошибки, Resident — «не знаю».
func TestOpenAIModelsAsOllamaShapes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen38","object":"model","max_model_len":32768}]}`))
	}))
	defer ts.Close()
	ctx := context.Background()

	c := New(loadServer(t, "[[servers]]\nname = \"v\"\nurl = \""+ts.URL+"\"\napi = \"openai\"\n"))
	tags, err := c.Tags(ctx)
	if err != nil || len(tags) != 1 || tags[0].Name != "qwen38" || tags[0].Details.ContextLength != 32768 ||
		!tags[0].HasCapability("tools") {
		t.Fatalf("Tags: %+v, %v", tags, err)
	}
	show, err := c.Show(ctx, "qwen38")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if n, ok := ollama.ContextLengthFromShow(show); !ok || n != 32768 {
		t.Errorf("окно из Show: %d %v, ждали 32768", n, ok)
	}
	if !show.RealTools() {
		t.Error("RealTools у модели OpenAI-сервера ложь — агент не дал бы ей инструментов")
	}
	if _, err := c.Show(ctx, "нет-такой"); err == nil || !strings.Contains(err.Error(), "qwen38") {
		t.Errorf("Show неизвестной модели: ждали отказ со списком моделей, получили %v", err)
	}
	if ps, err := c.PS(ctx); err != nil || len(ps) != 0 {
		t.Errorf("PS: %v %v, ждали пусто без ошибки", ps, err)
	}
	if res := c.Resident(ctx, "qwen38"); res.Known {
		t.Errorf("Resident: %+v — у API нет такого пути, ждали «не знаю»", res)
	}
	if v, err := c.Version(ctx); err != nil || v == "" {
		t.Errorf("Version: %q %v", v, err)
	}

	over := New(loadServer(t, "[[servers]]\nname = \"v\"\nurl = \""+ts.URL+"\"\napi = \"openai\"\ncontext_length = 8192\n"))
	if tags, _ := over.Tags(ctx); len(tags) != 1 || tags[0].Details.ContextLength != 8192 {
		t.Errorf("context_length конфига не перекрыл окно сервера: %+v", tags)
	}
}

// Ключ, который нельзя прочитать, не роняет запуск: клиент отвечает этой
// ошибкой на каждый вызов, называя сервер и поле, но не сам ключ.
func TestOpenAIBadKeyIsBrokenClient(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("sk-not-to-be-printed"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(loadServer(t, "[[servers]]\nname = \"gw\"\nurl = \"http://gw.example:4000\"\napi = \"openai\"\n"+
		"api_key_file = \""+key+"\"\n"))
	_, err := c.Tags(context.Background())
	if err == nil || !strings.Contains(err.Error(), `"gw"`) || !strings.Contains(err.Error(), "api_key_file") ||
		strings.Contains(err.Error(), "sk-not-to-be-printed") {
		t.Fatalf("ждали отказ с именем сервера и поля и без ключа, получили %v", err)
	}
	var got error
	for ev := range c.Chat(context.Background(), ollama.ChatRequest{Model: "m"}) {
		if ev.Kind == ollama.EventError {
			got = ev.Err
		}
	}
	if got == nil || !strings.Contains(got.Error(), "api_key_file") {
		t.Errorf("Chat сломанного клиента: ждали EventError с причиной, получили %v", got)
	}
}

// Имя учётной записи ОС уходит полем user запроса чата у сервера "openai";
// send_user = false его выключает.
func TestOpenAISendsOSUser(t *testing.T) {
	var got []any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body["user"])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer ts.Close()
	want := osUser()
	if want == "" {
		t.Skip("имя учётной записи ОС не определилось — проверять нечего")
	}
	req := ollama.ChatRequest{Model: "m", Messages: []ollama.Message{{Role: ollama.RoleUser, Content: "привет"}}}
	for _, body := range []string{"", "send_user = false\n"} {
		c := New(loadServer(t, "[[servers]]\nname = \"gw\"\nurl = \""+ts.URL+"\"\napi = \"openai\"\n"+body))
		for range c.Chat(context.Background(), req) {
		}
	}
	if len(got) != 2 || got[0] != want || got[1] != nil {
		t.Errorf("поле user по двум запросам: %v, ждали [%s <nil>]", got, want)
	}
}

// Ключ из переменной окружения уходит заголовком Authorization в каждый запрос.
func TestOpenAIKeyFromEnvSent(t *testing.T) {
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer ts.Close()
	t.Setenv("OLLCHAT_TEST_LITELLM_KEY", "sk-env-key")
	c := New(loadServer(t, "[[servers]]\nname = \"gw\"\nurl = \""+ts.URL+"\"\napi = \"openai\"\n"+
		"api_key_env = \"OLLCHAT_TEST_LITELLM_KEY\"\n"))
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-env-key" {
		t.Errorf("заголовок Authorization %q, ждали Bearer с ключом из переменной", auth)
	}
}
