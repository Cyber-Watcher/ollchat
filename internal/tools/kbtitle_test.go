package tools

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// firstLine — заголовок выдержки: всё до первого перевода строки.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Заголовок выдержки: у текстового файла после названия идёт путь от корня
// коллекции, у книги заголовок прежний. Те же заголовки обязана печатать
// и выдача /search (find.Line) — «один вид на все места выдачи».
func TestHitHeaderCarriesPathOfTextFile(t *testing.T) {
	cases := []struct {
		name string
		hit  kb.Result
		want string
	}{
		{
			name: "строки с путём",
			hit: kb.Result{
				ID: "projectdocs/3410#2", Book: "План этапа", Rel: "docs/plan/stage.md",
				Unit: "строки", UnitFrom: 20, UnitTo: 31, Snippet: "текст",
			},
			want: "[1] План этапа (docs/plan/stage.md) · строки 20–31 · id=projectdocs/3410#2",
		},
		{
			name: "название равно пути — путь не повторяется",
			hit: kb.Result{
				ID: "projectdocs/7#0", Book: "docs/notes.txt", Rel: "docs/notes.txt",
				Unit: "строки", UnitFrom: 1, UnitTo: 5, Snippet: "текст",
			},
			want: "[1] docs/notes.txt · строки 1–5 · id=projectdocs/7#0",
		},
		{
			name: "книга с автором и годом — без пути",
			hit: kb.Result{
				ID: "books/12#37", Book: "Язык Go", Author: "Автор А.", Year: 2019,
				Unit: "стр.", UnitFrom: 41, UnitTo: 42, Snippet: "текст",
			},
			want: "[1] Язык Go · Автор А. · 2019 г. · стр. 41–42 · id=books/12#37",
		},
	}
	for _, c := range cases {
		got := firstLine(formatHit(1, c.hit))
		if got != c.want {
			t.Errorf("%s: kb_search печатает\n  %q\nждали\n  %q", c.name, got, c.want)
		}
		e := find.Excerpt{
			ID: c.hit.ID, Book: c.hit.Book, Rel: c.hit.Rel, Author: c.hit.Author, Year: c.hit.Year,
			Unit: c.hit.Unit, From: c.hit.UnitFrom, To: c.hit.UnitTo,
		}
		if line := "[1] " + find.Line(e); line != c.want {
			t.Errorf("%s: /search и search печатают\n  %q\nждали\n  %q", c.name, line, c.want)
		}
	}
}
