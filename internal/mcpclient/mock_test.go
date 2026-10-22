package mcpclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Мок — то, чего служба проекта не делает, но чужие серверы вправе: поток SSE
// на каждый ответ, истёкший сеанс (404), отказ в ревизии, страницы списка,
// картинки в ответе, медленный ответ, закрытый до ответа поток.

const mockToken = "мок-ключ-5d1e"

// mockMsg — входящее сообщение, как его видит мок.
type mockMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// mockReq — что пришло: метод HTTP, метод JSON-RPC, заголовки, тело.
type mockReq struct {
	verb string
	msg  mockMsg
	hdr  http.Header
}

// mockServer отвечает на initialize и notifications/initialized сам: номер
// сеанса из sessions по очереди, ревизия — запрошенная. Остальное — handle;
// handle, вернувший false, отдаёт запрос этим умолчаниям.
type mockServer struct {
	t      *testing.T
	ts     *httptest.Server
	handle func(w http.ResponseWriter, r *http.Request, m mockMsg) bool

	mu       sync.Mutex
	log      []mockReq
	sessions []string
	issued   int
}

func newMock(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, m mockMsg) bool) *mockServer {
	t.Helper()
	ms := &mockServer{t: t, handle: handle, sessions: []string{"s1", "s2", "s3"}}
	ms.ts = httptest.NewServer(http.HandlerFunc(ms.serve))
	t.Cleanup(ms.ts.Close)
	return ms
}

func (ms *mockServer) serve(w http.ResponseWriter, r *http.Request) {
	var m mockMsg
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &m); err != nil {
			ms.t.Errorf("мок: тело не JSON: %q", body)
		}
	}
	ms.mu.Lock()
	ms.log = append(ms.log, mockReq{verb: r.Method, msg: m, hdr: r.Header.Clone()})
	ms.mu.Unlock()
	if ms.handle != nil && ms.handle(w, r, m) {
		return
	}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		ms.mu.Lock()
		session := ms.sessions[ms.issued%len(ms.sessions)]
		ms.issued++
		ms.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", session)
		writeResult(w, m.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "мок", "version": "9.9"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	default:
		writeRPCError(w, http.StatusOK, m.ID, -32601, "мок не знает "+m.Method, nil)
	}
}

// requests — что пришло с этим методом JSON-RPC.
func (ms *mockServer) requests(method string) []mockReq {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var out []mockReq
	for _, r := range ms.log {
		if r.msg.Method == method {
			out = append(out, r)
		}
	}
	return out
}

func (ms *mockServer) all() []mockReq {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return append([]mockReq(nil), ms.log...)
}

