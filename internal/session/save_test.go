package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func savedConv(text string) *Conversation {
	c := New("")
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: text})
	return c
}

// Два сохранения в одну секунду — две сессии. Имя бралось из времени
// до секунды, и второе сохранение молча затирало первое.
func TestSaveSameSecondKeepsBoth(t *testing.T) {
	st := NewStore(t.TempDir())
	first, err := st.Save(savedConv("первый"), "local", "m")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.Save(savedConv("второй"), "local", "m")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("оба сохранения легли в %s", first)
	}
	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("сессий %d, ожидалось 2", len(list))
	}
	for _, rec := range list {
		got, err := st.Load(rec.ID)
		if err != nil || len(got.Messages) != 1 {
			t.Errorf("сессия %s не читается: %v", rec.ID, err)
		}
	}
}

// Каталог сессий закрыт от чужих, файлы — тоже; прежний каталог 0755
// закрывается при первом сохранении. Временных файлов не остаётся.
func TestSavePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	st := NewStore(dir)
	path, err := st.Save(savedConv("вопрос"), "local", "m")
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm()&0o077 != 0 {
		t.Errorf("каталог сессий %o, ожидалось без прав для чужих", info.Mode().Perm())
	}
	if info, _ := os.Stat(path); info.Mode().Perm()&0o077 != 0 {
		t.Errorf("файл сессии %o, ожидалось без прав для чужих", info.Mode().Perm())
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(savedConv("ещё"), "local", "m"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("прежний каталог 0755 не закрыт: %o", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			t.Errorf("лишний файл в каталоге сессий: %s", e.Name())
		}
	}
}
