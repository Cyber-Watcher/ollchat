package kb

import "testing"

// Каталог отбирается по границам каталога, а не подстрокой.
//
// Дефект 29.09.2026: `--graph-folder` брал подстроку и захватывал книги,
// в названии которых встречалось короткое имя каталога, из других
// каталогов — 190 книг вместо 183, остаток 12 227 вместо 6 476.
func TestInFolder(t *testing.T) {
	const other = "/lib/Code/Web/XY in the title.pdf"
	const xy = "/lib/XY/one.pdf"
	const nested = "/lib/Code/Lang and Tools/three.pdf"
	cases := []struct {
		path, folder string
		want         bool
	}{
		{xy, "XY", true},
		{xy, "/XY", true},
		{xy, "XY/", true},
		{xy, "/XY/", true},
		{other, "XY", false},
		{other, "/XY/", false},
		{other, "Web", true},
		{other, "Code/Web", true},
		{other, "Code", true},
		{nested, "Lang and Tools", true},
		{nested, "Code/Lang and Tools", true},
		{nested, "Lang", false},
		{"/lib/XY2/x.pdf", "XY", false},
		{xy, "", true},
		{xy, "/", true},
	}
	for _, c := range cases {
		if got := InFolder(c.path, c.folder); got != c.want {
			t.Errorf("InFolder(%q, %q) = %v, ожидалось %v", c.path, c.folder, got, c.want)
		}
	}
}

// MatchingDocs и обход кусков подчиняются тому же правилу.
func TestFolderFilterIsNotSubstring(t *testing.T) {
	c := &Collection{docs: []BookRec{
		{ID: 1, Kind: BookOK, Path: "/lib/XY/one.pdf"},
		{ID: 2, Kind: BookOK, Path: "/lib/Code/Web/XY in the title.pdf"},
		{ID: 3, Kind: BookOK, Path: "/lib/XY/two.pdf"},
	}, deleted: map[uint32]bool{}}
	got := c.MatchingDocs(ChunkFilter{Folder: "XY"})
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("по каталогу XY отобрано %v, ожидались книги 1 и 3", got)
	}
	if n := len(c.MatchingDocs(ChunkFilter{PathContains: "XY"})); n != 3 {
		t.Fatalf("подстрока «XY» должна брать все три книги, взяла %d", n)
	}
	ok := c.docFilter(ChunkFilter{Folder: "/XY/"})
	if !ok(1) || ok(2) || !ok(3) {
		t.Error("docFilter по каталогу разошёлся с MatchingDocs")
	}
}
