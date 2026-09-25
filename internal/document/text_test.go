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

// Код читается как текст, но заголовком куска служит объявление — иначе
// ссылка «строки 120–140» не говорит, что это за место (этап 105, М3).
func TestReadGoCode(t *testing.T) {
	src := strings.Join([]string{
		"package tools",
		"",
		"// compileSearch готовит шаблон.",
		"func compileSearch(pat string) error {",
		"\tinner := func() {}",
		"\t_ = inner",
		"\treturn nil",
		"}",
		"",
		"type ThinLimits struct {",
		"\tMinChunks int",
		"}",
		"",
		"func (l ThinLimits) Verdict() bool { return false }",
	}, "\n")
	p := write(t, "search.go", src)
	d, parts, err := Parts(p, 0)
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	if !d.Kind.Text() || d.Units != len(parts) {
		t.Errorf("код должен читаться как текст со строками: %q, строк %d, частей %d",
			d.Kind, d.Units, len(parts))
	}
	// Название документа — с каталогом: файлов main.go в проекте десятки.
	if want := filepath.Base(filepath.Dir(p)) + "/search.go"; d.Title != want {
		t.Errorf("название %q, ожидалось %q", d.Title, want)
	}
	titles := map[int]string{}
	for _, part := range parts {
		titles[part.Number] = part.Title
	}
	for _, c := range []struct {
		line int
		want string
	}{
		{4, "func compileSearch"}, // объявление
		{7, "func compileSearch"}, // тело той же функции
		{10, "type ThinLimits"},   // тип
		{14, "func Verdict"},      // метод: приёмник пропускается
	} {
		if titles[c.line] != c.want {
			t.Errorf("строка %d: заголовок %q, ожидался %q", c.line, titles[c.line], c.want)
		}
	}
	// Вложенное замыкание разделом не считается: иначе ссылка уехала бы на него.
	if titles[5] != "func compileSearch" {
		t.Errorf("вложенная функция не должна менять заголовок, получено %q", titles[5])
	}
}

// Оболочка и Python: свои виды объявлений.
func TestCodeHeadingShellAndPython(t *testing.T) {
	for _, c := range []struct{ line, ext, want string }{
		{"wait_free() {", ".sh", "wait_free()"},
		{"function check {", ".sh", ""}, // без скобок не ловим: слишком общо
		{"check() {", ".sh", "check()"},
		{"  local x=1", ".sh", ""},
		{"def main():", ".py", "def main"},
		{"    def helper(self):", ".py", "def helper"}, // метод с отступом — берём
		{"class Judge:", ".py", "class Judge"},
		{"x = 1", ".py", ""},
		{"func Open() {", ".go", "func Open"},
		{"\tfunc inner() {}", ".go", ""}, // с отступом у Go — не раздел
	} {
		if got := codeHeading(c.line, c.ext); got != c.want {
			t.Errorf("codeHeading(%q, %s) = %q, ожидалось %q", c.line, c.ext, got, c.want)
		}
	}
}

// Расширения кода берутся как текстовые, чужие — нет.
func TestCodeExt(t *testing.T) {
	for _, c := range []struct {
		path string
		want bool
	}{
		{"a/b.go", true}, {"x.sh", true}, {"y.py", true}, {"z.bash", true},
		{"doc.md", false}, {"book.pdf", false}, {"conf.toml", false}, {"data.json", false},
	} {
		if got := CodeExt(c.path); got != c.want {
			t.Errorf("CodeExt(%q) = %v, ожидалось %v", c.path, got, c.want)
		}
		if c.want && !IndexExt(c.path) {
			t.Errorf("IndexExt(%q) должен брать код", c.path)
		}
	}
}
