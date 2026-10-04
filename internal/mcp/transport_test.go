package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postMCP(t *testing.T, mux *http.ServeMux, body, session, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	if session != "" {
		req.Header.Set(sessionHeader, session)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// HTTP: сеанс, начатый у прежнего экземпляра службы, узнаёт о возможной смене
// набора ровно один раз на каждый перезапуск, а вызов при этом выполняется
// (этап 109, А1). Сценарий A→B→A — тот, на котором 04.10.2026 живьём
// провалился первый вариант со сравнением отпечатков: возврат к набору A
// клиенту не сообщался, и у него оставался набор B.
func TestHTTPToolsChangedAfterRestart(t *testing.T) {
	const both = "application/json, text/event-stream"
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	isStream := func(rec *httptest.ResponseRecorder) bool {
		return rec.Header().Get("Content-Type") == "text/event-stream"
	}

	a := http.NewServeMux()
	MountHTTP(a, server(), "k", false)
	rec := postMCP(t, a, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "", both)
	session := rec.Header().Get(sessionHeader)
	if session == "" {
		t.Fatal("initialize не выдал номер сеанса")
	}
	if isStream(postMCP(t, a, list, session, both)) {
		t.Fatal("свой же сеанс получил уведомление")
	}

	// Перезапуск с набором B.
	b := http.NewServeMux()
	MountHTTP(b, server(probe()), "k", false)
	// Клиент, не понимающий потока (curl из скриптов), получает прежний JSON.
	if isStream(postMCP(t, b, list, session, "application/json")) {
		t.Fatal("клиенту без text/event-stream ушёл поток")
	}
	rec = postMCP(t, b, list, session, both)
	if !isStream(rec) {
		t.Fatalf("после перезапуска нет потока: %q", rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	note := strings.Index(body, "notifications/tools/list_changed")
	answer := strings.Index(body, `"id":2`)
	if note < 0 || answer < 0 || note > answer {
		t.Fatalf("в потоке нет уведомления перед ответом:\n%s", body)
	}
	if !strings.Contains(body, "проба") {
		t.Fatalf("ответ не со свежим набором:\n%s", body)
	}
	if isStream(postMCP(t, b, list, session, both)) {
		t.Fatal("уведомление повторилось в том же экземпляре")
	}

	// Перезапуск обратно с набором A: отпечаток совпал бы с исходным,
	// но у клиента набор B — уведомить обязаны.
	a2 := http.NewServeMux()
	MountHTTP(a2, server(), "k", false)
	if !isStream(postMCP(t, a2, list, session, both)) {
		t.Fatal("возврат к прежнему набору клиенту не сообщён")
	}
}
