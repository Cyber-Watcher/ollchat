//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Приёмочная проверка протокола на СОБРАННОМ бинаре (этап 109).
//
// Остальные тесты пакета зовут сервер внутри процесса теста. Здесь — то, что
// видит настоящий клиент: отдельный процесс ollmcp, его stdout, его порт,
// подмена его файла. Образец — «Claude Code Mastery», т. 2, разд. 25
// (conformance harness: initialize, tools/list, структура ответов), дополненный
// тем, на чём мы сами обжигались: посторонняя строка в stdout, ответ на
// уведомление, смена набора после выкладки (26.09.2026 клиент весь день звал
// инструменты, которых служба уже не отдавала).
//
// Сеть наружу и карта не нужны: конфиг временный, база пустая.

var (
	builtOnce sync.Once
	builtBin  string
	builtErr  error
)

// TestMain убирает бинарь, собранный приёмочной проверкой.
func TestMain(m *testing.M) {
	code := m.Run()
	if builtBin != "" {
		_ = os.RemoveAll(filepath.Dir(builtBin))
	}
	os.Exit(code)
}

// buildBinary собирает ollmcp один раз на прогон пакета.
func buildBinary(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ollmcp-conformance-")
		if err != nil {
			builtErr = err
			return
		}
		builtBin = filepath.Join(dir, "ollmcp")
		gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
		out, err := exec.Command(gobin, "build", "-o", builtBin, ".").CombinedOutput()
		if err != nil {
			builtErr = &buildError{string(out), err}
		}
	})
	if builtErr != nil {
		t.Fatalf("сборка ollmcp: %v", builtErr)
	}
	return builtBin
}

