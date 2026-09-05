package confluence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pageServer(t *testing.T, status int) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/child/attachment"):
			_, _ = w.Write([]byte(`{"results":[{"title":"схема.png","metadata":{"mediaType":"image/png"},"extensions":{"fileSize":1234}}]}`))
		case strings.Contains(r.URL.Path, "/child/page"):
			_, _ = w.Write([]byte(`{"results":[{"id":"124","title":"Дочерняя"}]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"123","title":"Заголовок","space":{"key":"DOC"},` +
				`"version":{"number":7,"when":"2026-09-01","by":{"displayName":"Автор"}},` +
				`"body":{"storage":{"value":"<p>Текст страницы</p>"}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestGetPageWithChildrenAndFiles(t *testing.T) {
	srv, auth := pageServer(t, http.StatusOK)
	c := New(srv.URL, func() string { return "секрет" }, 5*time.Second)
	p, err := c.Get(context.Background(), "https://wiki.example/pages/viewpage.action?pageId=123", true)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "123" || p.Title != "Заголовок" || p.Space != "DOC" || p.Version != 7 || p.Author != "Автор" {
		t.Fatalf("страница: %+v", p)
	}
	if len(p.Files) != 1 || p.Files[0].Title != "схема.png" || p.Files[0].Size != 1234 {
		t.Fatalf("вложения: %+v", p.Files)
	}
	if len(p.Children) != 1 || p.Children[0].ID != "124" {
		t.Fatalf("дети: %+v", p.Children)
	}
	if *auth != "Bearer секрет" {
		t.Fatalf("токен ушёл не так: %q", *auth)
	}
	md, err := p.Markdown()
	if err != nil || !strings.Contains(md, "Текст страницы") {
		t.Fatalf("markdown: %q, %v", md, err)
	}
}

func TestGetErrorsNeverLeakToken(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "не пустил"},
		{http.StatusForbidden, "не пустил"},
		{http.StatusNotFound, "не найдена"},
		{http.StatusInternalServerError, "ответил 500"},
	} {
		srv, _ := pageServer(t, tc.status)
		c := New(srv.URL, func() string { return "секрет-токен" }, 5*time.Second)
		_, err := c.Get(context.Background(), "123", false)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("код %d: ошибка %v, ожидалось %q", tc.status, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "секрет") {
			t.Errorf("код %d: токен попал в текст ошибки: %v", tc.status, err)
		}
		if err != nil && strings.Contains(err.Error(), "expand=") {
			t.Errorf("код %d: параметры запроса попали в ошибку: %v", tc.status, err)
		}
	}
}

func TestGetWithoutTokenDoesNotCallServer(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	c := New(srv.URL, func() string { return "  " }, time.Second)
	if _, err := c.Get(context.Background(), "123", false); err == nil || !strings.Contains(err.Error(), "токен") {
		t.Fatalf("без токена ожидался понятный отказ: %v", err)
	}
	if called {
		t.Fatal("без токена запрос на сервер уходить не должен")
	}
	if _, err := New("", nil, 0).Get(context.Background(), "123", false); err == nil {
		t.Fatal("без адреса ожидался отказ")
	}
}

func TestTokenFromFileChecksPermissions(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open")
	if err := os.WriteFile(open, []byte("t1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TokenFromFile(open); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("файл 0644 должен отклоняться с подсказкой: %v", err)
	}
	closed := filepath.Join(dir, "closed")
	if err := os.WriteFile(closed, []byte("  t2  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := TokenFromFile(closed); err != nil || got != "t2" {
		t.Fatalf("файл 0600: %q, %v", got, err)
	}
	if _, err := TokenFromFile(filepath.Join(dir, "нет")); err == nil {
		t.Fatal("отсутствующий файл должен давать ошибку")
	}
}

func TestTokenFromCmd(t *testing.T) {
	got, err := TokenFromCmd(context.Background(), "printf ' из-команды '")
	if err != nil || got != "из-команды" {
		t.Fatalf("%q, %v", got, err)
	}
	if _, err := TokenFromCmd(context.Background(), "exit 3"); err == nil {
		t.Fatal("сбой команды должен быть ошибкой")
	}
}

// Порядок добытчика: сеанс, файл, команда, переменная окружения.
func TestResolverOrder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")
	if err := os.WriteFile(file, []byte("из-файла"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLCHAT_TEST_CONF_TOKEN", "из-окружения")

	sess := &Session{}
	get := Resolver(sess, file, "printf из-команды", "OLLCHAT_TEST_CONF_TOKEN")
	if got := get(); got != "из-файла" {
		t.Fatalf("файл главнее команды и окружения: %q", got)
	}
	sess.Set("из-сеанса")
	if got := get(); got != "из-сеанса" {
		t.Fatalf("сеанс главнее всего: %q", got)
	}
	sess.Clear()
	if got := Resolver(nil, "", "printf из-команды", "OLLCHAT_TEST_CONF_TOKEN")(); got != "из-команды" {
		t.Fatalf("команда главнее окружения: %q", got)
	}
	if got := Resolver(nil, "", "", "OLLCHAT_TEST_CONF_TOKEN")(); got != "из-окружения" {
		t.Fatalf("окружение — последнее: %q", got)
	}
	if got := Resolver(nil, "", "", "")(); got != "" {
		t.Fatalf("без источников — пусто: %q", got)
	}
}
