package stats

import "testing"

// Каталог книги в замерах — по тому же правилу, что у доктора графа
// (maint.TopFolder). Своя копия сравнивала корень подстрокой: корень
// /data/lib находил книги из /data/library, и они числились в каталоге
// «/rary», искажая доли «своих» и «чужих» подтверждений.
func TestFolderOfRespectsDirectoryBoundary(t *testing.T) {
	cases := []struct {
		path  string
		roots []string
		want  string
	}{
		{"/data/lib/AI/rag.pdf", []string{"/data/lib"}, "/AI"},
		{"/data/library/AI/rag.pdf", []string{"/data/lib"}, "(корень)"},
		{"/data/lib/book.pdf", []string{"/data/lib"}, "(корень)"},
		// Самый короткий корень: у коллекции lab её корень — раздел библиотеки.
		{"/data/lib/AI/Agents/rag.pdf", []string{"/data/lib", "/data/lib/AI"}, "/AI"},
	}
	for _, c := range cases {
		if got := folderOf(c.path, c.roots); got != c.want {
			t.Errorf("folderOf(%q, %q) = %q, ожидалось %q", c.path, c.roots, got, c.want)
		}
	}
}
