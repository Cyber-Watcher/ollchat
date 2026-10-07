package tools

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Существующий файл не обнуляется на месте: прежнее содержимое остаётся
// целым, пока новое не записано полностью. Признак — открытый до записи
// дескриптор по-прежнему читает старый текст: os.WriteFile обнулила бы
// этот же файл, а атомарная замена кладёт рядом новый.
func TestWriteToolsReplaceAtomically(t *testing.T) {
	r, root := newTestRegistry(t)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{NameWriteFile, map[string]any{"path": "a.txt", "content": "новое\n"}},
		{NameEditFile, map[string]any{"path": "a.txt", "old_string": "старое", "new_string": "новое"}},
	}
	for _, c := range cases {
		path := filepath.Join(root, "a.txt")
		if err := os.WriteFile(path, []byte("старое\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		old, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		plan, err := r.Plan(c.tool, c.args)
		if err != nil {
			t.Fatalf("%s: план: %v", c.tool, err)
		}
		if _, err := plan.Run(context.Background()); err != nil {
			t.Fatalf("%s: запись: %v", c.tool, err)
		}

		before, _ := io.ReadAll(old)
		old.Close()
		if string(before) != "старое\n" {
			t.Errorf("%s: файл переписан на месте — прежний текст пропал бы при сбое: %q", c.tool, before)
		}
		after, _ := os.ReadFile(path)
		if string(after) != "новое\n" {
			t.Errorf("%s: новое содержимое: %q", c.tool, after)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s: права стали %v, были 0600", c.tool, info.Mode().Perm())
		}
		if matches, _ := filepath.Glob(filepath.Join(root, ".a.txt.*")); len(matches) != 0 {
			t.Errorf("%s: остались временные файлы: %v", c.tool, matches)
		}
	}
}

// Ссылка внутри песочницы пишется насквозь, как раньше: замена
// переименованием не должна подменить саму ссылку обычным файлом.
func TestWriteFileThroughSymlinkKeepsLink(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	sb, err := permissions.NewSandbox(root, false, true, 512)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry([]string{NameWriteFile}, Options{Sandbox: sb, BashTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("было\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("ссылки недоступны: %v", err)
	}

	plan, err := r.Plan(NameWriteFile, map[string]any{"path": "link.txt", "content": "стало\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("ссылка заменена обычным файлом: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "стало\n" {
		t.Fatalf("файл за ссылкой не изменён: %q", data)
	}
}

// Новый файл создаётся как раньше, вместе с недостающими каталогами.
func TestWriteFileCreatesNewFile(t *testing.T) {
	r, root := newTestRegistry(t)
	plan, err := r.Plan(NameWriteFile, map[string]any{"path": "dir/new.txt", "content": "текст\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "dir", "new.txt")); err != nil || string(data) != "текст\n" {
		t.Fatalf("новый файл: %q, %v", data, err)
	}
}
