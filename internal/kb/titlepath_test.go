package kb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Путь считается от общего каталога корней и только для файла внутри корня.
func TestRelToRoots(t *testing.T) {
	p := func(s string) string { return filepath.FromSlash(s) }
	roots := []string{p("/proj/docs"), p("/proj/internal"), p("/proj/cmd")}
	cases := []struct {
		name  string
		path  string
		roots []string
		want  string
	}{
		{"файл в первом корне", "/proj/docs/plan/a.md", roots, "docs/plan/a.md"},
		{"файл в другом корне", "/proj/internal/kb/kb.go", roots, "internal/kb/kb.go"},
		{"вне корней, хоть и рядом", "/proj/other/c.md", roots, ""},
		{"сосед с общим началом имени", "/proj/docs-old/d.md", roots, ""},
		{"имя на две точки внутри корня", "/proj/docs/..notes/e.md", roots, "docs/..notes/e.md"},
		{"один корень — путь от него самого", "/data/one/docs/a.md", []string{p("/data/one")}, "docs/a.md"},
		{"корни в разных ветках", "/b/y/f.md", []string{p("/a/x"), p("/b/y")}, "b/y/f.md"},
		{"корень вложен в корень", "/proj/docs/sub/g.md", []string{p("/proj/docs"), p("/proj/docs/sub")}, "sub/g.md"},
		{"корней нет", "/data/one/a.md", nil, ""},
	}
	for _, c := range cases {
		if got := relToRoots(p(c.path), c.roots); got != c.want {
			t.Errorf("%s: relToRoots(%q) = %q, ждали %q", c.name, c.path, got, c.want)
		}
	}
}

// Название с путём: путь дописывается, пока он что-то добавляет к названию.
func TestTitleWithPath(t *testing.T) {
	cases := []struct {
		name, title, rel, want string
	}{
		{"заголовок markdown", "План работ", "docs/plan/stage1.md", "План работ (docs/plan/stage1.md)"},
		{"книга: пути нет", "Язык Go", "", "Язык Go"},
		{"название равно пути", "docs/a.md", "docs/a.md", "docs/a.md"},
		{"название равно имени файла", "run.sh", "bin/run.sh", "run.sh"},
		{"название — хвост пути у кода", "bin/run.sh", "tools/bin/run.sh", "bin/run.sh (tools/bin/run.sh)"},
	}
	for _, c := range cases {
		if got := TitleWithPath(c.title, c.rel); got != c.want {
			t.Errorf("%s: TitleWithPath(%q, %q) = %q, ждали %q", c.name, c.title, c.rel, got, c.want)
		}
	}
}

// Выдача поиска и окружение куска заполняют Rel у текстовых файлов — по корню,
// из которого файл попал в коллекцию.
func TestSearchAndAroundFillRelForTextFiles(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	docs, scripts := filepath.Join(proj, "docs"), filepath.Join(proj, "scripts")
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(proj, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Заполнитель нужен затем, что совсем короткий файл индекс отвергает как тощий.
	filler := strings.Repeat("Вводные слова без отношения к делу.\n", 8)
	write("docs/plan/sub/stage.md", "# План этапа\n\n"+filler+"Здесь упомянут zebracorn один раз.\n")
	write("scripts/tools/bin/run.sh", "#!/bin/sh\n"+filler+"# запуск: wolpertinger здесь\necho ok\n")

	base, err := OpenBase(filepath.Join(root, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("t", "")
	if err != nil {
		t.Fatal(err)
	}
	// Два корня, как у projectdocs: путь обязан начинаться с имени корня.
	if _, err := coll.Add(context.Background(), []string{docs, scripts}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	// Корни записывает вызывающий (--kb-add, /kb add), а не Add: так и в работе.
	if err := coll.AddRoots([]string{docs, scripts}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		query, wantRel, wantHeader string
	}{
		{"zebracorn", "docs/plan/sub/stage.md", "План этапа (docs/plan/sub/stage.md)"},
		{"wolpertinger", "scripts/tools/bin/run.sh", "bin/run.sh (scripts/tools/bin/run.sh)"},
	} {
		hits, err := coll.Search(c.query, DefaultSearchOpts())
		if err != nil || len(hits) == 0 {
			t.Fatalf("поиск %q: %d находок, ошибка %v", c.query, len(hits), err)
		}
		h := hits[0]
		if h.Unit != "строки" {
			t.Fatalf("%q: единица %q, ждали «строки»", c.query, h.Unit)
		}
		if h.Rel != c.wantRel {
			t.Errorf("%q: Rel = %q, ждали %q", c.query, h.Rel, c.wantRel)
		}
		if got := TitleWithPath(h.Book, h.Rel); got != c.wantHeader {
			t.Errorf("%q: заголовок %q, ждали %q", c.query, got, c.wantHeader)
		}
		around, err := coll.Around(h.ID, 0)
		if err != nil || len(around) == 0 {
			t.Fatalf("окружение %s: %d кусков, ошибка %v", h.ID, len(around), err)
		}
		if around[0].Rel != c.wantRel {
			t.Errorf("Around %s: Rel = %q, ждали %q", h.ID, around[0].Rel, c.wantRel)
		}
	}
}
