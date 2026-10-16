package find

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTranslator — подставной перевод: возвращает заданное и считает вызовы.
type fakeTranslator struct {
	en    string
	err   error
	calls int
}

func (f *fakeTranslator) Translate(context.Context, string) (string, error) {
	f.calls++
	return f.en, f.err
}

// Перевод вопроса доходит до словесной ветки тем же вызовом, что в работе
// (find.Books), а сбой перевода не роняет поиск — он идёт без перевода
// и говорит об этом заметкой (этап 112, шаг 2, kb.translate).
func TestBooksTranslatesForWords(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir(t), "vectors.json"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("vectors.json ещё не записан: OLLCHAT_FIXTURE_RECORD=<адрес> go test ./internal/find/ -run FixtureRecord")
	}
	if err != nil {
		t.Fatal(err)
	}
	book := vectorBook{}
	if err := json.Unmarshal(raw, &book); err != nil {
		t.Fatal(err)
	}
	// Эмбеддер нужен только для сборки фикстуры; искать будем по словам.
	coll := buildFixture(t, &fakeEmbedder{book: book})
	ctx := context.Background()
	q := "ыыыщщщ ъъъжжж" // ни одного общего слова с фикстурой
	o := Opts{Mode: "search", TopK: 5}

	hits, _, err := Books(ctx, Deps{Coll: coll}, q, q, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("бессмысленный вопрос без перевода нашёл %d кусков", len(hits))
	}

	tr := &fakeTranslator{en: "BM25"}
	hits, _, err = Books(ctx, Deps{Coll: coll, Translator: tr}, q, q, o)
	if err != nil {
		t.Fatal(err)
	}
	if tr.calls != 1 {
		t.Errorf("перевод звали %d раз, ждали 1", tr.calls)
	}
	if len(hits) == 0 {
		t.Fatal("перевод «BM25» не дошёл до словесной ветки: ничего не найдено")
	}

	bad := &fakeTranslator{err: errors.New("модель не ответила")}
	hits, note, err := Books(ctx, Deps{Coll: coll, Translator: bad}, q, q, o)
	if err != nil {
		t.Fatalf("сбой перевода уронил поиск: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("без перевода ждали пустую выдачу, нашлось %d", len(hits))
	}
	if !strings.Contains(note, "перевод вопроса не получен") {
		t.Errorf("о сбое перевода не сказано: заметка %q", note)
	}
}
