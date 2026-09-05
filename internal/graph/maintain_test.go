package graph

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newGraphAt(t *testing.T) (string, *Graph) {
	t.Helper()
	dir := t.TempDir()
	g, err := Create(dir, "проба", 100, Rules{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return dir, g
}

// Проверка ловит главную беду: коллекцию уплотнили после сборки графа.
func TestCheckCatchesCompaction(t *testing.T) {
	_, g := newGraphAt(t)
	defer g.Close()

	if r := g.Check(100); !r.OK() {
		t.Fatalf("свежий граф не должен вызывать нареканий: %v", r.Problems)
	}
	r := g.Check(40) // кусков стало меньше — коллекцию уплотнили
	if r.OK() {
		t.Fatal("уменьшение числа кусков должно попадать в беды")
	}
	if !strings.Contains(strings.Join(r.Problems, " "), "уплотнили") {
		t.Errorf("беда названа непонятно: %v", r.Problems)
	}
}

// Удаление убирает граф и возвращает освобождённое место, а коллекцию
// не трогает.
func TestRemoveKeepsCollection(t *testing.T) {
	dir, g := newGraphAt(t)
	marker := filepath.Join(dir, "chunks.dat")
	if err := os.WriteFile(marker, []byte("данные коллекции"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.Close()

	size, err := Remove(dir, "")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if size <= 0 {
		t.Errorf("освобождённое место %d, ожидалось больше нуля", size)
	}
	if _, err := os.Stat(filepath.Join(dir, DirName)); !os.IsNotExist(err) {
		t.Error("каталог графа остался на месте")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("удаление графа задело коллекцию")
	}
	if _, err := Remove(dir, ""); err == nil {
		t.Error("повторное удаление должно сообщать, что графа нет")
	}
}

// Архив содержит коллекцию целиком, пути внутри относительные, признак
// идущей сборки не переносится.
func TestPackIsPortable(t *testing.T) {
	dir, g := newGraphAt(t)
	if err := os.WriteFile(filepath.Join(dir, "chunks.dat"), []byte("куски"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Lock(); err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	archive := filepath.Join(t.TempDir(), "coll.tar")
	res, err := Pack(dir, archive)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if res.Files == 0 {
		t.Fatal("архив пуст")
	}

	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	base := filepath.Base(dir)
	for _, n := range names {
		if strings.HasPrefix(n, "/") || strings.Contains(n, "..") {
			t.Errorf("в архиве непереносимый путь: %q", n)
		}
		if !strings.HasPrefix(n, base+"/") {
			t.Errorf("путь %q не внутри каталога коллекции", n)
		}
		if strings.HasSuffix(n, "/"+lockFile) {
			t.Error("признак идущей сборки не должен попадать в архив")
		}
	}
	if len(names) < 2 {
		t.Errorf("в архиве всего %d файлов: %v", len(names), names)
	}
}
