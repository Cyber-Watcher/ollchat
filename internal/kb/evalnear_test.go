package kb

import (
	"context"
	"testing"
)

// Разбор промаха: промах ПРИБОРА или промах поиска (этап 105).
//
// Набор засчитывает ровно тот кусок, по которому составлен вопрос, а доводка
// выдачи намеренно выбрасывает соседние куски — значит часть «промахов» это
// случаи, когда ответ достался, а замер его не увидел. Проверяется на
// подставном поиске: настоящая коллекция тут не нужна, важно правило разбора.
func TestEvalNearMissSeparatesInstrumentFromSearch(t *testing.T) {
	cases := []struct {
		name      string
		hits      []Result
		gold      string
		wantBook  int
		wantChunk int
		wantDist  int
	}{
		{
			name:      "сосед эталона — промах прибора",
			hits:      []Result{{ID: "books/98#174", Chunk: 1}, {ID: "books/7#12", Chunk: 2}},
			gold:      "books/98#173",
			wantBook:  1,
			wantChunk: 1,
			wantDist:  1,
		},
		{
			name:      "та же книга, но другая глава — не сосед",
			hits:      []Result{{ID: "books/98#900", Chunk: 1}},
			gold:      "books/98#173",
			wantBook:  1,
			wantChunk: 0,
			wantDist:  727,
		},
		{
			name:      "чужие книги — настоящий промах поиска",
			hits:      []Result{{ID: "books/7#12", Chunk: 1}, {ID: "books/9#3", Chunk: 2}},
			gold:      "books/98#173",
			wantBook:  0,
			wantChunk: 0,
		},
	}
	for _, c := range cases {
		hits := c.hits
		opt := EvalOpts{K: 5, Search: func(context.Context, string, SearchOpts, int) ([]Result, error) {
			return hits, nil
		}}
		var coll Collection
		rep, err := coll.Eval(context.Background(), []EvalCase{{Query: "вопрос", ChunkID: c.gold}},
			EvalLexical, opt, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(rep.Missed) != 1 {
			t.Fatalf("%s: ожидался промах, получено Missed=%v", c.name, rep.Missed)
		}
		if rep.NearBook != c.wantBook || rep.NearChunk != c.wantChunk {
			t.Errorf("%s: та же книга %d (ждали %d), кусок рядом %d (ждали %d)",
				c.name, rep.NearBook, c.wantBook, rep.NearChunk, c.wantChunk)
		}
		if c.wantBook > 0 && (len(rep.NearDist) != 1 || rep.NearDist[0] != c.wantDist) {
			t.Errorf("%s: расстояния %v, ждали [%d]", c.name, rep.NearDist, c.wantDist)
		}
	}
}

// Разбор ссылки на кусок: он же лежит в основе меры, и ошибка в нём тихо
// превратила бы все расстояния в ноль.
func TestSplitChunkID(t *testing.T) {
	cases := []struct {
		id       string
		doc, ord int
		ok       bool
	}{
		{"books/98#173", 98, 173, true},
		{"lab/7#0", 7, 0, true},
		{"books/98", 0, 0, false},
		{"98#173", 0, 0, false},
		{"books/abc#173", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		doc, ord, ok := splitChunkID(c.id)
		if ok != c.ok || doc != c.doc || ord != c.ord {
			t.Errorf("splitChunkID(%q) = %d, %d, %v — ждали %d, %d, %v",
				c.id, doc, ord, ok, c.doc, c.ord, c.ok)
		}
	}
}

// Мера «та же страница» (этап 105, З4): попаданием считается и кусок-сосед,
// накрывающий страницу эталона, а строгая мера при этом не меняется.
func TestEvalNearPageCountsAsHit(t *testing.T) {
	gold := "books/98#173"
	// Сосед по номеру и по странице: эталон на стр. 73, сосед накрывает 73–74.
	hits := []Result{
		{ID: "books/98#174", Chunk: 1, UnitFrom: 73, UnitTo: 74},
		{ID: "books/7#12", Chunk: 2, UnitFrom: 5, UnitTo: 6},
	}
	opt := EvalOpts{K: 5, Search: func(context.Context, string, SearchOpts, int) ([]Result, error) {
		return hits, nil
	}}
	var coll Collection
	rep, err := coll.Eval(context.Background(), []EvalCase{{Query: "вопрос", ChunkID: gold}},
		EvalLexical, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recall != 0 {
		t.Errorf("строгая мера не должна засчитывать соседа: recall %.2f", rep.Recall)
	}
	if rep.RecallNear != 1 {
		t.Errorf("мера «та же страница» обязана засчитать соседа: recall %.2f", rep.RecallNear)
	}
	if rep.AvgRankNear != 1 {
		t.Errorf("сосед стоял первым, среднее место %.1f", rep.AvgRankNear)
	}

	// Далёкий кусок той же книги — не попадание ни по одной мере.
	far := []Result{{ID: "books/98#900", Chunk: 1, UnitFrom: 400, UnitTo: 401}}
	opt.Search = func(context.Context, string, SearchOpts, int) ([]Result, error) { return far, nil }
	rep, err = coll.Eval(context.Background(), []EvalCase{{Query: "вопрос", ChunkID: gold}},
		EvalLexical, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RecallNear != 0 {
		t.Errorf("далёкий кусок засчитан как «та же страница»: recall %.2f", rep.RecallNear)
	}
}
