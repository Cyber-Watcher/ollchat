package kbserve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// recordingGraph запоминает запрос и отвечает картой с цепочкой.
type recordingGraph struct {
	got GraphSearchRequest
}

func (g *recordingGraph) Tool(context.Context, string, string, map[string]any) (string, error) {
	return "", nil
}

func (g *recordingGraph) Search(_ context.Context, req GraphSearchRequest) (GraphSearchResponse, error) {
	g.got = req
	return GraphSearchResponse{
		Result: graph.SearchResult{Entities: []graph.FoundEntity{{Mentions: 3}}},
		Chain:  []graph.PathStep{{From: "горутина", To: "канал"}, {From: "канал", To: "рантайм"}},
	}, nil
}

func (g *recordingGraph) Graph(string) (*graph.Graph, graph.NeighborRank, func(), error) {
	return nil, graph.NeighborRank{}, nil, errNoGraph
}

func postGraphSearch(t *testing.T, mux http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/graph/search", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// Договор /api/v1/graph/search (этап 121, А2 и А6): размер карты из запроса
// доходит до графа — в пределах службы, — а ответ несёт и карту, и цепочку.
func TestGraphSearchContract(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &recordingGraph{}
	mux := Handler(Opts{Base: base, Graph: g})

	w := postGraphSearch(t, mux, `{"collection":"books","query":"как связаны горутина и рантайм","entities":6,"neighbors":100000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("код %d: %s", w.Code, w.Body.String())
	}
	if g.got.Collection != "books" || g.got.Entities != 6 || g.got.Neighbors != maxTopK {
		t.Errorf("до графа дошло %+v; ждали books, 6 и потолок %d", g.got, maxTopK)
	}
	var resp struct {
		Result graph.SearchResult `json:"result"`
		Chain  []graph.PathStep   `json:"chain"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Result.Entities) != 1 || len(resp.Chain) != 2 || resp.Chain[1].To != "рантайм" {
		t.Errorf("ответ: %s", w.Body.String())
	}

	// Клиент прежней сборки полей размера не шлёт: служба считает по своим
	// настройкам (ноль), а отрицательное — тоже «как здесь».
	postGraphSearch(t, mux, `{"collection":"books","query":"q"}`)
	if g.got.Entities != 0 || g.got.Neighbors != 0 {
		t.Errorf("без полей размера до графа дошло %+v", g.got)
	}
	postGraphSearch(t, mux, `{"query":"q","entities":-3,"neighbors":-1}`)
	if g.got.Entities != 0 || g.got.Neighbors != 0 {
		t.Errorf("отрицательный размер дошёл до графа: %+v", g.got)
	}
}
