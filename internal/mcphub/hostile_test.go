package mcphub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Сервер, который ведёт себя не так, как обещает (аудит 23.10.2026, этап 121):
// повтор имени, гонка подключения с обрывом, зависшее перечитывание после
// отмены, картинки не того вида, брошенные сеансы.

// rpcCall — метод и номер запроса JSON-RPC и имя вызываемого инструмента;
// тело запроса остаётся читаемым для настоящего сервера.
func rpcCall(r *http.Request) (method string, id json.RawMessage, tool string) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var m struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Method, m.ID, m.Params.Name
}

// fronted — перед настоящим сервером svc стоит перехватчик: видит каждый
// запрос и может ответить сам (true) или пропустить дальше (false).
func fronted(t *testing.T, svc *service, intercept func(w http.ResponseWriter, r *http.Request) bool) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if intercept(w, r) {
			return
		}
		(*svc.h.Load()).ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// Набор, который реестр отверг, не оставляет сервер «подключённым»: состояние —
// ошибка с причиной, /mcp её показывает, у модели инструментов сервера нет, и
// счёт в /mcp — тот же ноль. До 23.10.2026 ошибку publish глотали Connect,
// refresh и fail: /mcp писал «подключён, инструментов N», а у модели — 0.
func TestPublishRefusalShownAsError(t *testing.T) {
	h := New(testConfig(t, "http://mcp.example/mcp", nil), "test")
	reg := newRegistry(t)
	if err := h.Attach(reg); err != nil {
		t.Fatal(err)
	}
	s := h.servers["wiki"]
	h.mu.Lock()
	h.apply(s, []mcpclient.Tool{schemaTool("echo", `{"type":"object"}`)})
	s.tools = append(s.tools, s.tools[0]) // мимо apply: так реестр точно откажет
	s.state = stateUp
	h.mu.Unlock()
	if err := h.publish("wiki"); err == nil {
		t.Fatal("реестр принял набор с повтором имени")
	}
	st := h.Statuses()[0]
	if st.Up() || st.Err == "" || st.Tools != 0 {
		t.Errorf("отказ реестра не виден в состоянии: %+v", st)
	}
	if names := reg.ExternalNames(); len(names) != 0 {
		t.Errorf("у модели остались инструменты сервера с ошибкой: %v", names)
	}
	if rep := h.Report(); !strings.Contains(rep, stateDown) || !strings.Contains(rep, "дважды") {
		t.Errorf("/mcp молчит о причине:\n%s", rep)
	}
}

// Подключение и обрыв одного сервера идут разом: у модели в итоге тот набор,
// что отвечает итоговому состоянию сервера. До 23.10.2026 publish брал снимок
// под замком, а SetExternal звал без него, и опоздавший старый снимок ложился
// поверх нового.
func TestPublishConcurrentKeepsLatest(t *testing.T) {
	h := New(testConfig(t, "http://mcp.example/mcp", nil), "test")
	reg := newRegistry(t)
	if err := h.Attach(reg); err != nil {
		t.Fatal(err)
	}
	s := h.servers["wiki"]
	list := []mcpclient.Tool{schemaTool("echo", `{"type":"object"}`)}
	// Окно гонки узкое: на коде до правки 3 000 кругов ловили её в трёх
	// прогонах из пяти, 10 000 — запас.
	for i := 0; i < 10000; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.mu.Lock()
			s.state, s.err = stateUp, nil
			h.apply(s, list)
			h.mu.Unlock()
			_ = h.publish("wiki")
		}()
		go func() {
			defer wg.Done()
			h.fail(s, errors.New("обрыв"))
		}()
		wg.Wait()
		up := h.Statuses()[0].Up()
		if has := reg.Has("mcp__wiki__echo"); has != up {
			t.Fatalf("круг %d: сервер подключён — %v, а его инструмент у модели — %v", i, up, has)
		}
	}
}

