package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Клиент против НАСТОЯЩЕГО сервера internal/mcp — того же, что отвечает
// в ollmcp: mcp.MountHTTP под kbserve.Protect (так их собирает ollmcp/main.go
// и kbserve.NewHTTPServer). Мок проверяет то, чего наша служба не делает;
// здесь — что клиент и сервер проекта понимают друг друга.

const realToken = "ключ-службы-7f3a"

// realService — служба на httptest. Обработчик подменяется «перезапуском»:
// новый экземпляр сервера не знает сеансов прежнего, как после exec у ollmcp.
type realService struct {
	ts      *httptest.Server
	handler atomic.Pointer[http.Handler]
	inits   atomic.Int64

	mu  sync.Mutex
	srv *mcp.Server
}

func startReal(t *testing.T, tools ...mcp.Tool) *realService {
	t.Helper()
	rs := &realService{}
	rs.restart(tools...)
	rs.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"initialize"`)) {
			rs.inits.Add(1)
		}
		(*rs.handler.Load()).ServeHTTP(w, r)
	}))
	t.Cleanup(rs.ts.Close)
	return rs
}

func (rs *realService) restart(tools ...mcp.Tool) {
	srv := mcp.NewServer(nil, tools...)
	mux := http.NewServeMux()
	mcp.MountHTTP(mux, srv, realToken, false)
	// loopback = true: httptest слушает 127.0.0.1, и Host у запросов петлевой.
	var h http.Handler = kbserve.Protect(mux, true)
	rs.mu.Lock()
	rs.srv = srv
	rs.mu.Unlock()
	rs.handler.Store(&h)
}

func (rs *realService) server() *mcp.Server {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.srv
}

func fakeTool(name, desc string, props map[string]ollama.ToolProp, required []string,
	run func(args map[string]any) (string, error)) mcp.Tool {
	return mcp.Tool{
		Spec: ollama.ToolSpec{Name: name, Description: desc,
			Parameters: ollama.ToolParams{Type: "object", Properties: props, Required: required}},
		Run: func(_ context.Context, args map[string]any) (string, error) { return run(args) },
	}
}

func echoTool() mcp.Tool {
	return fakeTool("echo", "повторяет текст",
		map[string]ollama.ToolProp{"text": {Type: "string", Description: "что повторить"}},
		[]string{"text"},
		func(a map[string]any) (string, error) { return fmt.Sprintf("эхо: %v", a["text"]), nil })
}

func failTool() mcp.Tool {
	return fakeTool("fail", "всегда ломается", map[string]ollama.ToolProp{}, nil,
		func(map[string]any) (string, error) {
			return "", errors.New("инструмент сломан нарочно")
		})
}

func probeTool() mcp.Tool {
	return fakeTool("probe", "появился после перезапуска", map[string]ollama.ToolProp{}, nil,
		func(map[string]any) (string, error) { return "проба", nil })
}

func realClient(t *testing.T, url, token string) *Client {
	t.Helper()
	c, err := New(Opts{Server: "ollmcp", URL: url, Token: token, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func toolNames(ts []Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// initialize → tools/list → tools/call → isError против сервера проекта.
func TestRealServerHandshakeListCall(t *testing.T) {
	rs := startReal(t, echoTool(), failTool())
	c := realClient(t, rs.ts.URL+"/mcp", realToken)
	ctx := context.Background()

	info, err := c.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Ожидаемое — из самого сервера (то же отдаёт /health), а не переписано сюда.
	want := mcp.Info(rs.server())
	if info.Protocol != want["protocol"] || info.ServerName != want["name"] || info.ServerVersion != want["version"] {
		t.Fatalf("рукопожатие: %+v, сервер о себе: %v", info, want)
	}
	if !info.ListChanged || info.Instructions == "" || info.Took <= 0 || info.At.IsZero() {
		t.Fatalf("сведения рукопожатия неполны: %+v", info)
	}

	list, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(toolNames(list), ","); got != "echo,fail" {
		t.Fatalf("инструменты: %s", got)
	}
	for _, tl := range list {
		if tl.Name != "echo" {
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
			t.Fatalf("схема не JSON: %s", tl.InputSchema)
		}
		if schema.Properties["text"].Type != "string" || len(schema.Required) != 1 || schema.Required[0] != "text" {
			t.Fatalf("схема дошла не та: %s", tl.InputSchema)
		}
		if tl.Description != "повторяет текст" {
			t.Fatalf("описание: %q", tl.Description)
		}
	}

	res, err := c.CallTool(ctx, "echo", map[string]any{"text": "привет"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Text != "эхо: привет" {
		t.Fatalf("вызов echo: %+v", res)
	}

	res, err = c.CallTool(ctx, "fail", nil)
	if err != nil {
		t.Fatalf("ошибка инструмента должна прийти результатом, а пришла ошибкой: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text, "инструмент сломан нарочно") {
		t.Fatalf("isError не дошёл: %+v", res)
	}

	// Незнакомый инструмент у сервера без реестра — ошибка JSON-RPC, не isError.
	_, err = c.CallTool(ctx, "нет-такого", nil)
	var re *RPCError
	if !errors.As(err, &re) || re.Server != "ollmcp" {
		t.Fatalf("ждали ошибку JSON-RPC, получили %v", err)
	}
	if n := rs.inits.Load(); n != 1 {
		t.Fatalf("рукопожатий %d, ждали одно", n)
	}

	// DELETE служба не принимает (405) — сеанс всё равно считается закрытым.
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := c.Info(); ok {
		t.Fatal("после Close сведения рукопожатия остались")
	}
}

// Неверный ключ: ошибка узнаётся как ErrUnauthorized, а ключа в тексте нет.
func TestRealServerWrongKey(t *testing.T) {
	rs := startReal(t, echoTool())
	const wrong = "неверный-ключ-91c2"
	c := realClient(t, rs.ts.URL+"/mcp", wrong)
	_, err := c.ListTools(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ждали ErrUnauthorized, получили %v", err)
	}
	if strings.Contains(err.Error(), wrong) || strings.Contains(err.Error(), realToken) {
		t.Fatalf("ключ в тексте ошибки: %v", err)
	}
	if !strings.Contains(err.Error(), "ollmcp") {
		t.Fatalf("ошибка не называет сервер: %v", err)
	}
}

// Служба перезапустилась с другим набором: первый же ответ приходит потоком
// SSE с notifications/tools/list_changed (internal/mcp/transport.go), клиент
// поднимает признак Stale, вызов при этом исполнен, свежий список — с новым
// инструментом.
func TestRealServerRestartMarksStale(t *testing.T) {
	rs := startReal(t, echoTool())
	c := realClient(t, rs.ts.URL+"/mcp", realToken)
	ctx := context.Background()
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Stale() {
		t.Fatal("список устарел, не успев смениться")
	}

	rs.restart(echoTool(), probeTool())
	res, err := c.CallTool(ctx, "echo", map[string]any{"text": "после"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "эхо: после" {
		t.Fatalf("ответ из потока: %+v", res)
	}
	if !c.Stale() {
		t.Fatal("уведомление о смене набора не дошло")
	}
	list, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(toolNames(list), ","); got != "echo,probe" {
		t.Fatalf("после перезапуска: %s", got)
	}
	if c.Stale() {
		t.Fatal("после перечитывания признак не снят")
	}
	// Служба незнакомый сеанс не отвергает — второго рукопожатия нет.
	if n := rs.inits.Load(); n != 1 {
		t.Fatalf("рукопожатий %d, ждали одно", n)
	}
}

// Одновременные первые вызовы: рукопожатие одно на всех, вызовы все исполнены.
// Под -race заодно проверяет общее состояние клиента.
func TestRealServerConcurrentFirstCalls(t *testing.T) {
	rs := startReal(t, echoTool())
	c := realClient(t, rs.ts.URL+"/mcp", realToken)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := c.CallTool(context.Background(), "echo", map[string]any{"text": i})
			if err == nil && res.Text != fmt.Sprintf("эхо: %d", i) {
				err = fmt.Errorf("вызов %d получил чужой ответ %q", i, res.Text)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := rs.inits.Load(); got != 1 {
		t.Fatalf("рукопожатий %d на %d одновременных вызовов, ждали одно", got, n)
	}
}