type buildError struct {
	out string
	err error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

// installBinary кладёт свою копию бинаря в каталог теста: подменять будем её.
func installBinary(t *testing.T, dir string) string {
	t.Helper()
	dst := filepath.Join(dir, "ollmcp")
	copyFile(t, buildBinary(t), dst)
	return dst
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// replaceBinary подменяет файл так, как это делает ollchat-build.sh: новый файл
// и mv поверх. mtime в прошлое — чтобы не ждать полсекунды «тишины».
func replaceBinary(t *testing.T, path string) {
	t.Helper()
	fresh := path + ".new"
	copyFile(t, buildBinary(t), fresh)
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(fresh, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fresh, path); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	body := "[kb]\ndir = \"" + filepath.Join(dir, "kb") + "\"\n" +
		"[log]\nenabled = false\n" +
		"[[servers]]\nname = \"local\"\nurl = \"http://127.0.0.1:11434\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// stdioPeer — процесс ollmcp в режиме stdio и строки его stdout.
type stdioPeer struct {
	t     *testing.T
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string
}

func startStdio(t *testing.T, bin, cfg string, env ...string) *stdioPeer {
	t.Helper()
	cmd := exec.Command(bin, "-c", cfg)
	cmd.Env = append(os.Environ(), env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &stdioPeer{t: t, cmd: cmd, in: in, lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	return p
}

func (p *stdioPeer) send(msg string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, msg+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

// next — следующее сообщение; в stdout не бывает ничего, кроме JSON.
func (p *stdioPeer) next() map[string]any {
	p.t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			p.t.Fatal("ollmcp закрыл stdout")
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			p.t.Fatalf("в stdout не JSON (клиент на этом ломается): %q", line)
		}
		return m
	case <-time.After(10 * time.Second):
		p.t.Fatal("ответа нет 10 с")
	}
	return nil
}

func (p *stdioPeer) call(id int, method, params string) map[string]any {
	p.t.Helper()
	if params == "" {
		params = "{}"
	}
	p.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":` + params + `}`)
	m := p.next()
	if got, _ := m["id"].(float64); int(got) != id {
		p.t.Fatalf("ответ не на тот запрос: ждали id %d, пришло %v", id, m)
	}
	return m
}

func result(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("нет result: %v", m)
	}
	return r
}

// checkToolList — каждый инструмент описан так, чтобы модель могла его позвать
// правильно: имя, описание, схема-объект, у каждого параметра тип и описание,
// обязательные поля существуют (чек-лист «Claude Code Mastery», т. 2, разд. 17).
func checkToolList(t *testing.T, r map[string]any) []string {
	t.Helper()
	list, ok := r["tools"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("tools/list без списка инструментов: %v", r)
	}
	var names []string
	for _, x := range list {
		tool := x.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "" {
			t.Errorf("инструмент без имени: %v", tool)
			continue
		}
		names = append(names, name)
		if d, _ := tool["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("%s: нет описания", name)
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("%s: схема не объект: %v", name, schema)
			continue
		}
		props, _ := schema["properties"].(map[string]any)
		for pname, pv := range props {
			prop, _ := pv.(map[string]any)
			if typ, _ := prop["type"].(string); typ == "" || typ == "object" {
				t.Errorf("%s.%s: тип %q — нужен простой тип или перечисление", name, pname, typ)
			}
			if d, _ := prop["description"].(string); strings.TrimSpace(d) == "" {
				t.Errorf("%s.%s: нет описания параметра", name, pname)
			}
		}
		req, _ := schema["required"].([]any)
		for _, rv := range req {
			if _, ok := props[rv.(string)]; !ok {
				t.Errorf("%s: обязательное поле %v не описано в properties", name, rv)
			}
		}
	}
	return names
}

func TestConformanceStdio(t *testing.T) {
	dir := t.TempDir()
	p := startStdio(t, installBinary(t, dir), testConfig(t, dir))

	r := result(t, p.call(1, "initialize",
		`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"conformance","version":"1"}}`))
	if v, _ := r["protocolVersion"].(string); v == "" {
		t.Error("нет protocolVersion")
	}
	if info, _ := r["serverInfo"].(map[string]any); info["name"] == "" || info["name"] == nil {
		t.Error("нет serverInfo.name")
	}
	caps, _ := r["capabilities"].(map[string]any)
	tools, _ := caps["tools"].(map[string]any)
	if tools["listChanged"] != true {
		t.Errorf("не объявлен tools.listChanged: %v", caps)
	}

	// На уведомление ответа нет: следующий ответ обязан быть на id 2.
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	names := checkToolList(t, result(t, p.call(2, "tools/list", "")))
	if !contains(names, "kb_status") {
		t.Errorf("нет kb_status среди %v", names)
	}

	if e, _ := p.call(3, "no/such/method", "")["error"].(map[string]any); e["code"] != float64(-32601) {
		t.Errorf("неизвестный метод: ждали -32601, пришло %v", e)
	}
	if r := result(t, p.call(4, "tools/call", `{"name":"no_such_tool","arguments":{}}`)); r["isError"] != true {
		t.Errorf("неизвестный инструмент должен давать isError, а не ошибку протокола: %v", r)
	}
	if r := result(t, p.call(5, "tools/call", `{"name":"kb_status","arguments":{}}`)); r["isError"] == true {
		t.Errorf("kb_status на пустой базе упал: %v", r)
	}
}

// stdio после выкладки: тот же процесс переходит на новый файл. Набор тот же —
// уведомления нет; продолжение с другим отпечатком — уведомление первым.
func TestConformanceStdioReplace(t *testing.T) {
	dir := t.TempDir()
	bin := installBinary(t, dir)
	cfg := testConfig(t, dir)
	p := startStdio(t, bin, cfg)
	result(t, p.call(1, "initialize", ""))
	pid := p.cmd.Process.Pid
	before := inode(t, pid)

	replaceBinary(t, bin)
	deadline := time.Now().Add(10 * time.Second)
	for inode(t, pid) == before {
		if time.Now().After(deadline) {
			t.Fatal("процесс не перешёл на новый файл за 10 с")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Первое сообщение после перехода — ответ, а не уведомление: набор тот же.
	checkToolList(t, result(t, p.call(2, "tools/list", "")))

	q := startStdio(t, bin, cfg, "OLLMCP_REEXEC=000000000000")
	if m := q.next(); m["method"] != "notifications/tools/list_changed" {
		t.Fatalf("после смены набора первым должно идти уведомление, пришло %v", m)
	}
	checkToolList(t, result(t, q.call(1, "tools/list", "")))
}

// inode файла, из которого запущен процесс pid.
func inode(t *testing.T, pid int) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat("/proc/"+strconv.Itoa(pid)+"/exe", &st); err != nil {
		t.Fatalf("процесс %d: %v", pid, err)
	}
	return uint64(st.Ino)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

type httpPeer struct {
	t     *testing.T
	url   string
	token string
}

func (h *httpPeer) post(body, session, accept string) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.url+"/mcp", strings.NewReader(body))
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitHealth(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := http.Get(url + "/health"); err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("служба не ответила на /health за 15 с")
}

func TestConformanceHTTP(t *testing.T) {
	dir := t.TempDir()
	bin := installBinary(t, dir)
	addr := freeAddr(t)
	cmd := exec.Command(bin, "-c", testConfig(t, dir), "--http", addr)
	cmd.Env = append(os.Environ(), "OLLMCP_TOKEN=conformance")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	h := &httpPeer{t: t, url: "http://" + addr, token: "conformance"}
	waitHealth(t, h.url)

	// Без ключа — отказ; не POST — отказ.
	if r := (&httpPeer{t: t, url: h.url}).post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("без ключа: %d, ждали 401", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, h.url+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer conformance")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp: %v %v, ждали 405", r, err)
	}

	const both = "application/json, text/event-stream"
	resp := h.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "", both)
	session := resp.Header.Get("Mcp-Session-Id")
	var init map[string]any
	if err := json.Unmarshal([]byte(readBody(t, resp)), &init); err != nil || session == "" {
		t.Fatalf("initialize: сеанс %q, %v", session, err)
	}
	if r := h.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, session, both); r.StatusCode != http.StatusAccepted {
		t.Errorf("уведомление: %d, ждали 202 без тела", r.StatusCode)
	}
	resp = h.post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, session, both)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("свой сеанс: Content-Type %q", ct)
	}
	var list map[string]any
	if err := json.Unmarshal([]byte(readBody(t, resp)), &list); err != nil {
		t.Fatal(err)
	}
	checkToolList(t, result(t, list))

	// Выкладка: тот же pid и тот же порт, а прежний сеанс один раз узнаёт
	// о возможной смене набора.
	pid := cmd.Process.Pid
	before := inode(t, pid)
	replaceBinary(t, bin)
	deadline := time.Now().Add(20 * time.Second)
	for inode(t, pid) == before {
		if time.Now().After(deadline) {
			t.Fatal("служба не перешла на новый файл за 20 с")
		}
		time.Sleep(100 * time.Millisecond)
	}
	waitHealth(t, h.url)
	resp = h.post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, session, both)
	body := readBody(t, resp)
	if resp.Header.Get("Content-Type") != "text/event-stream" ||
		!strings.HasPrefix(body, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}") ||
		!strings.Contains(body, `"id":3`) {
		t.Fatalf("после перезапуска нет уведомления перед ответом:\n%s", body)
	}
	if r := h.post(`{"jsonrpc":"2.0","id":4,"method":"ping"}`, session, both); r.Header.Get("Content-Type") != "application/json" {
		t.Error("уведомление повторилось")
	}
	// Скрипт на curl без заголовков — прежний простой JSON.
	if r := h.post(`{"jsonrpc":"2.0","id":5,"method":"ping"}`, "", ""); r.Header.Get("Content-Type") != "application/json" {
		t.Error("клиенту без сеанса ушёл не JSON")
	}
}

