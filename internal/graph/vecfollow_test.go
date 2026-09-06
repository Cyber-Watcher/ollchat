package graph

import (
	"context"
	"errors"
	"testing"
)

// countingEmbedder — подменный эмбеддер, который считает, сколько имён у него
// спросили, и умеет отвечать заданным отпечатком весов.
type countingEmbedder struct {
	model  string
	digest string
	dim    int
	asked  int
	fail   error
}

func (e *countingEmbedder) Model() string { return e.model }

func (e *countingEmbedder) Stamp(context.Context) (string, error) { return e.digest, nil }

func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.fail != nil {
		return nil, e.fail
	}
	e.asked += len(texts)
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, e.dim)
		v[i%e.dim] = 1
		out[i] = v
	}
	return out, nil
}

// growGraph — граф с n понятиями, названными по номеру.
func growGraph(t *testing.T, n int) *Graph {
	t.Helper()
	g, _ := graph(t)
	for i := 0; i < n; i++ {
		if _, _, err := g.Entities().Add(string(rune('a'+i%26))+string(rune('0'+i/26)), TypeConcept); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	return g
}

// Досчёт считает только новые понятия и дописывает их.
//
// Это и есть смысл догонщика: очередь понятий без векторов нигде не хранится,
// она выводится из двух чисел — покрытия в паспорте и числа понятий в реестре.
func TestEmbedNewCountsOnlyTail(t *testing.T) {
	g := growGraph(t, 10)
	emb := &countingEmbedder{model: "проба", digest: "sha256:aaa", dim: 8}
	ctx := context.Background()

	first, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil)
	if err != nil {
		t.Fatalf("первый заход: %v", err)
	}
	if first.Added != 10 || first.Before != 0 {
		t.Fatalf("первый заход: посчитано %d при %d прежних, ожидалось 10 при 0", first.Added, first.Before)
	}
	if emb.asked != 10 {
		t.Fatalf("эмбеддеру отдано %d имён вместо 10", emb.asked)
	}

	// Граф подрос на три понятия — считать надо ровно три.
	for i := 0; i < 3; i++ {
		if _, _, err := g.Entities().Add("новое"+string(rune('0'+i)), TypeConcept); err != nil {
			t.Fatal(err)
		}
	}
	emb.asked = 0
	second, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil)
	if err != nil {
		t.Fatalf("второй заход: %v", err)
	}
	if second.Before != 10 || second.Added != 3 || second.Total != 13 {
		t.Fatalf("второй заход: было %d, добавлено %d, всего %d — ожидалось 10/3/13",
			second.Before, second.Added, second.Total)
	}
	if emb.asked != 3 {
		t.Errorf("эмбеддеру отдано %d имён вместо 3: пересчитано лишнее", emb.asked)
	}
	if got := g.VectorsInfo().Count; got != 13 {
		t.Errorf("в паспорте %d векторов, ожидалось 13", got)
	}
}

// Ничего нового — заход ничего не спрашивает и не пишет.
func TestEmbedNewDoesNothingWhenUpToDate(t *testing.T) {
	g := growGraph(t, 4)
	emb := &countingEmbedder{model: "проба", digest: "sha256:aaa", dim: 8}
	ctx := context.Background()

	if _, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil); err != nil {
		t.Fatal(err)
	}
	emb.asked = 0
	res, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil)
	if err != nil {
		t.Fatalf("холостой заход: %v", err)
	}
	if res.Added != 0 || emb.asked != 0 {
		t.Errorf("на холостом заходе посчитано %d понятий (спрошено %d)", res.Added, emb.asked)
	}
	if res.Pending() != 0 {
		t.Errorf("ждут вектора %d понятий, ожидалось 0", res.Pending())
	}
}

// Предел на заход соблюдается, а остаток честно объявляется ждущим.
//
// Предел нужен догонщику: заход обязан кончаться за обозримое время, чтобы
// отпечаток весов сверялся каждую пачку, а остановка теряла не всю работу.
func TestEmbedNewRespectsLimit(t *testing.T) {
	g := growGraph(t, 10)
	emb := &countingEmbedder{model: "проба", digest: "sha256:aaa", dim: 8}

	res, err := g.EmbedNewEntities(context.Background(), emb, EmbedOpts{}, 4, nil)
	if err != nil {
		t.Fatalf("заход с пределом: %v", err)
	}
	if res.Added != 4 {
		t.Errorf("посчитано %d понятий вместо 4", res.Added)
	}
	if res.Pending() != 6 {
		t.Errorf("ждут %d понятий, ожидалось 6", res.Pending())
	}
	if got := g.VectorsInfo().Count; got != 4 {
		t.Errorf("в паспорте %d векторов, ожидалось 4", got)
	}
}

// Сменился эмбеддер — досчитывать нельзя, и об этом говорится.
func TestEmbedNewRefusesOtherModel(t *testing.T) {
	g := growGraph(t, 4)
	ctx := context.Background()
	if _, err := g.EmbedNewEntities(ctx, &countingEmbedder{model: "проба", dim: 8}, EmbedOpts{}, 0, nil); err != nil {
		t.Fatal(err)
	}
	_, err := g.EmbedNewEntities(ctx, &countingEmbedder{model: "другая", dim: 8}, EmbedOpts{}, 0, nil)
	if err == nil {
		t.Fatal("векторы другой модели дописаны молча")
	}
}

// Сбой эмбеддера не портит посчитанное: файл остаётся на прежнем месте.
func TestEmbedNewKeepsFileOnFailure(t *testing.T) {
	g := growGraph(t, 6)
	ctx := context.Background()
	emb := &countingEmbedder{model: "проба", digest: "sha256:aaa", dim: 8}
	if _, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 3, nil); err != nil {
		t.Fatal(err)
	}

	emb.fail = errors.New("сервер недоступен")
	if _, err := g.EmbedNewEntities(ctx, emb, EmbedOpts{}, 0, nil); err == nil {
		t.Fatal("сбой эмбеддера не превратился в ошибку")
	}
	if got := g.VectorsInfo().Count; got != 3 {
		t.Errorf("после сбоя в паспорте %d векторов, ожидалось 3", got)
	}
}
