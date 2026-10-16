package ui

import (
	"fmt"
	"os"
	"testing"
)

// TestMain даёт тестам пакета временный HOME и проверяет, что в каталоге
// пакета не появился каталог «~» (найден 15.10.2026: internal/ui/~/.local/share/
// ollchat/kb от 05.09 — база знаний из config.Default() без развёртки тильды).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ollchat-ui-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "временный HOME:", err)
		os.Exit(2)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	if _, err := os.Stat("~"); err == nil {
		fmt.Fprintln(os.Stderr, "ПРОВАЛ: тесты создали каталог «~» в internal/ui — путь с неразвёрнутой тильдой")
		code = 1
	}
	os.Exit(code)
}
