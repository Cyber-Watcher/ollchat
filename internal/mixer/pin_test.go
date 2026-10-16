package mixer

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// longChunk — кусок длиннее выдержки, нужное слово в самом конце: так видно,
// подан ли кусок целиком или вырезано окно вокруг совпадения.
func longChunk() kb.Result {
	text := strings.Repeat("посторонний текст про другое ", 60) + "ответ: ключевоеслово 42."
	return kb.Result{ID: "проба/7#3", Book: "Эталонная", Unit: "стр.", UnitFrom: 9,
		Text: text, Snippet: text}
}

// Закреплённый кусок идёт модели без поиска и без привратника: потолок поиска
// меряет ответ при безупречном поиске, и всё, что могло бы кусок не пропустить,
// и есть измеряемое (этап 112, О7).
func TestPinBypassesSearchAndGate(t *testing.T) {
	coll := &fakeColl{hits: threeHits(), byID: map[string]kb.Result{"проба/7#3": longChunk()}}
	// Ни графа, ни BooksOn: обычный путь вернул бы пустой подмес.
	res := Build("где ключевоеслово", Deps{Coll: coll}, Settings{Pin: []string{"проба/7#3"}, TopK: 3})
	if res.Chunks != 1 {
		t.Fatalf("кусков %d, ожидался один закреплённый:\n%s", res.Chunks, res.Text)
	}
	if coll.searched != 0 {
		t.Errorf("с закреплённым куском искать нельзя, а поиск звали %d раз", coll.searched)
	}
	if !strings.Contains(res.Text, "Эталонная") || strings.Contains(res.Text, "Первая") {
		t.Errorf("в подмесе не эталонный кусок:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, kb.QuotedDataNote) {
		t.Errorf("закреплённый кусок обязан идти тем же блоком, что найденные:\n%s", res.Text)
	}
}

// Без FullChunks закреплённый кусок режется той же выдержкой, что найденный
// поиском, — иначе разница двух ответов смешала бы поиск с длиной цитаты.
func TestPinCutsLikeSearch(t *testing.T) {
	ch := longChunk()
	coll := &fakeColl{byID: map[string]kb.Result{ch.ID: ch}}
	q := "где ключевоеслово"

	cut := Build(q, Deps{Coll: coll}, Settings{Pin: []string{ch.ID}})
	want := kb.Snippet(ch.Text, q, 0)
	if !strings.Contains(cut.Text, strings.TrimSpace(want)) {
		t.Fatalf("выдержка не та, что режет kb.Snippet:\n%s", cut.Text)
	}
	if strings.Contains(cut.Text, strings.TrimSpace(ch.Text)) {
		t.Fatalf("без FullChunks кусок подан целиком")
	}
	if !strings.Contains(cut.Text, "ключевоеслово") {
		t.Errorf("окно выдержки не захватило совпадение:\n%s", cut.Text)
	}

	full := Build(q, Deps{Coll: coll}, Settings{Pin: []string{ch.ID}, FullChunks: true})
	if !strings.Contains(full.Text, strings.TrimSpace(ch.Text)) {
		t.Errorf("с FullChunks кусок обязан идти целиком")
	}
}

// FullChunks действует и на найденное поиском: это вторая половина замера —
// «а если давать найденные куски целиком».
func TestFullChunksForSearchHits(t *testing.T) {
	hits := threeHits()
	for i := range hits {
		hits[i].Text = hits[i].Snippet + " и продолжение куска"
	}
	coll := &fakeColl{hits: hits}
	out, _ := books(coll, "вопрос", "вопрос", 3, Deps{}, Settings{TopK: 3, FullChunks: true})
	if !strings.Contains(out.Text, "первый и продолжение куска") {
		t.Errorf("найденные куски не поданы целиком:\n%s", out.Text)
	}
	cut, _ := books(coll, "вопрос", "вопрос", 3, Deps{}, Settings{TopK: 3})
	if strings.Contains(cut.Text, "продолжение куска") {
		t.Errorf("без FullChunks идёт выдержка, а не кусок:\n%s", cut.Text)
	}
}

// Ссылка, которой нет в коллекции, не подменяется ничем: подмес пуст,
// и прибор видит это по числу кусков, а не по ответу модели.
func TestPinMissingRef(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	res := Build("вопрос", Deps{Coll: coll, BooksOn: true}, Settings{Pin: []string{"проба/404#1"}, TopK: 3})
	if res.Chunks != 0 || !res.Empty() {
		t.Errorf("по несуществующей ссылке подмешано %d кусков:\n%s", res.Chunks, res.Text)
	}
	if coll.searched != 0 {
		t.Errorf("несуществующий эталон не повод искать обычным путём")
	}
}
