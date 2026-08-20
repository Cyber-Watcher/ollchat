package chatlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewSessionIDShape(t *testing.T) {
	id := NewSessionID()
	if len(id) != sessionIDLen {
		t.Fatalf("длина идентификатора сеанса = %d, ожидалось %d: %q", len(id), sessionIDLen, id)
	}
	for _, r := range id {
		if !strings.ContainsRune(idAlphabet, r) {
			t.Errorf("знак %q не из алфавита %q", r, idAlphabet)
		}
	}
	// Знаки, которые путают глазом, в алфавит попасть не должны.
	for _, bad := range "01oliu" {
		if strings.ContainsRune(idAlphabet, bad) {
			t.Errorf("знак %q не должен входить в алфавит идентификаторов", bad)
		}
	}
}

// Два запуска подряд не должны получить один идентификатор: иначе записи
// разных сеансов в одном каталоге журналов не различить.
func TestNewSessionIDDiffers(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		seen[NewSessionID()] = true
	}
	if len(seen) < 60 {
		t.Fatalf("64 идентификатора дали лишь %d разных значений", len(seen))
	}
}

func TestFormatTurnID(t *testing.T) {
	cases := []struct {
		session string
		turn    int
		want    string
	}{
		{"k7f3", 0, "k7f3-00"},
		{"k7f3", 1, "k7f3-01"},
		{"k7f3", 99, "k7f3-99"},
		{"k7f3", 100, "k7f3-100"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := FormatTurnID(c.session, c.turn); got != c.want {
			t.Errorf("FormatTurnID(%q, %d) = %q, ожидалось %q", c.session, c.turn, got, c.want)
		}
	}
}

// Порядок BeginTurn/EndTurn: внутри обмена — его номер, между обменами — 00.
func TestTurnCounterSequence(t *testing.T) {
	l := New(t.TempDir(), "chat.md", true)
	defer l.Close()
	s := l.SessionID()

	if got := l.TurnID(); got != s+"-00" {
		t.Errorf("до первого вопроса ожидался %s-00, получено %q", s, got)
	}
	if got := l.BeginTurn(); got != s+"-01" {
		t.Errorf("первый обмен = %q, ожидался %s-01", got, s)
	}
	if got := l.TurnID(); got != s+"-01" {
		t.Errorf("внутри обмена ожидался %s-01, получено %q", s, got)
	}
	l.EndTurn()
	if got := l.TurnID(); got != s+"-00" {
		t.Errorf("между обменами ожидался %s-00, получено %q", s, got)
	}
	if got := l.LastTurnID(); got != s+"-01" {
		t.Errorf("последний обмен между обменами = %q, ожидался %s-01", got, s)
	}
	if got := l.BeginTurn(); got != s+"-02" {
		t.Errorf("второй обмен = %q, ожидался %s-02", got, s)
	}
	if l.Turns() != 2 {
		t.Errorf("обменов = %d, ожидалось 2", l.Turns())
	}
}

// Счётчик обменов не зависит от того, ведётся ли запись: идентификатор
// показывается в интерфейсе и при выключенном журнале.
func TestTurnCounterWorksWhenDisabled(t *testing.T) {
	l := New(t.TempDir(), "chat.md", false)
	defer l.Close()
	s := l.SessionID()

	if got := l.BeginTurn(); got != s+"-01" {
		t.Fatalf("при выключенном журнале обмен = %q, ожидался %s-01", got, s)
	}
	if l.Turns() != 1 {
		t.Errorf("обменов = %d, ожидалось 1", l.Turns())
	}
}

// Главное свойство: вопрос, вызов инструмента и ответ одного обмена помечены
// одинаково, а запись между обменами — номером 00.
func TestEntriesOfOneTurnShareID(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat.md", true)
	defer l.Close()
	ts := time.Date(2026, 8, 20, 14, 30, 0, 0, time.Local)

	id := l.BeginTurn()
	if err := l.WriteAt(ts, KindQuestion, "что в файле?"); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteTool("read_file", "main.go", "package main", true); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteFromAt(ts, KindAnswer, "qwen3.5:122b", "пакет main"); err != nil {
		t.Fatal(err)
	}
	l.EndTurn()
	if err := l.WriteAt(ts, KindSystem, "Переключение на модель gemma4:12b."); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "chat.md"))
	if err != nil {
		t.Fatal(err)
	}
	var heads []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "[") {
			heads = append(heads, line[:strings.Index(line, "]")+1])
		}
	}
	want := []string{"[" + id + "]", "[" + id + "]", "[" + id + "]", "[" + l.SessionID() + "-00]"}
	if len(heads) != len(want) {
		t.Fatalf("шапок записей %d, ожидалось %d:\n%s", len(heads), len(want), data)
	}
	for i := range want {
		if heads[i] != want[i] {
			t.Errorf("шапка %d = %s, ожидалось %s", i+1, heads[i], want[i])
		}
	}
}
