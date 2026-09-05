package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func apiServer(t *testing.T) (*Client, *http.Request) {
	t.Helper()
	var last http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.32.13"}`))
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3.8:latest","size":123,"capabilities":["tools"]},{"name":"bge-m3:latest"}]}`))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3.8:latest"}]}`))
		case "/api/show":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] == "нет" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"model 'нет' not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"capabilities":["tools","thinking"],"model_info":{"qwen3.context_length":40960}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 5*time.Second, 0, map[string]string{"X-Test": "1"}), &last
}

func TestVersionTagsPS(t *testing.T) {
	c, last := apiServer(t)
	ctx := context.Background()
	v, err := c.Version(ctx)
	if err != nil || v != "0.32.13" {
		t.Fatalf("Version: %q, %v", v, err)
	}
	if last.Header.Get("X-Test") != "1" {
		t.Fatal("заголовки из настроек сервера не ушли в запрос")
	}
	tags, err := c.Tags(ctx)
	if err != nil || len(tags) != 2 || tags[0].Name != "qwen3.8:latest" || tags[0].Capabilities[0] != "tools" {
		t.Fatalf("Tags: %+v, %v", tags, err)
	}
	ps, err := c.PS(ctx)
	if err != nil || len(ps) != 1 {
		t.Fatalf("PS: %+v, %v", ps, err)
	}
}

func TestShowAndContextLength(t *testing.T) {
	c, _ := apiServer(t)
	s, err := c.Show(context.Background(), "qwen3.8:latest")
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := ContextLengthFromShow(s); !ok || n != 40960 {
		t.Fatalf("окно из model_info: %d, %v", n, ok)
	}
	if len(s.Capabilities) != 2 {
		t.Fatalf("возможности: %v", s.Capabilities)
	}
	_, err = c.Show(context.Background(), "нет")
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ошибка сервера должна нести код и тело: %v", err)
	}
}

func TestClientReportsUnreachableServer(t *testing.T) {
	c := New("http://127.0.0.1:1", 500*time.Millisecond, 0, nil)
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "/api/version") {
		t.Fatalf("недоступный сервер: %v", err)
	}
}
