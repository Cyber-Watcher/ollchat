package graph

import "testing"

// Отметки разбора по видам и по книгам, которых нет (этап 110, А0).
//
// Прежний `Counts` складывал в одно число «пропущено» три разные вещи:
// потерю (модель не дала разбираемого ответа), норму работы (служебный кусок,
// модели не показывали) и следы удалённых книг. 27.09.2026 на живом графе
// это давало 24 736, из которых 11 867 принадлежали книгам, которых
// в коллекции нет, — читающий видел вдвое большую беду, чем есть.

func TestMarkStatsSplitsKinds(t *testing.T) {
	dir := t.TempDir()
	p, err := openProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	mark := func(doc, ord uint32, m uint32) {
		if err := p.Mark(ChunkKey{Doc: doc, Ord: ord}, m); err != nil {
			t.Fatal(err)
		}
	}
	// книга 1 — живая: два разобранных, один пустой, один пропущен, один служебный
	mark(1, 1, MarkDone)
	mark(1, 2, MarkDone)
	mark(1, 3, MarkEmpty)
	mark(1, 4, MarkSkipped)
	mark(1, 5, MarkService)
	// книга 2 — живая: один разобранный
	mark(2, 1, MarkDone)
	// книги 7 и 9 — УДАЛЕНЫ: их отметки не должны попасть в счёт живых
	mark(7, 1, MarkSkipped)
	mark(7, 2, MarkSkipped)
	mark(9, 1, MarkDone)

	alive := func(doc uint32) bool { return doc == 1 || doc == 2 }
	st := p.Stats(alive)

	if st.Done != 3 {
		t.Errorf("разобранных %d, ожидалось 3 (книга 9 удалена и не в счёт)", st.Done)
	}
	if st.Empty != 1 {
		t.Errorf("пустых %d, ожидалась 1", st.Empty)
	}
	if st.Skipped != 1 {
		t.Errorf("не разобрала модель %d, ожидался 1 (два из книги 7 — удалённой)", st.Skipped)
	}
	if st.Service != 1 {
		t.Errorf("служебных %d, ожидался 1", st.Service)
	}
	if st.Gone != 3 {
		t.Errorf("отметок удалённых книг %d, ожидалось 3", st.Gone)
	}
	if st.GoneBooks != 2 {
		t.Errorf("удалённых книг %d, ожидалось 2", st.GoneBooks)
	}

	// Главное: «пропущено» больше не смешивает потерю со служебным.
	if st.Skipped == st.Skipped+st.Service {
		t.Fatal("виды отметок слиты в одно число — ради этого и заводился Stats")
	}
	// И прежний Counts по-прежнему работает, но складывает всё (это и было бедой).
	done, empty, skipped := p.Counts()
	if done != 4 || empty != 1 || skipped != 4 {
		t.Errorf("Counts дал (%d,%d,%d), ожидалось (4,1,4): он считает ВСЕ книги и слитно",
			done, empty, skipped)
	}
}

// Без списка живых книг Stats считает все книги живыми — чтобы вызывающий,
// которому отсев не нужен, не получал пустых чисел.
func TestMarkStatsWithoutAliveCountsAll(t *testing.T) {
	dir := t.TempDir()
	p, err := openProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Mark(ChunkKey{Doc: 1, Ord: 1}, MarkDone)
	p.Mark(ChunkKey{Doc: 77, Ord: 1}, MarkSkipped)

	st := p.Stats(nil)
	if st.Done != 1 || st.Skipped != 1 {
		t.Errorf("получено done=%d skipped=%d, ожидалось 1 и 1", st.Done, st.Skipped)
	}
	if st.Gone != 0 || st.GoneBooks != 0 {
		t.Errorf("без alive мёртвых быть не может, а вышло %d в %d книгах", st.Gone, st.GoneBooks)
	}
}
