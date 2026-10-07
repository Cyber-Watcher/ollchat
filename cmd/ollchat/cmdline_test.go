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