// searchTopKDesc — описание параметра top_k у search в ответе tools/list.
func searchTopKDesc(t *testing.T, r map[string]any) string {
	t.Helper()
	for _, x := range r["tools"].([]any) {
		tool := x.(map[string]any)
		if tool["name"] == "search" {
			props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			d, _ := props["top_k"].(map[string]any)["description"].(string)
			return d
		}
	}
	t.Fatal("в наборе нет search")
	return ""
}

// Правка ollmcp.toml у живой службы: тот же процесс перезапускается на новых
// настройках, клиент получает уведомление и видит новый предел в описании;
// негодная правка службу не роняет (решение владельца 04.10.2026: пробовать
// пределы вживую, без переписывания кода).
func TestConformanceSettingsLive(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	conf := SettingsPath(cfg)
	p := startStdio(t, installBinary(t, dir), cfg)
	result(t, p.call(1, "initialize", ""))
	if d := searchTopKDesc(t, result(t, p.call(2, "tools/list", ""))); !strings.Contains(d, "1..20") {
		t.Fatalf("без файла настроек ждали 1..20: %q", d)
	}
	pid := p.cmd.Process.Pid

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(conf, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("[limits]\nsearch_top_k = 8\n")
	if m := p.next(); m["method"] != "notifications/tools/list_changed" {
		t.Fatalf("после правки настроек ждали уведомление, пришло %v", m)
	}
	if d := searchTopKDesc(t, result(t, p.call(3, "tools/list", ""))); !strings.Contains(d, "1..8") {
		t.Errorf("новый предел не виден в описании: %q", d)
	}
	if p.cmd.Process.Pid != pid {
		t.Error("сменился номер процесса")
	}

	// Негодная правка: служба жива, предел прежний.
	write("[limits]\nsearch_top_k = 500\n")
	time.Sleep(2500 * time.Millisecond)
	if d := searchTopKDesc(t, result(t, p.call(4, "tools/list", ""))); !strings.Contains(d, "1..8") {
		t.Errorf("после негодной правки предел сменился: %q", d)
	}
}
