package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// dirEmbedder — вектор по таблице «текст → направление».
//
// fakeEmbedder кодирует длину текста, и у коротких имён векторы отличаются
// в четвёртом знаке: относительный отбор по смыслу на таком различии мерил бы
// шум. Здесь направления заданы явно, и «близко» отличается от «далеко»
// на всю единицу — тогда видно, КТО занял места входа, а не кому повезло.
type dirEmbedder struct {
	by map[string][]float32
}

func (dirEmbedder) Model() string { return "проба" }

func (e dirEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := e.by[t]
		if !ok {
			v = []float32{0, 0, 0, 1} // всё незнакомое — в свою сторону
		}
		out[i] = v
	}
	return out, nil
}

// quotaGraph — граф, в котором мест входа ровно столько, сколько претендентов.
//
// «альфа» и «бета» называет сам вопрос (словесный вход), «гамма» и «дельта»
// близки к нему по смыслу, «эпсилон —использует→ дзета» — единственная тройка
// индекса, и она тоже о вопросе. При TopEntities = 4 всем места не хватает:
// именно этот случай и разбирает квота.
func quotaGraph(t *testing.T) (*Graph, []int8) {
	t.Helper()
	g := newGraphWith(t, "альфа", "бета", "гамма", "дельта", "эпсилон", "дзета")
	for _, e := range []Edge{
		{Src: 5, Dst: 6, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}},
		{Src: 5, Dst: 6, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 9}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for id := uint32(1); id <= 6; id++ {
		for ord := uint32(1); ord <= 3; ord++ {
			if err := g.Mentions().Add(id, ChunkKey{Doc: 1, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}

	near := []float32{1, 0, 0, 0}
	emb := dirEmbedder{by: map[string][]float32{
		"гамма":  near,
		"дельта": near,
		"эпсилон —использует→ дзета": near,
		"альфа":   {0, 1, 0, 0},
		"бета":    {0, 1, 0, 0},
		"эпсилон": {0, 0, 1, 0},
		"дзета":   {0, 0, 1, 0},
	}}
	ctx := context.Background()
	if err := g.EmbedEntities(ctx, emb, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.EmbedTriples(ctx, emb, 2, EmbedOpts{Batch: 8}, nil); err != nil {
		t.Fatal(err)
	}
	if info := g.EdgeVectorsInfo(); !info.Ready || info.Count != 1 {
		t.Fatalf("индекс троек не собрался: %+v", info)
	}
	vec, err := emb.Embed(ctx, []string{"гамма"})
	if err != nil {
		t.Fatal(err)
	}
	return g, kb.Quantize(vec[0])
}

// matchedNames собирает имена понятий выдачи с заданной пометкой входа.
func matchedNames(res SearchResult, mark string) []string {
	var out []string
	for _, e := range res.Entities {
		if e.Matched == mark {
			out = append(out, e.Name)
		}
	}
	return out
}

// Квота мест для троек (этап 105, В2.б).
//
// Замер В1 20.09.2026 показал, что вход по тройкам ничего не меняет — выдача
// при triple_limit 0 и 3 побайтно одинакова. Причина не в отборе троек,
// а в очереди за местами: смысловой вход забирает свою половину раньше,
// и тройкам остаётся room = 0. Тест воспроизводит ровно это и проверяет,
// что квота очередь меняет, а без квоты поведение прежнее.
func TestTripleQuotaTakesRoomFromSense(t *testing.T) {
	g, q := quotaGraph(t)
	opt := SearchOpts{TopEntities: 4, QueryVector: q}

	// Без квоты: слово и смысл занимают все четыре места, троек нет.
	g.rules.TripleLimit = 2
	g.rules.TripleQuota = 0
	before := g.Search("альфа и бета", opt)
	if n := len(before.Entities); n != 4 {
		t.Fatalf("мест во входе должно быть занято 4, занято %d: %s", n, entryLine(before))
	}
	if by := matchedNames(before, "по смыслу"); len(by) != 2 {
		t.Errorf("по смыслу ожидались два понятия, получено %v (вся выдача: %s)", by, entryLine(before))
	}
	if by := matchedNames(before, "по связи"); len(by) != 0 {
		t.Errorf("без квоты тройкам мест не остаётся, а найдено %v", by)
	}

	// С квотой: те же два места уходят концам тройки.
	g.rules.TripleQuota = 2
	after := g.Search("альфа и бета", opt)
	if n := len(after.Entities); n != 4 {
		t.Fatalf("квота не должна менять число мест: занято %d (%s)", n, entryLine(after))
	}
	by := matchedNames(after, "по связи")
	if len(by) != 2 || by[0] != "эпсилон" || by[1] != "дзета" {
		t.Errorf("по связи ожидались концы тройки, получено %v (вся выдача: %s)", by, entryLine(after))
	}
	if sense := matchedNames(after, "по смыслу"); len(sense) != 0 {
		t.Errorf("квота обязана отнять места именно у смыслового входа, а он взял %v", sense)
	}

	// Понятия, названные вопросом, квота не трогает: словесный вход первый
	// и главный, иначе квота отнимала бы места у самого надёжного входа.
	for _, want := range []string{"альфа", "бета"} {
		if !strings.Contains(entryLine(after), want) {
			t.Errorf("квота вытеснила названное вопросом понятие %q: %s", want, entryLine(after))
		}
	}
}

// Квота без индекса троек и без самого входа по тройкам ничего не меняет:
// выключенное остаётся выключенным, сколько мест ему ни резервируй.
func TestTripleQuotaWithoutEntryChangesNothing(t *testing.T) {
	g, q := quotaGraph(t)
	opt := SearchOpts{TopEntities: 4, QueryVector: q}

	g.rules.TripleLimit = 0
	g.rules.TripleQuota = 2
	res := g.Search("альфа и бета", opt)
	if by := matchedNames(res, "по связи"); len(by) != 0 {
		t.Errorf("при triple_limit = 0 вход по тройкам обязан молчать, найдено %v", by)
	}
	if by := matchedNames(res, "по смыслу"); len(by) != 2 {
		t.Errorf("смысловой вход должен работать как прежде, получено %v (%s)", by, entryLine(res))
	}
}

// entryLine — вся выдача одной строкой, для сообщений об ошибке.
func entryLine(res SearchResult) string {
	var b strings.Builder
	for i, e := range res.Entities {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(e.Name)
		b.WriteString(" (" + e.Matched + ")")
	}
	return b.String()
}
