package kbserve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Числа отбора приводятся к пределам службы: ноль и отрицательное — умолчание
// сервера (прежде отрицательный max_per_book снимал предел на книгу вовсе),
// огромное — потолок.
func TestSearchOptsClamp(t *testing.T) {
	cases := []struct{ topK, perBook, wantTopK, wantPerBook int }{
		{0, 0, 0, 0},
		{-5, -1, 0, 0},
		{12, 3, 12, 3},
		{1_000_000, 500, maxTopK, maxTopK},
	}
	for _, c := range cases {
		fo := searchOpts(SearchRequest{TopK: c.topK, MaxPerBook: c.perBook}, "books", Opts{})
		if fo.TopK != c.wantTopK || fo.MaxPerBook != c.wantPerBook {
			t.Errorf("top_k %d, max_per_book %d → %d, %d; ожидалось %d, %d",
				c.topK, c.perBook, fo.TopK, fo.MaxPerBook, c.wantTopK, c.wantPerBook)
		}
	}
}

// blockingGraph держит каждый поиск по графу, пока тест не отпустит.
type blockingGraph struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (g *blockingGraph) Tool(context.Context, string, string, map[string]any) (string, error) {
	return "", nil
}

func (g *blockingGraph) Search(ctx context.Context, _, _ string) (any, error) {
	g.calls.Add(1)
	g.entered <- struct{}{}
	select {
	case <-g.release:
		return "ok", nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Тяжёлых запросов разом не больше HeavySlots, а ждущий в очереди уходит
// вместе с клиентом: в тяжёлую часть его запрос не попадает вовсе.
func TestHeavySlotsWaitHonorsClient(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &blockingGraph{entered: make(chan struct{}, 4), release: make(chan struct{})}
	mux := Handler(Opts{Base: base, Graph: g, HeavySlots: 1})
	post := func(ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/graph/search",
			strings.NewReader(`{"query":"мьютекс"}`)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	first := make(chan int, 1)
	go func() { first <- post(context.Background()).Code }()
	<-g.entered // первый занял единственное место

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if code := post(ctx).Code; code != http.StatusServiceUnavailable {
		t.Errorf("ушедший из очереди клиент: код %d, ожидался 503", code)
	}
	if n := g.calls.Load(); n != 1 {
		t.Errorf("в тяжёлую часть вошло %d запросов при одном месте", n)
	}

	close(g.release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("первый запрос: код %d", code)
	}
	if code := post(context.Background()).Code; code != http.StatusOK {
		t.Errorf("после освобождения места: код %d", code)
	}
}

// Сервер службы ограничивает чтение запроса и простой соединения: прежде
// стоял только ReadHeaderTimeout, и тело по байту держало соединение вечно.
func TestHTTPServerTimeouts(t *testing.T) {
	srv := NewHTTPServer(okHandler(), true)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("сроки: заголовки %v, запрос %v, простой %v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout %v оборвёт честно долгий ответ (холодный граф)", srv.WriteTimeout)
	}
}
