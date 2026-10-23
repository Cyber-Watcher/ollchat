package tools

import (
	"context"
	"errors"
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

// Положение /kb source (этап 121, Д3): один живой переключатель решает, кто
// исполняет search, kb_* и graph_* при общей библиотеке — служба с запасным
// путём (auto), только служба (service) или только свои файлы (local). Имена
// и описания инструментов во всех положениях одни, меняется исполнитель.

// countingService — живая служба над base (kbserve за httptest) и настоящий
// клиент kbremote к ней; hits — сколько запросов дошло до службы.
func countingService(t *testing.T, base *kb.Base) (*kbremote.Client, *atomic.Int64) {
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

// deadService — настоящий клиент kbremote к порту, где службы уже нет:
// отказ в соединении, драйвер метит его kb.ErrUnreachable.
func deadService(t *testing.T) *kbremote.Client {
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

// modeRegistry — реестр search, kb_search, kb_read и graph_search при общей
// библиотеке lib (search — по MCP через rc), своих файлах base, общем кэше
// графов и переключателе sw — как его собирает main.
func modeRegistry(t *testing.T, lib kb.Library, rc RemoteCaller, base *kb.Base, baseDir string,
	cache *graph.Cache, sw *kb.SourceSwitch) *Registry {
	t.Helper()
	reg, err := NewRegistry([]string{NameSearch, NameKBSearch, NameKBRead, NameGraphSearch}, Options{
		Library: lib, RemoteSearch: rc, LibraryMode: sw, KB: base, KBDir: baseDir, KBDefault: "test",
		KBTopK: 5, KBMaxPerBook: 3, MaxOutputKB: 64, GraphCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// runTool строит план и исполняет его, как агент.
func runTool(t *testing.T, reg *Registry, name string, args map[string]any) (string, *Plan, error) {
	t.Helper()
	plan, err := reg.Plan(name, args)
	if err != nil {
		t.Fatalf("%s: план: %v", name, err)
	}
	out, err := plan.Run(context.Background())
	return out, plan, err
}

// modeCalls — по одному вызову каждого инструмента, что уезжает на службу.
var modeCalls = []struct {
	name string
	args map[string]any
	want string // что обязано быть в выдаче своих файлов
}{
	{NameGraphSearch, map[string]any{"query": "goroutine"}, "goroutine"},
	{NameSearch, map[string]any{"query": "goroutines channels"}, "go.pdf"},
	{NameKBSearch, map[string]any{"query": "goroutines channels"}, "go.pdf"},
}

// local при живой службе: ни один вызов до неё не доходит (ни по /api/v1,
// ни по MCP), граф — свой, из общего кэша, а пометки о переходе нет:
// перехода не было, свои файлы выбрал человек.
func TestSourceLocalSkipsLiveService(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	addTestGraph(t, base)
	cache := graph.NewCache(time.Minute, graph.Rules{})
	defer cache.Close()
	svc, hits := countingService(t, base)
	rc := &fakeRemote{out: "выдача службы"}
	reg := modeRegistry(t, svc, rc, base, baseDir, cache, kb.NewSourceSwitch(kb.SourceLocal))

	for _, c := range modeCalls {
		out, plan, err := runTool(t, reg, c.name, c.args)
		if err != nil {
			t.Fatalf("%s в положении local: %v", c.name, err)
		}
		if !strings.Contains(out, c.want) || strings.Contains(out, kb.FallbackNote) || plan.Note != "" {
			t.Errorf("%s в положении local: note %q, выдача:\n%s", c.name, plan.Note, out)
		}
	}
	if n := hits.Load(); n != 0 || rc.name != "" {
		t.Errorf("в положении local служба спрошена: запросов к /api/v1 %d, по MCP %q", n, rc.name)
	}
	if cache.Len() != 1 {
		t.Errorf("свой граф не через общий кэш: графов в кэше %d", cache.Len())
	}
}

// service при лежащей службе: честная ошибка с пометкой, почему свои файлы
// не взяты; своего графа в кэше нет.
func TestSourceServiceDownNoFallback(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	addTestGraph(t, base)
	cache := graph.NewCache(time.Minute, graph.Rules{})
	defer cache.Close()
	rc := &fakeRemote{err: errServiceDown}
	reg := modeRegistry(t, deadService(t), rc, base, baseDir, cache, kb.NewSourceSwitch(kb.SourceService))

	for _, c := range modeCalls {
		out, plan, err := runTool(t, reg, c.name, c.args)
		if !errors.Is(err, kb.ErrUnreachable) || !strings.Contains(err.Error(), kb.ServiceOnlyNote) {
			t.Errorf("%s в положении service при лежащей службе: %v\n%s", c.name, err, out)
		}
		if plan.Note != "" || strings.Contains(out, c.want) {
			t.Errorf("%s в положении service ушёл на свои файлы: note %q\n%s", c.name, plan.Note, out)
		}
	}
	if rc.name != NameSearch {
		t.Errorf("search в положении service не спросил службу: %q", rc.name)
	}
	if cache.Len() != 0 {
		t.Errorf("в положении service открыт свой граф: графов в кэше %d", cache.Len())
	}
}

// Один реестр, переключатель меняется на ходу (как командой /kb source):
// auto при лежащей службе — переход на свои файлы с заметкой, service — та же
// служба уже ошибка, local — свои файлы без заметки. Описание инструмента то
// же, что без службы: модель разницы не видит.
func TestSourceSwitchOnTheFly(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	addTestGraph(t, base)
	cache := graph.NewCache(time.Minute, graph.Rules{})
	defer cache.Close()
	sw := kb.NewSourceSwitch(kb.SourceAuto)
	reg := modeRegistry(t, deadService(t), &fakeRemote{err: errServiceDown}, base, baseDir, cache, sw)
	args := map[string]any{"query": "goroutine"}

	out, plan, err := runTool(t, reg, NameGraphSearch, args)
	if err != nil || !strings.HasPrefix(out, "("+kb.FallbackNote+")") || plan.Note != kb.FallbackNote {
		t.Errorf("auto: err %v, note %q\n%s", err, plan.Note, out)
	}
	out, plan, err = runTool(t, reg, NameKBSearch, map[string]any{"query": "goroutines channels"})
	if err != nil || !strings.HasPrefix(out, "("+kb.FallbackNote+")") || plan.Note != kb.FallbackNote {
		t.Errorf("auto, kb_search: err %v, note %q\n%s", err, plan.Note, out)
	}

	sw.Set(kb.SourceService)
	if _, _, err = runTool(t, reg, NameGraphSearch, args); !strings.Contains(fmtErr(err), kb.ServiceOnlyNote) {
		t.Errorf("service после auto: %v", err)
	}

	sw.Set(kb.SourceLocal)
	out, plan, err = runTool(t, reg, NameGraphSearch, args)
	if err != nil || strings.Contains(out, kb.FallbackNote) || plan.Note != "" || !strings.Contains(out, "goroutine") {
		t.Errorf("local после service: err %v, note %q\n%s", err, plan.Note, out)
	}

	local, err := NewRegistry([]string{NameSearch, NameKBSearch, NameKBRead, NameGraphSearch},
		Options{KBDefault: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range local.Names() {
		if reg.spec(name) != local.spec(name) {
			t.Errorf("описание %s при службе разошлось с местным", name)
		}
	}
}

// fmtErr — текст ошибки; nil — пусто.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
