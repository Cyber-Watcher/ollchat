package kb

import (
	"context"
	"os"
	"testing"
)

// Строки замера (этап 112, О6, Л3): по одной на вопрос, в порядке набора,
// с местом эталона и выдачей как есть. Вопрос без эталона (набор «ответа
// в книгах нет») — всегда мимо, но его выдача и оценки в строке есть.
func TestEvalRowsFollowCases(t *testing.T) {
	hits := []Result{
		{ID: "books/7#12", Score: 3.5},
		{ID: "books/98#173", Score: 1.5},
	}
	opt := EvalOpts{K: 5, Search: func(context.Context, string, SearchOpts, int) ([]Result, error) {
		return hits, nil
	}}
	cases := []EvalCase{
		{Query: "с ответом", ChunkID: "books/98#173"},
		{Query: "без ответа", Note: "далёкий"},
	}
	var coll Collection
	rep, err := coll.Eval(context.Background(), cases, EvalLexical, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != len(cases) {
		t.Fatalf("строк %d, вопросов %d", len(rep.Rows), len(cases))
	}
	for i, r := range rep.Rows {
		if r.Query != cases[i].Query {
			t.Errorf("строка %d: вопрос %q, ждали %q — порядок набора нарушен", i, r.Query, cases[i].Query)
		}
		if r.Top1 != hits[0].Score {
			t.Errorf("строка %d: оценка первого места %.2f, в выдаче %.2f", i, r.Top1, hits[0].Score)
		}
		if r.Gap != TopGap(hits) {
			t.Errorf("строка %d: разрыв %.3f, по выдаче %.3f", i, r.Gap, TopGap(hits))
		}
		if len(r.IDs) != len(hits) || len(r.Scores) != len(hits) {
			t.Fatalf("строка %d: в выдаче %d кусков, в строке %d ссылок и %d оценок",
				i, len(hits), len(r.IDs), len(r.Scores))
		}
		for j, h := range hits {
			if r.IDs[j] != h.ID || r.Scores[j] != h.Score {
				t.Errorf("строка %d, место %d: %s %.2f, в выдаче %s %.2f", i, j+1, r.IDs[j], r.Scores[j], h.ID, h.Score)
			}
		}
	}
	if rep.Rows[0].Rank != 2 {
		t.Errorf("эталон стоял вторым, в строке место %d", rep.Rows[0].Rank)
	}
	if rep.Rows[1].Rank != 0 || rep.Rows[1].Note != "далёкий" {
		t.Errorf("вопрос без эталона: место %d (ждали 0), пометка %q", rep.Rows[1].Rank, rep.Rows[1].Note)
	}
	if rep.Rows[0].GoldClass != "" {
		t.Errorf("эталона нет в коллекции, а класс %q (ждали пусто)", rep.Rows[0].GoldClass)
	}
}

// TestGoldClassProse — класс эталона берётся у куска коллекции (этап 112, Ф6):
// обычный абзац — «проза», чужая ссылка — пусто.
func TestGoldClassProse(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "one.pdf", longPage("goroutines and channels"), longPage("mutex and locking"))
	c, err := base.Create("test", "проверочная коллекция")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	hits, err := c.Search("mutex locking", DefaultSearchOpts())
	if err != nil || len(hits) == 0 {
		t.Fatalf("поиск: %v, найдено %d", err, len(hits))
	}
	if got := c.goldClass(EvalCase{ChunkID: hits[0].ID}); got != "проза" {
		t.Fatalf("класс абзаца %q, ждали «проза» (ссылка %s)", got, hits[0].ID)
	}
	if got := c.goldClass(EvalCase{ChunkID: "test/999#1"}); got != "" {
		t.Fatalf("класс несуществующего куска %q, ждали пусто", got)
	}
}
