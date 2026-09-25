package kb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Каталоги, начинающиеся с точки, обход индексации пропускает — и это
// единственное, что защищает индекс от `.sync/Archive` Resilio (этап 105, М3).
//
// **Почему это отдельный тест.** В `.sync/Archive` лежат СТАРЫЕ копии файлов
// проекта: тот же `tools.go` недельной давности. Попади они в индекс, поиск
// возвращал бы позавчерашний код наравне с нынешним, и отличить их было бы
// нечем — ровно та беда, что со списком книг из журнала (my-mistakes #24).
// Правило живёт одной строкой в collect() и ломается незаметно; с 25.09.2026,
// когда в индекс пошёл код проекта, цена поломки выросла.
func TestCollectSkipsDotDirs(t *testing.T) {
	base, root := newBase(t)
	live := filepath.Join(root, "internal")
	archive := filepath.Join(root, ".sync", "Archive", "internal")
	for _, d := range []string{live, archive} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Файл достаточно длинный, чтобы дать хотя бы один кусок.
	body := "package tools\n\n" + strings.Repeat(
		"// Открытие коллекции: разбор реестра понятий и сверка словарей.\n", 40) +
		"func Open() error {\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(live, "tools.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Копия в архиве Resilio: по имени та же, по содержимому устаревшая.
	if err := os.WriteFile(filepath.Join(archive, "tools.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := base.Create("code", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{root}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, b := range c.LiveBooks() {
		paths = append(paths, b.Path)
	}
	if len(paths) != 1 {
		t.Fatalf("взято файлов %d, ожидался один (архив Resilio брать нельзя): %v", len(paths), paths)
	}
	if strings.Contains(paths[0], ".sync") {
		t.Errorf("в индекс попала копия из .sync: %s", paths[0])
	}
}