func (ms *mockServer) client(t *testing.T, timeout time.Duration) *Client {
	t.Helper()
	c, err := New(Opts{Server: "мок", URL: ms.ts.URL + "/mcp", Token: mockToken, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string, data any) {
	e := map[string]any{"code": code, "message": msg}
	if data != nil {
		e["data"] = data
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": e})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func textResult(text string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

func setPause(t *testing.T, p *time.Duration, d time.Duration) {
	t.Helper()
	old := *p
	*p = d
	t.Cleanup(func() { *p = old })
}

// Каждый POST несёт Content-Type, Accept с обоими типами и ключ; после
// рукопожатия — номер сеанса и договорённую ревизию; Origin — никогда.
// Мок, как kbserve.Protect, отвечает 415 без Content-Type: прошедшие вызовы
// и есть проверка, что клиент его шлёт.
func TestHeadersOnEveryPost(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"тело запроса — только JSON"}`, http.StatusUnsupportedMediaType)
			return true
		}
		if m.Method == "tools/call" {
			writeResult(w, m.ID, textResult("ок"))
			return true
		}
		if m.Method == "tools/list" {
			writeResult(w, m.ID, map[string]any{"tools": []any{}})
			return true
		}
		return false
	})
	c := ms.client(t, 5*time.Second)
	ctx := context.Background()
	info, err := c.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Protocol != Revision || info.ServerName != "мок" || info.ServerVersion != "9.9" || !info.ListChanged {
		t.Fatalf("сведения: %+v", info)
	}
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallTool(ctx, "x", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}

	log := ms.all()
	if len(log) != 4 {
		t.Fatalf("запросов %d, ждали 4 (initialize, initialized, list, call)", len(log))
	}
	for _, r := range log {
		if r.verb != http.MethodPost {
			t.Fatalf("метод %s", r.verb)
		}
		if acc := r.hdr.Get("Accept"); !strings.Contains(acc, "application/json") || !strings.Contains(acc, "text/event-stream") {
			t.Fatalf("%s: Accept %q", r.msg.Method, acc)
		}
		if got := r.hdr.Get("Authorization"); got != "Bearer "+mockToken {
			t.Fatalf("%s: Authorization %q", r.msg.Method, got)
		}
		if o := r.hdr.Get("Origin"); o != "" {
			t.Fatalf("%s: клиент прислал Origin %q", r.msg.Method, o)
		}
		if r.msg.Method == "initialize" {
			if r.hdr.Get("Mcp-Session-Id") != "" || r.hdr.Get("MCP-Protocol-Version") != "" {
				t.Fatalf("initialize ушёл с заголовками сеанса: %v", r.hdr)
			}
			var p struct {
				ProtocolVersion string          `json:"protocolVersion"`
				Capabilities    json.RawMessage `json:"capabilities"`
				ClientInfo      struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			if err := json.Unmarshal(r.msg.Params, &p); err != nil {
				t.Fatal(err)
			}
			if p.ProtocolVersion != Revision || string(p.Capabilities) != "{}" ||
				p.ClientInfo.Name != "ollchat" || p.ClientInfo.Version == "" {
				t.Fatalf("параметры initialize: %s", r.msg.Params)
			}
			continue
		}
		if s := r.hdr.Get("Mcp-Session-Id"); s != "s1" {
			t.Fatalf("%s: Mcp-Session-Id %q", r.msg.Method, s)
		}
		if v := r.hdr.Get("MCP-Protocol-Version"); v != info.Protocol {
			t.Fatalf("%s: MCP-Protocol-Version %q, договорились о %q", r.msg.Method, v, info.Protocol)
		}
	}
	if len(ms.requests("notifications/initialized")) != 1 {
		t.Fatal("нет notifications/initialized")
	}
}

// 415 и прочие не-2xx без JSON-RPC в теле — статус и начало тела.
func TestStatusErrorShowsStatusAndBody(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		http.Error(w, "тело запроса — только JSON: нужен заголовок Content-Type", http.StatusUnsupportedMediaType)
		return true
	})
	_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "415") || !strings.Contains(err.Error(), "Content-Type") {
		t.Fatalf("ошибка без статуса или тела: %v", err)
	}
}

// Ответ потоком: комментарий, «затравка» с одним id, ответ на чужой id,
// уведомление о смене набора, затем наш ответ, разбитый на несколько строк
// data и с концами строк CRLF.
func TestSSEListChangedBeforeAnswer(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": держим соединение\n\n")
		fmt.Fprint(w, "id: 7\ndata: \n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":999,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"чужой\"}]}}\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
		fmt.Fprintf(w, "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":%s,\r\ndata: \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"из потока\"}]}}\r\n\r\n", m.ID)
		return true
	})
	c := ms.client(t, 5*time.Second)
	res, err := c.CallTool(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "из потока" {
		t.Fatalf("ответ: %+v", res)
	}
	if !c.Stale() {
		t.Fatal("list_changed из потока не поднял признак")
	}
}

// Запрос сервера посреди потока (ping) получает ответ отдельным POST,
// а наш вызов — свой ответ.
func TestServerPingInStreamAnswered(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		switch {
		case m.Method == "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-2\",\"method\":\"sampling/createMessage\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[]}}\n\n", m.ID)
			return true
		case m.Method == "" && len(m.ID) > 0:
			w.WriteHeader(http.StatusAccepted) // ответ клиента на наш запрос
			return true
		}
		return false
	})
	c := ms.client(t, 5*time.Second)
	if _, err := c.CallTool(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	var replies []string
	for _, r := range ms.all() {
		if r.msg.Method == "" && len(r.msg.ID) > 0 {
			replies = append(replies, string(r.msg.ID)+"="+string(r.msg.Result))
		}
	}
	if len(replies) != 2 || replies[0] != `"srv-1"={}` || !strings.HasPrefix(replies[1], `"srv-2"=`) {
		t.Fatalf("ответы клиента серверу: %v", replies)
	}
}

// Сервер закрыл поток до ответа, назвав id и retry: клиент переподключается
// GET с Last-Event-ID и дочитывает; сам вызов второй раз не шлётся.
func TestStreamResumedAfterClose(t *testing.T) {
	var callID atomic.Value // id вызова: ответ в продолжении потока несёт его
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		switch {
		case r.Method == http.MethodGet:
			if r.Header.Get("Last-Event-ID") != "ev-1" || r.Header.Get("Mcp-Session-Id") != "s1" ||
				r.Header.Get("Authorization") != "Bearer "+mockToken {
				http.Error(w, "не тот курсор, сеанс или ключ", http.StatusBadRequest)
				return true
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "id: ev-2\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"дочитано\"}]}}\n\n",
				callID.Load())
			return true
		case m.Method == "tools/call":
			callID.Store(string(m.ID))
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: ev-1\nretry: 10\ndata: \n\n")
			return true
		}
		return false
	})
	c := ms.client(t, 5*time.Second)
	res, err := c.CallTool(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "дочитано" {
		t.Fatalf("ответ: %+v", res)
	}
	if n := len(ms.requests("tools/call")); n != 1 {
		t.Fatalf("вызов ушёл %d раз", n)
	}
}

// Сервер закрыл поток, а продолжить не даёт (405 на GET): ошибка, и вызов
// не повторяется — он, может быть, уже исполнен.
func TestStreamClosedNoResumeNoRepeat(t *testing.T) {
	setPause(t, &resumePause, 5*time.Millisecond)
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		switch {
		case r.Method == http.MethodGet:
			http.Error(w, "нужен POST", http.StatusMethodNotAllowed)
			return true
		case m.Method == "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: ev-1\ndata: \n\n")
			return true
		}
		return false
	})
	_, err := ms.client(t, 5*time.Second).CallTool(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "405") {
		t.Fatalf("ждали отказ с 405, получили %v", err)
	}
	if n := len(ms.requests("tools/call")); n != 1 {
		t.Fatalf("вызов ушёл %d раз", n)
	}
}

// Поток кончился без ответа и без id — переподключаться не к чему.
func TestStreamEndsWithoutAnswer(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		return true
	})
	_, err := ms.client(t, 5*time.Second).CallTool(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "без ответа") {
		t.Fatalf("ждали «поток закрылся без ответа», получили %v", err)
	}
}

// 404 на запрос с номером сеанса — сеанс истёк: новое рукопожатие без
// прежнего номера, запрос повторён один раз, список помечен устаревшим.
func TestSessionExpiredRehandshake(t *testing.T) {
	var dead atomic.Bool
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		if dead.Load() && r.Header.Get("Mcp-Session-Id") == "s1" {
			http.Error(w, "сеанс не найден", http.StatusNotFound)
			return true
		}
		writeResult(w, m.ID, textResult("сеанс "+r.Header.Get("Mcp-Session-Id")))
		return true
	})
	c := ms.client(t, 5*time.Second)
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Stale() {
		t.Fatal("признак поднят раньше времени")
	}
	dead.Store(true) // «сервер перезапустился»
	res, err := c.CallTool(ctx, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "сеанс s2" {
		t.Fatalf("повтор ушёл не в новом сеансе: %q", res.Text)
	}
	inits := ms.requests("initialize")
	if len(inits) != 2 {
		t.Fatalf("рукопожатий %d, ждали 2", len(inits))
	}
	if s := inits[1].hdr.Get("Mcp-Session-Id"); s != "" {
		t.Fatalf("новое рукопожатие ушло с прежним номером %q", s)
	}
	if n := len(ms.requests("tools/call")); n != 2 {
		t.Fatalf("вызов ушёл %d раз, ждали 2 (отказ 404 и повтор)", n)
	}
	if !c.Stale() {
		t.Fatal("после нового сеанса список не помечен устаревшим")
	}
}

// 404 и после нового рукопожатия — ошибка, а не бесконечный круг.
func TestSessionExpiredTwiceStops(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method == "tools/call" {
			http.Error(w, "сеанс не найден", http.StatusNotFound)
			return true
		}
		return false
	})
	_, err := ms.client(t, 5*time.Second).CallTool(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("ждали ошибку 404, получили %v", err)
	}
	if n := len(ms.requests("tools/call")); n != 2 {
		t.Fatalf("вызов ушёл %d раз, ждали 2", n)
	}
}

// 401: ErrUnauthorized, ключа в тексте нет; сервер, повторивший ключ в теле
// ответа или в сообщении JSON-RPC, его в ошибку тоже не протащит.
func TestUnauthorizedAndKeyNeverLeaks(t *testing.T) {
	t.Run("401", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			http.Error(w, "нужен ключ "+r.Header.Get("Authorization"), http.StatusUnauthorized)
			return true
		})
		_, err := ms.client(t, 5*time.Second).ListTools(context.Background())
		if !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "нужен ключ доступа") {
			t.Fatalf("ждали ErrUnauthorized, получили %v", err)
		}
		if strings.Contains(err.Error(), mockToken) {
			t.Fatalf("ключ в ошибке: %v", err)
		}
	})
	t.Run("500 с ключом в теле", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			http.Error(w, "упал на запросе с "+r.Header.Get("Authorization"), http.StatusInternalServerError)
			return true
		})
		_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("ждали 500, получили %v", err)
		}
		if strings.Contains(err.Error(), mockToken) {
			t.Fatalf("ключ в ошибке: %v", err)
		}
	})
	t.Run("ключ в сообщении JSON-RPC", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "tools/call" {
				return false
			}
			writeRPCError(w, http.StatusOK, m.ID, -32603, "внутри: "+r.Header.Get("Authorization"), nil)
			return true
		})
		_, err := ms.client(t, 5*time.Second).CallTool(context.Background(), "x", nil)
		var re *RPCError
		if !errors.As(err, &re) || re.Code != -32603 {
			t.Fatalf("ждали RPCError, получили %v", err)
		}
		if strings.Contains(err.Error(), mockToken) {
			t.Fatalf("ключ в ошибке: %v", err)
		}
	})
}

// Медленный ответ — срок: и когда молчат заголовки, и когда поток начат,
// но ответа в нём нет. Ошибка узнаётся как ErrUnavailable и DeadlineExceeded.
func TestSlowResponseTimesOut(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("поток=%v", stream), func(t *testing.T) {
			ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
				if m.Method != "tools/call" {
					return false
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, ": думаю\n\n")
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
				}
				return true
			})
			c := ms.client(t, 300*time.Millisecond)
			if _, err := c.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err := c.CallTool(context.Background(), "x", nil)
			if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ждали срок, получили %v", err)
			}
			if !strings.Contains(err.Error(), "не ответил за") {
				t.Fatalf("текст ошибки: %v", err)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("срок 300 мс, а ждали %s", d)
			}
		})
	}
}

// Отмена вызывающим — не «сервер недоступен»: человек нажал Ctrl+C сам.
//
// Прежде тест отменял вызов сроком (WithTimeout) и тем закреплял, что срок
// вызывающего — тоже «прерван» без метки. Это и было расхождение с kbremote
// (рецензия этапа 121): срок теперь — ErrUnavailable, см. следующий тест,
// а здесь — настоящая отмена.
func TestCallerCancelIsNotUnavailable(t *testing.T) {
	arrived := make(chan struct{}, 1)
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		arrived <- struct{}{}
		<-r.Context().Done()
		return true
	})
	c := ms.client(t, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()
	_, err := c.CallTool(ctx, "x", nil)
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "прерван") {
		t.Fatalf("ждали «вызов прерван», получили %v", err)
	}
}

// Срок вызывающего истёк, а сервер молчит — это «ответа нет», как в kbremote:
// метка ErrUnavailable, по ней search через службу уходит на свои файлы.
func TestCallerDeadlineIsUnavailable(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		<-r.Context().Done()
		return true
	})
	c := ms.client(t, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.CallTool(ctx, "x", nil)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ждали ErrUnavailable со сроком, получили %v", err)
	}
}

// Обрыв посреди тела ответа — тоже «ответа нет»: метка ErrUnavailable, как
// у kbremote («ответ не дочитался»).
func TestBodyDropIsUnavailable(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return true
		}
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n" +
			`{"jsonrpc":"2.0","id":` + string(m.ID))
		buf.Flush()
		conn.Close()
		return true
	})
	c := ms.client(t, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := c.CallTool(context.Background(), "x", nil)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "не дочитался") {
		t.Fatalf("ждали ErrUnavailable «ответ не дочитался», получили %v", err)
	}
}

// deadAddr — адрес, на котором никто не слушает: настоящий отказ в соединении.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// countingTransport — транспорт, считающий соединения; первое соединение
// можно направить на мёртвый адрес.
func countingTransport(dials *atomic.Int64, firstTo string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) == 1 && firstTo != "" {
			addr = firstTo
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

// Отказ в соединении: ровно один повтор после паузы. Отказ и на повторе —
// ErrUnavailable; сервер ожил к повтору — вызов проходит.
func TestConnectionRefusedRetriedOnce(t *testing.T) {
	setPause(t, &retryPause, 50*time.Millisecond)
	t.Run("отказ дважды", func(t *testing.T) {
		c, err := New(Opts{Server: "мёртвый", URL: "http://" + deadAddr(t) + "/mcp", Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		var dials atomic.Int64
		c.http.Transport = countingTransport(&dials, "")
		start := time.Now()
		_, err = c.Initialize(context.Background())
		if !errors.Is(err, ErrUnavailable) || !refused(err) {
			t.Fatalf("ждали отказ в соединении, получили %v", err)
		}
		if n := dials.Load(); n != 2 {
			t.Fatalf("соединений %d, ждали 2 (попытка и один повтор)", n)
		}
		if d := time.Since(start); d < retryPause {
			t.Fatalf("повтор без паузы: %s", d)
		}
	})
	t.Run("ожил к повтору", func(t *testing.T) {
		ms := newMock(t, nil)
		c := ms.client(t, 5*time.Second)
		var dials atomic.Int64
		c.http.Transport = countingTransport(&dials, deadAddr(t))
		if _, err := c.Initialize(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := len(ms.requests("initialize")); n != 1 {
			t.Fatalf("initialize дошёл %d раз", n)
		}
	})
}

// Обрыв после отправки запроса — без повтора: инструмент мог уже сработать.
func TestNoRetryAfterRequestSent(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close() // запрос прочитан целиком, ответа не будет
		}
		return true
	})
	c := ms.client(t, 5*time.Second)
	_, err := c.CallTool(context.Background(), "x", nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ждали ErrUnavailable, получили %v", err)
	}
	if n := len(ms.requests("tools/call")); n != 1 {
		t.Fatalf("вызов ушёл %d раз, ждали один", n)
	}
}

// Ревизия без рукопожатия (2026-07-28) — понятный отказ во всех видах,
// в которых его может прислать сервер; сервер старой ревизии, перечисливший
// свои, — одна попытка с общей.
func TestRevisionNegotiation(t *testing.T) {
	refuse := func(status, code int, data any) func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		return func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "initialize" {
				return false
			}
			writeRPCError(w, status, m.ID, code, "отказ мока", data)
			return true
		}
	}
	modern := []struct {
		name   string
		handle func(w http.ResponseWriter, r *http.Request, m mockMsg) bool
	}{
		{"400 UnsupportedProtocolVersion", refuse(400, -32022, map[string]any{"supported": []string{"2026-07-28"}, "requested": Revision})},
		{"400 HeaderMismatch", refuse(400, -32020, nil)},
		{"400 черновой номер", refuse(400, -32004, nil)},
		{"404 Method not found", refuse(404, -32601, nil)},
		{"200 со списком новых ревизий", refuse(200, -32602, map[string]any{"supported": []string{"2026-07-28"}})},
		{"ответ с ревизией 2026-07-28", func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "initialize" {
				return false
			}
			writeResult(w, m.ID, map[string]any{"protocolVersion": "2026-07-28", "capabilities": map[string]any{}})
			return true
		}},
	}
	for _, tc := range modern {
		t.Run(tc.name, func(t *testing.T) {
			ms := newMock(t, tc.handle)
			_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
			if !errors.Is(err, ErrRevision) {
				t.Fatalf("ждали ErrRevision, получили %v", err)
			}
			if !strings.Contains(err.Error(), "2026-07-28") || !strings.Contains(err.Error(), Revision) {
				t.Fatalf("отказ не называет ревизии: %v", err)
			}
			if len(ms.requests("notifications/initialized")) != 0 {
				t.Fatal("после отказа ушло notifications/initialized")
			}
		})
	}

	t.Run("старый сервер перечислил свои", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "initialize" || strings.Contains(string(m.Params), `"2025-03-26"`) {
				return false
			}
			writeRPCError(w, 200, m.ID, -32602, "Unsupported protocol version",
				map[string]any{"supported": []string{"2025-03-26"}, "requested": Revision})
			return true
		})
		info, err := ms.client(t, 5*time.Second).Initialize(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Protocol != "2025-03-26" || len(ms.requests("initialize")) != 2 {
			t.Fatalf("договорились о %q за %d попыток", info.Protocol, len(ms.requests("initialize")))
		}
	})
	t.Run("общей ревизии нет", func(t *testing.T) {
		ms := newMock(t, refuse(200, -32602, map[string]any{"supported": []string{"1.0"}}))
		_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
		if !errors.Is(err, ErrRevision) || !strings.Contains(err.Error(), "1.0") {
			t.Fatalf("ждали ErrRevision с ревизией сервера, получили %v", err)
		}
	})
	t.Run("405 — старый транспорт", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			http.Error(w, "нужен GET", http.StatusMethodNotAllowed)
			return true
		})
		_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP+SSE") || errors.Is(err, ErrRevision) {
			t.Fatalf("ждали подсказку про транспорт, получили %v", err)
		}
	})
}

// Страницы списка: курсоры по очереди, все инструменты; курсор по кругу —
// ошибка. Схема и пометки — как прислал сервер, байт в байт.
func TestListToolsPagination(t *testing.T) {
	schema := `{"type":"object","properties":{"q":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}`
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/list" {
			return false
		}
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(m.Params, &p)
		page := map[string]any{}
		switch p.Cursor {
		case "":
			page["tools"] = []any{
				map[string]any{"name": "a", "description": "первый", "inputSchema": json.RawMessage(schema),
					"annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "", "description": "безымянный"},
			}
			page["nextCursor"] = "p2"
		case "p2":
			page["tools"] = []any{map[string]any{"name": "b"}}
			page["nextCursor"] = "p3"
		case "p3":
			page["tools"] = []any{map[string]any{"name": "c", "inputSchema": map[string]any{"type": "object"}}}
		case "круг":
			page["tools"] = []any{}
			page["nextCursor"] = "круг"
		}
		writeResult(w, m.ID, page)
		return true
	})
	c := ms.client(t, 5*time.Second)
	list, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(toolNames(list), ","); got != "a,b,c" {
		t.Fatalf("инструменты: %s", got)
	}
	if string(list[0].InputSchema) != schema {
		t.Fatalf("схема изменена: %s", list[0].InputSchema)
	}
	if string(list[0].Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("пометки: %s", list[0].Annotations)
	}
	var empty map[string]any
	if err := json.Unmarshal(list[1].InputSchema, &empty); err != nil || empty["type"] != "object" {
		t.Fatalf("инструмент без схемы получил %s", list[1].InputSchema)
	}
	var cursors []string
	for _, r := range ms.requests("tools/list") {
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(r.msg.Params, &p)
		cursors = append(cursors, p.Cursor)
	}
	if strings.Join(cursors, "|") != "|p2|p3" {
		t.Fatalf("курсоры: %q", cursors)
	}
}

func TestListToolsCursorLoop(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/list" {
			return false
		}
		writeResult(w, m.ID, map[string]any{"tools": []any{}, "nextCursor": "снова"})
		return true
	})
	_, err := ms.client(t, 5*time.Second).ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "по кругу") {
		t.Fatalf("ждали ошибку круга, получили %v", err)
	}
	if n := len(ms.requests("tools/list")); n != 2 {
		t.Fatalf("страниц запрошено %d, ждали 2", n)
	}
}

// Части ответа: текст, картинка, текстовый и двоичный ресурс, ссылка, звук,
// испорченная картинка, незнакомый тип.
func TestCallToolContentKinds(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nне совсем png"))
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		writeResult(w, m.ID, map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "раз"},
			map[string]any{"type": "image", "data": png, "mimeType": "image/png"},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///a.txt", "text": "из ресурса"}},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///b.bin", "mimeType": "application/zip", "blob": "AAAA"}},
			map[string]any{"type": "resource_link", "uri": "file:///c", "name": "отчёт"},
			map[string]any{"type": "audio", "data": "AAAA", "mimeType": "audio/wav"},
			map[string]any{"type": "image", "data": "это не base64!", "mimeType": "image/jpeg"},
			map[string]any{"type": "hologram"},
		}})
		return true
	})
	res, err := ms.client(t, 5*time.Second).CallTool(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].MIME != "image/png" || res.Images[0].Data != png {
		t.Fatalf("картинки: %+v", res.Images)
	}
	lines := strings.Split(res.Text, "\n")
	want := []string{"раз", "из ресурса", "b.bin", "отчёт", "audio/wav", "image/jpeg", "hologram"}
	if len(lines) != len(want) {
		t.Fatalf("строк %d, ждали %d:\n%s", len(lines), len(want), res.Text)
	}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Fatalf("строка %d %q не содержит %q", i, lines[i], w)
		}
	}
}

// structuredContent без текста — его JSON в Text; при тексте — текст.
func TestCallToolStructuredWithoutText(t *testing.T) {
	var withText atomic.Bool
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		res := map[string]any{"content": []any{}, "structuredContent": map[string]any{"градусов": 21}}
		if withText.Load() {
			res["content"] = []any{map[string]any{"type": "text", "text": "тепло"}}
		}
		writeResult(w, m.ID, res)
		return true
	})
	c := ms.client(t, 5*time.Second)
	res, err := c.CallTool(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil || got["градусов"] != float64(21) {
		t.Fatalf("Text без структуры: %q", res.Text)
	}
	withText.Store(true)
	if res, err = c.CallTool(context.Background(), "x", nil); err != nil || res.Text != "тепло" {
		t.Fatalf("при тексте: %+v, %v", res, err)
	}
}

// Ошибка JSON-RPC — error с кодом; isError — результат с признаком.
func TestRPCErrorVersusIsError(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Name == "rpc" {
			writeRPCError(w, http.StatusOK, m.ID, -32602, "неверные доводы", nil)
			return true
		}
		writeResult(w, m.ID, map[string]any{"isError": true,
			"content": []any{map[string]any{"type": "text", "text": "диск полон"}}})
		return true
	})
	c := ms.client(t, 5*time.Second)
	_, err := c.CallTool(context.Background(), "rpc", nil)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != -32602 || !strings.Contains(err.Error(), "неверные доводы") {
		t.Fatalf("ждали RPCError -32602, получили %v", err)
	}
	res, err := c.CallTool(context.Background(), "tool", nil)
	if err != nil || !res.IsError || res.Text != "диск полон" {
		t.Fatalf("isError: %+v, %v", res, err)
	}
}

// Перенаправление не выполняется: ключ и тело вызова не уходят по чужому адресу.
func TestRedirectNotFollowed(t *testing.T) {
	var elsewhere atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		http.Redirect(w, r, other.URL+"/mcp?key=секрет", http.StatusTemporaryRedirect)
		return true
	})
	_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "307") || !strings.Contains(err.Error(), other.URL+"/mcp") {
		t.Fatalf("ждали отказ с новым адресом, получили %v", err)
	}
	if strings.Contains(err.Error(), "секрет") {
		t.Fatalf("строка запроса нового адреса в ошибке: %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatal("клиент пошёл по перенаправлению")
	}
}

// Close шлёт DELETE с номером сеанса; следующий вызов здоровается заново.
func TestCloseDeletesSession(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		if m.Method == "tools/list" {
			writeResult(w, m.ID, map[string]any{"tools": []any{}})
			return true
		}
		return false
	})
	c := ms.client(t, 5*time.Second)
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var deletes []string
	for _, r := range ms.all() {
		if r.verb == http.MethodDelete {
			deletes = append(deletes, r.hdr.Get("Mcp-Session-Id"))
		}
	}
	if len(deletes) != 1 || deletes[0] != "s1" {
		t.Fatalf("DELETE: %v", deletes)
	}
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(ms.requests("initialize")); n != 2 {
		t.Fatalf("после Close рукопожатий %d, ждали 2", n)
	}
}

// Адрес проверяется до сети; ключ из строки запроса в ошибку не попадает.
func TestNewValidatesURL(t *testing.T) {
	for _, u := range []string{"", "ftp://mcp.example/mcp?key=СЕКРЕТ", "mcp.example:8080/mcp", "http:///mcp", "http://[::1/mcp?key=СЕКРЕТ"} {
		_, err := New(Opts{Server: "s", URL: u})
		if err == nil {
			t.Fatalf("адрес %q принят", u)
		}
		if strings.Contains(err.Error(), "СЕКРЕТ") {
			t.Fatalf("строка запроса в ошибке: %v", err)
		}
	}
	c, err := New(Opts{URL: "https://mcp.example/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if c.timeout != 60*time.Second || c.Server() != "mcp.example" || c.version == "" {
		t.Fatalf("умолчания: срок %s, имя %q, версия %q", c.timeout, c.Server(), c.version)
	}
}
