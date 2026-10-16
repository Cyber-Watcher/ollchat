package mixer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Ссылки идут в том же порядке, что выдержки в тексте для модели: замер
// (этап 112, О7) читает по ним, каким по счёту модель увидела нужный кусок.
func TestRefsFollowMixOrder(t *testing.T) {
	book := map[string]string{}
	for _, h := range threeHits() {
		book[h.ID] = h.Book
	}
	res := Build("где ключевоеслово", Deps{Coll: &fakeColl{hits: threeHits()}, BooksOn: true}, Settings{TopK: 3})
	if res.Chunks == 0 {
		t.Fatal("подготовка: подмес пуст")
	}
	if len(res.Refs) != res.Chunks {
		t.Fatalf("ссылок %d при %d выдержках: %v", len(res.Refs), res.Chunks, res.Refs)
	}
	for i, ref := range res.Refs {
		name, ok := book[ref]
		if !ok {
			t.Fatalf("ссылка %q не из выдачи поиска", ref)
		}
		if head := fmt.Sprintf("[%d] %s", i+1, name); !strings.Contains(res.Text, head) {
			t.Errorf("ссылка %d (%s) не совпадает с выдержкой %q в тексте:\n%s", i+1, ref, head, res.Text)
		}
	}
}

// У закреплённого куска ссылка — сам эталон: прибор сверяет её с chunk_id.
func TestRefsForPin(t *testing.T) {
	ch := longChunk()
	coll := &fakeColl{byID: map[string]kb.Result{ch.ID: ch}}
	res := Build("где ключевоеслово", Deps{Coll: coll}, Settings{Pin: []string{ch.ID}})
	if len(res.Refs) != 1 || res.Refs[0] != ch.ID {
		t.Fatalf("ссылки %v, ждали [%s]", res.Refs, ch.ID)
	}
}
