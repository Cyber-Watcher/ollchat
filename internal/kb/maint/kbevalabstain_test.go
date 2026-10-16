package maint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Колонка «молчит» в строке режима (этап 112, Ч20): порог тот же, что у
// kb_search, — с реранкером по оценке первого места, без него по разрыву мест.
func TestWorkAbstain(t *testing.T) {
	points := []kb.GapPoint{
		{Top1: -5, Gap: 0.01, Hit: false}, // далеко ниже −2, разрыв мал: молчит, честно
		{Top1: -3, Gap: 0.30, Hit: true},  // ниже −2, но кусок был: молчит зря
		{Top1: 1.5, Gap: 0.02, Hit: true}, // выше −2; разрыв мал — без реранкера молчит зря
		{Top1: 0.5, Gap: 0.50, Hit: false},
	}
	score := -2.0
	if got := workAbstain(points, &score, 0, true); got != "2/1" {
		t.Errorf("реранкер, порог −2: %q, ждали «2/1»", got)
	}
	if got := workAbstain(points, nil, 0.05, false); got != "2/1" {
		t.Errorf("без реранкера, разрыв 0.05: %q, ждали «2/1»", got)
	}
	// С реранкером, но без abstain_score — путь по разрыву, как у kb_search.
	if got := workAbstain(points, nil, 0.05, true); got != "2/1" {
		t.Errorf("реранкер без abstain_score, разрыв 0.05: %q, ждали «2/1»", got)
	}
	// Оценка первого места без реранкера несопоставима — по ней не считаем.
	if got := workAbstain(points, &score, 0, false); got != "—" {
		t.Errorf("без реранкера и без разрыва: %q, ждали «—»", got)
	}
	if got := workAbstainName(nil, 0, false); got == "" {
		t.Error("пустое имя порога")
	}
}

// Части набора (этап 112, Л2, Ч12): не пересекаются, вместе — весь набор,
// деление не зависит от порядка; на настоящем kb_golden2 доля dev около 70 %.
func TestEvalPart(t *testing.T) {
	var cases []kb.EvalCase
	for i := 0; i < 200; i++ {
		cases = append(cases, kb.EvalCase{Query: "вопрос номер " + strings.Repeat("я", i%7) + string(rune('а'+i%30)) + strings.Repeat("ё", i)})
	}
	dev, err := evalPart(cases, "dev")
	if err != nil {
		t.Fatal(err)
	}
	hold, _ := evalPart(cases, "holdout")
	if len(dev)+len(hold) != len(cases) {
		t.Fatalf("dev %d + holdout %d != %d", len(dev), len(hold), len(cases))
	}
	inDev := map[string]bool{}
	for _, c := range dev {
		inDev[c.Query] = true
	}
	for _, c := range hold {
		if inDev[c.Query] {
			t.Fatalf("вопрос в обеих частях: %q", c.Query)
		}
	}
	// Перестановка набора не меняет состав части.
	rev := make([]kb.EvalCase, len(cases))
	for i, c := range cases {
		rev[len(cases)-1-i] = c
	}
	dev2, _ := evalPart(rev, "dev")
	if len(dev2) != len(dev) {
		t.Fatalf("после перестановки dev %d против %d", len(dev2), len(dev))
	}
	if _, err := evalPart(cases, "test"); err == nil {
		t.Error("неизвестная часть принята")
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "eval", "kb_golden2.toml"))
	if err != nil {
		t.Skip("kb_golden2.toml не найден:", err)
	}
	var f evalFile
	if err := toml.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	d, _ := evalPart(f.Case, "dev")
	h, _ := evalPart(f.Case, "holdout")
	t.Logf("kb_golden2: всего %d, dev %d, holdout %d", len(f.Case), len(d), len(h))
	if share := float64(len(d)) / float64(len(f.Case)); share < 0.6 || share > 0.8 {
		t.Errorf("доля dev %.2f вне 0,6–0,8", share)
	}
}

// Паспорт в шапке --kb-eval (этап 112, У3): по нему видно, что между двумя
// прогонами не меняли библиотеку, векторы и разбор.
func TestEvalPassport(t *testing.T) {
	got := evalPassport(kb.Stats{Books: 661, Indexed: 650, Chunks: 769615, Vectors: 769615,
		VecModel: "bge-m3", VecDim: 1024, Analyzer: "v3"})
	for _, want := range []string{"книг 661", "с текстом 650", "кусков 769615", "векторов 769615",
		"bge-m3, размерность 1024", "разбор v3"} {
		if !strings.Contains(got, want) {
			t.Errorf("в паспорте нет %q: %s", want, got)
		}
	}
	if !strings.Contains(evalPassport(kb.Stats{Analyzer: "v2", Stale: true}), "УСТАРЕЛ") {
		t.Error("устаревший разбор не назван")
	}
}
