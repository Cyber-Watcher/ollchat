package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Этап 112, Н5: перед правкой — копия в .ollchat/backups/<дата>/<путь>.<ЧЧММСС>,
// строка в журнале дня; новый файл копии не получает, записан как созданный;
// рядом заводится .gitignore со строкой backups/.
func TestSaveCopiesAndLogs(t *testing.T) {
	root := t.TempDir()
	st := New(root, 14)
	st.now = func() time.Time { return time.Date(2026, 10, 16, 14, 30, 5, 0, time.Local) }
	f := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("было"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp, err := st.Save(f)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".ollchat", "backups", "2026-10-16", "src", "main.go.143005")
	if cp != want {
		t.Errorf("копия %s, ждали %s", cp, want)
	}
	if b, _ := os.ReadFile(cp); string(b) != "было" {
		t.Errorf("в копии %q", b)
	}
	cp2, _ := st.Save(f) // вторая правка в ту же секунду — своя копия
	if cp2 == cp || !strings.HasSuffix(cp2, ".143005.2") {
		t.Errorf("вторая копия той же секунды %s", cp2)
	}
	if cp3, err := st.Save(filepath.Join(root, "новый.txt")); err != nil || cp3 != "" {
		t.Errorf("новый файл: копия %q, ошибка %v", cp3, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".ollchat", ".gitignore")); !strings.Contains(string(b), "backups/") {
		t.Errorf(".gitignore: %q", b)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".ollchat", "backups", "2026-10-16", "index.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("строк журнала %d, ждали 3", len(lines))
	}
	var last Entry
	_ = json.Unmarshal([]byte(lines[2]), &last)
	if !last.Created || last.Backup != "" {
		t.Errorf("новый файл в журнале: %+v", last)
	}
	var first Entry
	_ = json.Unmarshal([]byte(lines[0]), &first)
	if first.SHA256 == "" || first.Bytes != int64(len("было")) || first.Backup != filepath.Join("2026-10-16", "src", "main.go.143005") {
		t.Errorf("первая строка журнала: %+v", first)
	}
}

// Дни старше keep_days удаляются; отрицательное — не удалять никогда.
func TestPrune(t *testing.T) {
	root := t.TempDir()
	st := New(root, 14)
	st.now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local) }
	for _, d := range []string{"2026-09-30", "2026-10-01", "2026-10-02", "2026-10-16", "не-дата"} {
		if err := os.MkdirAll(filepath.Join(st.Dir(), d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.Prune()
	if err != nil || n != 2 {
		t.Fatalf("удалено дней %d (%v), ждали 2 (30.09 и 01.10)", n, err)
	}
	for _, d := range []string{"2026-10-02", "2026-10-16", "не-дата"} {
		if !exists(filepath.Join(st.Dir(), d)) {
			t.Errorf("%s удалён зря", d)
		}
	}
	keep := New(root, -1)
	keep.now = st.now
	if n, _ := keep.Prune(); n != 0 {
		t.Errorf("keep_days -1, а удалено %d", n)
	}
}

// Правка файла, копию которого не сделать, — необратима.
func TestIrreversible(t *testing.T) {
	root := t.TempDir()
	st := New(root, 14)
	if why := st.Irreversible(filepath.Join(root, "нет.txt")); why != "" {
		t.Errorf("новый файл: %q", why)
	}
	f := filepath.Join(root, "f.txt")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if why := st.Irreversible(f); why != "" {
		t.Errorf("обычный файл: %q", why)
	}
	if why := st.Irreversible(root); why == "" {
		t.Error("каталог — не обычный файл, ждали «нельзя»")
	}
	// Каталог копий занят файлом — завести его нельзя.
	blocked := t.TempDir()
	_ = os.WriteFile(filepath.Join(blocked, DirName), []byte("x"), 0o644)
	if why := New(blocked, 14).Irreversible(f); why == "" {
		t.Error("каталог копий не заводится, а правка названа обратимой")
	}
}
