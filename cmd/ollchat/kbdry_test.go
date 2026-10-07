package main

import (
	"flag"
	"strings"
	"testing"
	"time"

	kmaint "github.com/Cyber-Watcher/ollchat/internal/kb/maint"
)

// Сухой прогон обязан быть сухим при любой команде.
//
// 29.08.2026 --kb-sync --kb-dry-run доиндексировал 49 книг по-настоящему:
// ключ действовал только на --kb-embed, а к остальным молча не применялся.
// Человек просил оценку, а получил работу.
func TestDryRunFlagCoversIndexCommands(t *testing.T) {
	// Проверяется описание ключа: оно и есть обещание пользователю.
	// Если команду добавили, а в описание не внесли — тест напомнит.
	for _, cmd := range []string{"--kb-embed", "--kb-sync", "--kb-index", "--kb-refresh", "--kb-rebase"} {
		if !strings.Contains(dryRunHelp(), cmd) {
			t.Errorf("в описании --kb-dry-run не упомянута команда %s", cmd)
		}
	}
}

// Команды, которые молча пропускали --kb-dry-run и выполнялись
// по-настоящему (аудит 07.10.2026), теперь отказывают до запуска.
func TestDryRunRefusedWhereNotUnderstood(t *testing.T) {
	for _, name := range []string{"--kb-reindex", "--kb-years", "--kb-hash",
		"--graph-compact", "--graph-communities", "--graph-summaries", "--graph-findings",
		"--graph-recheck", "--graph-embed", "--graph-build", "--graph-record-books"} {
		f := newFlags(t, name+"=books", "--kb-dry-run")
		err := checkDryRun(f)
		if err == nil {
			t.Errorf("%s --kb-dry-run: отказа нет — команда выполнилась бы по-настоящему", name)
			continue
		}
		// Отказ называет и команду, и то, что сухой прогон понимает.
		for _, want := range []string{name, "--kb-sync", "--graph-merge"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: в отказе нет %q:\n%s", name, want, err)
			}
		}
	}
	// У кого есть свой сухой прогон, отказ его называет.
	err := checkDryRun(newFlags(t, "--graph-compact=books", "--kb-dry-run"))
	if err == nil || !strings.Contains(err.Error(), "--graph-compact-check") {
		t.Errorf("отказ не подсказывает --graph-compact-check: %v", err)
	}
}

// Команды, которые сухой прогон понимают, работают с ним как раньше.
func TestDryRunAcceptedWhereUnderstood(t *testing.T) {
	for _, name := range dryRunCommands() {
		if err := checkDryRun(newFlags(t, name+"=books", "--kb-dry-run")); err != nil {
			t.Errorf("%s --kb-dry-run отклонён: %v", name, err)
		}
	}
	// Без ключа проверять нечего.
	if err := checkDryRun(newFlags(t, "--graph-build=books")); err != nil {
		t.Errorf("сборка без --kb-dry-run отклонена: %v", err)
	}
}

// Сухой прогон без команды — тоже отказ: иначе запустился бы интерфейс,
// а человек думал бы, что ничего не происходит.
func TestDryRunWithoutCommandRefused(t *testing.T) {
	if err := checkDryRun(newFlags(t, "--kb-dry-run")); err == nil {
		t.Error("--kb-dry-run без команды принят молча")
	}
}

// --graph-merge с общим ключом получает сухой прогон: до 07.10.2026 он
// склеивал по-настоящему, слушаясь только своего --graph-merge-dry.
func TestGraphMergeTakesKBDryRun(t *testing.T) {
	if !newFlags(t, "--graph-merge=books", "--kb-dry-run").mergeDry() {
		t.Error("--graph-merge --kb-dry-run: сухой прогон не передан")
	}
	if !newFlags(t, "--graph-merge=books", "--graph-merge-dry").mergeDry() {
		t.Error("--graph-merge-dry перестал действовать")
	}
	if newFlags(t, "--graph-merge=books").mergeDry() {
		t.Error("без ключей склейка стала сухой")
	}
}

// Таблица команд сверена с объявленными ключами: опечатка в имени или ключ,
// привязанный не к своей переменной, иначе всплыли бы только в работе.
func TestCommandTableMatchesFlags(t *testing.T) {
	for _, c := range commands(newFlags(t)) {
		name := strings.TrimPrefix(c.name, "--")
		fl := flag.CommandLine.Lookup(name)
		if fl == nil {
			t.Errorf("команда %s: такого ключа нет", c.name)
			continue
		}
		arg := c.name + "=x"
		if b, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			arg = c.name
		}
		got := requested(newFlags(t, arg))
		if len(got) != 1 || got[0].name != c.name {
			var names []string
			for _, g := range got {
				names = append(names, g.name)
			}
			t.Errorf("%s: запрошенными считаются %v", arg, names)
		}
	}
}

// Срок оценки короткий: человек спрашивает «сколько займёт» и ждёт ответа
// быстрее, чем за минуту работы.
func TestEstimateTimeoutIsShort(t *testing.T) {
	if kmaint.EstimateTimeout > 2*time.Minute {
		t.Errorf("срок оценки %v слишком велик", kmaint.EstimateTimeout)
	}
	if kmaint.EstimateTimeout < 10*time.Second {
		t.Errorf("срок оценки %v слишком мал: первый запрос грузит модель", kmaint.EstimateTimeout)
	}
}
