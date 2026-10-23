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

func (s *askService) GraphMap(_ context.Context, _, query string, entities, neighbors int) (kbremote.GraphMap, error) {
	s.calls++
	if s.down {
		return kbremote.GraphMap{}, errAskServiceDown
	}
	res := s.g.Search(query, graph.SearchOpts{TopEntities: entities, TopNeighbors: neighbors, TopChunks: 1, MinMentions: 2})
	return kbremote.GraphMap{Result: res, Chain: find.Chain(s.g, res.Entities, res.Relations, find.Opts{})}, nil
}

// GraphSearch — то же без годов и очереди, как kbremote.Client.GraphSearch
// (его зовёт --kb-remote-check).
func (s *askService) GraphSearch(ctx context.Context, coll, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error) {
	m, err := s.GraphMap(ctx, coll, query, entities, neighbors)
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
