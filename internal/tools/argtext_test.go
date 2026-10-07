package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Цель действия — путь, команда, адрес — принимается только строкой.
// Раньше любое значение приводилось к строке через %v: список ["a","b"]
// становился файлом «[a b]», объект — файлом «map[…]», и инструмент молча
// работал не с тем, о чём просили.
func TestTargetArgumentsMustBeStrings(t *testing.T) {
	r, root := newTestRegistry(t)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{NameReadFile, map[string]any{"path": []any{"a", "b"}}},
		{NameListDir, map[string]any{"path": map[string]any{"x": 1.0}}},
		{NameWriteFile, map[string]any{"path": []any{"a.txt"}, "content": "x"}},
		{NameEditFile, map[string]any{"path": 42.0, "old_string": "a", "new_string": "b"}},
		{NameGrep, map[string]any{"pattern": "x", "path": true}},
		{NameViewImage, map[string]any{"path": []any{"doc.pdf"}}},
		{NameScanRedact, map[string]any{"path": map[string]any{"file": "scan.pdf"}}},
		{NameBash, map[string]any{"command": []any{"ls", "-la"}}},
		{NameHTTPFetch, map[string]any{"url": map[string]any{"href": "https://example.org"}}},
	}
	for _, c := range cases {
		_, err := r.Plan(c.tool, c.args)
		if err == nil || !strings.Contains(err.Error(), "должен быть строкой") {
			t.Errorf("%s %v: ожидалась ошибка о типе, получено %v", c.tool, c.args, err)
		}
	}
	// Ничего не создано и не записано под именем вида «[a.txt]».
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("в корне появились файлы: %v", entries)
	}

	// Числа там, где ждут числа, по-прежнему принимаются.
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("строка\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Plan(NameReadFile, map[string]any{"path": "f.txt", "offset": 1.0, "limit": 10.0}); err != nil {
		t.Errorf("строковый путь с числовыми параметрами: %v", err)
	}
}
