package mixer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Этап 112, Ч17: подмешивание пишет ОДНУ запись `search` на вопрос с оценкой
// реранкера — по ней `stepstat.py --gaps` строит отчёт о пробелах библиотеки.
// До 16.10.2026 подмешивание журнала не знало, и поисков по книгам в отчёте
// не было ни одного.
func TestBuildLogsOneSearch(t *testing.T) {
	score := -2.0
	cases := []struct {
		name      string
		hits      []kb.Result
		rr        kb.Reranker
		wantMixed bool
		wantTop1  string // "low", "high" или "none"
	}{
		// Графа в пробе нет, а без графа привратник не стоит (прежний порядок Build):
		// выдержки поданы и при оценке ниже порога. Отчёт о пробелах смотрит на top1.
		{"не по предмету: ниже порога", threeHits(), lowScorer{}, true, "low"},
		{"по делу: выше порога, выдержки поданы", threeHits(), highScorer{}, true, "high"},
		{"в книгах пусто: hits 0 и без top1", nil, highScorer{}, false, "none"},
	}
	for _, c := range cases {
		pat, err := chatlog.ParsePattern("steps.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		w := steplog.New(t.TempDir(), pat, time.Now(), "test", true)
		coll := &fakeColl{hits: c.hits}
		Build("вопрос", Deps{Coll: coll, Reranker: c.rr, BooksOn: true, Steps: w, Turn: "t1"},
			Settings{Collection: "проба", TopK: 3, Abstain: true, AbstainScore: &score})
		w.Close()
		data, err := os.ReadFile(w.Path())
		if err != nil {
			t.Fatalf("%s: журнал не записан: %v", c.name, err)
		}
		var found []steplog.Step
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var st steplog.Step
			if json.Unmarshal([]byte(line), &st) == nil && st.Kind == steplog.KindSearch {
				found = append(found, st)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s: записей search %d, ждали 1", c.name, len(found))
		}
		st := found[0]
		if st.Turn != "t1" || st.Args != "вопрос" || st.Extra["collection"] != "проба" || st.Extra["reranked"] != true {
			t.Errorf("%s: поля записи %+v", c.name, st)
		}
		if st.Extra["mixed"] != c.wantMixed {
			t.Errorf("%s: mixed %v, ждали %v", c.name, st.Extra["mixed"], c.wantMixed)
		}
		top1, has := st.Extra["top1"].(float64)
		switch c.wantTop1 {
		case "low":
			if !has || top1 >= score {
				t.Errorf("%s: top1 %v, ждали ниже %v", c.name, st.Extra["top1"], score)
			}
		case "high":
			if !has || top1 <= score {
				t.Errorf("%s: top1 %v, ждали выше %v", c.name, st.Extra["top1"], score)
			}
		case "none":
			if has || st.Extra["hits"] != float64(0) {
				t.Errorf("%s: пустая выдача записана как %+v", c.name, st.Extra)
			}
		}
	}
}

// Без журнала подмешивание работает как прежде и ничего не пишет.
func TestBuildWithoutStepsUnchanged(t *testing.T) {
	coll := &fakeColl{hits: threeHits()}
	out := Build("вопрос", Deps{Coll: coll, Reranker: highScorer{}, BooksOn: true},
		Settings{Collection: "проба", TopK: 3})
	if out.Empty() {
		t.Fatal("без журнала подмес пуст — сломано само подмешивание")
	}
}
