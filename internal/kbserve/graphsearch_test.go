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
	resp := GraphSearchResponse{
		Result: graph.SearchResult{Entities: []graph.FoundEntity{{Mentions: 3}}},
		Chain:  []graph.PathStep{{From: "горутина", To: "канал"}, {From: "канал", To: "рантайм"}},
	}
	if req.PathCount > 1 {
		resp.ChainMore = [][]graph.PathStep{{{From: "горутина", To: "планировщик"}, {From: "планировщик", To: "рантайм"}}}
	}
	return resp, nil
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

// Число путей клиента (этап 126, Я2.1) доходит до графа в пределах службы,
// а пути 2…K уходят полем chain_more. Клиент прежней сборки поля path_count
// не шлёт — до графа доходит ноль (одна цепочка), и chain_more в ответе нет.
func TestGraphSearchPassesPathCount(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &recordingGraph{}
	mux := Handler(Opts{Base: base, Graph: g})

	w := postGraphSearch(t, mux, `{"collection":"books","query":"q","path_count":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("код %d: %s", w.Code, w.Body.String())
	}
	if g.got.PathCount != 2 {
		t.Errorf("до графа дошло путей %d, ждали 2", g.got.PathCount)
	}
	var resp struct {
		Chain     []graph.PathStep   `json:"chain"`
		ChainMore [][]graph.PathStep `json:"chain_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Chain) != 2 || len(resp.ChainMore) != 1 || resp.ChainMore[0][0].To != "планировщик" {
		t.Errorf("ответ: %s", w.Body.String())
	}

	w = postGraphSearch(t, mux, `{"collection":"books","query":"q"}`)
	if g.got.PathCount != 0 || strings.Contains(w.Body.String(), "chain_more") {
		t.Errorf("без path_count: до графа %d путей, ответ %s", g.got.PathCount, w.Body.String())
	}
	postGraphSearch(t, mux, `{"collection":"books","query":"q","path_count":-2}`)
	if g.got.PathCount != 0 {
		t.Errorf("отрицательное число путей дошло до графа: %d", g.got.PathCount)
	}
	postGraphSearch(t, mux, `{"collection":"books","query":"q","path_count":100000}`)
	if g.got.PathCount != maxTopK {
		t.Errorf("путей %d дошло до графа; ждали потолок %d", g.got.PathCount, maxTopK)
	}
}

// Живые настройки клиента (этап 121, Д4) доходят до графа: смысловой вход,
// вес и пул ранжирования связей. Вес ноль — осмысленное «не пересортировывать»,
// пул — в пределах службы, отрицательный вес — отказ до очереди и до графа.
func TestGraphSearchPassesClientRank(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &recordingGraph{}
	mux := Handler(Opts{Base: base, Graph: g})

	w := postGraphSearch(t, mux, `{"collection":"books","query":"q","sense_entry":false,"sense_weight":0,"pool":100000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("код %d: %s", w.Code, w.Body.String())
	}
	if g.got.SenseEntry == nil || *g.got.SenseEntry {
		t.Errorf("смысловой вход клиента не дошёл до графа: %v", g.got.SenseEntry)
	}
	if g.got.SenseWeight == nil || *g.got.SenseWeight != 0 {
		t.Errorf("вес 0 клиента не дошёл до графа: %v", g.got.SenseWeight)
	}
	if g.got.Pool != maxTopK {
		t.Errorf("пул %d дошёл до графа; ждали потолок %d", g.got.Pool, maxTopK)
	}

	// Клиент прежней сборки полей не шлёт — граф считает по настройкам службы.
	g.got = GraphSearchRequest{}
	postGraphSearch(t, mux, `{"collection":"books","query":"q"}`)
	if g.got.SenseEntry != nil || g.got.SenseWeight != nil || g.got.Pool != 0 {
		t.Errorf("без полей до графа дошло: вход %v, вес %v, пул %d", g.got.SenseEntry, g.got.SenseWeight, g.got.Pool)
	}

	g.got = GraphSearchRequest{}
	w = postGraphSearch(t, mux, `{"collection":"books","query":"q","sense_weight":-1}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("отрицательный вес: код %d, ждали %d", w.Code, http.StatusBadRequest)
	}
	if g.got.Query != "" {
		t.Errorf("запрос с отрицательным весом дошёл до графа: %+v", g.got)
	}
}
