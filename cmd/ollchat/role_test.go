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

// Каждый ключ командной строки отнесён ровно к одному из трёх списков.
//
// Списки пишутся руками намеренно: новый ключ без строки здесь роняет тест,
// и решать, пишет ли его команда в коллекцию, приходится сознательно.
// До 07.10.2026 такого теста не было, хотя комментарий к writingFlags его
// обещал, и --kb-hash с --kb-retitle остались открыты машине-читателю.
var (
	// flagsWriting — команды, которые создают или правят коллекцию и граф;
	// машине-читателю запрещены. Ровно ключи writingFlags.
	flagsWriting = []string{
		"--kb-index", "--kb-sync", "--kb-reindex", "--kb-refresh", "--kb-merge",
		"--kb-embed", "--kb-years", "--kb-hash", "--kb-retitle", "--kb-reanalyze",
		"--kb-flag-toc", "--graph-build", "--graph-embed", "--graph-embed-stale",
		"--graph-embed-edges", "--graph-embed-follow", "--graph-communities",
		"--graph-drift", "--graph-summaries", "--graph-recheck", "--graph-findings",
		"--graph-merge", "--graph-unmerge", "--graph-queue-doubts", "--graph-compact",
		"--graph-forget-chunks", "--graph-drop-dead-marks", "--graph-deny-aliases",
		"--graph-rebase-books", "--graph-record-books", "--graph-groups-build",
		"--graph-drop-book", "--graph-resolve", "--graph-tune", "--graph-bench",
		"--graph-forget-toc",
		// Не команда, а уточнение --graph-build, но пишет своё: links.jsonl.
		"--graph-link-new",
	}
	// flagsReading — команды, которые коллекцию и граф не правят, и приём
	// архивов с ведущей машины; читателю разрешены.
	flagsReading = []string{
		"--version", "--init-config", "--kb-list", "--kb-doctor", "--kb-rebase",
		"--graph-pending", "--nodes", "--graph-doctor", "--graph-repartition-due",
		"--graph-archive", "--graph-archives", "--graph-restore", "--graph-find",
		"--kb-eval-gen", "--kb-sample", "--kb-eval", "--graph-entry-eval", "--graph-book",
		"--graph-stats", "--doc-probe", "--scan-redact", "--scan-redact-llm", "--census",
		"--probes", "--graph-status", "--ask", "--ask-stdin", "--questions", "--serve",
	}
	// flagsModifying — не команды: уточняют команду или запуск целиком.
	flagsModifying = []string{
		"-c", "--server", "--model", "--cwd", "--mode", "--steps-file",
		"--kb-merge-force", "--kb-yes", "--kb-rebase-from", "--kb-rebase-to",
		"--kb-dry-run", "--kb-keep-thin", "--kb-quick", "--kb-recount", "--kb-embed-plain",
		"--graph-forget-file", "--graph-forget-skip", "--graph-unmerge-file",
		"--graph-unmerge-why", "--graph-deny-file", "--graph-folder",
		"--graph-entry-eval-collection", "--graph-entry-eval-limit",
		"--graph-entry-eval-show", "--graph-entry-eval-entities", "--graph-groups",
		"--graph-name", "--graph-kind", "--graph-note", "--graph-limit", "--graph-workers",
		"--graph-redo-empty", "--graph-ignore-busy", "--graph-log",
		"--graph-allow-model-change", "--graph-allow-prompt-change", "--graph-books",
		"--graph-summaries-force", "--graph-summaries-min", "--kb-eval-gen-n", "--kb-seed",
		"--kb-eval-collection", "--kb-eval-k", "--kb-eval-wide", "--kb-eval-dedupe",
		"--kb-eval-keep-adjacent", "--kb-eval-weight", "--kb-eval-table-boost",
		"--kb-eval-rrfk", "--kb-eval-only", "--kb-eval-rerank", "--kb-eval-candidates",
		"--kb-eval-expand", "--kb-eval-snippet", "--graph-embed-recount",
		"--graph-findings-min-rating", "--graph-findings-min-members",
		"--graph-findings-redo", "--graph-findings-dry", "--graph-communities-fresh",
		"--graph-carry-similarity", "--graph-bench-models", "--graph-bench-keep",
		"--graph-tune-resolutions", "--graph-tune-betas", "--graph-tune-show",
		"--graph-drift-similarity", "--graph-drift-show", "--graph-merge-file",
		"--graph-merge-level", "--graph-merge-min-cos-same", "--graph-merge-drop",
		"--graph-book-name", "--graph-docs-file", "--from", "--graph-groups-min-cos",
		"--graph-groups-min-cos-mutual", "--graph-restore-book", "--apply",
		"--graph-compact-check", "--graph-compact-drop-dead", "--graph-compact-force",
		"--graph-merge-dry", "--graph-resolve-full", "--graph-resolve-min-cos",
		"--graph-resolve-min-cos-mutual", "--graph-resolve-cross", "--graph-resolve-normkey",
		"--graph-resolve-show", "--graph-resolve-out", "--graph-verdicts",
		"--graph-embed-node", "--graph-embed-every", "--graph-embed-limit",
		"--graph-embed-once", "--graph-recheck-count", "--graph-restore-yes",
		"--graph-collection", "--graph-json", "--json", "--repeat", "--mix", "--show-mix",
		"--tools", "--temperature", "--seed", "--num-ctx", "--think", "--kb-use", "--mcp",
		"--graph-sense", "--graph-pool", "--mix-entities", "--mix-neighbors", "--kb-topk",
		"--kb-max-per-book", "--kb-min-cosine", "--kb-semantic-weight",
	}
)

