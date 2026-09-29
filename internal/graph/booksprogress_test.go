package graph

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Таблица по книгам и остаток каталога считаются одним правилом (WillTake):
// сумма «осталось» по книгам равна --graph-pending, «разобрано» — любая
// отметка, неотмеченное оглавление не считается ни туда, ни сюда.
//
// Этап 113 (29.09.2026): три прибора называли по одному каталогу 12 227,
// 1 906 и 6 476 оставшихся кусков. Этот тест ловит расхождение раньше глаз.
func TestBooksProgressAgreesWithPending(t *testing.T) {
	g, _ := graph(t)
	src := &source{}
	add := func(doc uint32, path string, n int, toc bool) {
		for i := 0; i < n; i++ {
			src.chunks = append(src.chunks, kb.ChunkInfo{
				Index: len(src.chunks), Doc: doc, Ord: uint32(i + 1),
				Book: kb.BookRec{ID: doc, Path: path}, TOC: toc,
			})
		}
	}
	add(1, "/lib/XY/one.pdf", 5, false) // 2 с понятиями, пустой, пропущенный, нетронутый
	add(1, "/lib/XY/one.pdf", 0, false)
	src.chunks = append(src.chunks, kb.ChunkInfo{Index: len(src.chunks), Doc: 1, Ord: 6,
		Book: kb.BookRec{ID: 1, Path: "/lib/XY/one.pdf"}, TOC: true}) // оглавление без отметки
	add(2, "/lib/XY/two.pdf", 3, false) // нетронутая книга
	add(3, "/lib/Code/Web/XY in the title.pdf", 4, false)

	must(t, g.Progress().Mark(ChunkKey{Doc: 1, Ord: 1}, MarkDone))
	must(t, g.Progress().Mark(ChunkKey{Doc: 1, Ord: 2}, MarkDone))
	must(t, g.Progress().Mark(ChunkKey{Doc: 1, Ord: 3}, MarkEmpty))
	must(t, g.Progress().Mark(ChunkKey{Doc: 1, Ord: 4}, MarkSkipped))

	for _, folder := range []string{"XY", "/XY/", "XY/"} {
		filter := kb.ChunkFilter{Folder: folder}
		rows, err := BooksProgress(src, g, filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("каталог %q: книг %d, ожидалось 2 — подстрока «XY» захватила чужую книгу", folder, len(rows))
		}
		pending, err := PendingChunks(src, g, filter)
		if err != nil {
			t.Fatal(err)
		}
		var sum int
		for _, r := range rows {
			sum += r.Pending
		}
		if sum != pending {
			t.Errorf("каталог %q: сумма остатка по книгам %d, --graph-pending %d", folder, sum, pending)
		}
		// От большего остатка к меньшему: нетронутая книга первой.
		if rows[0].Doc != 2 || rows[0].Pending != 3 || rows[0].Total != 3 || rows[0].Marked() != 0 {
			t.Errorf("первая строка %+v, ожидалась нетронутая книга 2 с остатком 3", rows[0])
		}
		one := rows[1]
		if one.Doc != 1 || one.Total != 6 || one.Done != 2 || one.Empty != 1 || one.Skipped != 1 ||
			one.Service != 0 || one.Marked() != 4 || one.Pending != 1 {
			t.Errorf("книга 1: %+v — ожидалось 6 кусков, разобрано 4, осталось 1 (оглавление не в счёт)", one)
		}
	}
	// Без каталога — вся коллекция, чужая книга видна.
	all, err := BooksProgress(src, g, kb.ChunkFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("вся коллекция: книг %d, %v", len(all), err)
	}
}
