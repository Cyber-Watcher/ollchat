package kb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSenseRR — реранкер-заглушка: предложению со словом want ставит высшую оценку.
type fakeSenseRR struct {
	want string
	fail bool
}

func (f fakeSenseRR) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	if f.fail {
		return nil, errors.New("реранкер недоступен")
	}
	out := make([]float64, len(docs))
	for i, d := range docs {
		if strings.Contains(d, f.want) {
			out[i] = 10
		}
	}
	return out, nil
}

func (fakeSenseRR) Model() string { return "заглушка" }

// TestSnippetBySense — выдержка по смыслу берёт предложение с ответом, даже если
// слова вопроса стоят в другом месте куска (этап 112, Ч21).
func TestSnippetBySense(t *testing.T) {
	text := "Порт репликации упомянут в начале главы. " + filler(10) +
		" Узлы обмениваются журналом через порт 9443 по умолчанию. " + filler(10)
	got := SnippetBySense(context.Background(), text, "какой порт репликации", 320,
		fakeSenseRR{want: "9443"}, "по словам")
	if !strings.Contains(got, "9443") {
		t.Fatalf("предложение с ответом не взято: %q", got)
	}
	if n := len([]rune(got)); n > 400 {
		t.Fatalf("выдержка %d знаков при пределе 320", n)
	}
	if got := SnippetBySense(context.Background(), text, "порт", 320, fakeSenseRR{fail: true}, "по словам"); got != "по словам" {
		t.Fatalf("при сбое реранкера ждали выдержку по словам, получили %q", got)
	}
	short := "Короткий кусок. Целиком."
	if got := SnippetBySense(context.Background(), short, "кусок", 320, fakeSenseRR{}, "по словам"); got != short {
		t.Fatalf("короткий кусок ждали целиком, получили %q", got)
	}
}

// TestSplitSentences — предложения по точке с пробелом и по строкам.
func TestSplitSentences(t *testing.T) {
	got := splitSentences("Первое. Второе! Версия 1.21 вышла.\nfunc main() {\n}")
	want := []string{"Первое.", "Второе!", "Версия 1.21 вышла.", "func main() {", "}"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("предложения %q, ждали %q", got, want)
	}
}
