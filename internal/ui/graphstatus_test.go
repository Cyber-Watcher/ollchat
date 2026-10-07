package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// /graph status считает разбор тем же правилом, что доктор графа
// и --graph-status: по кускам живых книг, виды отметок порознь.
//
// Прежде он брал все отметки журнала вместе со следами удалённых книг и делил
// их на все куски хранилища — на этой коллекции выходило «разобрано кусков 44
// из 5 (осталось 0)», — а «пропущено» складывало «не разобрала модель»
// со «служебными» (аудит 07.10.2026).
func TestGraphStatusCountsLiveBooks(t *testing.T) {
	m, books := kbTestModel(t)
	names := []string{"a-go.pdf", "b-k8s.pdf", "c-rust.pdf", "d-docker.pdf", "e-gone.pdf"}
	for _, n := range names {
		writeTestBook(t, books, n, "book "+n+" about the subject")
	}
	drainJob(t, m, m.runCommand("/kb add lib "+books))
	coll, err := m.kbCollection("lib")
	if err != nil {
		t.Fatal(err)
	}

	// Кусок каждой книги — по имени файла: так отметки ложатся предсказуемо.
	byName := map[string]graph.ChunkKey{}
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(r kb.ChunkRef) error {
		byName[filepath.Base(r.Book.Path)] = graph.ChunkKey{Doc: r.Doc, Ord: r.Ord}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(byName) != len(names) || coll.ChunkCount() != len(names) {
		t.Fatalf("подготовка: ждали по куску на книгу, кусков %d, книг с кусками %d",
			coll.ChunkCount(), len(byName))
	}

	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	mark := func(k graph.ChunkKey, v uint32) {
		if err := g.Progress().Mark(k, v); err != nil {
			t.Fatal(err)
		}
	}
	mark(byName["a-go.pdf"], graph.MarkDone)
	mark(byName["b-k8s.pdf"], graph.MarkSkipped)
	// «Служебный» без признака оглавления: сборка возьмёт его снова, поэтому
	// он и в «разобрано», и в «осталось» — как в --graph-status.
	mark(byName["c-rust.pdf"], graph.MarkService)
	mark(byName["e-gone.pdf"], graph.MarkDone)
	// Следы книги, которой нет и в хранилище кусков.
	for ord := uint32(0); ord < 40; ord++ {
		mark(graph.ChunkKey{Doc: 999, Ord: ord}, graph.MarkDone)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	// Книгу убрали из выдачи: её отметка — тоже след удалённой книги.
	if err := coll.Forget(filepath.Join(books, "e-gone.pdf")); err != nil {
		t.Fatal(err)
	}

	cmd := m.runCommand("/graph status lib")
	if cmd == nil {
		t.Fatalf("статус не ушёл в фон: %q", lastBlock(m).text)
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("из фона пришло не сообщение со статусом")
	}
	for _, want := range []string{
		"разобрано кусков 3 из 4 (осталось 2)",
		"с понятиями 1, пустых 0, не разобрала модель 1, служебных 1",
		"отметок книг, которых в коллекции НЕТ: 41 (в 2 книгах)",
	} {
		if !strings.Contains(msg.text, want) {
			t.Errorf("в статусе нет %q:\n%s", want, msg.text)
		}
	}
}
