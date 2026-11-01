package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
)

// askService — общая библиотека для --ask: книги той же коллекции, карта
// и цепочка по графу службы (в тесте — граф в памяти).
type askService struct {
	coll  kb.Source
	g     *graph.Graph
	down  bool
	calls int
}

var errAskServiceDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", kb.ErrUnreachable)

func (s *askService) Names() ([]string, error) {
	if s.down {
		return nil, errAskServiceDown
	}
	return []string{"books"}, nil
}

func (s *askService) Source(string) (kb.Source, error) {
	if s.down {
		return nil, errAskServiceDown
	}
	return s.coll, nil
}

func (s *askService) GraphMap(_ context.Context, _, query string, o kbremote.MapOpts) (kbremote.GraphMap, error) {
	s.calls++
	if s.down {
		return kbremote.GraphMap{}, errAskServiceDown
	}
	var rank graph.NeighborRank
	if o.Rank != nil {
		rank = *o.Rank
	}
	res := s.g.Search(query, graph.SearchOpts{TopEntities: o.Entities, TopNeighbors: o.Neighbors,
		TopChunks: 1, MinMentions: 2, Neighbors: rank})
	return kbremote.GraphMap{Result: res, Chain: find.Chain(s.g, res.Entities, res.Relations, find.Opts{})}, nil
}

// GraphSearch — то же без годов и очереди, как kbremote.Client.GraphSearch
// (его зовёт --kb-remote-check).
func (s *askService) GraphSearch(ctx context.Context, coll, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error) {
	m, err := s.GraphMap(ctx, coll, query, kbremote.MapOpts{Entities: entities, Neighbors: neighbors})
	return m.Result, m.Chain, err
}

var _ mixer.MapSource = (*askService)(nil)

