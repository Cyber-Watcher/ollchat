package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

func TestMarkUntrustedPrefix(t *testing.T) {
	if MarkUntrusted("") != "" {
		t.Fatal("пустой текст помечать нечем")
	}
	got := MarkUntrusted("тело")
	if !strings.HasPrefix(got, "⚠") || !strings.HasSuffix(got, "тело") {
		t.Fatalf("пометка не приписана: %q", got)
	}
}

// Сетевые инструменты объявляют вывод чужим уже в плане; обычный файл
// пользователя — нет.
func TestForeignFlagByTool(t *testing.T) {
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Sandbox: sb, MaxOutputKB: 64, SearxURL: "http://searx.example", ConfluenceURL: "http://wiki.example"}

	fetch := &httpFetchTool{opts: opts}
	p, err := fetch.Plan(map[string]any{"url": "http://example.invalid/page"})
	if err != nil {
		t.Fatalf("http_fetch: %v", err)
	}
	if !p.Foreign {
		t.Error("http_fetch должен помечать вывод как чужой")
	}

	ws := &webSearchTool{opts: opts}
	p, err = ws.Plan(map[string]any{"query": "go"})
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !p.Foreign {
		t.Error("web_search должен помечать вывод как чужой")
	}

	cf := &confluenceTool{opts: opts}
	p, err = cf.Plan(map[string]any{"page": "123"})
	if err != nil {
		t.Fatalf("confluence: %v", err)
	}
	if !p.Foreign {
		t.Error("confluence_connector должен помечать вывод как чужой")
	}

	// Собственный файл пользователя: признак ставится после чтения и остаётся ложным.
	if err := os.WriteFile(filepath.Join(realRoot, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rf := &readFileTool{opts: opts}
	p, err = rf.Plan(map[string]any{"path": "a.go"})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("read_file run: %v", err)
	}
	if p.Foreign {
		t.Error("read_file на собственном файле не должен помечать вывод как чужой")
	}
}
