package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
)

// Режим stdio на одном вызове: строка запроса на входе — строка ответа на
// выходе, и ничего постороннего в stdout (этап 91, R5.8).
func TestServeStdioOneCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[kb]\ndir = \"" + filepath.Join(dir, "kb") + "\"\n" +
		"[[servers]]\nname = \"local\"\nurl = \"http://127.0.0.1:11434\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, _, err := build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
			"\n" + // пустая строка пропускается
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_status","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := mcp.Serve(context.Background(), srv, in, &out, false); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("ответов %d, ожидалось 2:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"id":1`) || !strings.Contains(lines[0], "kb_status") {
		t.Fatalf("первый ответ: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"id":2`) || !strings.Contains(lines[1], `"result"`) || strings.Contains(lines[1], `"isError":true`) {
		t.Fatalf("второй ответ: %s", lines[1])
	}
}