// askChainService — служба с графом «горутина → канал → рантайм».
func askChainService(t *testing.T) *askService {
	t.Helper()
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "")
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(coll.Dir(), "books", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, mid, b := add("горутина"), add("канал"), add("рантайм")
	for _, e := range []graph.Edge{
		{Src: a, Dst: mid, Type: graph.RelUses, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}},
		{Src: mid, Dst: b, Type: graph.RelPart, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 2}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint32{a, mid, b} {
		for ord := uint32(1); ord <= 3; ord++ {
			if err := g.Mentions().Add(id, graph.ChunkKey{Doc: 1, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &askService{coll: coll, g: g}
}

// --ask с общей библиотекой: подмес тем же askOne, карта и цепочка — от
// службы, своя коллекция и свой граф не открываются (этап 121, А2, А6).
func TestAskMixThroughService(t *testing.T) {
	cfg := config.Default()
	cfg.KB.EmbedModel = "" // без эмбеддера подмес считается без сети
	cfg.Mix.Chain = true
	srv := &config.Server{Name: "проба", URL: "http://ollama.example:11434"}
	svc := askChainService(t)
	o := askOpts{Mix: "graph", MixOnly: true, Collection: "books", library: svc}

	ans := askOne(nil, cfg, srv, "модель", "как связаны горутина и рантайм", o, askSettings(cfg, o), nil, nil)
	if ans.MixEntities == 0 || svc.calls != 1 {
		t.Fatalf("карта от службы не подмешалась: понятий %d, вызовов службы %d", ans.MixEntities, svc.calls)
	}
	if !strings.Contains(ans.MixText, "Путь из") {
		t.Errorf("цепочка от службы не попала в подмес:\n%s", ans.MixText)
	}
	if ans.MixRemote != "" {
		t.Errorf("служба ответила, а заметка о ней есть: %q", ans.MixRemote)
	}
}

// Служба не отвечает, коллекция не названа, своих коллекций несколько —
// запасной путь не берёт молча первую, а отказывает с перечнем, как TUI;
// с единственной своей коллекцией — ищет по ней и называет её в заметке
// (аудит 23.10.2026).
func TestAskRemoteMixLocalCollectionChoice(t *testing.T) {
	cfg := config.Default()
	cfg.KB.EmbedModel = ""
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	for _, n := range []string{"alpha", "beta"} {
		if _, err := base.Create(n, ""); err != nil {
			t.Fatal(err)
		}
	}
	svc := askChainService(t)
	svc.down = true
	o := askOpts{library: svc, base: base}
	out := askRemoteMix(cfg, o, "как связаны горутина и рантайм", mixer.Deps{BooksOn: true, GraphOn: true},
		mixer.Settings{TopK: 3})
	if !strings.Contains(out.Remote, "alpha, beta") || strings.Contains(out.Remote, kb.FallbackNote) {
		t.Errorf("при двух своих коллекциях без kb.default запасной путь выбрал сам: %q", out.Remote)
	}

	solo, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { solo.Close() })
	if _, err := solo.Create("solo", ""); err != nil {
		t.Fatal(err)
	}
	o.base = solo
	out = askRemoteMix(cfg, o, "как связаны горутина и рантайм", mixer.Deps{BooksOn: true}, mixer.Settings{TopK: 3})
	if !strings.Contains(out.Remote, kb.FallbackNote) || !strings.Contains(out.Remote, "solo") {
		t.Errorf("переход на единственную свою коллекцию без её имени в заметке: %q", out.Remote)
	}
}

// Служба не отвечает, своих файлов нет — подмеса нет, а в ответе причина
// с адресом службы.
func TestAskMixServiceDownWithoutOwnFiles(t *testing.T) {
	cfg := config.Default()
	cfg.KB.EmbedModel = ""
	srv := &config.Server{Name: "проба", URL: "http://ollama.example:11434"}
	svc := askChainService(t)
	svc.down = true
	o := askOpts{Mix: "all", MixOnly: true, Collection: "books", library: svc}

	ans := askOne(nil, cfg, srv, "модель", "как связаны горутина и рантайм", o, askSettings(cfg, o), nil, nil)
	if ans.MixText != "" || !strings.Contains(ans.MixRemote, "kb.example") {
		t.Errorf("подмес без службы и своих файлов: текст %q, заметка %q", ans.MixText, ans.MixRemote)
	}
}

// --kb-source service (этап 121, Д3): служба не отвечает, свои файлы есть —
// а подмес на них не уходит: ни книг, ни карты, в заметке — почему.
func TestAskMixServiceOnlyNoFallback(t *testing.T) {
	cfg := config.Default()
	cfg.KB.EmbedModel = ""
	solo, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { solo.Close() })
	if _, err := solo.Create("solo", ""); err != nil {
		t.Fatal(err)
	}
	svc := askChainService(t)
	svc.down = true

	o := askOpts{library: svc, base: solo, mode: kb.SourceService}
	out := askRemoteMix(cfg, o, "как связаны горутина и рантайм", mixer.Deps{BooksOn: true, GraphOn: true},
		mixer.Settings{TopK: 3})
	if strings.Contains(out.Remote, kb.FallbackNote) || !strings.Contains(out.Remote, kb.ServiceOnlyNote) {
		t.Errorf("положение service ушло на свои файлы или молчит почему: %q", out.Remote)
	}
	if out.Text != "" {
		t.Errorf("положение service при лежащей службе подмешало: %q", out.Text)
	}

	// То же без ключа (auto) — переход на свои файлы, как было.
	o.mode = kb.SourceAuto
	out = askRemoteMix(cfg, o, "как связаны горутина и рантайм", mixer.Deps{BooksOn: true}, mixer.Settings{TopK: 3})
	if !strings.Contains(out.Remote, kb.FallbackNote) {
		t.Errorf("положение auto не перешло на свои файлы: %q", out.Remote)
	}
}

// --serve при kb.server_url (этап 121, Д2, решение владельца 31.10.2026):
// служба отвечает — отказ со словами «используйте её»; ответила ошибкой
// (ключ, очередь) — тоже отказ, она жива; не отвечает — свои файлы
// (положение local) и строка о том в stderr.
func TestServeSource(t *testing.T) {
	var warn strings.Builder
	sw := kb.NewSourceSwitch(kb.SourceAuto)
	err := serveSource(&askService{}, "http://kb.example:8377", sw, &warn)
	if err == nil || !strings.Contains(err.Error(), "используйте её") || !strings.Contains(err.Error(), "kb.example:8377") {
		t.Errorf("служба отвечает, а --serve не отказал или не назвал адрес: %v", err)
	}
	if sw.Mode() != kb.SourceAuto {
		t.Errorf("при отказе положение сменилось: %v", sw.Mode())
	}

	keyErr := fmt.Errorf("библиотека http://kb.example:8377: ключ не подошёл (401)")
	if err := serveSource(namesErr{keyErr}, "http://kb.example:8377", sw, &warn); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("служба ответила отказом по ключу — она жива, а --serve: %v", err)
	}

	if err := serveSource(&askService{down: true}, "http://kb.example:8377", sw, &warn); err != nil {
		t.Fatalf("служба не отвечает, а --serve отказал: %v", err)
	}
	if sw.Mode() != kb.SourceLocal {
		t.Errorf("служба молчит: положение %v, ждали local", sw.Mode())
	}
	if !strings.Contains(warn.String(), "по своим файлам") {
		t.Errorf("переход на свои файлы без строки в stderr: %q", warn.String())
	}

	if err := serveSource(nil, "", sw, &warn); err != nil {
		t.Errorf("без kb.server_url --serve отказал: %v", err)
	}
}

// namesErr — служба, которая на список коллекций отвечает ошибкой err.
type namesErr struct {
	err error
}

func (n namesErr) Names() ([]string, error)         { return nil, n.err }
func (n namesErr) Source(string) (kb.Source, error) { return nil, n.err }

// Положение из --kb-source (этап 121, Д3): при службе — какое названо; без
// службы положение остаётся local, а «только служба» — отказ.
func TestApplyKBSource(t *testing.T) {
	var lib kb.Library = &askService{}
	sw := kb.NewSourceSwitch(kb.SourceAuto)
	if err := applyKBSource("local", lib, sw); err != nil || sw.Mode() != kb.SourceLocal {
		t.Errorf("при службе --kb-source local: положение %v, ошибка %v", sw.Mode(), err)
	}
	if err := applyKBSource("", lib, sw); err != nil || sw.Mode() != kb.SourceLocal {
		t.Errorf("без ключа положение сменилось: %v, ошибка %v", sw.Mode(), err)
	}
	none := kb.NewSourceSwitch(kb.SourceLocal)
	if err := applyKBSource("auto", nil, none); err != nil || none.Mode() != kb.SourceLocal {
		t.Errorf("без службы --kb-source auto: положение %v, ошибка %v", none.Mode(), err)
	}
	if err := applyKBSource("service", nil, none); err == nil || !strings.Contains(err.Error(), "kb.server_url") {
		t.Errorf("без службы --kb-source service не отказал: %v", err)
	}
}