// Человек прервал вызов, а сервер как раз прислал «список поменялся»:
// перечитывание не держит вызов до своего срока (10 с), а доживает отдельно;
// пока оно не кончилось, вызовы этого сервера спрашивают (как при смене
// описания, Б5), потом — как прежде.
func TestRefreshAfterCancelDoesNotHoldCall(t *testing.T) {
	svc := startService(t, echo("повторяет текст"))
	var block atomic.Bool
	listing := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	url := fronted(t, svc, func(w http.ResponseWriter, r *http.Request) bool {
		if m, _, _ := rpcCall(r); m == "tools/list" && block.Load() {
			select {
			case listing <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
		return false
	})
	h, reg := connected(t, testConfig(t, url, nil))
	g := guardWith(t, h, permissions.ModeNoAsk)
	svc.restart(echo("повторяет текст"),
		tool("probe", "появился после перезапуска", func(map[string]any) (string, error) { return "проба", nil }))
	block.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-listing
		cancel()
	}()
	plan, err := reg.Plan("mcp__wiki__echo", map[string]any{"text": "x"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _ = plan.Run(ctx)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("после отмены вызов держал перечитывание списка %v", took)
	}
	pending, err := reg.Plan("mcp__wiki__echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Req.Changed || g.Check(pending.Req).Decision == permissions.DecisionAllow {
		t.Errorf("список ещё не перечитан, а echo из allow идёт без вопроса: %+v", pending.Req)
	}
	free()
	deadline := time.Now().Add(5 * time.Second)
	for !reg.Has("mcp__wiki__probe") {
		if time.Now().After(deadline) {
			t.Fatalf("перечитывание после отмены не дожило: %v", reg.ExternalNames())
		}
		time.Sleep(20 * time.Millisecond)
	}
	after, _ := reg.Plan("mcp__wiki__echo", nil)
	if after == nil || after.Req.Changed {
		t.Errorf("список перечитан, описание то же, а echo всё ещё спрашивает: %+v", after)
	}
}

// Переподключение заканчивает прежний сеанс (DELETE), закрытие хаба —
// текущие: сервер не держит брошенные сеансы до своего срока.
func TestReconnectAndCloseEndSessions(t *testing.T) {
	svc := startService(t, echo("повторяет текст"))
	var deletes atomic.Int64
	url := fronted(t, svc, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		return false
	})
	h, reg := connected(t, testConfig(t, url, nil))
	if err := h.Reconnect(context.Background(), "wiki"); err != nil {
		t.Fatal(err)
	}
	if n := deletes.Load(); n != 1 {
		t.Errorf("после переподключения прежних сеансов закрыто %d, ждали 1", n)
	}
	plan, _ := reg.Plan("mcp__wiki__echo", map[string]any{"text": "после"})
	if out, err := plan.Run(context.Background()); err != nil || !strings.Contains(out, "эхо: после") {
		t.Fatalf("вызов после переподключения: %q, %v", out, err)
	}
	h.Close(context.Background())
	if n := deletes.Load(); n != 2 {
		t.Errorf("после закрытия хаба сеансов закрыто %d, ждали 2", n)
	}
}

// pngData — настоящая картинка PNG w×h в base64.
func pngData(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// Картинки ответа сервера: модели — только PNG, JPEG, GIF и WebP, чьё
// содержимое совпадает с заявленным видом, не больше предела по размеру и
// числу; прочее отброшено с заметкой в тексте ответа, а не молча.
func TestServerImagesChecked(t *testing.T) {
	good := pngData(t, 2, 2)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	huge := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxServerImageBytes)...))
	answers := map[string][]map[string]any{
		"pic": {
			{"type": "text", "text": "вот"},
			{"type": "image", "mimeType": "image/png", "data": good},
			{"type": "image", "mimeType": "image/svg+xml", "data": b64(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
			{"type": "image", "mimeType": "image/png", "data": b64("это не картинка, а текст")},
			{"type": "image", "mimeType": "image/png", "data": huge},
		},
		"many": nil,
	}
	for i := 0; i < maxServerImages+2; i++ {
		answers["many"] = append(answers["many"], map[string]any{"type": "image", "mimeType": "image/png", "data": good})
	}
	svc := startService(t,
		tool("pic", "картинки", func(map[string]any) (string, error) { return "", nil }),
		tool("many", "много картинок", func(map[string]any) (string, error) { return "", nil }))
	url := fronted(t, svc, func(w http.ResponseWriter, r *http.Request) bool {
		m, id, name := rpcCall(r)
		parts, ok := answers[name]
		if m != "tools/call" || !ok {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"content": parts}})
		return true
	})
	_, reg := connected(t, testConfig(t, url, nil))

	plan, err := reg.Plan("mcp__wiki__pic", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if imgs := plan.Images(); len(imgs) != 1 || imgs[0] != good {
		t.Errorf("модели ушло картинок %d, ждали одну годную PNG", len(imgs))
	}
	if n := strings.Count(out, "отброшена"); n != 3 {
		t.Errorf("заметок об отброшенных картинках %d, ждали 3:\n%s", n, out)
	}

	plan, _ = reg.Plan("mcp__wiki__many", nil)
	out, err = plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if imgs := plan.Images(); len(imgs) != maxServerImages {
		t.Errorf("картинок ушло %d, предел %d", len(imgs), maxServerImages)
	}
	if !strings.Contains(out, "отброшено") {
		t.Errorf("о картинках сверх предела ни слова:\n%s", out)
	}
}

