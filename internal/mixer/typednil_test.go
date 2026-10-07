package mixer

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// Ненастроенный реранкер — это отсутствие реранкера, даже когда он пришёл
// nil своего типа.
//
// kbrerank.New без адреса отдаёт (*Reranker)(nil), и в Deps он проходил
// проверку `!= nil`: привратник считал выдачу переранжированной и мерил её
// порогами реранкера, хотя второй ступени не было. С порогом разрыва мест
// это заглушало подмешивание по вопросу, на который в книгах ответ есть.
func TestTypedNilRerankerIsNoReranker(t *testing.T) {
	dir := t.TempDir()
	base, err := kb.OpenBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Create("books", "")
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(coll.Dir(), "books", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	id, _, err := g.Entities().Add("горутина", graph.TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	for ord := uint32(1); ord <= 3; ord++ {
		if err := g.Mentions().Add(id, graph.ChunkKey{Doc: 1, Ord: ord}); err != nil {
			t.Fatal(err)
		}
	}

	var rr *kbrerank.Reranker // так kbrerank.New отвечает «не настроено»
	var emb *kbembed.Embedder // так kbembed.New отвечает «модель не задана»
	s := Settings{Collection: "books", TopK: 3, Entities: 6, Neighbors: 4,
		Abstain: true, AbstainGap: 0.99, SenseEntry: true}
	src := &fakeColl{hits: threeHits()}

	want := Build("как устроена горутина", Deps{Coll: src, Graph: g, GraphOn: true, BooksOn: true}, s)
	if want.Empty() {
		t.Fatal("подготовка: без реранкера подмешивание пусто")
	}
	got := Build("как устроена горутина",
		Deps{Coll: src, Graph: g, GraphOn: true, BooksOn: true, Reranker: rr, Embedder: emb}, s)
	if got.Empty() {
		t.Fatal("nil своего типа принят за реранкер: привратник заглушил подмешивание")
	}
	if got.Text != want.Text {
		t.Errorf("с nil своего типа подмешано иное, чем без зависимостей:\n%s\n---\n%s", got.Text, want.Text)
	}
}
