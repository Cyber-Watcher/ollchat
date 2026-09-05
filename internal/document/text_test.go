package document

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("запись файла: %v", err)
	}
	return p
}

// Markdown читается как документ со строками вместо страниц, название берётся
// из первого заголовка первого уровня.
func TestReadMarkdown(t *testing.T) {
	p := write(t, "guide.md", "# Руководство по сборке\n\n## Тулчейн\n\nСобирать нечем иным.\n")
	d, parts, err := Parts(p, 0)
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	if d.Kind != KindMarkdown || !d.Kind.Text() {
		t.Errorf("формат определён как %q", d.Kind)
	}
	if d.Unit != "строк" {
		t.Errorf("единица ссылки %q, ожидались строки", d.Unit)
	}
	if d.Title != "Руководство по сборке" {
		t.Errorf("название %q", d.Title)
	}
	if len(parts) != 5 && len(parts) != 6 { // последняя строка пустая
		t.Fatalf("частей %d, ожидалось по одной на строку", len(parts))
	}
	if parts[0].Number != 1 {
		t.Errorf("нумерация начинается с %d", parts[0].Number)
	}
	// Строка под «## Тулчейн» знает свой заголовок.
	if got := parts[4].Title; got != "Тулчейн" {
		t.Errorf("заголовок строки 5 — %q, ожидался «Тулчейн»", got)
	}
}

// Обычный текст тоже читается, название берётся из имени файла.
func TestReadPlainText(t *testing.T) {
	p := write(t, "notes.txt", "первая строка\nвторая строка\n")
	d, parts, err := Parts(p, 0)
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	if d.Kind != KindText {
		t.Errorf("формат %q", d.Kind)
	}
	if d.Title != "notes" {
		t.Errorf("название %q, ожидалось имя файла", d.Title)
	}
	if len(parts) < 2 || parts[1].Text != "вторая строка" {
		t.Errorf("части разобраны неверно: %+v", parts)
	}
}

// Файл длиннее предела не индексируется, а сообщение объясняет, что делать,
// и переживает сокращение до одной строки.
func TestReadTextTooManyLines(t *testing.T) {
	p := write(t, "huge.md", strings.Repeat("строка\n", MaxTextLines+10))
	_, _, err := Parts(p, 0)
	if err == nil {
		t.Fatal("файл длиннее предела должен отклоняться")
	}
	if !errors.Is(err, ErrTooManyLines) {
		t.Fatalf("ошибка не того рода: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"65535", "разбейте"} {
		if !strings.Contains(msg, want) {
			t.Errorf("в сообщении нет %q: %s", want, msg)
		}
	}
	// /kb печатает причину одной строкой и режет её по первому двоеточию
	// раньше 60-го знака либо по 120 знакам. Совет обязан уцелеть.
	if i := strings.Index(msg, ":"); i >= 0 && i < 60 {
		t.Errorf("двоеточие на %d-м знаке обрежет совет: %s", i, msg)
	}
	if n := utf8.RuneCountInString(msg); n > 120 {
		t.Errorf("сообщение длиной %d знаков будет обрезано: %s", n, msg)
	}
}

// Файл не в UTF-8 отклоняется внятно, а не приходит мусором в контекст.
func TestReadTextNotUTF8(t *testing.T) {
	p := write(t, "cp1251.txt", string([]byte{0xef, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}))
	if _, _, err := Parts(p, 0); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("ожидался отказ по кодировке, получено: %v", err)
	}
}

// Расширение решает: .pdf с текстом внутри остаётся PDF-путём, .md — текстом.
func TestTextExt(t *testing.T) {
	for _, name := range []string{"a.md", "b.MARKDOWN", "c.txt", "d.Text"} {
		if !TextExt(name) {
			t.Errorf("%s должен считаться текстовым", name)
		}
	}
	for _, name := range []string{"a.pdf", "b.epub", "c.html", "d.go"} {
		if TextExt(name) {
			t.Errorf("%s не текстовый", name)
		}
	}
}
