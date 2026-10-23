package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Служба библиотеки не отвечает — инструменты ищут по своим файлам (решение
// владельца, этап 121, А4).

var errServiceDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", kb.ErrUnreachable)

// downService — общая библиотека, до которой нет соединения: ни книги, ни граф.
type downService struct{ graphCalls int }

func (*downService) Names() ([]string, error)         { return nil, errServiceDown }
func (*downService) Source(string) (kb.Source, error) { return nil, errServiceDown }
func (s *downService) GraphTool(context.Context, string, string, map[string]any) (string, error) {
	s.graphCalls++
	return "", errServiceDown
}

// refusingService — служба отвечает, но отказом по смыслу.
type refusingService struct{}

var errRefusal = errors.New("библиотека http://kb.example:8377: на этой службе граф понятий не собран или не подключён")

func (refusingService) Names() ([]string, error) { return nil, errRefusal }
func (refusingService) Source(string) (kb.Source, error) {
	return nil, errors.New("на сервере http://kb.example:8377 нет коллекции \"test\"")
}
func (refusingService) GraphTool(context.Context, string, string, map[string]any) (string, error) {
	return "", errRefusal
}

// remoteRegistry — реестр с общей библиотекой lib и своими файлами base
// (nil — своих нет).
func remoteRegistry(t *testing.T, lib kb.Library, base *kb.Base, baseDir string, cache *graph.Cache) *Registry {
	t.Helper()
	o := Options{Library: lib, KBDir: baseDir, KBDefault: "test", KBTopK: 5, KBMaxPerBook: 3,
		MaxOutputKB: 64, GraphCache: cache}
	if base != nil {
		o.KB = base
	}
	reg, err := NewRegistry([]string{NameKBSearch, NameKBRead, NameGraphSearch}, o)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// kb_search и kb_read: служба молчит — тот же запрос по своей коллекции,
// выдача помечена строкой, журнал шагов — Plan.Note.
func TestKBToolsFallBackToOwnFiles(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	reg := remoteRegistry(t, &downService{}, base, baseDir, nil)

	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines channels"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("при недоступной службе и своих файлах поиск не должен падать: %v", err)
	}
	if !strings.HasPrefix(out, "("+kb.FallbackNote+")") || !strings.Contains(out, "go.pdf") {
		t.Errorf("выдача по своим файлам не помечена или пуста:\n%s", out)
	}
	if plan.Note != kb.FallbackNote {
		t.Errorf("пометка для журнала шагов %q", plan.Note)
	}

	i := strings.Index(out, "id=")
	if i < 0 {
		t.Fatalf("в выдаче нет номера фрагмента:\n%s", out)
	}
	id := strings.Fields(out[i+3:])[0]
	plan, err = reg.Plan(NameKBRead, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	text, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "("+kb.FallbackNote+")") || plan.Note != kb.FallbackNote {
		t.Errorf("чтение по своим файлам не помечено: note %q\n%s", plan.Note, text)
	}
}

// Своих файлов нет — ошибка службы с её адресом, а не «база не настроена».
func TestKBSearchServiceDownWithoutOwnFiles(t *testing.T) {
	reg := remoteRegistry(t, &downService{}, nil, "", nil)
	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.Run(context.Background())
	if !errors.Is(err, kb.ErrUnreachable) || !strings.Contains(err.Error(), "kb.example") {
		t.Errorf("ошибка без своих файлов: %v", err)
	}
}

// Отказ службы по смыслу — ошибка, даже когда свои файлы есть.
func TestKBSearchRefusalIsNotFallback(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	reg := remoteRegistry(t, refusingService{}, base, baseDir, nil)
	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "нет коллекции") {
		t.Errorf("отказ службы подменён своими файлами: %v\n%s", err, out)
	}
	if plan.Note != "" {
		t.Errorf("пометка о переходе при отказе по смыслу: %q", plan.Note)
	}
}

