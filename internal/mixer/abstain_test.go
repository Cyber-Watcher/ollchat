package mixer

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// lowScorer — реранкер, который всем кускам ставит оценку далеко ниже порога:
// так он ведёт себя на вопросе не по предмету (по замеру — около −11, тогда как
// «по делу» около +1).
type lowScorer struct{}

func (lowScorer) Model() string { return "низкие оценки" }
func (lowScorer) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	out := make([]float64, len(docs))
	for i := range docs {
		out[i] = -11 - float64(i)/10
	}
	return out, nil
}

// highScorer — оценки уверенно выше порога, как на вопросе по делу.
type highScorer struct{}

func (highScorer) Model() string { return "высокие оценки" }
func (highScorer) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	out := make([]float64, len(docs))
	for i := range docs {
		out[i] = 1.5 - float64(i)/10
	}
	return out, nil
}

// Признаки уверенности доезжают из поиска до привратника (этап 105, Ж9):
// по ним он и решает, молчать ли. Без них решать было бы нечем.
func TestBooksReturnsSignals(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	s := Settings{Collection: "проба", TopK: 3}

	out, sig := books(coll, "запрос", "вопрос", 3, Deps{Coll: coll, Reranker: highScorer{}}, s)
	if out.Empty() {
		t.Fatal("выдержки не собрались — тест мерит не то")
	}
	if sig.Hits != 3 {
		t.Errorf("кусков в признаках %d, ожидалось 3", sig.Hits)
	}
	if sig.Top1 < 1.0 {
		t.Errorf("оценка первого места %.2f — высокий реранкер должен давать больше 1", sig.Top1)
	}

	_, low := books(coll, "запрос", "вопрос", 3, Deps{Coll: coll, Reranker: lowScorer{}}, s)
	if low.Top1 > -10 {
		t.Errorf("оценка первого места %.2f — низкий реранкер должен давать около −11", low.Top1)
	}
}

// Воздержание молчит на вопросе не по предмету и не мешает вопросу по делу.
// Карта понятий здесь не нужна: решение принимается до её сборки, и графа
// в пробе нет вовсе — значит проверяется ровно ветвь выдержек.
func TestAbstainDecision(t *testing.T) {
	score := -2.0
	cases := []struct {
		name     string
		rr       kb.Reranker
		abstain  bool
		wantHush bool
	}{
		{"не по предмету, воздержание включено", lowScorer{}, true, true},
		{"по делу, воздержание включено", highScorer{}, true, false},
		{"не по предмету, воздержание выключено", lowScorer{}, false, false},
	}
	for _, c := range cases {
		coll := &fakeColl{hits: threeHits()}
		s := Settings{Collection: "проба", TopK: 3, Abstain: c.abstain, AbstainScore: &score}
		out, sig := books(coll, "запрос", "вопрос", 3, Deps{Coll: coll, Reranker: c.rr}, s)
		hush := s.Abstain && sig.Top1 < score
		if hush != c.wantHush {
			t.Errorf("%s: молчать=%v, ожидалось %v (оценка %.2f, порог %.2f)",
				c.name, hush, c.wantHush, sig.Top1, score)
		}
		// Сами выдержки собираются в любом случае: молчать или нет — решает
		// привратник выше, и именно это разделение позволяет мерить цену.
		if !c.wantHush && !strings.Contains(out.Text, "Выдержки из книг") {
			t.Errorf("%s: выдержки не собрались", c.name)
		}
	}
}
