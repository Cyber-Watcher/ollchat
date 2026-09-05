package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Главное, ради чего таблица переписана: ширина считается по символам.
// Спецификаторы вроде %-24s считают байты, и кириллица разъезжается —
// именно так выглядели прежние «таблицы».
func TestTableAlignsCyrillicByRunes(t *testing.T) {
	tb := newTable(
		column{"модель", alignLeft},
		column{"потолок", alignRight},
	)
	tb.row("nemotron-3.5-lightning:latest", "1024k")
	tb.row("gemma4:12b", "256k")

	lines := strings.Split(tb.String(), "\n")
	width := utf8.RuneCountInString(lines[0])
	for i, l := range lines {
		if got := utf8.RuneCountInString(l); got != width {
			t.Errorf("строка %d шириной %d символов, а рамка %d:\n%s", i, got, width, tb.String())
		}
	}
}

// Длинное значение расширяет колонку, а не вылезает из рамки.
func TestTableGrowsForLongestCell(t *testing.T) {
	tb := newTable(column{"м", alignLeft})
	tb.row("очень длинное имя модели")
	out := tb.String()
	for _, l := range strings.Split(out, "\n") {
		if utf8.RuneCountInString(l) < utf8.RuneCountInString("очень длинное имя модели")+4 {
			t.Fatalf("рамка уже содержимого:\n%s", out)
		}
	}
}

func TestTableAlignment(t *testing.T) {
	tb := newTable(
		column{"слева", alignLeft},
		column{"справа", alignRight},
	)
	tb.row("a", "b")
	lines := strings.Split(tb.String(), "\n")
	dataRow := lines[3]

	if !strings.Contains(dataRow, "│ a     │") {
		t.Errorf("левая колонка выровнена неверно: %q", dataRow)
	}
	if !strings.Contains(dataRow, "│      b │") {
		t.Errorf("правая колонка выровнена неверно: %q", dataRow)
	}
}

// Недостающие ячейки не должны ломать таблицу.
func TestTableToleratesShortRows(t *testing.T) {
	tb := newTable(column{"a", alignLeft}, column{"b", alignLeft}, column{"c", alignLeft})
	tb.row("1")
	tb.row("1", "2", "3", "лишнее")

	lines := strings.Split(tb.String(), "\n")
	width := utf8.RuneCountInString(lines[0])
	for _, l := range lines {
		if utf8.RuneCountInString(l) != width {
			t.Fatalf("строки разной ширины:\n%s", tb.String())
		}
	}
}

func TestTableHasBorders(t *testing.T) {
	tb := newTable(column{"a", alignLeft})
	tb.row("1")
	out := tb.String()
	for _, want := range []string{"┌", "┐", "├", "┤", "└", "┘", "│", "─"} {
		if !strings.Contains(out, want) {
			t.Errorf("в таблице нет символа рамки %q:\n%s", want, out)
		}
	}
}