// addTestGraph кладёт рядом с коллекцией test граф с понятием goroutine.
func addTestGraph(t *testing.T, base *kb.Base) {
	t.Helper()
	coll, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	var keys []graph.ChunkKey
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(c kb.ChunkRef) error {
		keys = append(keys, graph.ChunkKey{Doc: c.Doc, Ord: c.Ord})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := g.Entities().Add("goroutine", graph.TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		g.Entities().Touch(id, true)
		if err := g.Mentions().Add(id, k); err != nil {
			t.Fatal(err)
		}
		if err := g.Progress().Mark(k, graph.MarkDone); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Entities().SaveCounters(); err != nil {
		t.Fatal(err)
	}
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

// liveService — служба отвечает: книги отдаёт src; queued — сколько её
// вызовы ждали очереди (kb.Queued).
type liveService struct {
	src    kb.Source
	queued time.Duration
}

func (s liveService) Names() ([]string, error) { return []string{"test"}, nil }
func (s liveService) Source(string) (kb.Source, error) {
	return queuedSource{Source: s.src, queued: s.queued}, nil
}

type queuedSource struct {
	kb.Source
	queued time.Duration
}

func (q queuedSource) Queued() time.Duration { return q.queued }

// kb_search при живой службе и kb.expand_limit > 0 своего графа не открывает:
// дополнять запрос нечем, и это обещает комментарий graphEntities (аудит
// 23.10.2026). Прежде граф открывался на каждый вызов — гигабайт памяти
// при службе, которая держит граф ради того, чтобы его не держал клиент.
func TestKBSearchExpandSkipsOwnGraphWithLiveService(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	addTestGraph(t, base)
	coll, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	cache := graph.NewCache(time.Minute, graph.Rules{})
	defer cache.Close()

	o := Options{Library: liveService{src: coll}, KB: base, KBDir: baseDir, KBDefault: "test",
		KBTopK: 5, KBMaxPerBook: 3, MaxOutputKB: 64, GraphCache: cache, KBExpandLimit: 3}
	reg, err := NewRegistry([]string{NameKBSearch}, o)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines channels"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.Len() != 0 {
		t.Errorf("kb_search при живой службе открыл свой граф (kb.expand_limit = 3): графов в кэше %d", cache.Len())
	}

	// Без службы дополнение по своему графу на месте.
	o.Library = nil
	reg, err = NewRegistry([]string{NameKBSearch}, o)
	if err != nil {
		t.Fatal(err)
	}
	if plan, err = reg.Plan(NameKBSearch, map[string]any{"query": "goroutines channels"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.Len() == 0 {
		t.Error("без службы kb_search перестал дополнять запрос своим графом")
	}
}

// Служба дождалась места в очереди — выдача kb_search говорит, сколько ждала
// (решение владельца 23.10.2026), и журнал шагов это видит (Plan.Note).
func TestKBSearchReportsQueue(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	coll, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Library: liveService{src: coll, queued: 2300 * time.Millisecond}, KB: base, KBDir: baseDir,
		KBDefault: "test", KBTopK: 5, KBMaxPerBook: 3, MaxOutputKB: 64}
	reg, err := NewRegistry([]string{NameKBSearch}, o)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines channels"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "(служба занята: ждал в очереди 2300 мс)") || plan.Note == "" {
		t.Errorf("выдача молчит об очереди службы (note %q):\n%s", plan.Note, out)
	}
}

// Инструмент графа: служба молчит — тот же вызов исполняет местный
// инструмент, и граф берётся из общего кэша графов (закроет его простой),
// а не открывается навсегда.
func TestGraphToolFallsBackToOwnFiles(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	addTestGraph(t, base)
	cache := graph.NewCache(time.Minute, graph.Rules{})
	defer cache.Close()
	svc := &downService{}
	reg := remoteRegistry(t, svc, base, baseDir, cache)

	plan, err := reg.Plan(NameGraphSearch, map[string]any{"query": "goroutine"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("при недоступной службе и своём графе вызов не должен падать: %v", err)
	}
	if svc.graphCalls != 1 {
		t.Errorf("служба спрошена %d раз, ждали один", svc.graphCalls)
	}
	if !strings.HasPrefix(out, "("+kb.FallbackNote+")") || !strings.Contains(out, "goroutine") {
		t.Errorf("ответ своего графа не помечен или пуст:\n%s", out)
	}
	if plan.Note != kb.FallbackNote {
		t.Errorf("пометка для журнала шагов %q", plan.Note)
	}
	if cache.Len() != 1 {
		t.Errorf("граф не в общем кэше: графов в кэше %d", cache.Len())
	}
}

// Своих файлов нет — у инструмента графа остаётся ошибка службы; отказ
// по смыслу — ошибка и при своих файлах.
func TestGraphToolNoFallbackWithoutFilesOrOnRefusal(t *testing.T) {
	reg := remoteRegistry(t, &downService{}, nil, "", nil)
	plan, err := reg.Plan(NameGraphSearch, map[string]any{"query": "goroutine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Run(context.Background()); !errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("без своих файлов: %v", err)
	}

	_, base, baseDir := newKBRegistry(t)
	addTestGraph(t, base)
	reg = remoteRegistry(t, refusingService{}, base, baseDir, nil)
	plan, err = reg.Plan(NameGraphSearch, map[string]any{"query": "goroutine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Run(context.Background()); !errors.Is(err, errRefusal) || plan.Note != "" {
		t.Errorf("отказ по смыслу: %v, note %q", err, plan.Note)
	}
}

// fakeRemote — служба, исполняющая search по MCP: отвечает out или err и
// запоминает доводы.
type fakeRemote struct {
	out  string
	err  error
	name string
	args map[string]any
}

func (f *fakeRemote) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	f.name, f.args = name, args
	return f.out, f.err
}

// searchRegistry — реестр с search через службу (Options.RemoteSearch) и,
// если base задан, своими файлами.
func searchRegistry(t *testing.T, rc RemoteCaller, base *kb.Base, baseDir string) *Registry {
	t.Helper()
	o := Options{Library: &downService{}, RemoteSearch: rc, KBDir: baseDir, KBDefault: "test",
		KBTopK: 5, KBMaxPerBook: 3, MaxOutputKB: 64}
	if base != nil {
		o.KB = base
	}
	reg, err := NewRegistry([]string{NameSearch}, o)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// search уходит в службу под своим именем, с коллекцией по умолчанию своей,
// а не службы (этап 121, А3); названная моделью коллекция — как есть.
func TestSearchGoesToService(t *testing.T) {
	rc := &fakeRemote{out: "выдача службы"}
	reg := searchRegistry(t, rc, nil, "")
	plan, err := reg.Plan(NameSearch, map[string]any{"query": "каналы"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil || out != "выдача службы" {
		t.Fatalf("ответ %q, ошибка %v", out, err)
	}
	if rc.name != NameSearch || rc.args["collection"] != "test" || rc.args["query"] != "каналы" {
		t.Errorf("службе ушло %s %v", rc.name, rc.args)
	}
	if plan.Note != "" {
		t.Errorf("служба ответила, а пометка о своих файлах есть: %q", plan.Note)
	}
	plan, _ = reg.Plan(NameSearch, map[string]any{"query": "каналы", "collection": "books"})
	if _, err := plan.Run(context.Background()); err != nil || rc.args["collection"] != "books" {
		t.Errorf("коллекция модели заменена: %v, %v", rc.args, err)
	}
	// Описание то же, что у местного search: модель не должна видеть разницы.
	local, err := NewRegistry([]string{NameSearch}, Options{KBDefault: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if reg.spec(NameSearch) != local.spec(NameSearch) {
		t.Error("описание search через службу разошлось с местным")
	}
}

// Служба не ответила — тот же поиск по своим файлам с пометкой (А4);
// своих нет — ошибка службы; отказ по смыслу переходом не лечится.
func TestSearchServiceDownFallsBack(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	reg := searchRegistry(t, &fakeRemote{err: errServiceDown}, base, baseDir)
	plan, err := reg.Plan(NameSearch, map[string]any{"query": "goroutines channels"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("служба молчит, свои файлы есть, а поиск упал: %v", err)
	}
	if !strings.HasPrefix(out, "("+kb.FallbackNote+")") || !strings.Contains(out, "go.pdf") || plan.Note != kb.FallbackNote {
		t.Errorf("поиск по своим файлам не помечен или пуст (note %q):\n%s", plan.Note, out)
	}

	reg = searchRegistry(t, &fakeRemote{err: errServiceDown}, nil, "")
	plan, _ = reg.Plan(NameSearch, map[string]any{"query": "goroutines"})
	if _, err := plan.Run(context.Background()); !errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("без своих файлов: %v", err)
	}

	reg = searchRegistry(t, &fakeRemote{err: errRefusal}, base, baseDir)
	plan, _ = reg.Plan(NameSearch, map[string]any{"query": "goroutines"})
	if _, err := plan.Run(context.Background()); !errors.Is(err, errRefusal) || plan.Note != "" {
		t.Errorf("отказ по смыслу: %v, note %q", err, plan.Note)
	}
}
