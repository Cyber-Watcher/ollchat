package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
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
// второе проверяется только перечислением.
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

// kb_status берёт граф из общего кеша службы и сверяет его актуальность.
//
// Без изменений на диске второй вызов отдаёт тот же экземпляр: до 11.09.2026
// kb_status открывал граф на каждый вызов, и на redos8dev это стоило 77 с —
// клиент MCP не дожидался. После записи в файлы графа (так пишет сборка) кеш
// обязан открыть граф заново, иначе kb_status врал бы числами вчерашнего графа.
// Без кеша (graph.cache = false) граф открывается на каждый вызов, как прежде.
func TestStatusGraphUsesCacheAndNoticesChanges(t *testing.T) {
	collDir := filepath.Join(t.TempDir(), "books")
	if err := os.MkdirAll(collDir, 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(collDir, "books", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	cache := graph.NewCache(0, graph.Rules{})
	defer cache.Close()
	get := func(c *graph.Cache) *graph.Graph {
		t.Helper()
		g, release, err := statusGraph(c, collDir, 100, graph.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		release()
		return g
	}

	g1 := get(cache)
	if g2 := get(cache); g2 != g1 {
		t.Error("файлы графа не менялись, а kb_status открыл граф заново — кеш не используется")
	}

	// Сдвинуть время правки одного файла графа — то, что видит кеш после дозаписи сборкой.
	graphDir := filepath.Join(collDir, graph.DirFor(""))
	ents, err := os.ReadDir(graphDir)
	if err != nil {
		t.Fatal(err)
	}
	touched := false
	later := time.Now().Add(time.Minute)
	for _, e := range ents {
		if !e.IsDir() {
			if err := os.Chtimes(filepath.Join(graphDir, e.Name()), later, later); err != nil {
				t.Fatal(err)
			}
			touched = true
			break
		}
	}
	if !touched {
		t.Fatalf("в каталоге графа %s нет файлов", graphDir)
	}
	if g3 := get(cache); g3 == g1 {
		t.Error("файлы графа изменились, а kb_status получил прежний граф из кеша")
	}

	if a, b := get(nil), get(nil); a == b {
		t.Error("без кеша kb_status обязан открывать граф на каждый вызов")
	}
}
