package find

import (
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Оформление выдачи для человека.
//
// Порядок разделов тот же, что у графа: сперва «о чём речь» (понятия), потом
// «как связано» (связи), потом «где написано» (выдержки). Он не случаен: человек
// сначала убеждается, что найдено то самое понятие, и только потом читает текст.
//
// **Оговорка про поиск по словам стоит второй строкой, а не в хвосте.** Она
// меняет то, как читается всё ниже: если смысл не участвовал, отсутствие
// нужного куска ничего не доказывает. В конце длинной выдачи её просто не увидят.

// Render печатает выдачу целиком — один текст для одного блока ленты.
// Render печатает выдачу поиска.
//
// src — источник кусков для выдержки под связью. До 02.09.2026 здесь стоял
// nil, и строка с фразой из книги под связью была видна только в командной
// строке: `/search` в интерфейсе показывал «горутина —использует→ канал
// (подтверждений 48)» без единого слова о том, ЧЕМ они связаны. Передавать
// коллекцию дёшево — это одно обращение к хранилищу на связь.
func Render(r Result, full bool, src graph.Chunks) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Найдено по «%s»", r.Query)
	if r.Collection != "" {
		fmt.Fprintf(&b, " в коллекции %s", r.Collection)
	}
	b.WriteByte('\n')
	if r.WordsWhy != "" {
		b.WriteString(r.WordsWhy + "\n")
	}

	// Понятия и связи рисует сам граф — тем же кодом, что командная строка
	// и инструменты модели. Куски-подтверждения ему не передаются: выдержки
	// печатаются ниже одним общим списком, иначе цитаты идут дважды. А вот
	// источник для строки под связью нужен — это другая цитата, короткая
	// и на месте.
	if len(r.Entities) > 0 {
		b.WriteByte('\n')
		b.WriteString(graph.Render(src, graph.SearchResult{
			Entities: r.Entities, Relations: r.Relations,
		}, graph.RenderOpts{Collection: r.Collection}))
	}

	if len(r.Excerpts) > 0 {
		fmt.Fprintf(&b, "\nВыдержки из книг: %d\n", len(r.Excerpts))
		for i, e := range r.Excerpts {
			fmt.Fprintf(&b, "\n[%d] %s\n", i+1, Line(e))
			text := e.Snippet
			if full {
				text = strings.TrimSpace(e.Text)
			}
			b.WriteString(indent(text, "    "))
			b.WriteByte('\n')
		}
	}

	if r.Empty() {
		b.WriteString("\nничего не нашлось\n")
	}
	if r.GraphNote != "" {
		b.WriteString("\n" + r.GraphNote + "\n")
	}
	b.WriteString(hint(r, full))
	return b.String()
}

// excerptLine — ссылка на книгу: название, автор, год, страницы и id.
//
// Один вид на все места выдачи. До этого их было три разных: у /kb search
// без автора и без диапазона страниц, у инструментов модели полный, у графа
// свой — и человек, сверяя выдачи, не понимал, одна ли это книга.
func Line(e Excerpt) string {
	parts := []string{e.Book}
	if e.Author != "" {
		parts = append(parts, e.Author)
	}
	if e.Year > 0 {
		parts = append(parts, fmt.Sprintf("%d г.", e.Year))
	}
	if e.Unit != "" && e.From > 0 {
		if e.To > e.From {
			parts = append(parts, fmt.Sprintf("%s %d–%d", e.Unit, e.From, e.To))
		} else {
			parts = append(parts, fmt.Sprintf("%s %d", e.Unit, e.From))
		}
	}
	if e.ID != "" {
		parts = append(parts, "id="+e.ID)
	}
	if e.Graph {
		parts = append(parts, "подтверждение графа")
	}
	return strings.Join(parts, " · ")
}

// hint — строка о том, как читать дальше. Показывается только когда есть что
// читать: подсказка под пустой выдачей раздражает, а не помогает.
func hint(r Result, full bool) string {
	if len(r.Excerpts) == 0 {
		return ""
	}
	if full {
		return "\nсписок найденного: Ctrl+F\n"
	}
	return "\nцеликом: /read " + r.Excerpts[0].ID + " · список: Ctrl+F · всё сразу: /search -f\n"
}

// indent сдвигает текст вправо, чтобы выдержка отличалась от ссылки на неё.
func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
