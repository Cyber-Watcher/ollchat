package mixer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Подмес через службу общей библиотеки (этап 121, А2, А4, А6).

// remoteFixture — коллекция и граф «горутина → канал → рантайм» без прямой
// связи между крайними: на таком графе есть и карта, и цепочка.
func remoteFixture(t *testing.T) (*kb.Base, *kb.Collection, *graph.Graph) {
	t.Helper()
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "проверочная коллекция")
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
	return base, coll, g
}

// fakeMap — служба, считающая карту по тому же графу тем же поиском, что
// подмешивание по своему графу, и цепочку тем же find.Chain
// (gmaint.GraphService.Search устроен так же).
type fakeMap struct {
	g   *graph.Graph
	err error

	calls               int
	coll                string
	entities, neighbors int
}

func (f *fakeMap) GraphSearch(_ context.Context, coll, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error) {
	f.calls++
	f.coll, f.entities, f.neighbors = coll, entities, neighbors
	if f.err != nil {
		return graph.SearchResult{}, nil, f.err
	}
	res := f.g.Search(query, graph.SearchOpts{TopEntities: entities, TopNeighbors: neighbors,
		TopChunks: 1, MinMentions: 2})
	return res, find.Chain(f.g, res.Entities, res.Relations, find.Opts{}), nil
}

// errDown — так kbremote сообщает об отказе в соединении: адрес в тексте,
// метка kb.ErrUnreachable внутри.
var errDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", kb.ErrUnreachable)

const pairQuestion = "как связаны горутина и рантайм"

func remoteSettings() Settings {
	return Settings{Entities: 6, Neighbors: 4, Chain: true, Collection: "books"}
}

// Карта от службы подмешивается так же, как по своему графу: тот же текст
// и та же цепочка, а размер карты — числа клиента.
func TestServiceMapMatchesOwnGraph(t *testing.T) {
	_, coll, g := remoteFixture(t)
	local := Build(pairQuestion, Deps{Coll: coll, Graph: g, GraphOn: true}, remoteSettings())
	if local.Empty() || local.Chain == 0 {
		t.Fatalf("подготовка: по своему графу нет карты с цепочкой: %+v", local)
	}

	fm := &fakeMap{g: g}
	remote := Build(pairQuestion, Deps{Coll: coll, Map: fm, GraphOn: true}, remoteSettings())
	if remote.Text != local.Text {
		t.Errorf("подмес через службу разошёлся с подмесом по своему графу:\n--- служба ---\n%s\n--- свой граф ---\n%s",
			remote.Text, local.Text)
	}
	if remote.Chain != local.Chain || remote.Entities != local.Entities {
		t.Errorf("цепочка или понятия разошлись: служба %+v, свой граф %+v", remote, local)
	}
	if remote.Remote != "" {
		t.Errorf("служба ответила, а заметка о ней есть: %q", remote.Remote)
	}
	if fm.entities != 6 || fm.neighbors != 4 || fm.coll != "books" {
		t.Errorf("службе ушло: коллекция %q, понятий %d, связей %d; ждали books, 6, 4",
			fm.coll, fm.entities, fm.neighbors)
	}
}

// Служба не ответила — карту считает свой граф, заметка говорит об этом,
// граф отпускается в конце подмеса (решение владельца, А4).
func TestServiceDownUsesOwnGraph(t *testing.T) {
	_, coll, g := remoteFixture(t)
	opened, released := 0, 0
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: errDown}, GraphOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) {
			opened++
			return g, func() { released++ }, nil
		}}, remoteSettings())
	if out.Entities == 0 || out.Chain == 0 {
		t.Fatalf("при недоступной службе карта по своему графу не подмешалась: %+v", out)
	}
	if out.Remote != kb.FallbackNote {
		t.Errorf("заметка %q, ждали %q", out.Remote, kb.FallbackNote)
	}
	if opened != 1 || released != 1 {
		t.Errorf("свой граф взят %d раз, отпущен %d; ждали по разу", opened, released)
	}
}

