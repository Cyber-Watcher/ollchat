package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ollmcp --http без OLLMCP_TOKEN не открывает порт сети: прежде служба лишь
// писала «ключ НЕ ЗАДАН» и отдавала библиотеку всем, кто достучится.
func TestHTTPRefusesNetworkWithoutToken(t *testing.T) {
	t.Setenv("OLLMCP_TOKEN", "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := "[kb]\ndir = \"" + filepath.Join(dir, "kb") + "\"\n" +
		"[log]\nenabled = false\n" +
		"[[servers]]\nname = \"local\"\nurl = \"http://127.0.0.1:11434\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"0.0.0.0:0", ":0"} {
		// Без проверки служба поднялась бы и ждала сигнала — тест не должен
		// висеть вместе с ней.
		done := make(chan error, 1)
		go func() { done <- run(cfg, "", addr, false, false) }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "OLLMCP_TOKEN") {
				t.Errorf("--http %s без ключа: %v, ожидался отказ с OLLMCP_TOKEN", addr, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("--http %s без ключа: служба поднялась", addr)
		}
	}
}
