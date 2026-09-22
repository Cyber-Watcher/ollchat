package main

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// newFlags — разбор пустой командной строки в своём наборе флагов: parseFlags
// пишет в flag.CommandLine, и тесты не должны мешать друг другу.
func newFlags(t *testing.T, args ...string) *cliFlags {
	t.Helper()
	old := flag.CommandLine
	t.Cleanup(func() { flag.CommandLine = old })
	flag.CommandLine = flag.NewFlagSet("ollchat", flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stderr)
	f := parseFlagsWith(args)
	return f
}

// Читателю запрещены команды, создающие содержимое, и разрешён приём архивов.
func TestReaderForbidsWritingFlags(t *testing.T) {
	forbidden := []string{
		"--graph-build=books", "--kb-index=books", "--graph-embed=books",
		"--graph-communities=books", "--graph-summaries=books", "--kb-reindex=books",
		"--graph-merge=books", "--graph-compact=books", "--graph-embed-edges=books",
	}
	for _, arg := range forbidden {
		f := newFlags(t, arg)
		if got := readerRefusal(f); got == "" {
			t.Errorf("%s: читателю разрешено, а не должно быть", arg)
		}
	}

	// Приём свежего графа и чтение — работают.
	allowed := []string{
		"--graph-restore=books-20260922.tar.gz", "--kb-rebase=books",
		"--graph-doctor=books", "--kb-doctor=books", "--graph-status=books",
		"--graph-archive=books", "--kb-list",
	}
	for _, arg := range allowed {
		f := newFlags(t, arg)
		if got := readerRefusal(f); got != "" {
			t.Errorf("%s: читателю запрещено (%s), а должно быть можно", arg, got)
		}
	}
}

// Сообщение об отказе называет и команду, и настройку, и что делать дальше.
func TestReaderRefusalExplains(t *testing.T) {
	cfg := config.Default()
	cfg.General.Role = config.RoleReader
	cfg.Path = "/home/i/.config/ollchat/config.toml"
	f := newFlags(t, "--graph-build=books")

	handled, err := dispatchCLI(cfg, f)
	if !handled || err == nil {
		t.Fatalf("сборка на читателе не отклонена: handled=%v err=%v", handled, err)
	}
	for _, want := range []string{"--graph-build", "general.role", "reader", "--graph-restore", cfg.Path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в сообщении нет %q:\n%s", want, err)
		}
	}
}

// На сборщике (умолчание) ничего не отклоняется.
func TestBuilderNotRestricted(t *testing.T) {
	cfg := config.Default()
	if cfg.ReadOnlyMachine() {
		t.Fatal("умолчание сделало машину читателем")
	}
	if got := readerRefusal(newFlags(t, "--graph-build=books")); got == "" {
		t.Skip("нечего проверять")
	}
}
