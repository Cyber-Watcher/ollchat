package find

import (
	"context"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// fakeSource — коллекция с заранее известной выдачей: проверяем ядро,
// а не BM25.
type fakeSource struct {
	hits []kb.Result
	seen kb.SearchOpts
	note string
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) SearchWith(_ context.Context, _ string, opt kb.SearchOpts, _ kb.Embedder) ([]kb.Result, error) {
	f.seen = opt
	out := f.hits
	if len(out) > opt.TopK {
		out = out[:opt.TopK]
	}
	return out, nil
}
func (f *fakeSource) Around(string, int) ([]kb.Result, error) { return nil, nil }
func (f *fakeSource) Books() []kb.BookRec                     { return nil }
func (f *fakeSource) Stats() kb.Stats                         { return kb.Stats{} }
func (f *fakeSource) SearchNote() string                      { return f.note }

// fakeReranker переворачивает порядок: так видно, что вторая ступень
// отработала и что ей отдали расширенный набор кандидатов.
type fakeReranker struct{ got int }

func (r *fakeReranker) Model() string { return "rr" }
func (r *fakeReranker) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	r.got = len(docs)
	out := make([]float64, len(docs))
	for i := range docs {
		out[i] = float64(i)
	}
	return out, nil
}

func hits(n int) []kb.Result {
	out := make([]kb.Result, n)
	for i := range out {
		out[i] = kb.Result{ID: "fake/1#" + string(rune('a'+i)), Chunk: i, Text: "t", Score: float64(10 - i)}
	}
	return out
}

// Без второй ступени выдача — первые TopK; с ней первая ступень отдаёт
// кандидатов сверх TopK, а вторая обрезает до TopK.
func TestBooksRerankWidensCandidates(t *testing.T) {
	src := &fakeSource{hits: hits(30), note: "смысл в порядке"}
	o := Opts{TopK: 5, Semantic: false, Rerank: true, RerankOpts: kb.RerankOpts{Candidates: 20}}
	got, note, err := Books(context.Background(), Deps{Source: src}, "q", "q", o)
	if err != nil || len(got) != 5 {
		t.Fatalf("без реранкера: %d, %v", len(got), err)
	}
	if src.seen.TopK != 5 || note != "смысл в порядке" {
		t.Fatalf("без реранкера первая ступень должна отдать ровно TopK: %d; note %q", src.seen.TopK, note)
	}

	rr := &fakeReranker{}
	got, _, err = Books(context.Background(), Deps{Source: src, Reranker: rr}, "q", "q", o)
	if err != nil || len(got) != 5 {
		t.Fatalf("с реранкером: %d, %v", len(got), err)
	}
	if src.seen.TopK != 20 || rr.got != 20 {
		t.Fatalf("реранкеру должны отдать 20 кандидатов: первая ступень %d, реранкер %d", src.seen.TopK, rr.got)
	}
	if got[0].Chunk == 0 {
		t.Fatal("реранкер должен был переставить верхушку")
	}
}

func TestSignalsAndExpand(t *testing.T) {
	s := signalsOf(hits(3))
	if s.Hits != 3 || s.Top1Gap < 0.09 || s.Top1Gap > 0.11 {
		t.Fatalf("признаки: %+v", s)
	}
	if s := signalsOf(nil); s.Hits != 0 || s.Top1Gap != 0 {
		t.Fatalf("пустая выдача: %+v", s)
	}
	if got := Expand("вопрос", nil, Opts{ExpandLimit: 3}); got != "вопрос" {
		t.Fatalf("без понятий запрос не меняется: %q", got)
	}
}

// Docs и Exact доходят до коллекции: инструмент ищет по одной книге.
func TestBooksPassesFilters(t *testing.T) {
	src := &fakeSource{hits: hits(3)}
	_, _, err := Books(context.Background(), Deps{Source: src}, "q", "q", Opts{TopK: 3, Docs: []uint32{7}, Exact: "точно"})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.seen.Docs) != 1 || src.seen.Docs[0] != 7 || src.seen.Exact != "точно" {
		t.Fatalf("фильтры не дошли: %+v", src.seen)
	}
}

// Ноль в kb.rerank_candidates значит «двадцать» (config.go), и первая ступень
// обязана читать его так же, как вторая. Замер 04.09.2026: пока ядро брало
// ноль буквально, реранкер переставлял ровно TopK кусков, и recall с ним
// падал до recall одной ступени (0.370 → 0.350 на 457 вопросах).
func TestBooksRerankZeroCandidatesMeansDefault(t *testing.T) {
	src := &fakeSource{hits: hits(30)}
	rr := &fakeReranker{}
	o := Opts{TopK: 5, Rerank: true} // RerankOpts не задан вовсе
	got, _, err := Books(context.Background(), Deps{Source: src, Reranker: rr}, "q", "q", o)
	if err != nil || len(got) != 5 {
		t.Fatalf("с реранкером: %d, %v", len(got), err)
	}
	if src.seen.TopK != 20 || rr.got != 20 {
		t.Fatalf("ноль кандидатов должен значить 20: первая ступень %d, реранкер %d", src.seen.TopK, rr.got)
	}
}