// Служба библиотеки узнаётся по адресу, записанному иначе: явный порт по
// умолчанию, регистр, косая черта в конце. Иначе она ушла бы модели вторым
// набором mcp__<имя>__… против решения «вторым набором не отдаются».
func TestLibraryRecognizedByNormalizedURL(t *testing.T) {
	same := [][2]string{
		{"http://mcp.example:80/mcp", "http://mcp.example/mcp"},
		{"HTTPS://MCP.example:443/mcp/", "https://mcp.example/mcp"},
		{"http://mcp.example/mcp/", "http://mcp.example:80/mcp"},
	}
	for _, p := range same {
		if !sameURL(p[0], p[1]) {
			t.Errorf("%s и %s — один вход", p[0], p[1])
		}
	}
	differ := [][2]string{
		{"http://mcp.example:443/mcp", "https://mcp.example/mcp"},
		{"http://mcp.example:8080/mcp", "http://mcp.example/mcp"},
		{"http://mcp.example/mcp", "http://mcp.example/other"},
	}
	for _, p := range differ {
		if sameURL(p[0], p[1]) {
			t.Errorf("%s и %s — разные входы", p[0], p[1])
		}
	}
	cfg := config.Default()
	cfg.KB.ServerURL = "http://mcp.example"
	cfg.MCP.Servers = map[string]config.MCPServer{"lib": {URL: "http://MCP.example:80/mcp/"}}
	h := New(cfg, "test")
	if got := h.LibraryName(); got != "lib" {
		t.Errorf("служба библиотеки, описанная с явным портом, не узнана: LibraryName = %q", got)
	}
}

// Сервер переехал и отвечает перенаправлением: новый адрес видит человек — в
// /mcp и в ошибке --mcp-ping, — а в тексте ошибки, который может уйти модели,
// его нет (mcpclient.MovedTo, решение владельца 23.10.2026).
func TestMovedAddressShownToHuman(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://moved.example/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(ts.Close)
	h := New(testConfig(t, ts.URL+"/mcp", nil), "test")
	_, err := h.Ping(context.Background(), "wiki")
	if err == nil || !strings.Contains(err.Error(), "https://moved.example/mcp") {
		t.Errorf("--mcp-ping не называет нового адреса: %v", err)
	}
	if mcpclient.MovedTo(err) == "" {
		t.Error("признак перенаправления потерян обёрткой")
	}
	st := h.Statuses()[0]
	if st.Moved != "https://moved.example/mcp" || strings.Contains(st.Err, "moved.example") {
		t.Errorf("состояние: Moved %q, Err %q", st.Moved, st.Err)
	}
	if rep := h.Report(); !strings.Contains(rep, "перенаправляет на https://moved.example/mcp") {
		t.Errorf("/mcp не называет нового адреса:\n%s", rep)
	}
	if err := h.Reconnect(context.Background(), "wiki"); err == nil || strings.Contains(err.Error(), "moved.example") {
		t.Errorf("ошибка переподключения: %v", err)
	}
}

// kb.server_url: /mcp дописывается к пути адреса (с хвостовым «/» и без).
// Якорь, строка запроса и учётные данные — понятный отказ ещё до команды за
// ключом и без их значений (ключ в адресе не поддерживается, решение
// владельца 23.10.2026; та же проверка kbremote.CheckURL, что у клиента
// библиотеки), а не запрос по кривому пути и не паника.
func TestLibraryURLAppendsToPath(t *testing.T) {
	svc := startService(t, mcp.Tool{
		Spec: ollama.ToolSpec{Name: tools.NameSearch, Description: "поиск службы",
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{"query": {Type: "string"}}}},
		Run: func(context.Context, map[string]any) (string, error) { return "нашлось", nil },
	})
	t.Setenv("OLLCHAT_TEST_KB_TOKEN", testToken)
	k := config.Default().KB
	k.ServerTokenEnv, k.ServerTimeout = "OLLCHAT_TEST_KB_TOKEN", "5s"
	k.ServerURL = svc.ts.URL + "/"
	rc, err := LibrarySearch(context.Background(), k, "test")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := rc.CallTool(context.Background(), tools.NameSearch, map[string]any{"query": "x"}); err != nil || out != "нашлось" {
		t.Errorf("адрес с хвостовым /: %q, %v", out, err)
	}
	for _, bad := range []string{"http://mcp.example/?api_key=s3cr3t", "http://user:s3cr3t@mcp.example/", "http://mcp.example/#s3cr3t"} {
		k.ServerURL = bad
		_, err := LibrarySearch(context.Background(), k, "test")
		if err == nil || strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: %v", bad, err)
		}
		cfg := config.Default()
		cfg.KB.ServerURL, cfg.KB.ServerTokenEnv = bad, "OLLCHAT_TEST_KB_TOKEN"
		h := New(cfg, "test")
		if _, err := h.Ping(context.Background(), LibraryServer); err == nil || strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("хаб, %s: %v", bad, err)
		}
	}
	if got := libraryURL("http://mcp.example/base/?x=1#y"); got != "http://mcp.example/base/mcp?x=1#y" {
		t.Errorf("libraryURL = %q", got)
	}
}

// Строка в одну строку для ленты и отчётов: ни C0, ни C1 (U+009B — CSI в
// одном знаке), ни невидимых знаков направления.
func TestOneLineDropsControls(t *testing.T) {
	got := OneLine("a\x1b]0;x\x07b\u009b31mc‮d\r\ne")
	for _, r := range got {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("в строке остался знак %U: %q", r, got)
		}
	}
	if !strings.Contains(got, "d e") {
		t.Errorf("перевод строки не стал пробелом: %q", got)
	}
}
