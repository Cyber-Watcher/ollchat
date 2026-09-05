package mixer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// fakeColl — коллекция из нескольких кусков, отдающая их в заданном порядке.
type fakeColl struct {
	hits []kb.Result
	got  kb.SearchOpts
}

func (c *fakeColl) Name() string { return "проба" }
func (c *fakeColl) SearchWith(_ context.Context, _ string, opt kb.SearchOpts, _ kb.Embedder) ([]kb.Result, error) {
	c.got = opt
	out := append([]kb.Result(nil), c.hits...)
	if opt.TopK > 0 && len(out) > opt.TopK {
		out = out[:opt.TopK]
	}
	return out, nil
}
func (c *fakeColl) Around(string, int) ([]kb.Result, error) { return nil, nil }
func (c *fakeColl) Books() []kb.BookRec                     { return nil }
func (c *fakeColl) Stats() kb.Stats                         { return kb.Stats{} }
func (c *fakeColl) SearchNote() string                      { return "" }

// reverser — реранкер, переворачивающий выдачу: так видно, применили его или нет.
type reverser struct{}

func (reverser) Model() string { return "переворот" }
func (reverser) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	out := make([]float64, len(docs))
	for i := range docs {
		out[i] = float64(i) // чем дальше в списке, тем уместнее
	}
	return out, nil
}

// broken — служба переранжирования, которая не отвечает.
type broken struct{}

func (broken) Model() string { return "сломанный" }
func (broken) Rerank(context.Context, string, []string) ([]float64, error) {
	return nil, errors.New("служба недоступна")
}

func threeHits() []kb.Result {
	return []kb.Result{
		{ID: "проба/1#1", Book: "Первая", Unit: "стр.", UnitFrom: 1, Snippet: "первый"},
		{ID: "проба/2#2", Book: "Вторая", Unit: "стр.", UnitFrom: 2, Snippet: "второй"},
		{ID: "проба/3#3", Book: "Третья", Unit: "стр.", UnitFrom: 3, Snippet: "третий"},
	}
}

// Вторая ступень применяется к подмешиванию — ради этого правка и делалась:
// раньше её получали только модели, умеющие инструменты.
func TestMixerUsesReranker(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	s := Settings{TopK: 3, RerankOpts: kb.RerankOpts{Candidates: 10}}
	res := books(coll, "запрос", "вопрос", 3, Deps{Coll: coll, Reranker: reverser{}}, s)
	if res.Empty() {
		t.Fatal("подмешивание пусто")
	}
	first := strings.Index(res.Text, "Третья")
	last := strings.Index(res.Text, "Первая")
	if first < 0 || last < 0 || first > last {
		t.Errorf("порядок не переставлен второй ступенью:\n%s", res.Text)
	}
	// Первая ступень обязана отдать шире, чем показываем.
	if coll.got.TopK != 10 {
		t.Errorf("кандидатов у первой ступени %d, ожидалось 10", coll.got.TopK)
	}
}

// Сбой службы переранжирования не отменяет подмешивания: выдача первой
// ступени остаётся как есть.
func TestMixerSurvivesRerankerFailure(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	s := Settings{TopK: 2, RerankOpts: kb.RerankOpts{Candidates: 10}}
	res := books(coll, "запрос", "вопрос", 2, Deps{Coll: coll, Reranker: broken{}}, s)
	if res.Empty() {
		t.Fatal("сбой второй ступени не должен отменять подмешивание")
	}
	if !strings.Contains(res.Text, "Первая") {
		t.Errorf("ожидалась выдача первой ступени:\n%s", res.Text)
	}
	if res.Chunks != 2 {
		t.Errorf("показано кусков %d, ожидалось 2 — лишние кандидаты обязаны отрезаться", res.Chunks)
	}
}

// Без реранкера ничего не меняется: первая ступень не расширяется.
func TestMixerWithoutRerankerUnchanged(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	res := books(coll, "запрос", "вопрос", 2, Deps{Coll: coll}, Settings{TopK: 2})
	if res.Chunks != 2 || coll.got.TopK != 2 {
		t.Errorf("без второй ступени TopK=%d, кусков %d — ожидалось 2 и 2", coll.got.TopK, res.Chunks)
	}
}

// Порядок частей блока выдержек: граница чужого текста — выдержки — требование
// ссылаться на книгу.
//
// Оба края закреплены замерами и оба легко потерять при следующей правке.
// Требование стоит ПОСЛЕДНИМ по замеру 24.08.2026 (в начале блока модель
// отвечала по памяти и ни разу не сослалась на книгу). Граница стоит ПЕРВОЙ,
// потому что она объявляет, чем является всё, что за ней следует; поставленная
// после, она объявляла бы это задним числом.
func TestBooksBlockOrder(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	out := books(coll, "вопрос", "вопрос", 3, Deps{BooksOn: true}, Settings{TopK: 3})

	note := strings.Index(out.Text, kb.QuotedDataNote)
	quotes := strings.Index(out.Text, "Выдержки из книг пользователя")
	style := strings.Index(out.Text, "Это опора для ответа")

	if note < 0 || quotes < 0 || style < 0 {
		t.Fatalf("в блоке нет одной из частей (граница %d, выдержки %d, требование %d):\n%s",
			note, quotes, style, out.Text)
	}
	if !(note < quotes && quotes < style) {
		t.Fatalf("порядок частей нарушен: граница %d, выдержки %d, требование %d:\n%s",
			note, quotes, style, out.Text)
	}
}
