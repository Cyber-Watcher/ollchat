package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// searxStub изображает SearXNG: отдаёт то, что ему велено.
func searxStub(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.String())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func webRegistry(t *testing.T, url string) *Registry {
	t.Helper()
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry([]string{NameWebSearch}, Options{
		Sandbox: sb, MaxOutputKB: 64, SearxURL: url, SearxTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const searxJSON = `{"query":"golang","results":[
 {"title":"Go Scheduler","url":"https://go.dev/src/runtime/HACKING","content":"To interact\ndirectly with the\ngoroutine scheduler","engine":"google"},
 {"title":"Ardan Labs","url":"https://ardanlabs.com/blog/x.html","content":"A Goroutine is essentially a Coroutine","engine":"bing"},
 {"title":"Третья","url":"https://example.org/3","content":"третья выдержка","engine":"ddg"}
]}`

func TestWebSearchFormatsResults(t *testing.T) {
	srv, seen := searxStub(t, http.StatusOK, searxJSON)
	plan, err := webRegistry(t, srv.URL).Plan(NameWebSearch,
		map[string]any{"query": "golang scheduler", "limit": 2, "language": "en"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[1]", "Go Scheduler", "https://go.dev/src/runtime/HACKING", NameHTTPFetch} {
		if !strings.Contains(out, want) {
			t.Fatalf("в выдаче нет %q:\n%s", want, out)
		}
	}
	// limit обязан соблюдаться: третья ссылка лишняя.
	if strings.Contains(out, "example.org/3") {
		t.Fatalf("limit не соблюдён:\n%s", out)
	}
	// Переводы строк внутри выдержки только рвут вид.
	if strings.Contains(out, "To interact\ndirectly") {
		t.Fatalf("выдержка не склеена в строку:\n%s", out)
	}
	// Язык и формат должны уйти поисковику.
	q := (*seen)[0]
	for _, want := range []string{"format=json", "language=en", "q=golang+scheduler"} {
		if !strings.Contains(q, want) {
			t.Fatalf("в запросе к поисковику нет %q: %s", want, q)
		}
	}
}

// TestWebSearchExplainsHTMLAnswer — самая частая беда настройки: JSON выключен,
// и вместо данных приходит страница. Молчать об этом нельзя.
func TestWebSearchExplainsHTMLAnswer(t *testing.T) {
	srv, _ := searxStub(t, http.StatusOK, "<!doctype html><html><body>ищем…</body></html>")
	plan, _ := webRegistry(t, srv.URL).Plan(NameWebSearch, map[string]any{"query": "x"})
	_, err := plan.Run(context.Background())
	if err == nil {
		t.Fatal("страница вместо JSON принята за выдачу")
	}
	if !strings.Contains(err.Error(), "search.formats") {
		t.Fatalf("не сказано, что чинить: %v", err)
	}
}

// TestWebSearchWithoutURL — инструмент включён, а поисковик не настроен.
func TestWebSearchWithoutURL(t *testing.T) {
	_, err := webRegistry(t, "").Plan(NameWebSearch, map[string]any{"query": "x"})
	if err == nil {
		t.Fatal("поиск без настроенного адреса не отклонён")
	}
	if !strings.Contains(err.Error(), "searxng_url") || !strings.Contains(err.Error(), "searxngmanage") {
		t.Fatalf("не сказано, как настроить: %v", err)
	}
}

// TestWebSearchEmptyResults — «ничего не нашлось» это ответ, а не ошибка.
func TestWebSearchEmptyResults(t *testing.T) {
	srv, _ := searxStub(t, http.StatusOK, `{"query":"zzz","results":[]}`)
	plan, _ := webRegistry(t, srv.URL).Plan(NameWebSearch, map[string]any{"query": "zzz"})
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("пустая выдача превращена в ошибку: %v", err)
	}
	if !strings.Contains(out, "ничего не нашёл") {
		t.Fatalf("не сказано, что ничего не нашлось: %s", out)
	}
}

// TestWebSearchNeedsFetchPermission — запрос уходит наружу, значит проверка
// та же, что у http_fetch.
func TestWebSearchNeedsFetchPermission(t *testing.T) {
	srv, _ := searxStub(t, http.StatusOK, searxJSON)
	plan, err := webRegistry(t, srv.URL).Plan(NameWebSearch, map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Req.Kind != permissions.KindFetch {
		t.Fatalf("вид проверки %v, ожидался Fetch", plan.Req.Kind)
	}
	if plan.Req.Target != srv.URL {
		t.Fatalf("целью проверки стал %q, а не адрес поисковика", plan.Req.Target)
	}
}