func TestEveryFlagClassified(t *testing.T) {
	f := newFlags(t)
	class := map[string]string{}
	for kind, names := range map[string][]string{
		"пишет": flagsWriting, "читает": flagsReading, "уточняет": flagsModifying,
	} {
		for _, n := range names {
			n = strings.TrimLeft(n, "-")
			if prev, dup := class[n]; dup {
				t.Errorf("ключ %s в двух списках: «%s» и «%s»", n, prev, kind)
			}
			class[n] = kind
		}
	}
	defined := map[string]bool{}
	flag.CommandLine.VisitAll(func(fl *flag.Flag) {
		defined[fl.Name] = true
		if class[fl.Name] == "" {
			t.Errorf("ключ -%s не отнесён ни к одному списку: решите, пишет ли он в коллекцию "+
				"или граф (тогда и в writingFlags), только читает или уточняет команду", fl.Name)
		}
	})
	for n := range class {
		if !defined[n] {
			t.Errorf("в списках есть -%s, а такого ключа нет", n)
		}
	}

	// Запрет читателю — ровно список пишущих.
	w := writingFlags(f)
	for _, n := range flagsWriting {
		if _, ok := w[n]; !ok {
			t.Errorf("%s пишет, а в writingFlags его нет — читатель сможет им писать", n)
		}
	}
	for n := range w {
		if class[strings.TrimLeft(n, "-")] != "пишет" {
			t.Errorf("%s в writingFlags, а в списке пишущих его нет", n)
		}
	}

	// Каждая команда таблицы — пишущая или читающая, и наоборот.
	inTable := map[string]bool{}
	for _, c := range commands(f) {
		inTable[c.name] = true
		if k := class[strings.TrimLeft(c.name, "-")]; k != "пишет" && k != "читает" {
			t.Errorf("команда %s в списке «%s»", c.name, k)
		}
	}
	for _, n := range append(append([]string{}, flagsReading...), flagsWriting...) {
		if !inTable[n] && n != "--graph-link-new" {
			t.Errorf("%s — команда, а в таблице commands её нет", n)
		}
	}
}

// Читателю запрещены команды, создающие содержимое, и разрешён приём архивов.
func TestReaderForbidsWritingFlags(t *testing.T) {
	forbidden := []string{
		"--graph-build=books", "--kb-index=books", "--graph-embed=books",
		"--graph-communities=books", "--graph-summaries=books", "--kb-reindex=books",
		"--graph-merge=books", "--graph-compact=books", "--graph-embed-edges=books",
		"--kb-hash=books", "--kb-retitle=books",
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
