package textx

import "testing"

func TestShorten(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"короткая", 20, "короткая"},
		{"ровно восемь", 12, "ровно восемь"},
		{"длинная строка текста", 10, "длинная с…"},
		{"abc", 1, "…"},
		{"", 5, ""},
	} {
		if got := Shorten(tc.in, tc.n); got != tc.want {
			t.Errorf("Shorten(%q, %d) = %q, ожидалось %q", tc.in, tc.n, got, tc.want)
		}
	}
	if got := ShortenOneLine("ls  -la\n  /tmp", 20); got != "ls -la /tmp" {
		t.Errorf("ShortenOneLine: %q", got)
	}
}

func TestShortenMiddle(t *testing.T) {
	if got := ShortenMiddle("/home/user/projects/ollchat/internal/ui/model.go", 20); len([]rune(got)) != 20 || got[:5] != "/home" || got[len(got)-3:] != ".go" {
		t.Errorf("ShortenMiddle: %q", got)
	}
	if got := ShortenMiddle("короткий", 20); got != "короткий" {
		t.Errorf("ShortenMiddle короткой строки: %q", got)
	}
}
