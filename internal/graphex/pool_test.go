package graphex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
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
		// Обрыв связи валит всё, а не только чат: узел, отвечающий на /api/version,
		// но роняющий чат, — это другой случай (карта занята), и он проверяется
		// отдельно.
		if atomic.LoadInt32(&n.fail) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"503 упал"}`))
			return
		}
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

	// Пока в строю есть кто-то ещё, откат соблюдается: узел, только что
	// выключенный, не возвращается той же секундой — иначе сервер, отвечающий
	// на /api/version, но роняющий чат, мигал бы без конца.
	p.revive(context.Background())
	for _, s := range p.Stats() {
		if s.Name == "упавший" && !s.Off {
			t.Fatal("узел вернулся в строй, не выждав отката")
		}
	}

	// Срок отката настал — узел возвращается.
	p.mu.Lock()
	for _, n := range p.nodes {
		n.nextTry = time.Now()
	}
	p.mu.Unlock()
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

// Когда живых узлов не осталось, пул ждёт их возвращения, а не роняет заход:
// узлы стоят за интернетом, и обрыв на пять минут не должен стоить ночной
// сборки.
func TestPoolWaitsForRevival(t *testing.T) {
	n := newFakeNode(t, 0)
	atomic.StoreInt32(&n.fail, 1)
	p := pool(t, Node{Name: "единственный", URL: n.srv.URL, Workers: 1})
	p.nodeWait = 10 * time.Second
	p.deadEvery = 10 * time.Millisecond

	// Один кусок: узел валится под ним, пул ждёт узла и доводит кусок сам.
	// Ошибку наверх отдавать нельзя — сборка на ней остановится.
	done := make(chan error, 1)
	go func() {
		_, err := p.Extract(context.Background(), "с", "в")
		done <- err
	}()

	// Пока связь не восстановилась, заход не падает.
	select {
	case err := <-done:
		t.Fatalf("пул сдался, не дождавшись возвращения узла: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	atomic.StoreInt32(&n.fail, 0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("узел вернулся, но кусок не разобран: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("пул не заметил возвращения узла")
	}
}

// Обрыв связи под сборкой. Сборка (build.go) на любой ошибке извлечения,
// кроме плохого ответа модели, ставит firstErr и снимает заход, поэтому
// проверять пул надо циклом того же вида, а не одиночными вызовами:
// до 06.09.2026 одиночные вызовы проходили, а сборка на первом же обрыве
// останавливалась — воркер, исчерпавший две попытки, отдавал ошибку наверх,
// пока остальные ждали узла.
func TestPoolSurvivesOutageUnderBuildLoop(t *testing.T) {
	n := newFakeNode(t, 2*time.Millisecond)
	p := pool(t, Node{Name: "основной", URL: n.srv.URL, Workers: 4})
	p.nodeWait = 10 * time.Second
	p.deadEvery = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const chunks = 60
	queue := make(chan int)
	var (
		mu       sync.Mutex
		firstErr error
		done     int32
		wg       sync.WaitGroup
	)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range queue {
				if ctx.Err() != nil {
					return
				}
				if _, err := p.Extract(ctx, "с", "в"); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					cancel() // так поступает сборка: stop() и выход
					return
				}
				atomic.AddInt32(&done, 1)
			}
		}()
	}
	// Обрыв посреди захода на 300 мс — пока воркеры держат запросы.
	go func() {
		for i := 0; i < chunks; i++ {
			if i == 10 {
				atomic.StoreInt32(&n.fail, 1)
				time.AfterFunc(300*time.Millisecond, func() { atomic.StoreInt32(&n.fail, 0) })
			}
			select {
			case queue <- i:
			case <-ctx.Done():
				close(queue)
				return
			}
		}
		close(queue)
	}()
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("обрыв на 300 мс остановил сборку: %v", firstErr)
	}
	if got := atomic.LoadInt32(&done); got != chunks {
		t.Fatalf("разобрано %d кусков из %d", got, chunks)
	}
}

// Срок node_wait считается от смерти пула, а не от прихода воркера в ожидание:
// иначе каждый новый воркер начинал бы отсчёт заново.
func TestNodeWaitCountsFromPoolDeath(t *testing.T) {
	n := newFakeNode(t, 0)
	atomic.StoreInt32(&n.fail, 1)
	p := pool(t, Node{Name: "единственный", URL: n.srv.URL, Workers: 1})
	p.nodeWait = 300 * time.Millisecond
	p.deadEvery = 10 * time.Millisecond

	// Первый воркер валит узел и ждёт полный срок.
	started := time.Now()
	if _, err := p.Extract(context.Background(), "с", "в"); err == nil {
		t.Fatal("мёртвый узел вернул ответ")
	}
	// Второй приходит к уже мёртвому пулу: ждать ему нечего — срок вышел.
	since := time.Now()
	if _, err := p.Extract(context.Background(), "с", "в"); err == nil {
		t.Fatal("мёртвый узел вернул ответ")
	}
	if waited := time.Since(since); waited > 150*time.Millisecond {
		t.Fatalf("второй воркер ждал %s заново, хотя пул мёртв с %s назад",
			waited, time.Since(started))
	}
}

// takeNode вынимает из оборота слот именно этого узла, возвращая чужие.
func takeNode(t *testing.T, p *Pool, want *poolNode) {
	t.Helper()
	for i := 0; i < 16; i++ {
		n, err := p.take(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		p.release(n)
	}
	t.Fatalf("слот узла %s не достался за 16 попыток", want.name)
}

// killNode выключает узел неудачами подряд, как это делает Extract.
func killNode(t *testing.T, p *Pool, n *poolNode) {
	t.Helper()
	takeNode(t, p, n)
	for i := 0; i < nodeFailLimit; i++ {
		p.fail(n, ollama.MarkRetryable(errors.New("упал")))
		if i < nodeFailLimit-1 {
			takeNode(t, p, n)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !n.off {
		t.Fatalf("узел %s не выключился после %d неудач", n.name, nodeFailLimit)
	}
}

// Снятие занятости у выключенного узла слотов ему не возвращает: их вернёт
// revive, когда сервер ответит на проверку. Иначе мёртвый пул оживал бы
// на мгновение, а воркеры начинали бы ожидание заново.
func TestPollKeepsOffNodeOut(t *testing.T) {
	body := probeForeign
	a, b := newFakeNode(t, 0), newFakeNode(t, 0)
	p := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 1, Probe: fakeProbe(t, &body)},
		Node{Name: "b", URL: b.srv.URL, Workers: 1})
	na := p.nodes[0]
	killNode(t, p, na)

	p.Poll(context.Background(), true) // занят чужим
	body = probeClean
	p.Poll(context.Background(), true) // освободился — но он выключен

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.left != 1 || na.out != 1 {
		t.Fatalf("выключенному узлу вернули слоты: в обороте %d, изъято у узла %d", p.left, na.out)
	}
}

// Возвращённый узел с занятой картой слотов не получает: их вернёт опрос
// наблюдателя, когда занятость снимется.
func TestReviveKeepsBusyNodeOut(t *testing.T) {
	body := probeForeign
	a, b := newFakeNode(t, 0), newFakeNode(t, 0)
	p := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 1, Probe: fakeProbe(t, &body)},
		Node{Name: "b", URL: b.srv.URL, Workers: 1})
	na := p.nodes[0]
	killNode(t, p, na)
	p.Poll(context.Background(), true) // занят чужим

	p.mu.Lock()
	na.nextTry = time.Time{} // откат не ждём
	p.mu.Unlock()
	p.revive(context.Background())

	p.mu.Lock()
	off, out, left := na.off, na.out, p.left
	p.mu.Unlock()
	if off {
		t.Fatal("узел не вернулся, хотя сервер отвечает")
	}
	if out != 1 || left != 1 {
		t.Fatalf("занятому узлу вернули слоты: изъято %d, в обороте %d", out, left)
	}

	body = probeClean
	p.Poll(context.Background(), true)
	p.mu.Lock()
	out, left = na.out, p.left
	p.mu.Unlock()
	if out != 0 || left != 2 {
		t.Fatalf("после снятия занятости слоты не вернулись: изъято %d, в обороте %d", out, left)
	}
}

// Предел ожидания честный: за ним заход останавливается, а не висит вечно.
func TestPoolGivesUpAfterNodeWait(t *testing.T) {
	n := newFakeNode(t, 0)
	atomic.StoreInt32(&n.fail, 1)
	p := pool(t, Node{Name: "единственный", URL: n.srv.URL, Workers: 1})
	p.nodeWait = 50 * time.Millisecond
	p.deadEvery = 10 * time.Millisecond

	var err error
	for i := 0; i < 5 && err == nil; i++ {
		_, err = p.Extract(context.Background(), "с", "в")
	}
	if err == nil {
		t.Fatal("пул не остановился по пределу ожидания")
	}
	if !strings.Contains(err.Error(), "не вернулись") && !strings.Contains(err.Error(), "503") {
		t.Errorf("невнятная ошибка: %v", err)
	}
}

// fakeProbe поднимает наблюдателя, отвечающего заданным снимком.
func fakeProbe(t *testing.T, body *string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

const probeClean = `{"gpus":[{"index":0,"name":"A100","mem_used_mib":100,"util_pct":2}],
	"gpu_procs":[{"pid":1,"name":"/usr/bin/ollama","used_mib":100,"ours":true}]}`

const probeForeign = `{"gpus":[{"index":0,"name":"RTX 3090","mem_used_mib":20000,"util_pct":97}],
	"gpu_procs":[{"pid":999,"name":"/usr/bin/python3","used_mib":20000,"ours":false}]}`

// Узел, чью карту занял чужой процесс, выводится из раздачи заранее —
// не дожидаясь трёх неудач и девяти запросов в очередь за чужой моделью.
func TestPoolParksBusyNode(t *testing.T) {
	a, b := newFakeNode(t, 0), newFakeNode(t, 0)
	clean, busy := probeClean, probeForeign
	p := pool(t,
		Node{Name: "a100", URL: a.srv.URL, Workers: 1, Probe: fakeProbe(t, &clean)},
		Node{Name: "rtx3090", URL: b.srv.URL, Workers: 1, Probe: fakeProbe(t, &busy)})

	p.pollProbes(context.Background())

	var parked, working int
	for _, s := range p.Stats() {
		switch s.Name {
		case "rtx3090":
			if s.Busy == "" {
				t.Error("узел с чужим процессом на карте продолжает брать работу")
			} else if !strings.Contains(s.Busy, "python3") {
				t.Errorf("причина невнятная: %q", s.Busy)
			}
			parked++
		case "a100":
			if s.Busy != "" {
				t.Errorf("чистый узел выведен: %q", s.Busy)
			}
			working++
		}
	}
	if parked != 1 || working != 1 {
		t.Fatalf("выведено %d, работает %d", parked, working)
	}

	// Вся работа уходит на чистый узел.
	for i := 0; i < 6; i++ {
		if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&b.calls); got != 0 {
		t.Errorf("выведенный узел получил %d кусков", got)
	}
	if got := atomic.LoadInt32(&a.calls); got != 6 {
		t.Errorf("чистый узел разобрал %d кусков из 6", got)
	}

	// Карта освободилась — узел возвращается сам.
	busy = probeClean
	p.pollProbes(context.Background())
	for _, s := range p.Stats() {
		if s.Name == "rtx3090" && s.Busy != "" {
			t.Fatalf("узел не вернулся по чистому снимку: %q", s.Busy)
		}
	}
	for i := 0; i < 8; i++ {
		if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&b.calls) == 0 {
		t.Error("вернувшийся узел не получил ни одного куска")
	}
}

// Последний работающий узел по занятости не выводится: на единственной карте
// «занято» означает «медленнее», а не «нельзя». Иначе наблюдатель
// останавливал бы сборку вместо того, чтобы её беречь.
func TestPoolKeepsLastNodeDespiteBusy(t *testing.T) {
	a := newFakeNode(t, 0)
	busy := probeForeign
	p := pool(t, Node{Name: "один", URL: a.srv.URL, Workers: 1, Probe: fakeProbe(t, &busy)})

	p.pollProbes(context.Background())
	for _, s := range p.Stats() {
		if s.Busy != "" {
			t.Fatalf("единственный узел выведен из-за занятости: %q", s.Busy)
		}
	}
	if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
		t.Fatalf("работа встала на единственном узле: %v", err)
	}

	// И то же самое, когда заняты все узлы разом: кто-то обязан работать.
	b := newFakeNode(t, 0)
	busy2 := probeForeign
	p2 := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 1, Probe: fakeProbe(t, &busy)},
		Node{Name: "b", URL: b.srv.URL, Workers: 1, Probe: fakeProbe(t, &busy2)})
	p2.pollProbes(context.Background())
	free := 0
	for _, s := range p2.Stats() {
		if s.Busy == "" {
			free++
		}
	}
	if free == 0 {
		t.Error("заняты все узлы — и работать стало некому")
	}
}

// Недоступный наблюдатель ничего не ломает: данные вспомогательные.
func TestPoolSurvivesDeadProbe(t *testing.T) {
	a := newFakeNode(t, 0)
	p := pool(t, Node{Name: "один", URL: a.srv.URL, Workers: 1,
		Probe: "http://127.0.0.1:1"})

	p.pollProbes(context.Background())
	if _, err := p.Extract(context.Background(), "с", "в"); err != nil {
		t.Fatalf("мёртвый наблюдатель остановил работу: %v", err)
	}
	for _, s := range p.Stats() {
		if s.Busy != "" {
			t.Errorf("узел выведен по недоступному наблюдателю: %q", s.Busy)
		}
	}
}

// Вытеснение модели в оперативную память — тоже повод не давать узлу работу:
// он исправен, но втрое медленнее.
func TestPoolParksEvictedNode(t *testing.T) {
	a, b := newFakeNode(t, 0), newFakeNode(t, 0)
	clean := probeClean
	evicted := `{"gpus":[{"index":0,"name":"RTX 3090","mem_used_mib":20000,"util_pct":30}],
		"models":[{"name":"qwen3.8:latest","size":19000000000,"size_vram":6000000000,
		"size_ram":13000000000,"vram_pct":31}]}`
	p := pool(t,
		Node{Name: "a100", URL: a.srv.URL, Workers: 1, Probe: fakeProbe(t, &clean)},
		Node{Name: "rtx3090", URL: b.srv.URL, Workers: 1, Probe: fakeProbe(t, &evicted)})

	p.pollProbes(context.Background())
	for _, s := range p.Stats() {
		if s.Name == "rtx3090" && !strings.Contains(s.Busy, "вытеснена") {
			t.Errorf("вытеснение не поймано: %q", s.Busy)
		}
	}
}
