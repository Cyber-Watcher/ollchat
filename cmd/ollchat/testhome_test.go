package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain даёт тестам пакета временный HOME и проверяет, что в каталоге
// пакета не появился каталог «~».
//
// 15.10.2026: тесты строят конфиг через config.Default(), где каталог журналов —
// «~/.local/share/ollchat/logs» без развёртки (её делает config.Load). Журнал
// шагов ложился в cmd/ollchat/~ — 61 файл, и владелец увидел их в индексе git.
// С развёрткой в steplog.New «~» ведёт в HOME — здесь это временный каталог,
// а не настоящий журнал владельца.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ollchat-cmd-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "временный HOME:", err)
		os.Exit(2)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	if _, err := os.Stat("~"); err == nil {
		fmt.Fprintln(os.Stderr, "ПРОВАЛ: тесты создали каталог «~» в cmd/ollchat — путь с неразвёрнутой тильдой")
		code = 1
	}
	os.Exit(code)
}
