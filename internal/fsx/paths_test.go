package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := ExpandHome("~"); got != home {
		t.Errorf("~ → %q", got)
	}
	if got := ExpandHome("~/x/y"); got != filepath.Join(home, "x/y") {
		t.Errorf("~/x/y → %q", got)
	}
	for _, p := range []string{"~user/x", "/abs", "rel/~", ""} {
		if got := ExpandHome(p); got != p {
			t.Errorf("%q должен остаться как есть, получено %q", p, got)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 Б", 512: "512 Б", 2048: "2 КБ", 1<<20 + 10: "1.0 МБ", 3 << 30: "3.0 ГБ"} {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}
