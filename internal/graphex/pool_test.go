package graphex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// узел изображает сервер Ollama целиком: версия, список моделей и чат.
// Умеет отвечать медленно и умеет отказывать — этим проверяются раздача
// по освобождению слота и выключение мёртвого узла.
type fakeNode struct {
	srv    *httptest.Server
	calls  int32 // сколько кусков разобрано
	fail   int32 // 1 — отвечать 503 на чат
	delay  time.Duration
	digest string
	quant  string
}

func newFakeNode(t *testing.T, delay time.Duration) *fakeNode {
	t.Helper()
	n := &fakeNode{delay: delay, digest: "aaaabbbbccccdddd", quant: "Q4_K_M"}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.32.13"})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{
				"name": "проба", "model": "проба", "size": 17,
				"digest":  n.digest,
				"details": map[string]any{"quantization_level": n.quant},
			}}})
		case strings.HasSuffix(r.URL.Path, "/api/chat"):
			if atomic.LoadInt32(&n.fail) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"503 упал"}`))
				return
			}
			if n.delay > 0 {
				time.Sleep(n.delay)
			}
			atomic.AddInt32(&n.calls, 1)
			w.Header().Set("Content-Type", "application/x-ndjson")
			enc := json.NewEncoder(w)
			_ = enc.Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": "{}"}, "done": false})
			_ = enc.Encode(map[string]any{"done": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(n.srv.Close)
	return n
}

// pool собирает пул по узлам с быстрым повтором: иначе проверка выключения
// узла ждала бы по шесть секунд на каждую неудачу.
func pool(t *testing.T, nodes ...Node) *Pool {
	t.Helper()
	p, err := NewPool(nodes, Options{Model: "проба"}, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.nodes {
		n.ex.retryPause = time.Millisecond
	}
	return p
}

// Быстрый узел разбирает больше кусков, чем медленный: работа раздаётся
// по освобождению слота, а не поровну.
func TestPoolBalancesByFreeSlot(t *testing.T) {
	fast := newFakeNode(t, 2*time.Millisecond)
	slow := newFakeNode(t, 20*time.Millisecond)
	p := pool(t,
		Node{Name: "быстрый", URL: fast.srv.URL, Workers: 1},
		Node{Name: "медленный", URL: slow.srv.URL, Workers: 1})

	done := make(chan struct{})
	for i := 0; i < 40; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
				t.Errorf("извлечение: %v", err)
			}
		}()
	}
	for i := 0; i < 40; i++ {
		<-done
	}

	f, s := atomic.LoadInt32(&fast.calls), atomic.LoadInt32(&slow.calls)
	if f+s != 40 {
		t.Fatalf("разобрано %d+%d, ожидалось 40", f, s)
	}
	if f <= s {
		t.Errorf("быстрый узел взял %d кусков, медленный %d — раздача не по освобождению слота", f, s)
	}
}

// Узел, отказывающий подряд, выключается, и работа уходит на живой.
func TestPoolDisablesFailingNode(t *testing.T) {
	good := newFakeNode(t, 0)
	bad := newFakeNode(t, 0)
	atomic.StoreInt32(&bad.fail, 1)

	p := pool(t,
		Node{Name: "живой", URL: good.srv.URL, Workers: 1},
		Node{Name: "мёртвый", URL: bad.srv.URL, Workers: 1})

	for i := 0; i < 20; i++ {
		if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
			t.Fatalf("кусок %d: %v", i, err)
		}
	}

	var off bool
	for _, s := range p.Stats() {
		if s.Name == "мёртвый" {
			off = s.Off
		}
	}
	if !off {
		t.Error("отказывающий узел остался в строю")
	}
	if got := atomic.LoadInt32(&good.calls); int(got) != 20 {
		t.Errorf("живой узел разобрал %d кусков из 20", got)
	}
}

// Когда живых узлов не осталось, пул отказывает, а не виснет на пустом канале.
func TestPoolDiesWhenAllNodesDown(t *testing.T) {
	bad := newFakeNode(t, 0)
	atomic.StoreInt32(&bad.fail, 1)
	p := pool(t, Node{Name: "единственный", URL: bad.srv.URL, Workers: 1})

	deadline := time.After(10 * time.Second)
	errc := make(chan error, 1)
	go func() {
		var last error
		for i := 0; i < 10; i++ {
			if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
				last = err
				break
			}
		}
		errc <- last
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("пул мёртвых узлов не вернул ошибки")
		}
	case <-deadline:
		t.Fatal("пул завис на пустом канале слотов")
	}
}

// Выключенный узел возвращается в строй, когда сервер ожил.
func TestPoolRevivesNode(t *testing.T) {
	good := newFakeNode(t, 0)
	bad := newFakeNode(t, 0)
	atomic.StoreInt32(&bad.fail, 1)
	p := pool(t,
		Node{Name: "живой", URL: good.srv.URL, Workers: 1},
		Node{Name: "упавший", URL: bad.srv.URL, Workers: 1})

	for i := 0; i < 10; i++ {
		if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
			t.Fatal(err)
		}
	}
	atomic.StoreInt32(&bad.fail, 0)
	p.revive(context.Background())

	for _, s := range p.Stats() {
		if s.Name == "упавший" && s.Off {
			t.Fatal("узел не вернулся в строй после проверки")
		}
	}
	before := atomic.LoadInt32(&bad.calls)
	for i := 0; i < 20; i++ {
		if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&bad.calls) == before {
		t.Error("вернувшийся узел не получил ни одного куска")
	}
}

// Разные веса одноимённой модели на узлах — отказ до начала сборки.
func TestPoolCheckRejectsDifferentWeights(t *testing.T) {
	a := newFakeNode(t, 0)
	b := newFakeNode(t, 0)
	b.digest = "0000111122223333"

	p := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 1},
		Node{Name: "b", URL: b.srv.URL, Workers: 1})
	err := p.Check(context.Background())
	if err == nil {
		t.Fatal("разные веса модели приняты")
	}
	if !strings.Contains(err.Error(), "разные веса") {
		t.Errorf("невнятный отказ: %v", err)
	}

	b.digest, b.quant = a.digest, "Q8_0"
	err = p.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "квантование") {
		t.Errorf("разное квантование не поймано: %v", err)
	}
}

// Одинаковые веса — проверка проходит; узел без модели назван по имени.
func TestPoolCheck(t *testing.T) {
	a := newFakeNode(t, 0)
	b := newFakeNode(t, 0)
	p := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 2},
		Node{Name: "b", URL: b.srv.URL, Workers: 3})
	if err := p.Check(context.Background()); err != nil {
		t.Fatalf("одинаковые узлы не приняты: %v", err)
	}
	if p.Slots() != 5 {
		t.Errorf("слотов %d, ожидалось 5", p.Slots())
	}
	if got := strings.Join(p.Names(), ","); got != "a,b" {
		t.Errorf("имена узлов %q", got)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	p2 := pool(t, Node{Name: "a", URL: a.srv.URL, Workers: 1}, Node{Name: "молчун", URL: dead.URL, Workers: 1})
	err := p2.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "молчун") {
		t.Errorf("недоступный узел не назван по имени: %v", err)
	}
}

// Пул из одного узла ведёт себя как обычный извлекатель.
func TestPoolOfOneNode(t *testing.T) {
	n := newFakeNode(t, 0)
	p := pool(t, Node{Name: "один", URL: n.srv.URL, Workers: 2})
	if p.Model() != "проба" {
		t.Errorf("модель %q", p.Model())
	}
	got, err := p.Extract(context.Background(), "с", "в")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != "{}" {
		t.Errorf("ответ %q", got)
	}
	if !strings.Contains(p.Line(), "один 1") {
		t.Errorf("строка хода без узла: %q", p.Line())
	}
}

// Пул отказывается собираться на дурных настройках.
func TestNewPoolRejectsBadNodes(t *testing.T) {
	cases := []struct {
		name  string
		nodes []Node
	}{
		{"без узлов", nil},
		{"без имени", []Node{{URL: "http://ollama.example:11434", Workers: 1}}},
		{"без адреса", []Node{{Name: "a", Workers: 1}}},
		{"имя дважды", []Node{
			{Name: "a", URL: "http://ollama.example:11434"},
			{Name: "a", URL: "http://ollama.example:11435"}}},
	}
	for _, c := range cases {
		if _, err := NewPool(c.nodes, Options{Model: "проба"}, time.Second, nil); err == nil {
			t.Errorf("%s: пул собрался", c.name)
		}
	}
	if _, err := NewPool([]Node{{Name: "a", URL: "http://ollama.example:11434"}},
		Options{}, time.Second, nil); err == nil {
		t.Error("пул собрался без модели извлечения")
	}
}

// Отмена не считается виной узла: он остаётся в строю.
func TestPoolCancelKeepsNode(t *testing.T) {
	n := newFakeNode(t, 50*time.Millisecond)
	p := pool(t, Node{Name: "один", URL: n.srv.URL, Workers: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := p.Extract(ctx, "с", "в"); err == nil {
		t.Fatal("отменённый запрос вернул ответ")
	}
	for _, s := range p.Stats() {
		if s.Off {
			t.Errorf("узел %s выключен из-за отмены", s.Name)
		}
	}
	// Слот вернулся: следующий запрос не должен ждать вечно.
	done := make(chan error, 1)
	go func() {
		_, err := p.Extract(context.Background(), "с", "в")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("после отмены узел не работает: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("слот не вернулся в оборот после отмены")
	}
}
