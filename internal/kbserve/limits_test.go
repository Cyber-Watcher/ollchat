package kbserve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
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

func (g *blockingGraph) Search(ctx context.Context, _ GraphSearchRequest) (GraphSearchResponse, error) {
	g.calls.Add(1)
	g.entered <- struct{}{}
	select {
	case <-g.release:
		return GraphSearchResponse{}, nil
	case <-ctx.Done():
		return GraphSearchResponse{}, ctx.Err()
	}
}

func (g *blockingGraph) Graph(string) (*graph.Graph, graph.NeighborRank, func(), error) {
	return nil, graph.NeighborRank{}, nil, errors.New("графа нет")
}

// Очередь — не недоступность (решение владельца 23.10.2026): не дождавшийся
// места за предел службы получает 503 с признаком «занята» и временем
// ожидания, а дождавшийся — обычный ответ с queued_ms. Клиент может
// попросить ждать меньше заголовком.
func TestHeavySlotsBusyAnswer(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &blockingGraph{entered: make(chan struct{}, 4), release: make(chan struct{})}
	mux := Handler(Opts{Base: base, Graph: g, HeavySlots: 1, QueueWait: time.Hour})
	post := func(wait string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/graph/search",
			strings.NewReader(`{"query":"мьютекс"}`))
		r.Header.Set("Content-Type", "application/json")
		if wait != "" {
			r.Header.Set(QueueWaitHeader, wait)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- post("") }()
	<-g.entered

	started := time.Now()
	w := post("80") // клиент готов ждать 80 мс, предел службы — час
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("служба ждала %v вместо 80 мс, о которых просил клиент", took)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("занятая служба: код %d, ожидался 503", w.Code)
	}
	var busy BusyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &busy); err != nil || !busy.Busy || busy.QueuedMS < 80 {
		t.Errorf("тело 503 без признака «занята» или времени ожидания: %s (%v)", w.Body.String(), err)
	}
	if n := g.calls.Load(); n != 1 {
		t.Errorf("в тяжёлую часть вошло %d запросов при одном месте", n)
	}

	// Дождавшийся места узнаёт, сколько ждал.
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- post("") }()
	time.Sleep(60 * time.Millisecond)
	g.release <- struct{}{} // отпустить первый: место переходит второму
	<-g.entered
	g.release <- struct{}{}
	if w := <-first; w.Code != http.StatusOK {
		t.Fatalf("первый запрос: код %d", w.Code)
	}
	w = <-second
	var res GraphSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("второй запрос: код %d, тело %s", w.Code, w.Body.String())
	}
	if res.QueuedMS < 50 {
		t.Errorf("дождавшийся места не узнал, сколько ждал: queued_ms = %d", res.QueuedMS)
	}
}

// Предел ожидания: ноль — умолчание службы, отрицательное — без предела,
// заголовок клиента сокращает, но не удлиняет.
func TestQueueLimit(t *testing.T) {
	req := func(h string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/search", nil)
		if h != "" {
			r.Header.Set(QueueWaitHeader, h)
		}
		return r
	}
	cases := []struct {
		own    time.Duration
		header string
		want   time.Duration
	}{
		{0, "", queueWait},
		{-1, "", 0},
		{time.Second, "", time.Second},
		{0, "500", 500 * time.Millisecond},
		{time.Second, "5000", time.Second},
		{-1, "700", 700 * time.Millisecond},
		{0, "мусор", queueWait},
		{0, "-3", queueWait},
	}
	for _, c := range cases {
		if got := queueLimit(c.own, req(c.header)); got != c.want {
			t.Errorf("свой %v, заголовок %q → %v, ожидалось %v", c.own, c.header, got, c.want)
		}
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
