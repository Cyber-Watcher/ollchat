package kb

import (
	"context"
	"os"
	"testing"
)

// Слова для дописывания (этап 112, О8): редкое в коллекции слово первых
// кусков идёт раньше частого, слова вопроса, числа и слова, которых нет
// в индексе (df < 2), не берутся.
func TestPRFTermsPrefersRareWords(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "k1.pdf", "the kubelet watches pods on every system node one")
	makeBook(t, books, "k2.pdf", "a kubelet restarts failed pods when the system asks two")
	for i := 0; i < 4; i++ {
		letter := string(rune('a' + i))
		makeBook(t, books, "s"+letter+".pdf", "system design notes, chapter "+letter+".")
	}
	c, _ := base.Create("test", "")
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	texts := []string{
		"the kubelet watches pods 2026 system system zzzunique",
		"kubelet restarts pods system",
	}
	got := c.PRFTerms("how are pods restarted", texts, 2)
	if len(got) == 0 {
		t.Fatal("ни одного слова")
	}
	if got[0] != "kubelet" {
		t.Errorf("первым ждали редкое kubelet, получили %v", got)
	}
	for _, w := range got {
		switch w {
		case "pods":
			t.Errorf("слово вопроса дописано повторно: %v", got)
		case "2026":
			t.Errorf("число дописано как слово: %v", got)
		case "zzzunique":
			t.Errorf("слово, которого нет в индексе, дописано: %v", got)
		}
	}
	if len(got) > 2 {
		t.Errorf("просили не больше двух слов, получили %v", got)
	}
	if c.PRFTerms("pods", nil, 3) != nil || c.PRFTerms("pods", texts, 0) != nil {
		t.Error("без текстов или при n = 0 слов быть не должно")
	}

	// WordsExtra доходит до словесного отбора: вопрос без общих с книгами слов
	// сам по себе пуст, с дописанным словом находит обе книги о kubelet.
	opt := DefaultSearchOpts()
	none, err := c.SearchWith(context.Background(), "qqqmissing", opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("вопрос без общих слов нашёл %d кусков", len(none))
	}
	opt.WordsExtra = "kubelet"
	with, err := c.SearchWith(context.Background(), "qqqmissing", opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(with) != 2 {
		t.Errorf("с дописанным kubelet найдено %d кусков, ждали 2 (k1 и k2)", len(with))
	}
}
