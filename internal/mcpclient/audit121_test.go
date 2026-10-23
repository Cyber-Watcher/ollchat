package mcpclient

// Правки по аудиту этапа 121 (docs/ollchat-audit-2026-10-23-stage121.md).
// Каждая проверка, кроме помеченных «страховка», падала на коде коммита
// 2858cd6. Адреса — петля httptest и выдуманные (mcp.example, 198.51.100.10,
// 192.0.2.7); где нужен чужой адрес, работает транспорт без сети.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// rtFunc — транспорт из функции: ошибка сети без сети.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeRT — транспорт без сети: на initialize и notifications/initialized
// отвечает сам, остальное отдаёт call. Нужен там, где петлёй не воспроизвести
// ответ: ошибка чтения с чужими адресами, тело кусками по 16 КиБ.
type fakeRT struct {
	call func(r *http.Request, m mockMsg) (*http.Response, error)
}

func (f fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	var m mockMsg
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
	}
	switch m.Method {
	case "initialize":
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{},"serverInfo":{"name":"f"}}}`,
			m.ID, Revision)
		return fakeResp(r, http.StatusOK, "application/json", io.NopCloser(strings.NewReader(body)), "f1"), nil
	case "notifications/initialized":
		return fakeResp(r, http.StatusAccepted, "", http.NoBody, ""), nil
	}
	return f.call(r, m)
}

func fakeResp(r *http.Request, status int, ctype string, body io.ReadCloser, session string) *http.Response {
	h := http.Header{}
	if ctype != "" {
		h.Set("Content-Type", ctype)
	}
	if session != "" {
		h.Set("Mcp-Session-Id", session)
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h, Body: body, Request: r}
}

// failBody — тело, чтение которого отказывает с заданной ошибкой.
type failBody struct{ err error }

func (b failBody) Read([]byte) (int, error) { return 0, b.err }
func (b failBody) Close() error             { return nil }

// pieceBody — тело кусками не больше n байт за Read, как из сети; закрытый
// контекст запроса обрывает чтение, как у настоящего транспорта.
type pieceBody struct {
	ctx context.Context
	r   io.Reader
	n   int
}

func (b *pieceBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.n {
		p = p[:b.n]
	}
	return b.r.Read(p)
}

func (b *pieceBody) Close() error { return nil }

// pieces — читатель кусками по n байт, без контекста.
type pieces struct {
	r io.Reader
	n int
}

func (p *pieces) Read(b []byte) (int, error) {
	if len(b) > p.n {
		b = b[:p.n]
	}
	return p.r.Read(b)
}

// noLeak — в тексте ошибки нет ни одной из строк адреса.
func noLeak(t *testing.T, err error, leaks ...string) {
	t.Helper()
	for _, l := range leaks {
		if strings.Contains(err.Error(), l) {
			t.Errorf("в тексте ошибки %q: %v", l, err)
		}
	}
}

// №7. Переподключиться к потоку не вышло: в тексте — имя сервера и причина,
// без машины, порта и пути; это «ответа нет», с меткой ErrUnavailable.
func TestResumeErrorHidesAddress(t *testing.T) {
	setPause(t, &resumePause, 5*time.Millisecond)
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if r.Method == http.MethodGet {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return true
		}
		if m.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: e1\n\n")
		return true
	})
	c, err := New(Opts{Server: "мок", URL: ms.ts.URL + "/path-7f/mcp", Token: mockToken, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallTool(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	noLeak(t, err, strings.TrimPrefix(ms.ts.URL, "http://"), "127.0.0.1", "path-7f")
	if !strings.Contains(err.Error(), "сервер мок") || !strings.Contains(err.Error(), "переподключиться") {
		t.Errorf("ни сервера, ни сути: %v", err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("обрыв переподключения — «ответа нет», ждали ErrUnavailable: %v", err)
	}
}

// №8. Отказ в соединении по-настоящему: имя сервера и «отказ в соединении»,
// без машины, порта, пути; метка и причина узнаются через errors.Is.
func TestTransportErrorHidesAddress(t *testing.T) {
	setPause(t, &retryPause, time.Millisecond)
	addr := deadAddr(t)
	c, err := New(Opts{Server: "s", URL: "http://" + addr + "/path-7f/mcp", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallTool(context.Background(), "x", nil)
	if !errors.Is(err, ErrUnavailable) || !refused(err) {
		t.Fatalf("ждали отказ в соединении с меткой, получили %v", err)
	}
	noLeak(t, err, addr, "127.0.0.1", "path-7f")
	if !strings.Contains(err.Error(), "сервер s") || !strings.Contains(err.Error(), "отказ в соединении") {
		t.Errorf("ни сервера, ни причины словами: %v", err)
	}
}

// №8, тот же класс: ошибки net/http, которые несут адрес сами (*net.OpError —
// машину, порт и IP из DNS, *net.DNSError — имя, x509 — имена сертификата),
// и обрыв тела с адресами обеих сторон.
func TestNetErrorsWithoutAddress(t *testing.T) {
	setPause(t, &retryPause, time.Millisecond)
	ip := net.ParseIP("198.51.100.10")
	peer := &net.TCPAddr{IP: ip, Port: 8443}
	local := &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 50123}
	leaks := []string{"198.51.100.10", "192.0.2.7", "mcp.example", "8443", "50123", "path-7f", "other.example"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"отказ", &net.OpError{Op: "dial", Net: "tcp", Addr: peer,
			Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "отказ в соединении"},
		{"сброс", &net.OpError{Op: "read", Net: "tcp", Source: local, Addr: peer,
			Err: os.NewSyscallError("read", syscall.ECONNRESET)}, "соединение сброшено"},
		{"DNS", &net.DNSError{Err: "no such host", Name: "mcp.example", IsNotFound: true}, "DNS"},
		{"сертификат", &tls.CertificateVerificationError{Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{"other.example"}}, Host: "mcp.example"}}, "сертификат"},
		{"незнакомая", &net.OpError{Op: "dial", Net: "tcp", Source: local, Addr: peer,
			Err: errors.New("странная причина")}, "странная причина"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(Opts{Server: "s", URL: "https://mcp.example:8443/path-7f/mcp", Timeout: 2 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = rtFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err })
			_, err = c.CallTool(context.Background(), "x", nil)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("ждали ErrUnavailable, получили %v", err)
			}
			noLeak(t, err, leaks...)
			if !strings.Contains(err.Error(), "сервер s") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ждали имя сервера и %q: %v", tc.want, err)
			}
		})
	}
	t.Run("обрыв тела", func(t *testing.T) {
		c, err := New(Opts{Server: "s", URL: "https://mcp.example:8443/path-7f/mcp", Timeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		c.http.Transport = fakeRT{call: func(r *http.Request, m mockMsg) (*http.Response, error) {
			drop := &net.OpError{Op: "read", Net: "tcp", Source: local, Addr: peer,
				Err: os.NewSyscallError("read", syscall.ECONNRESET)}
			return fakeResp(r, http.StatusOK, "application/json", failBody{drop}, ""), nil
		}}
		_, err = c.CallTool(context.Background(), "x", nil)
		if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "не дочитался") {
			t.Fatalf("ждали ErrUnavailable «ответ не дочитался», получили %v", err)
		}
		noLeak(t, err, leaks...)
	})
}

// Перенаправление: новый адрес — не в тексте ошибки (текст уходит модели),
// а у MovedTo, для /mcp и ключей. Прежде тест требовал адрес в тексте; поменялось
// требование (решение владельца 23.10.2026), а не подгонка под код.
func TestRedirectAddressNotInText(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		http.Redirect(w, r, "https://other.example/new/mcp?key=секрет", http.StatusTemporaryRedirect)
		return true
	})
	_, err := ms.client(t, 5*time.Second).Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("ждали отказ 307, получили %v", err)
	}
	noLeak(t, err, "other.example", "секрет")
	if got := MovedTo(err); got != "https://other.example/new/mcp" {
		t.Fatalf("новый адрес для человека: %q", got)
	}
	if MovedTo(errors.New("другое")) != "" {
		t.Fatal("адрес у ошибки без перенаправления")
	}
}

// №5. Предел на весь список инструментов, по всем страницам: maxBody стоит
// на одну страницу, а страниц до сотни.
func TestListToolsTotalLimit(t *testing.T) {
	t.Run("байты", func(t *testing.T) {
		const pages = 40 // по 1 МиБ описания: вместе больше 32 МиБ одного ответа
		desc := strings.Repeat("a", 1<<20)
		var n atomic.Int64
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "tools/list" {
				return false
			}
			i := n.Add(1)
			res := map[string]any{"tools": []any{map[string]any{
				"name": fmt.Sprintf("t%d", i), "description": desc,
				"inputSchema": map[string]any{"type": "object"},
			}}}
			if i < pages {
				res["nextCursor"] = fmt.Sprintf("c%d", i)
			}
			writeResult(w, m.ID, res)
			return true
		})
		_, err := ms.client(t, 10*time.Second).ListTools(context.Background())
		if err == nil || !strings.Contains(err.Error(), "МиБ") || !strings.Contains(err.Error(), "сервер мок") {
			t.Fatalf("ждали отказ по объёму всего списка, получили %v", err)
		}
		if got := len(ms.requests("tools/list")); got >= pages {
			t.Errorf("прочитаны все %d страниц, ждали остановку на пределе", got)
		}
	})
	t.Run("число", func(t *testing.T) {
		tools := make([]any, 5000)
		for i := range tools {
			tools[i] = map[string]any{"name": fmt.Sprintf("t%d", i), "inputSchema": map[string]any{"type": "object"}}
		}
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "tools/list" {
				return false
			}
			writeResult(w, m.ID, map[string]any{"tools": tools})
			return true
		})
		_, err := ms.client(t, 10*time.Second).ListTools(context.Background())
		if err == nil || !strings.Contains(err.Error(), "инструментов больше") {
			t.Fatalf("ждали отказ по числу инструментов, получили %v", err)
		}
	})
}

// №6. Строка data длиннее предела, тело кусками по 16 КиБ, как из сети:
// отказ «ответ больше 32 МиБ» без метки ErrUnavailable — и раньше срока.
// Прежде делитель строк искал конец с начала буфера при каждом дочитывании,
// разбор шёл квадратично, срок истекал первым, и выходило «не ответил»
// с меткой — поиск уходил бы на свои файлы.
func TestSSELongLineHitsLimitNotDeadline(t *testing.T) {
	line := "data: " + strings.Repeat("a", maxBody+(1<<20))
	c, err := New(Opts{Server: "s", URL: "https://mcp.example/mcp", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = fakeRT{call: func(r *http.Request, m mockMsg) (*http.Response, error) {
		body := &pieceBody{ctx: r.Context(), r: strings.NewReader(line), n: 16 << 10}
		return fakeResp(r, http.StatusOK, "text/event-stream", body, ""), nil
	}}
	start := time.Now()
	_, err = c.CallTool(context.Background(), "x", nil)
	t.Logf("за %s: %v", time.Since(start), err)
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "больше 32 МиБ") {
		t.Fatalf("ждали «ответ больше 32 МиБ» без метки, получили %v", err)
	}
}

// Страховка к №6: делитель с памятью о просмотренном режет строки так же,
// как прежде, когда CR и LF приходят в разных чтениях.
func TestSSEParserChunked(t *testing.T) {
	stream := "id: 1\r\ndata: раз\r\rdata: два\r\ndata: три\n\r\n" + ": к\rretry: 7\r\n\r\ndata: хвост"
	read := func(r io.Reader) []string {
		sse := newSSE(r)
		var got []string
		for {
			ev, err := sse.next()
			if err == io.EOF {
				return append(got, "id="+sse.lastID)
			}
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, ev.data)
		}
	}
	want := strings.Join(read(strings.NewReader(stream)), "§")
	if want != "раз§два\nтри§id=1" {
		t.Fatalf("целиком: %q", want)
	}
	for _, n := range []int{1, 2, 3, 5} {
		if got := strings.Join(read(&pieces{r: strings.NewReader(stream), n: n}), "§"); got != want {
			t.Fatalf("кусками по %d: %q, ждали %q", n, got, want)
		}
	}
}

// Низкая: неудачное перечитывание списка не теряет признак «набор сменился».
func TestStaleKeptOnFailedList(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		switch m.Method {
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[]}}\n\n", m.ID)
			return true
		case "tools/list":
			http.Error(w, "перезапуск", http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	c := ms.client(t, 2*time.Second)
	if _, err := c.CallTool(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	if !c.Stale() {
		t.Fatal("признак не поднялся")
	}
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("ждали ошибку списка")
	}
	if !c.Stale() {
		t.Error("список не перечитан, а признак снят — сигнал потерян")
	}
}

// Низкая: 404 с ошибкой JSON-RPC на НАШ запрос (его id) — ответ сервера на
// сам запрос, а не «сеанс истёк»: tools/call не уходит второй раз.
func Test404WithAnswerIsNotSessionLoss(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		writeRPCError(w, http.StatusNotFound, m.ID, -32602, "Unknown tool: x", nil)
		return true
	})
	_, err := ms.client(t, 2*time.Second).CallTool(context.Background(), "x", nil)
	if n := len(ms.requests("tools/call")); n != 1 {
		t.Errorf("tools/call ушёл %d раз", n)
	}
	var re *RPCError
	if !errors.As(err, &re) || re.Code != -32602 || !strings.Contains(err.Error(), "Unknown tool") {
		t.Errorf("ждали ошибку сервера, получили %v", err)
	}
}

// Страховка: 404 с отказом без нашего id (id null, «Session not found») —
// сеанс истёк, как при пустом теле: новое рукопожатие и один повтор.
func Test404SessionNotFoundWithNullID(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method != "tools/call" {
			return false
		}
		if r.Header.Get("Mcp-Session-Id") == "s1" {
			writeRPCError(w, http.StatusNotFound, json.RawMessage("null"), -32001, "Session not found", nil)
			return true
		}
		writeResult(w, m.ID, textResult("ok"))
		return true
	})
	res, err := ms.client(t, 2*time.Second).CallTool(context.Background(), "x", nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("ждали ответ после нового сеанса: %+v, %v", res, err)
	}
	if a, b := len(ms.requests("initialize")), len(ms.requests("tools/call")); a != 2 || b != 2 {
		t.Fatalf("рукопожатий %d, вызовов %d; ждали 2 и 2", a, b)
	}
}

// Низкая: 404 на notifications/initialized — понятная ошибка с именем
// сервера; служебная errSessionGone наружу не выходит.
func TestInitialized404NotLeaked(t *testing.T) {
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if m.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return false
	})
	_, err := ms.client(t, 2*time.Second).CallTool(context.Background(), "x", nil)
	if err == nil || errors.Is(err, errSessionGone) || !strings.Contains(err.Error(), "сервер мок") {
		t.Fatalf("ждали ошибку с именем сервера, без errSessionGone: %v", err)
	}
}

// Низкая: id события вступает в силу, когда событие отдано (WHATWG); id
// события, оборванного концом потока, не в счёт. Затравка из одного id —
// событие без данных, но id и она даёт.
func TestSSEIDTakesEffectOnDispatch(t *testing.T) {
	sse := newSSE(strings.NewReader("id: 6\ndata: a\n\nid: 7\ndata: b"))
	if ev, err := sse.next(); err != nil || ev.data != "a" {
		t.Fatalf("первое событие: %+v, %v", ev, err)
	}
	if _, err := sse.next(); err != io.EOF {
		t.Fatalf("ждали конец потока: %v", err)
	}
	if sse.lastID != "6" {
		t.Fatalf("lastID %q, ждали 6: событие 7 оборвано", sse.lastID)
	}
	primer := newSSE(strings.NewReader("id: e1\n\n"))
	if _, err := primer.next(); err != io.EOF || primer.lastID != "e1" {
		t.Fatalf("затравка: lastID %q, %v", primer.lastID, err)
	}
}

// То же сквозь HTTP: сервер, честно повторяющий всё после Last-Event-ID,
// повторит оборванный ответ, только если клиент назовёт id отданного события.
func TestTruncatedEventReplayed(t *testing.T) {
	setPause(t, &resumePause, 5*time.Millisecond)
	var callID atomic.Value
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			id, _ := callID.Load().(string)
			if r.Header.Get("Last-Event-ID") == "6" {
				fmt.Fprintf(w, "id: 7\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n", id)
			}
			return true
		}
		if m.Method != "tools/call" {
			return false
		}
		callID.Store(string(m.ID))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: 6\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "id: 7\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,", m.ID) // оборвано
		return true
	})
	res, err := ms.client(t, 2*time.Second).CallTool(context.Background(), "x", nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("ответ потерян: %+v, %v", res, err)
	}
}

// Низкая: строки сервера в ошибках — обрезаны и без управляющих знаков.
func TestServerStringsCleanedInErrors(t *testing.T) {
	const esc = "\x1b[2J\x1b]0;title\x07"
	long := strings.Repeat("x", 5000)
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("ждали ошибку")
		}
		if n := utf8.RuneCountInString(err.Error()); n > 1200 {
			t.Errorf("текст ошибки %d знаков", n)
		}
		for _, r := range err.Error() {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("управляющий знак %U в ошибке: %q", r, err.Error())
			}
		}
	}
	initWith := func(reply func(w http.ResponseWriter, m mockMsg)) *mockServer {
		return newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "initialize" {
				return false
			}
			reply(w, m)
			return true
		})
	}
	t.Run("protocolVersion", func(t *testing.T) {
		ms := initWith(func(w http.ResponseWriter, m mockMsg) {
			writeResult(w, m.ID, map[string]any{"protocolVersion": "9999" + esc + long,
				"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "m"}})
		})
		_, err := ms.client(t, 2*time.Second).Initialize(context.Background())
		check(t, err)
	})
	for _, sup := range []string{"2099-01-01", "1999-01-01"} { // ревизия без рукопожатия и старая
		t.Run("data.supported "+sup, func(t *testing.T) {
			ms := initWith(func(w http.ResponseWriter, m mockMsg) {
				writeRPCError(w, http.StatusBadRequest, m.ID, -32602, "нет"+esc,
					map[string]any{"supported": []string{sup + esc + long, sup + long}})
			})
			_, err := ms.client(t, 2*time.Second).Initialize(context.Background())
			check(t, err)
		})
	}
	t.Run("сообщение ошибки JSON-RPC", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "tools/call" {
				return false
			}
			writeRPCError(w, http.StatusOK, m.ID, -32000, "плохо"+esc+long, nil)
			return true
		})
		_, err := ms.client(t, 2*time.Second).CallTool(context.Background(), "x", nil)
		check(t, err)
	})
	t.Run("тело статуса", func(t *testing.T) {
		ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
			if m.Method != "tools/call" {
				return false
			}
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "боль"+esc+long)
			return true
		})
		_, err := ms.client(t, 2*time.Second).CallTool(context.Background(), "x", nil)
		check(t, err)
	})
}

// Низкая: перевод строки внутри ключа — ошибка настройки до запроса, без
// метки ErrUnavailable (прежде: отказ net/http при запросе, «недоступен»,
// и поиск молча уходил на свои файлы). Ключа в тексте нет.
func TestTokenWithLineBreakIsConfigError(t *testing.T) {
	ctx := context.Background()
	const one, two = "первая-половина", "вторая-половина"
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(one+"\n"+two+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Token(ctx, file, "", "")
	if err == nil || !strings.Contains(err.Error(), "token_file") {
		t.Fatalf("файл ключа в две строки: %v", err)
	}
	noLeak(t, err, one, two)

	t.Setenv("OLLCHAT_MCPCLIENT_TEST_TWO_LINES", one+"\r\n"+two)
	if _, err := Token(ctx, "", "OLLCHAT_MCPCLIENT_TEST_TWO_LINES", ""); err == nil {
		t.Fatal("переменная в две строки принята")
	}
	if _, err := Token(ctx, "", "", "printf 'a\\nb'"); err == nil {
		t.Fatal("вывод команды в две строки принят")
	}

	ms := newMock(t, nil)
	c, err := New(Opts{Server: "мок", URL: ms.ts.URL + "/mcp", Token: one + "\n" + two, Timeout: 2 * time.Second})
	if err == nil {
		_, err = c.Initialize(ctx)
	}
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("ждали ошибку настройки без метки ErrUnavailable: %v", err)
	}
	noLeak(t, err, one, two)
	if n := len(ms.all()); n != 0 {
		t.Fatalf("с негодным ключом ушло %d запросов", n)
	}
}

// Решение владельца 23.10.2026: ключ в адресе (user:pass@, строка запроса)
// не поддерживается — New отказывает с подсказкой, адреса в тексте нет.
func TestNewRefusesKeyInURL(t *testing.T) {
	for _, u := range []string{
		"https://user:pass-СЕКРЕТ@mcp.example/mcp",
		"https://СЕКРЕТ@mcp.example/mcp",
		"https://mcp.example/mcp?key=СЕКРЕТ",
		"https://mcp.example/mcp?СЕКРЕТ",
	} {
		_, err := New(Opts{Server: "s", URL: u})
		if err == nil {
			t.Fatalf("адрес %q принят", u)
		}
		noLeak(t, err, "СЕКРЕТ", "mcp.example")
		if !strings.Contains(err.Error(), "token_file") {
			t.Errorf("нет подсказки, куда девать ключ: %v", err)
		}
	}
}

// Низкая: Initialize из другой горутины, пока вызов у сервера, вызов не
// роняет — дважды подряд. 404 на сеанс, который сменил другой, — повод
// догнать новый сеанс, а не отказ сервера.
func TestCallSurvivesConcurrentReinit(t *testing.T) {
	var (
		client  atomic.Pointer[Client]
		seq     atomic.Int64
		bumps   atomic.Int64
		mu      sync.Mutex
		current string
	)
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		switch m.Method {
		case "initialize":
			s := fmt.Sprintf("n%d", seq.Add(1))
			mu.Lock()
			current = s
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", s)
			writeResult(w, m.ID, map[string]any{"protocolVersion": Revision,
				"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "m"}})
			return true
		case "tools/call":
			mu.Lock()
			ok := r.Header.Get("Mcp-Session-Id") == current
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			if bumps.Add(1) <= 2 {
				// Другой «поток» переподключается, пока этот вызов у сервера.
				if _, err := client.Load().Initialize(context.Background()); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			writeResult(w, m.ID, textResult("ok"))
			return true
		}
		return false
	})
	client.Store(ms.client(t, 5*time.Second))
	res, err := client.Load().CallTool(context.Background(), "x", nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("вызов упал на чужом переподключении: %+v, %v", res, err)
	}
	if n := len(ms.requests("initialize")); n != 3 {
		t.Errorf("рукопожатий %d, ждали 3: своё и два чужих, лишнего своего нет", n)
	}
}

// Проба аудитора (П10) как есть: вызовы из 12 горутин, пока ещё одна
// переподключается и закрывает сеанс; на коде 2858cd6 падало 11 из 300.
func TestConcurrentReinitAndClose(t *testing.T) {
	var mu sync.Mutex
	current := ""
	var seq atomic.Int64
	ms := newMock(t, func(w http.ResponseWriter, r *http.Request, m mockMsg) bool {
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			return true
		case m.Method == "initialize":
			s := fmt.Sprintf("sess-%d", seq.Add(1))
			mu.Lock()
			current = s
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", s)
			writeResult(w, m.ID, map[string]any{"protocolVersion": Revision,
				"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "m"}})
			return true
		case m.Method == "tools/call":
			mu.Lock()
			ok := r.Header.Get("Mcp-Session-Id") == current
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			writeResult(w, m.ID, textResult("ok"))
			return true
		}
		return false
	})
	c := ms.client(t, 2*time.Second)
	var wg sync.WaitGroup
	var fails atomic.Int64
	var sample atomic.Value
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if _, err := c.CallTool(context.Background(), "x", nil); err != nil {
					fails.Add(1)
					sample.Store(err.Error())
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			_, _ = c.Initialize(context.Background())
			_ = c.Close(context.Background())
		}
	}()
	wg.Wait()
	if fails.Load() > 0 {
		s, _ := sample.Load().(string)
		t.Errorf("неудач %d из 300, пример: %s", fails.Load(), s)
	}
}