// Отказ службы по смыслу — не повод искать у себя: свой граф не трогается,
// карты нет, а человеку видна причина.
func TestServiceRefusalIsNotFallback(t *testing.T) {
	_, coll, _ := remoteFixture(t)
	refusal := errors.New("библиотека http://kb.example:8377: на этой службе граф понятий не собран или не подключён")
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: refusal}, GraphOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) {
			t.Error("при отказе по смыслу открыт свой граф")
			return nil, nil, errors.New("не нужно")
		}}, remoteSettings())
	if out.Entities != 0 {
		t.Errorf("карта подмешана при отказе службы: %+v", out)
	}
	if !strings.Contains(out.Remote, "граф понятий не собран") {
		t.Errorf("причина отказа не дошла до человека: %q", out.Remote)
	}
}

// Карта выключена (GraphOn = false): у службы её не спрашивают, а при
// недоступной службе свой граф не открывается — карта не нужна (рецензия
// этапа 121: каждый вопрос открывал граф через CachedGraph).
func TestMapOffSkipsServiceAndOwnGraph(t *testing.T) {
	_, coll, _ := remoteFixture(t)
	fm := &fakeMap{err: errDown}
	out := Build(pairQuestion, Deps{Coll: coll, Map: fm, GraphOn: false,
		LocalGraph: func() (*graph.Graph, func(), error) {
			t.Error("карта выключена, а свой граф открыт")
			return nil, nil, errors.New("не нужно")
		}}, remoteSettings())
	if fm.calls != 0 {
		t.Errorf("карта выключена, а у службы спрошена %d раз", fm.calls)
	}
	if out.Entities != 0 {
		t.Errorf("карта подмешана при выключенной карте: %+v", out)
	}
}

// Служба не ответила, а своего графа нет — карты нет, и сказано почему.
func TestServiceDownWithoutOwnGraph(t *testing.T) {
	_, coll, _ := remoteFixture(t)
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: errDown}, GraphOn: true}, remoteSettings())
	if !out.Empty() {
		t.Errorf("подмешано без службы и без своего графа: %q", out.Text)
	}
	if !strings.Contains(out.Remote, "kb.example") || !strings.Contains(out.Remote, "своего графа нет") {
		t.Errorf("заметка не называет ни службу, ни причину: %q", out.Remote)
	}
}

// downLibrary — служба, до которой нет соединения.
type downLibrary struct{}

func (downLibrary) Names() ([]string, error)         { return nil, errDown }
func (downLibrary) Source(string) (kb.Source, error) { return nil, errDown }

// Если книги уже ушли на свои файлы при открытии коллекции, карту у службы
// не спрашивают второй раз — сразу свой граф.
func TestCollectionFellBackSkipsServiceMap(t *testing.T) {
	base, _, g := remoteFixture(t)
	src, err := kb.Fallback{Remote: downLibrary{}, Local: base, Default: "books"}.Source("")
	if err != nil {
		t.Fatal(err)
	}
	fm := &fakeMap{g: g}
	out := Build(pairQuestion, Deps{Coll: src, Map: fm, GraphOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) { return g, func() {}, nil }}, remoteSettings())
	if fm.calls != 0 {
		t.Errorf("служба, не ответившая на открытие коллекции, спрошена ещё %d раз", fm.calls)
	}
	if out.Entities == 0 || out.Remote != kb.FallbackNote {
		t.Errorf("подмес по своим файлам: %+v", out)
	}
}

// Журнал шагов: запись подмеса через службу помечена, а переход на свои
// файлы назван в note (А4).
func TestServiceMixLogsFallback(t *testing.T) {
	_, coll, g := remoteFixture(t)
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := steplog.New(t.TempDir(), pat, time.Now(), "test", true)
	Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: errDown}, GraphOn: true, Steps: w, Turn: "t1",
		LocalGraph: func() (*graph.Graph, func(), error) { return g, func() {}, nil }}, remoteSettings())
	w.Close()
	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatalf("журнал не записан: %v", err)
	}
	var st steplog.Step
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var s steplog.Step
		if json.Unmarshal([]byte(line), &s) == nil && s.Kind == steplog.KindSearch {
			st = s
		}
	}
	if st.Note != kb.FallbackNote || st.Extra["service"] != true {
		t.Errorf("запись подмеса: note %q, extra %v", st.Note, st.Extra)
	}
}
