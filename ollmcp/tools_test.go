package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Служба отдаёт только чтение: ни bash, ни записи файлов среди её инструментов
// быть не может, что бы ни было включено в настройках ollchat.
func TestServiceIsReadOnly(t *testing.T) {
	forbidden := map[string]bool{
		"bash": true, "write_file": true, "edit_file": true,
		"read_file": true, "list_dir": true, "grep": true, "view_image": true,
	}
	for _, name := range readOnlyTools {
		if forbidden[name] {
			t.Errorf("в списке службы инструмент, меняющий машину: %s", name)
		}
	}
}

// TestToolsListEqualsReadOnlyNames — поверхность службы РАВНА списку
// tools.ReadOnlyNames() плюс её собственный kb_status: ни больше, ни меньше.
// «Служба поднялась» и «служба отдаёт то, что должна» — разные утверждения;
// второе проверяется только перечислением (memory/my-mistakes.md, №12).
func TestToolsListEqualsReadOnlyNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[kb]\ndir = \"" + filepath.Join(dir, "kb") + "\"\n" +
		"[web]\nsearxng_url = \"http://searx.example\"\n" +
		"[[servers]]\nname = \"local\"\nurl = \"http://127.0.0.1:11434\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("конфиг: %v", err)
	}
	srv, _, err := build(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range srv.Tools() {
		got[tool.Name] = true
	}
	want := map[string]bool{"kb_status": true}
	for _, n := range tools.ReadOnlyNames() {
		want[n] = true
	}
	for n := range want {
		if !got[n] {
			t.Errorf("служба не отдаёт %s", n)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("служба отдаёт лишнее: %s", n)
		}
	}
}
