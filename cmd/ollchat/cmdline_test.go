package main

import (
	"strings"
	"testing"
)

// Две команды в одной строке — отказ с обеими названными. Раньше
// выполнялась первая из них, а вторая молча пропускалась.
func TestSeveralCommandsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--kb-list", "--graph-build=books"},
		{"--graph-status=books", "--graph-communities=books"},
		{"--ask=вопрос", "--questions=q.txt"},
	} {
		err := checkCommandLine(newFlags(t, args...))
		if err == nil {
			t.Errorf("%v: принято, а выполнилась бы только одна команда", args)
			continue
		}
		for _, a := range args {
			name, _, _ := strings.Cut(a, "=")
			if !strings.Contains(err.Error(), name) {
				t.Errorf("%v: в отказе не названа %s: %v", args, name, err)
			}
		}
	}
}

// Ключи после значения разбираются, а не уходят в значения. Раньше
// `--ask q --tools on --json` давал json=false: разбор вставал на «on».
func TestFlagsAfterValueParsed(t *testing.T) {
	f := newFlags(t, "--ask", "вопрос", "--tools", "on", "--json")
	if !*f.askJSON || !*f.askTools {
		t.Errorf("json=%v tools=%v: ключи после значения не разобраны", *f.askJSON, *f.askTools)
	}
	if len(f.args) != 1 || f.args[0] != "on" {
		t.Errorf("значения: %q", f.args)
	}
	// А сама строка отклоняется: «on» ни к чему не относится, и с «off»
	// она включила бы инструменты вопреки написанному.
	err := checkCommandLine(f)
	if err == nil || !strings.Contains(err.Error(), `"on"`) || !strings.Contains(err.Error(), "--tools=false") {
		t.Errorf("лишнее значение не отклонено как следует: %v", err)
	}

	f = newFlags(t, "--kb-index", "go", "/книги/a", "--kb-keep-thin", "/книги/b")
	if !*f.kbKeepThin {
		t.Error("--kb-keep-thin между путями не разобран")
	}
	if strings.Join(f.args, "|") != "/книги/a|/книги/b" {
		t.Errorf("пути книг: %q", f.args)
	}
	if err := checkCommandLine(f); err != nil {
		t.Errorf("пути у --kb-index отклонены: %v", err)
	}
}

// «--» останавливает разбор: дальше ключи подкоманды, и разбирает их она.
// Но «--» в роли значения ключа — просто значение.
func TestDashesStopParsing(t *testing.T) {
	f := newFlags(t, "--graph-stats", "books", "--", "-hubs", "--kb-list")
	if *f.kbList || strings.Join(f.args, "|") != "-hubs|--kb-list" {
		t.Errorf("после «--»: kb-list=%v, значения %q", *f.kbList, f.args)
	}
	if err := checkCommandLine(f); err != nil {
		t.Errorf("ключи подкоманды отклонены: %v", err)
	}

	f = newFlags(t, "--kb-index", "go", "/a", "--", "/b", "-c")
	if strings.Join(f.args, "|") != "/a|/b|-c" {
		t.Errorf("значения вокруг «--»: %q", f.args)
	}

	f = newFlags(t, "--graph-unmerge-why", "--", "--kb-list")
	if *f.graphUnmergeWhy != "--" || !*f.kbList || len(f.args) != 0 {
		t.Errorf("«--» как значение: why=%q kb-list=%v значения %q", *f.graphUnmergeWhy, *f.kbList, f.args)
	}
}

// Лишние значения у команды, которая их не берёт, и без команды — отказ.
func TestStrayValuesRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--graph-status=books", "лишнее"},
		{"--ask", "как", "связаны"},
		{"просто-слово"},
	} {
		if err := checkCommandLine(newFlags(t, args...)); err == nil {
			t.Errorf("%v: лишние значения приняты молча", args)
		}
	}
}

// Одна команда с уточнениями — обычный запуск.
func TestOneCommandWithModifiersAccepted(t *testing.T) {
	for _, args := range [][]string{
		{"--graph-status=books", "--graph-folder=/AI", "--graph-books"},
		{"--graph-merge=books", "--graph-merge-file=v.tsv", "--graph-merge-drop"},
		{"--ask=вопрос", "--json", "--tools", "--repeat=3"},
		{"--graph-build=books", "--graph-link-new"},
		{},
	} {
		if err := checkCommandLine(newFlags(t, args...)); err != nil {
			t.Errorf("%v: отказ %v", args, err)
		}
	}
}
