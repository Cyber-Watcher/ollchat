package graph

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// коллекция изображает каталог коллекции книг: графу нужен только он сам.
func collection(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "books")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Создать и открыть.
func TestCreateAndOpen(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if g.Meta().Version != FormatVersion {
		t.Errorf("версия формата = %d", g.Meta().Version)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	g2, err := Open(dir, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if g2.Meta().Collection != "books" {
		t.Errorf("имя коллекции = %q", g2.Meta().Collection)
	}
}

// Открыть без графа.
func TestOpenWithoutGraph(t *testing.T) {
	if _, err := Open(collection(t), 100, Rules{}); !errors.Is(err, ErrNoGraph) {
		t.Errorf("ошибка = %v, ожидалось ErrNoGraph", err)
	}
}

// Создать поверх существующего.
func TestCreateOverExisting(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	if _, err := Create(dir, "books", 10, Rules{}); err == nil {
		t.Error("готовый граф затёрт: это дни работы модели")
	}
}

// Уплотнение коллекции переписывает хранилище кусков, и сквозная нумерация
// меняется. Граф обязан отказаться открываться, а не отвечать ссылками
// на чужие страницы.
func TestRefusedAfterMerge(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	g.Close()

	_, err = Open(dir, 700, Rules{})
	if !errors.Is(err, ErrCompacted) {
		t.Fatalf("ошибка = %v, ожидалось ErrCompacted", err)
	}
	// Долитые книги — наоборот, норма: кусков стало больше.
	g2, err := Open(dir, 1500, Rules{})
	if err != nil {
		t.Fatalf("долитые книги приняты за уплотнение: %v", err)
	}
	g2.Close()
}

// Отказ на чужой версии формата.
func TestForeignFormatVersionRefused(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	g.Close()

	m, err := readMeta(filepath.Join(dir, DirName))
	if err != nil {
		t.Fatal(err)
	}
	m.Version = FormatVersion + 7
	if err := writeMeta(filepath.Join(dir, DirName), m); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 10, Rules{}); !errors.Is(err, ErrVersion) {
		t.Errorf("ошибка = %v, ожидалось ErrVersion", err)
	}
}

// Признак сборки.
func TestBuildMark(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if err := g.Lock(); err != nil {
		t.Fatal(err)
	}
	if !g.Locked() {
		t.Error("признак сборки не выставлен")
	}
	// Второй прогон в тот же граф писать не должен: журналы перемешаются.
	g2, err := Open(dir, 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if err := g2.Lock(); !errors.Is(err, ErrLocked) {
		t.Errorf("вторая сборка допущена: %v", err)
	}
	if err := g.Unlock(); err != nil {
		t.Fatal(err)
	}
	if g.Locked() {
		t.Error("признак сборки остался после снятия")
	}
}

// Сводка считает остаток.
func TestStatsCountsRemainder(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 268686, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	for ord := uint32(1); ord <= 5; ord++ {
		if err := g.Progress().Mark(ChunkKey{Doc: 1, Ord: ord}, MarkDone); err != nil {
			t.Fatal(err)
		}
	}
	st := g.Stats(268686)
	if st.Covered != 5 {
		t.Errorf("разобрано = %d, ожидалось 5", st.Covered)
	}
	if st.Pending != 268681 {
		t.Errorf("осталось = %d, ожидалось 268681", st.Pending)
	}
}

// Номер куска переживает упаковку.
func TestChunkKeySurvivesPacking(t *testing.T) {
	for _, k := range []ChunkKey{{0, 0}, {1, 37}, {4294967295, 4294967295}, {12, 0}} {
		if got := UnpackChunk(k.Pack()); got != k {
			t.Errorf("%v → %v", k, got)
		}
	}
	if got := (ChunkKey{Doc: 12, Ord: 37}).String(); got != "12#37" {
		t.Errorf("печать номера = %q", got)
	}
}

// CoversDoc отличает разобранную графом книгу от неразобранной.
//
// Нужно проверке коллекции: у книги, которую граф успел разобрать, в нём
// остаются понятия и связи после её удаления; у неразобранной следов нет,
// и чистить нечего.
func TestCoversDoc(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if g.CoversDoc(7) {
		t.Error("в пустом графе не может быть разобранных книг")
	}
	if err := g.Progress().Mark(ChunkKey{Doc: 7, Ord: 3}, MarkDone); err != nil {
		t.Fatal(err)
	}
	if !g.CoversDoc(7) {
		t.Error("книга с отметкой разбора должна считаться разобранной")
	}
	if g.CoversDoc(8) {
		t.Error("соседняя книга разобранной не становится")
	}

	// Пустой граф спрашивать безопасно: доктор зовёт это и там, где графа нет.
	var none *Graph
	if none.CoversDoc(7) {
		t.Error("у пустого графа ответ должен быть отрицательным")
	}
}
