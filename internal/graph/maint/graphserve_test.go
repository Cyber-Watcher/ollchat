package maint

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
)

// serveFixture — коллекция из одной книги и граф «горутина → канал → рантайм»
// на диске, без прямой связи между крайними: служба открывает его тем же
// путём, что в работе.
func serveFixture(t *testing.T) *GraphService {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "go.txt"), []byte(fixtureText("goroutines and channels", 120)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.KB.Dir = filepath.Join(root, "kb")
	cfg.Mix.Entities, cfg.Mix.Neighbors = 2, 4 // свои настройки службы
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	var keys []graph.ChunkKey
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(c kb.ChunkRef) error {
		keys = append(keys, graph.ChunkKey{Doc: c.Doc, Ord: c.Ord})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) < 3 {
		t.Fatalf("фикстура: кусков %d, нужно хотя бы 3", len(keys))
	}

	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, mid, b := add("горутина"), add("канал"), add("рантайм")
	for _, e := range []graph.Edge{
		{Src: a, Dst: mid, Type: graph.RelUses, Weight: 2, Evidence: keys[0]},
		{Src: mid, Dst: b, Type: graph.RelPart, Weight: 2, Evidence: keys[1]},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint32{a, mid, b} {
		for _, k := range keys[:3] {
			g.Entities().Touch(id, true)
			if err := g.Mentions().Add(id, k); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, k := range keys {
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
	return &GraphService{Cfg: cfg, Base: base}
}

// Размер карты задаёт клиент (этап 121, А2): entities из запроса берётся,
// ноль — свои настройки службы.
func TestGraphServiceSearchHonorsClientSize(t *testing.T) {
	svc := serveFixture(t)
	ctx := context.Background()

	resp, err := svc.Search(ctx, kbserve.GraphSearchRequest{Collection: "books",
		Query: "горутина канал рантайм", Entities: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(resp.Result.Entities); n != 1 {
		t.Errorf("клиент просил 1 понятие, служба отдала %d", n)
	}

	resp, err = svc.Search(ctx, kbserve.GraphSearchRequest{Collection: "books", Query: "горутина канал рантайм"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(resp.Result.Entities); n != svc.Cfg.Mix.Entities {
		t.Errorf("без размера в запросе понятий %d, ждали своих %d", n, svc.Cfg.Mix.Entities)
	}
}

// Вектор вопроса для карты службы считается с заголовками сервера из конфига,
// как у её инструментов (ollmcp): за прокси с авторизацией без них карта
// молча считалась без смыслового входа (рецензия этапа 121).
func TestGraphServiceSearchSendsServerHeaders(t *testing.T) {
	got := make(chan string, 4)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		got <- r.Header.Get("X-Proxy-Auth")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer ollama.Close()

	svc := serveFixture(t)
	svc.Cfg.KB.EmbedModel = "bge-m3"
	svc.Cfg.Servers = []config.Server{{Name: "gpu", URL: ollama.URL,
		Headers: map[string]string{"X-Proxy-Auth": "секрет"}}}
	// Вопрос уникален: одиночные векторы кэшируются на весь процесс.
	q := fmt.Sprintf("горутина заголовки %d", time.Now().UnixNano())
	if _, err := svc.Search(context.Background(), kbserve.GraphSearchRequest{Collection: "books", Query: q}); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-got:
		if h != "секрет" {
			t.Errorf("эмбеддер карты службы пришёл без заголовков сервера: X-Proxy-Auth = %q", h)
		}
	default:
		t.Fatal("вектор вопроса не запрашивался")
	}
}

// Цепочка между двумя понятиями вопроса приходит вместе с картой (А6):
// у клиента, берущего карту у службы, своего графа нет.
func TestGraphServiceSearchReturnsChain(t *testing.T) {
	svc := serveFixture(t)
	resp, err := svc.Search(context.Background(), kbserve.GraphSearchRequest{Collection: "books",
		Query: "как связаны горутина и рантайм", Entities: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Chain) < 2 {
		t.Fatalf("цепочка «горутина → канал → рантайм» не пришла: %+v", resp.Chain)
	}
	first, last := resp.Chain[0], resp.Chain[len(resp.Chain)-1]
	ends := map[string]bool{first.From: true, last.To: true}
	if !ends["горутина"] || !ends["рантайм"] {
		t.Errorf("цепочка не между понятиями вопроса: %+v", resp.Chain)
	}
}
