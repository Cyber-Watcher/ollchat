package kb

import (
	"os"
	"path/filepath"
	"testing"
)

// Граница каталога, а не подстрока.
//
// Корень `/books` не должен подменять начало у `/booksold/…` — иначе перенос
// молча уводил бы часть библиотеки в чужой каталог.
func TestSwapPrefixRespectsDirBoundary(t *testing.T) {
	cases := []struct {
		path, from, to string
		want           string
		ok             bool
	}{
		{"/books/go/kniga.pdf", "/books", "/new", "/new/go/kniga.pdf", true},
		{"/books", "/books", "/new", "/new", true},
		{"/booksold/kniga.pdf", "/books", "/new", "", false},
		{"/other/kniga.pdf", "/books", "/new", "", false},
		// Хвостовая черта у корня ничего не меняет: она снимается заранее.
		{"/books/go/kniga.pdf", "/books/", "/new", "/new/go/kniga.pdf", true},
	}
	for _, c := range cases {
		from, _ := cleanRoot(c.from)
		got, ok := swapPrefix(c.path, from, c.to)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("swapPrefix(%q, %q) = %q,%v; ожидалось %q,%v",
				c.path, c.from, got, ok, c.want, c.ok)
		}
	}
}

// rebaseFixture: коллекция из двух книг под одним корнем.
func rebaseFixture(t *testing.T) (*Collection, string, string) {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(filepath.Join(books, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"go/first.pdf", "second.pdf"} {
		if err := os.WriteFile(filepath.Join(books, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base, err := OpenBase(filepath.Join(root, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	// Записи заводятся напрямую: перенос правит реестр, а не индексирует,
	// и настоящий разбор PDF здесь ничего не проверяет.
	for i, n := range []string{"go/first.pdf", "second.pdf"} {
		if err := c.appendDoc(BookRec{ID: uint32(i + 1), Path: filepath.Join(books, n), Kind: BookOK}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	return c, books, filepath.Join(root, "moved")
}

// Перенос правит пути книг и корни коллекции, ничего не переиндексируя.
func TestRebaseRewritesPathsAndRoots(t *testing.T) {
	c, oldRoot, newRoot := rebaseFixture(t)

	res, err := c.Rebase(oldRoot, newRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Books != 2 || res.Roots != 1 {
		t.Fatalf("переписано книг %d, корней %d; ожидалось 2 и 1", res.Books, res.Roots)
	}
	for _, d := range c.Books() {
		if got := d.Path; len(got) < len(newRoot) || got[:len(newRoot)] != newRoot {
			t.Errorf("путь книги не переписан: %q", got)
		}
	}
	if r := c.Roots(); len(r) != 1 || r[0] != newRoot {
		t.Errorf("корень коллекции не переписан: %v", r)
	}
}

// Переживает перечитывание с диска: правка попала в файлы, а не только в память.
func TestRebaseSurvivesReopen(t *testing.T) {
	c, oldRoot, newRoot := rebaseFixture(t)
	dir := c.Dir()
	if _, err := c.Rebase(oldRoot, newRoot, false); err != nil {
		t.Fatal(err)
	}

	// Dir() отдаёт …/collections/<имя>, а базе нужен корень над collections.
	base, err := OpenBase(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	again, err := base.Open("proba")
	if err != nil {
		t.Fatal(err)
	}
	if r := again.Roots(); len(r) != 1 || r[0] != newRoot {
		t.Errorf("после перечитывания корень %v, ожидался %q", r, newRoot)
	}
	for _, d := range again.Books() {
		if d.Path[:len(newRoot)] != newRoot {
			t.Errorf("после перечитывания путь %q", d.Path)
		}
	}
}

// Сухой прогон считает, но ничего не пишет.
func TestRebaseDryRunChangesNothing(t *testing.T) {
	c, oldRoot, newRoot := rebaseFixture(t)
	before := c.Books()[0].Path

	res, err := c.Rebase(oldRoot, newRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Books != 2 {
		t.Errorf("сухой прогон насчитал %d книг, ожидалось 2", res.Books)
	}
	if got := c.Books()[0].Path; got != before {
		t.Errorf("сухой прогон изменил путь: %q → %q", before, got)
	}
}

// Книги, которые не под старым корнем, не трогаются вовсе.
func TestRebaseSkipsForeignPaths(t *testing.T) {
	c, oldRoot, newRoot := rebaseFixture(t)
	foreign := "/совсем/другой/каталог/третья.pdf"
	if err := c.appendDoc(BookRec{ID: 3, Path: foreign, Kind: BookOK}); err != nil {
		t.Fatal(err)
	}

	res, err := c.Rebase(oldRoot, newRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 {
		t.Errorf("не под корнем осталось %d, ожидалась 1", res.Skipped)
	}
	var found bool
	for _, d := range c.Books() {
		if d.Path == foreign {
			found = true
		}
	}
	if !found {
		t.Error("чужой путь не должен был измениться")
	}
}

// Ошибки, на которых легко потерять коллекцию.
func TestRebaseRefusesBadInput(t *testing.T) {
	c, oldRoot, _ := rebaseFixture(t)

	if _, err := c.Rebase(oldRoot, oldRoot, false); err == nil {
		t.Error("совпадающие корни должны отклоняться")
	}
	if _, err := c.Rebase("/такого/корня/нет", "/куда/то", false); err == nil {
		t.Error("корень, под которым ничего нет, должен отклоняться — иначе перенос молчит и не делает ничего")
	}
	if _, err := c.Rebase("", "/куда/то", false); err == nil {
		t.Error("пустой старый корень должен отклоняться")
	}
}

// Отсутствие файлов по новому пути — предупреждение, а не отказ.
//
// Поиск, смыслы и граф файлов не открывают: коллекция остаётся рабочей, даже
// когда библиотеки на машине нет вовсе.
func TestRebaseReportsMissingButSucceeds(t *testing.T) {
	c, oldRoot, _ := rebaseFixture(t)
	nowhere := filepath.Join(t.TempDir(), "нет-такого")

	res, err := c.Rebase(oldRoot, nowhere, false)
	if err != nil {
		t.Fatalf("перенос обязан пройти даже без файлов на месте: %v", err)
	}
	if len(res.Missing) == 0 {
		t.Error("о пропавших файлах надо предупредить")
	}
	if res.Books != 2 {
		t.Errorf("переписано %d книг, ожидалось 2", res.Books)
	}
}
