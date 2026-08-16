package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chunksOf собирает куски с заданными текстами.
func chunksOf(texts ...string) []Chunk {
	out := make([]Chunk, len(texts))
	for i, t := range texts {
		out[i] = Chunk{Text: t, UnitFrom: i + 1, UnitTo: i + 1}
	}
	return out
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := CreateWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 200) // больше одного блока
	for i := range texts {
		texts[i] = fmt.Sprintf("кусок номер %d с текстом про каналы и горутины", i)
	}
	if err := w.Append(1, chunksOf(texts...)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Count() != len(texts) {
		t.Fatalf("кусков %d, ожидалось %d", s.Count(), len(texts))
	}
	for i, want := range texts {
		got, err := s.Text(i)
		if err != nil {
			t.Fatalf("кусок %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("кусок %d: %q вместо %q", i, got, want)
		}
	}
	// Страницы и длина в термах должны сохраниться: без них не сослаться
	// на источник и не посчитать ранжирование.
	rec := s.Rec(5)
	if rec.UnitFrom != 6 || rec.Doc != 1 || rec.Ord != 5 || rec.Tokens == 0 {
		t.Fatalf("указатель куска неверен: %+v", rec)
	}
}

// TestStoreCompresses — тексты должны храниться сжатыми, иначе база знаний
// по библиотеке займёт больше самой библиотеки.
func TestStoreCompresses(t *testing.T) {
	dir := t.TempDir()
	w, _ := CreateWriter(dir)
	var raw int
	for i := 0; i < 300; i++ {
		text := fmt.Sprintf("Обычный текст книги про программирование, абзац %d. %s", i,
			strings.Repeat("Слова повторяются, как это и бывает в книгах. ", 6))
		raw += len(text)
		w.Append(1, chunksOf(text))
	}
	w.Commit()
	w.Close()

	st, err := os.Stat(filepath.Join(dir, "chunks.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size()*2 > int64(raw) {
		t.Fatalf("сжатие не работает: %d байт на диске против %d исходных", st.Size(), raw)
	}
}

// TestStoreAppendAcrossSessions — доливка книг не должна переписывать хранилище.
func TestStoreAppendAcrossSessions(t *testing.T) {
	dir := t.TempDir()

	w, _ := CreateWriter(dir)
	w.Append(1, chunksOf("первая книга, кусок один", "первая книга, кусок два"))
	w.Commit()
	w.Close()

	w2, err := CreateWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Count() != 2 {
		t.Fatalf("после повторного открытия кусков %d, ожидалось 2", w2.Count())
	}
	w2.Append(2, chunksOf("вторая книга, кусок один"))
	w2.Commit()
	w2.Close()

	s, _ := OpenStore(dir)
	defer s.Close()
	if s.Count() != 3 {
		t.Fatalf("кусков %d, ожидалось 3", s.Count())
	}
	if text, _ := s.Text(0); !strings.Contains(text, "первая книга") {
		t.Fatal("старые куски испортились при доливке")
	}
	if text, _ := s.Text(2); !strings.Contains(text, "вторая книга") {
		t.Fatal("новый кусок не дописался")
	}
}

// TestStoreRollbackDropsOneBook закрепляет главное свойство журнала: прерывание
// стоит ровно одной книги, а всё записанное до неё остаётся целым.
func TestStoreRollbackDropsOneBook(t *testing.T) {
	dir := t.TempDir()
	w, _ := CreateWriter(dir)
	w.Append(1, chunksOf("книга один, кусок один", "книга один, кусок два"))
	state, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}

	// Пишем вторую книгу и «прерываемся» на середине.
	w.Append(2, chunksOf("книга два, начало", "книга два, продолжение"))
	if err := w.Rollback(state); err != nil {
		t.Fatal(err)
	}
	if w.Count() != 2 {
		t.Fatalf("после отката кусков %d, ожидалось 2", w.Count())
	}
	// И продолжаем работу — хранилище обязано остаться исправным.
	w.Append(3, chunksOf("книга три, кусок один"))
	w.Commit()
	w.Close()

	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("хранилище испорчено откатом: %v", err)
	}
	defer s.Close()
	if s.Count() != 3 {
		t.Fatalf("кусков %d, ожидалось 3", s.Count())
	}
	for i := 0; i < s.Count(); i++ {
		text, err := s.Text(i)
		if err != nil {
			t.Fatalf("кусок %d нечитаем: %v", i, err)
		}
		if strings.Contains(text, "книга два") {
			t.Fatalf("откат не выбросил прерванную книгу: %q", text)
		}
	}
}

// TestStoreTextsReadsBlocksOnce — групповое чтение не должно разжимать один
// блок по разу на каждый кусок.
func TestStoreTextsReadsBlocksOnce(t *testing.T) {
	dir := t.TempDir()
	w, _ := CreateWriter(dir)
	texts := make([]string, 150)
	for i := range texts {
		texts[i] = fmt.Sprintf("текст куска %d", i)
	}
	w.Append(1, chunksOf(texts...))
	w.Commit()
	w.Close()

	s, _ := OpenStore(dir)
	defer s.Close()
	ids := []int{140, 5, 70, 6, 141}
	got, err := s.Texts(ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if got[id] != texts[id] {
			t.Fatalf("кусок %d: %q вместо %q", id, got[id], texts[id])
		}
	}
}

func TestStoreRejectsBadIndex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "chunks.idx"), []byte("не кратно тридцати двум"), 0o644)
	os.WriteFile(filepath.Join(dir, "chunks.dat"), []byte("x"), 0o644)
	if _, err := OpenStore(dir); err == nil {
		t.Fatal("испорченный указатель принят как исправный")
	}
}
