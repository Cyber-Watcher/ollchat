package chatlog

import (
	"strings"
	"testing"
	"time"
)

func TestParsePatternName(t *testing.T) {
	ts := time.Date(2026, 8, 20, 9, 5, 3, 0, time.Local)
	cases := []struct {
		pattern string
		want    string
		session bool
	}{
		{"chat-%Y-%m-%d_%H-%M-%S.md", "chat-2026-08-20_09-05-03.md", true},
		{"chat-%Y-%m-%d.md", "chat-2026-08-20.md", false},
		{"chat.md", "chat.md", false},
		{"%y%m%d-%H%M.log", "260820-0905.log", true},
		{"100%%-%Y.md", "100%-2026.md", false},
		// Литеральные цифры остаются литеральными: разбор идёт по директивам,
		// а не через time.Format, где "2006" превратилось бы в год.
		{"log2006-%Y.md", "log2006-2026.md", false},
		{"logs/%Y/chat-%m-%d.md", "logs/2026/chat-08-20.md", false},
	}
	for _, c := range cases {
		p, err := ParsePattern(c.pattern)
		if err != nil {
			t.Fatalf("ParsePattern(%q): %v", c.pattern, err)
		}
		if got := p.Name(ts); got != c.want {
			t.Errorf("Name(%q) = %q, ожидалось %q", c.pattern, got, c.want)
		}
		if p.PerSession() != c.session {
			t.Errorf("PerSession(%q) = %v, ожидалось %v", c.pattern, p.PerSession(), c.session)
		}
	}
}

func TestParsePatternHourIs24(t *testing.T) {
	p, err := ParsePattern("chat-%H-%M-%S.md")
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 8, 20, 23, 59, 59, 0, time.Local)
	if got := p.Name(ts); got != "chat-23-59-59.md" {
		t.Fatalf("получено %q, ожидалось chat-23-59-59.md", got)
	}
}

func TestParsePatternErrors(t *testing.T) {
	cases := []struct {
		pattern string
		substr  string
	}{
		{"", "пустой"},
		{"   ", "пустой"},
		{"chat-%Q.md", "%Q"},
		{"chat-%Y.md%", "оборван"},
		{"/tmp/chat.md", "относительным"},
		{"../chat.md", ".."},
		{"logs/../../chat.md", ".."},
	}
	for _, c := range cases {
		_, err := ParsePattern(c.pattern)
		if err == nil {
			t.Errorf("ParsePattern(%q) ошибки не вернул", c.pattern)
			continue
		}
		if !strings.Contains(err.Error(), c.substr) {
			t.Errorf("ParsePattern(%q): ошибка %q не содержит %q", c.pattern, err, c.substr)
		}
	}
}

func TestLegacyPatternUsesGoLayout(t *testing.T) {
	p := LegacyPattern("chat-2006-01-02.md")
	ts := time.Date(2026, 8, 20, 9, 5, 3, 0, time.Local)
	if got := p.Name(ts); got != "chat-2026-08-20.md" {
		t.Fatalf("получено %q, ожидалось chat-2026-08-20.md", got)
	}
	if p.PerSession() {
		t.Fatal("устаревший шаблон не должен требовать файл на запуск")
	}
	if got := LegacyPattern("").Name(ts); got != "chat-2026-08-20.md" {
		t.Fatalf("пустой устаревший шаблон дал %q", got)
	}
}
