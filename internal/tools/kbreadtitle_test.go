package tools

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Заголовок kb_read повторяет правило выдачи поиска: у текстового файла после
// названия идёт путь от корня коллекции, у книги — название, автор и ничего
// лишнего. Пути и имена выдуманные.
func TestReadHeaderCarriesPathOfTextFile(t *testing.T) {
	cases := []struct {
		name  string
		parts []kb.Result
		want  string
	}{
		{
			name: "текстовый файл — путь в скобках",
			parts: []kb.Result{
				{ID: "projectdocs/3#0", Book: "План работ", Rel: "docs/plan/stage1.md", Unit: "строки", UnitFrom: 1},
				{ID: "projectdocs/3#1", Book: "План работ", Rel: "docs/plan/stage1.md", Unit: "строки", UnitFrom: 20},
			},
			want: "План работ (docs/plan/stage1.md) · фрагменты 2\n\n",
		},
		{
			name: "название равно пути — путь не повторяется",
			parts: []kb.Result{
				{ID: "projectdocs/7#0", Book: "docs/notes.txt", Rel: "docs/notes.txt", Unit: "строки", UnitFrom: 1},
			},
			want: "docs/notes.txt · фрагменты 1\n\n",
		},
		{
			name: "книга с автором — без пути",
			parts: []kb.Result{
				{ID: "books/12#37", Book: "Язык Go", Author: "Автор А.", Year: 2019, Unit: "стр.", UnitFrom: 41},
			},
			want: "Язык Go · Автор А. · фрагменты 1\n\n",
		},
	}
	for _, c := range cases {
		if got := readHeader(c.parts); got != c.want {
			t.Errorf("%s: получено %q, ожидалось %q", c.name, got, c.want)
		}
	}
}
