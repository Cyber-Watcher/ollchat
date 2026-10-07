package kbserve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// Чужие веб-страницы не достают до службы: ни межсайтовым запросом
// (Origin), ни подменой DNS (Host на петле), ни «простым» POST без JSON.
// Свои клиенты — ollchat, curl, клиенты MCP — Origin не шлют и ходят по
// имени, на котором служба слушает, и проходят.
func TestProtect(t *testing.T) {
	cases := []struct {
		name     string
		loopback bool
		method   string
		host     string
		origin   string
		ctype    string
		want     int
	}{
		{"свой клиент на петле", true, "POST", "127.0.0.1:8377", "", "application/json", 200},
		{"localhost с портом", true, "POST", "localhost:8377", "", "application/json", 200},
		{"LOCALHOST", true, "GET", "LOCALHOST:8377", "", "", 200},
		{"ipv6 петля", true, "GET", "[::1]:8377", "", "", 200},
		{"json с charset", true, "POST", "127.0.0.1:8377", "", "application/json; charset=utf-8", 200},
		{"GET без Content-Type", true, "GET", "127.0.0.1:8377", "", "", 200},
		{"подмена DNS", true, "POST", "attacker.example:8377", "http://attacker.example:8377", "application/json", 403},
		{"подменённое имя без Origin", true, "GET", "attacker.example:8377", "", "", 403},
		{"пустой Host на петле", true, "GET", "", "", "", 403},
		{"чужая страница", true, "POST", "127.0.0.1:8377", "https://evil.example", "application/json", 403},
		{"чужая страница GET", true, "GET", "127.0.0.1:8377", "https://evil.example", "", 403},
		{"Origin null", true, "POST", "127.0.0.1:8377", "null", "application/json", 403},
		{"Origin своей службы", true, "POST", "localhost:8377", "http://localhost:8377", "application/json", 200},
		{"простой POST text/plain", true, "POST", "127.0.0.1:8377", "", "text/plain", 415},
		{"POST формой", true, "POST", "127.0.0.1:8377", "", "application/x-www-form-urlencoded", 415},
		{"POST без Content-Type", true, "POST", "127.0.0.1:8377", "", "", 415},
		{"сеть с ключом: любое имя", false, "POST", "kb.corp.local:8377", "", "application/json", 200},
		{"сеть: чужая страница", false, "POST", "kb.corp.local:8377", "https://evil.example", "application/json", 403},
		{"сеть: простой POST", false, "POST", "kb.corp.local:8377", "", "text/plain", 415},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/api/v1/search", strings.NewReader("{}"))
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.ctype != "" {
			r.Header.Set("Content-Type", c.ctype)
		}
		w := httptest.NewRecorder()
		Protect(okHandler(), c.loopback).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: код %d, ожидался %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
}

// Сервер службы собирается уже под защитой: проверка не должна зависеть
// от того, не забыл ли её очередной вызывающий.
func TestNewHTTPServerIsProtected(t *testing.T) {
	srv := NewHTTPServer(okHandler(), true)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/health", nil)
	req.Host = "attacker.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("подменённое имя: код %d, ожидался 403", resp.StatusCode)
	}

	resp, err = http.Post(ts.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("свой клиент: код %d, ожидался 200", resp.StatusCode)
	}
}
