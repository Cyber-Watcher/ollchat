package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	c := New(srv.URL, fixedToken("секрет"), 5*time.Second)
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
		c := New(srv.URL, fixedToken("секрет-токен"), 5*time.Second)
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
	c := New(srv.URL, fixedToken("  "), time.Second)
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
	if got, _ := get(); got != "из-файла" {
		t.Fatalf("файл главнее команды и окружения: %q", got)
	}
	sess.Set("из-сеанса")
	if got, _ := get(); got != "из-сеанса" {
		t.Fatalf("сеанс главнее всего: %q", got)
	}
	sess.Clear()
	if got, _ := Resolver(nil, "", "printf из-команды", "OLLCHAT_TEST_CONF_TOKEN")(); got != "из-команды" {
		t.Fatalf("команда главнее окружения: %q", got)
	}
	if got, _ := Resolver(nil, "", "", "OLLCHAT_TEST_CONF_TOKEN")(); got != "из-окружения" {
		t.Fatalf("окружение — последнее: %q", got)
	}
	if got, err := Resolver(nil, "", "", "")(); got != "" || err != nil {
		t.Fatalf("без источников — пусто и без ошибки: %q, %v", got, err)
	}
}

// listServer отдаёт страницу с files вложениями и kids детьми — постранично,
// как Confluence: results по limit штук, начиная со start, и _links.next,
// пока есть ещё.
func listServer(t *testing.T, files, kids int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total, kind := 0, ""
		switch {
		case strings.HasSuffix(r.URL.Path, "/child/attachment"):
			total, kind = files, "файл"
		case strings.HasSuffix(r.URL.Path, "/child/page"):
			total, kind = kids, "ребёнок"
		default:
			_, _ = w.Write([]byte(`{"id":"123","title":"Т","body":{"storage":{"value":"<p>ок</p>"}}}`))
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var results []map[string]any
		for i := start; i < total && i < start+limit; i++ {
			results = append(results, map[string]any{"id": strconv.Itoa(1000 + i),
				"title": fmt.Sprintf("%s %d", kind, i)})
		}
		resp := map[string]any{"results": results, "size": len(results), "_links": map[string]any{}}
		if start+limit < total {
			resp["_links"] = map[string]any{"next": fmt.Sprintf("%s?limit=%d&start=%d", r.URL.Path, limit, start+limit)}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Вложения и дети читаются постранично, а не первой страницей: прежде всё
// после 50 вложений и 100 детей пропадало молча, и список выглядел полным.
// Сверх предела список помечен неполным.
func TestListsArePaginated(t *testing.T) {
	srv := listServer(t, 120, maxChildren+30)
	p, err := New(srv.URL, fixedToken("т"), 5*time.Second).Get(context.Background(), "123", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 120 || p.FilesCut {
		t.Errorf("вложений %d (неполон %v), ожидалось все 120", len(p.Files), p.FilesCut)
	} else if p.Files[119].Title != "файл 119" {
		t.Errorf("последнее вложение %q", p.Files[119].Title)
	}
	if len(p.Children) != maxChildren || !p.ChildrenCut {
		t.Errorf("детей %d (неполон %v), ожидалось %d и пометка", len(p.Children), p.ChildrenCut, maxChildren)
	}
	md, err := p.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, fmt.Sprintf("список неполон: показаны первые %d", maxChildren)) {
		t.Errorf("неполный список детей не помечен")
	}
	if strings.Count(md, "список неполон") != 1 {
		t.Errorf("полный список вложений помечен неполным")
	}
}

func fixedToken(tok string) func() (string, error) {
	return func() (string, error) { return tok, nil }
}

// Файл с токеном, открытый всем, не пропускается молча: подсказка
// «chmod 600» доходит до человека через ошибку инструмента. Прежде
// добытчик глотал её, и человек слышал «токен не задан» при заданном файле.
func TestResolverSurfacesFileError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(file, []byte("секрет-из-файла"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolver(nil, file, "", "")(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("ошибка прав файла потеряна: %v", err)
	}
	srv, _ := pageServer(t, http.StatusOK)
	_, err := New(srv.URL, Resolver(nil, file, "", ""), time.Second).Get(context.Background(), "123", false)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("инструмент не объяснил, что не так с файлом: %v", err)
	}
	if strings.Contains(err.Error(), "секрет-из-файла") {
		t.Fatalf("токен попал в текст ошибки: %v", err)
	}
	// Есть другой источник — работаем им, как прежде.
	t.Setenv("OLLCHAT_TEST_CONF_TOKEN", "из-окружения")
	if got, err := Resolver(nil, file, "", "OLLCHAT_TEST_CONF_TOKEN")(); got != "из-окружения" || err != nil {
		t.Fatalf("запасной источник не сработал: %q, %v", got, err)
	}
}

// token_cmd запускается один раз на клиента, а не на каждый HTTP-запрос:
// страница с вложениями и детьми — три запроса, и прежде три запуска `sh -c`.
func TestTokenCmdRunsOncePerClient(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "запуски")
	cmd := "echo x >> '" + counter + "'; printf секрет"
	srv, _ := pageServer(t, http.StatusOK)
	c := New(srv.URL, Resolver(nil, "", cmd, ""), 5*time.Second)
	if _, err := c.Get(context.Background(), "123", true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Errorf("команда за токеном запущена %d раз на одну страницу", n)
	}
}

// На 401 токен спрашивается у источника заново, один раз: он мог истечь
// или смениться. Тот же токен повторно не шлётся.
func TestTokenRefreshedOnUnauthorized(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer новый" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"123","title":"Т","body":{"storage":{"value":"<p>ок</p>"}}}`))
	}))
	t.Cleanup(srv.Close)

	asked := 0
	rotating := func() (string, error) {
		asked++
		if asked == 1 {
			return "старый", nil
		}
		return "новый", nil
	}
	if _, err := New(srv.URL, rotating, time.Second).Get(context.Background(), "123", false); err != nil {
		t.Fatalf("сменившийся токен не подхвачен: %v", err)
	}
	if asked != 2 {
		t.Errorf("источник спрошен %d раз, ожидалось 2", asked)
	}

	hits = 0
	_, err := New(srv.URL, fixedToken("старый"), time.Second).Get(context.Background(), "123", false)
	if err == nil || !strings.Contains(err.Error(), "не пустил") {
		t.Fatalf("неверный токен должен давать отказ: %v", err)
	}
	if hits != 1 {
		t.Errorf("тот же токен отправлен повторно: запросов %d", hits)
	}
}
