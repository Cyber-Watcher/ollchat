package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
)

// /kb source auto|service|local (этап 121, Д3): кто исполняет подмес, /search,
// /read и /kb use при kb.server_url. Службы здесь настоящие: kbserve за
// httptest и клиент kbremote, как в работе.

// liveKBService — живая служба над base и клиент к ней; hits — сколько
// запросов до неё дошло.
func liveKBService(t *testing.T, base *kb.Base) (*kbremote.Client, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	h := kbserve.Handler(kbserve.Opts{Base: base})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := kbremote.New(kbremote.Opts{URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, hits
}

// deadKBService — клиент к порту, где службы уже нет: отказ в соединении.
func deadKBService(t *testing.T) *kbremote.Client {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c, err := kbremote.New(kbremote.Opts{URL: url, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// runKBUse выполняет /kb use при службе так, как программа: запрос уходит
// в горутину команды, её ответ возвращается через Update.
func runKBUse(t *testing.T, m *Model, line string) {
	t.Helper()
	cmd := m.runCommand(line)
	if cmd == nil {
		t.Fatalf("%s при службе не ушёл в фон: %q", line, m.blocks[len(m.blocks)-1].text)
	}
	m.Update(cmd())
}

// local при живой службе: подмес и /search идут по своим файлам, служба не
// спрашивается вовсе, свой граф берётся из общего кэша и в модель не ложится;
// уход в service закрывает свой граф простоем кэша, а не держит его.
func TestKBSourceLocalSkipsLiveService(t *testing.T) {
	m, _ := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	svc, hits := liveKBService(t, m.kb.base)
	m.SetLibrary(svc)

	m.runCommand("/kb source local")
	if last := m.blocks[len(m.blocks)-1]; last.kind == blockError || !strings.Contains(last.text, "положение local") {
		t.Fatalf("/kb source local: %q", last.text)
	}
	mix := m.autoMix("как связаны goroutine и channel")
	if mix.Entities == 0 || mix.Remote != "" {
		t.Errorf("подмес в положении local: понятий %d, заметка %q", mix.Entities, mix.Remote)
	}
	if m.gr.open != nil || cache.Len() != 1 {
		t.Errorf("свой граф не через кэш: в модели %v, графов в кэше %d", m.gr.open, cache.Len())
	}
	done := runSearch(t, m, "/search goroutines channels")
	if done.err != nil || len(done.res.Excerpts) == 0 || strings.Contains(done.note, kb.FallbackNote) {
		t.Errorf("/search в положении local: err %v, выдержек %d, оговорка %q", done.err, len(done.res.Excerpts), done.note)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("в положении local служба спрошена %d раз", n)
	}

	m.runCommand("/kb source service")
	if m.gr.open != nil || !strings.Contains(m.blocks[len(m.blocks)-1].text, "простоя") {
		t.Errorf("уход из local: граф в модели %v, %q", m.gr.open, m.blocks[len(m.blocks)-1].text)
	}
}

// service при лежащей службе: подмес, /search, /read и /kb use — ошибка
// с пометкой о положении; свои файлы и свой граф не открываются.
func TestKBSourceServiceDownNoFallback(t *testing.T) {
	m, _ := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	m.SetLibrary(deadKBService(t))
	m.runCommand("/kb source service")

	mix := m.autoMix("как связаны goroutine и channel")
	if mix.Entities != 0 || !strings.Contains(mix.Remote, kb.ServiceOnlyNote) {
		t.Errorf("подмес в положении service при лежащей службе: понятий %d, заметка %q", mix.Entities, mix.Remote)
	}
	done := runSearch(t, m, "/search goroutines channels")
	if done.err == nil || !strings.Contains(done.err.Error(), kb.ServiceOnlyNote) {
		t.Errorf("/search в положении service: %v (оговорка %q)", done.err, done.note)
	}
	cmd := m.runCommand("/read go/1#1")
	if cmd == nil {
		t.Fatalf("/read в положении service не ушёл к службе: %q", m.blocks[len(m.blocks)-1].text)
	}
	if msg, ok := cmd().(errorMsg); !ok || !strings.Contains(msg.err.Error(), kb.ServiceOnlyNote) {
		t.Errorf("/read в положении service: %#v", msg)
	}
	runKBUse(t, m, "/kb use go")
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError || !strings.Contains(last.text, kb.ServiceOnlyNote) {
		t.Errorf("/kb use в положении service: %q", last.text)
	}
	if cache.Len() != 0 || m.gr.open != nil {
		t.Errorf("в положении service открыт свой граф: графов в кэше %d, в модели %v", cache.Len(), m.gr.open)
	}
}

// auto (положение при старте со службой) при лежащей службе: переход на свои
// файлы с заметкой, граф — из кэша.
func TestKBSourceAutoFallsBackWithNote(t *testing.T) {
	m, _ := remoteTestModel(t)
	cache := graph.NewCache(time.Minute, m.cfg.Graph.Rules())
	defer cache.Close()
	m.SetGraphCache(cache)
	m.SetLibrary(deadKBService(t))
	if m.kbMode() != kb.SourceAuto {
		t.Fatalf("положение при старте со службой %v, ждали auto", m.kbMode())
	}
	mix := m.autoMix("как связаны goroutine и channel")
	if mix.Entities == 0 || mix.Remote != kb.FallbackNote {
		t.Errorf("auto при лежащей службе: понятий %d, заметка %q", mix.Entities, mix.Remote)
	}
	if m.gr.open != nil || cache.Len() != 1 {
		t.Errorf("граф запасного пути: в модели %v, графов в кэше %d", m.gr.open, cache.Len())
	}
	done := runSearch(t, m, "/search goroutines channels")
	if done.err != nil || !strings.Contains(done.note, kb.FallbackNote) {
		t.Errorf("/search в auto при лежащей службе: err %v, оговорка %q", done.err, done.note)
	}
}

// Строка состояния: без службы значка нет и команда говорит, что службы нет;
// со службой — положение, и оно меняется командой. Переключатель тот же, что
// отдан снаружи (SetLibraryMode): его читают инструменты модели.
func TestStatusShowsKBSource(t *testing.T) {
	m := newTestModelWith(t, nil)
	m.width = 300
	if strings.Contains(m.statusView(), "kb: ") {
		t.Errorf("без службы в строке состояния положение: %q", m.statusView())
	}
	m.runCommand("/kb source")
	if last := m.blocks[len(m.blocks)-1]; last.kind == blockError || !strings.Contains(last.text, "службы нет") {
		t.Errorf("/kb source без службы: %q", last.text)
	}
	m.runCommand("/kb source service")
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError || !strings.Contains(last.text, "службы нет") {
		t.Errorf("/kb source service без службы: %q", last.text)
	}
	if m.kbMode() != kb.SourceLocal {
		t.Errorf("без службы положение %v, ждали local", m.kbMode())
	}

	sw := kb.NewSourceSwitch(kb.SourceAuto)
	m.SetLibraryMode(sw)
	m.SetLibrary(&fakeService{t: t})
	if !strings.Contains(m.statusView(), "kb: auto") {
		t.Errorf("со службой нет положения auto: %q", m.statusView())
	}
	for _, want := range []kb.SourceMode{kb.SourceLocal, kb.SourceService, kb.SourceAuto} {
		m.runCommand("/kb source " + want.String())
		if sw.Mode() != want || !strings.Contains(m.statusView(), "kb: "+want.String()) {
			t.Errorf("/kb source %s: у инструментов %v, строка %q", want, sw.Mode(), m.statusView())
		}
	}
	m.runCommand("/kb source nowhere")
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError || sw.Mode() != kb.SourceAuto {
		t.Errorf("неизвестное положение: %q, положение %v", last.text, sw.Mode())
	}
	m.runCommand("/kb source")
	if last := m.blocks[len(m.blocks)-1]; !strings.Contains(last.text, "положение auto") {
		t.Errorf("/kb source без довода: %q", last.text)
	}

	// Своих файлов нет — local выбрать нельзя.
	bare := newTestModelWith(t, nil)
	bare.kb.base = nil
	bare.SetLibrary(&fakeService{t: t})
	bare.runCommand("/kb source local")
	if last := bare.blocks[len(bare.blocks)-1]; last.kind != blockError || bare.kbMode() != kb.SourceAuto {
		t.Errorf("local без своих файлов: %q, положение %v", last.text, bare.kbMode())
	}
}

// /kb use при службе не ходит в сеть в цикле событий: команда отдаёт работу
// горутине, выбор ложится в модель с ответом службы, а ответ на прежний
// выбор, пришедший после нового, выбрасывается.
func TestKBUseRemoteRunsInBackground(t *testing.T) {
	_, coll := remoteTestModel(t)
	m := newTestModelWith(t, nil)
	svc := &fakeService{t: t, coll: coll}
	m.SetLibrary(svc)
	before := m.kb.use

	first := m.runCommand("/kb use go")
	if first == nil {
		t.Fatal("/kb use при службе не ушёл в фон")
	}
	if len(svc.asked) != 0 || m.kb.use != before {
		t.Errorf("/kb use спросил службу в цикле событий: спрошено %q, выбрано %q", svc.asked, m.kb.use)
	}
	second := m.runCommand("/kb use go")
	m.Update(first())
	if m.kb.use != before {
		t.Errorf("ответ на прежний выбор лёг поверх нового: %q", m.kb.use)
	}
	m.Update(second())
	if m.kb.use != "go" || len(svc.asked) != 2 {
		t.Errorf("ответ службы не лёг: выбрано %q, спрошено %q", m.kb.use, svc.asked)
	}
}
