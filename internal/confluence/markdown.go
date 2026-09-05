package confluence

import (
	"fmt"
	"strings"
)

// Перевод разобранного дерева в markdown.
//
// Почему markdown, а не «просто текст». Модель просят прочитать страницу
// и часто — сохранить её в репозиторий как .md. Если инструмент отдаёт готовый
// markdown, сохранение сводится к записи в файл, и таблица кодов доезжает
// побайтово. Если отдавать текст, модель будет пересобирать таблицу сама —
// а таблицу кодов через модель прогонять нельзя: она молча поправит опечатку
// в коде, и это не заметит никто.

// ToMarkdown переводит storage format в markdown.
func ToMarkdown(data []byte) (string, error) {
	root, err := parse(data)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	render(&b, root, ctx{})
	return collapseSpaces(b.String()), nil
}

// ctx — обстановка вокруг узла: она решает, как печатать текст.
type ctx struct {
	listDepth int
	ordered   bool
	inCell    bool // внутри ячейки таблицы переносов строк быть не должно
	inCode    bool
}

func render(b *strings.Builder, n *node, c ctx) {
	if n.text != "" {
		b.WriteString(n.text)
		return
	}
	switch {
	case n.space == "ac":
		renderMacro(b, n, c)
		return
	case n.space == "ri":
		return // ссылки на страницы и вложения печатает вызывающий макрос
	}

	switch n.name {
	case "root":
		kids(b, n, c)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.name[1] - '0')
		b.WriteString("\n\n" + strings.Repeat("#", level) + " ")
		kids(b, n, c)
		b.WriteString("\n\n")
	case "p":
		if c.inCell {
			kids(b, n, c)
			b.WriteString(" ")
			return
		}
		b.WriteString("\n\n")
		kids(b, n, c)
		b.WriteString("\n\n")
	case "br":
		if c.inCell {
			b.WriteString(" ")
		} else {
			b.WriteString("\n")
		}
	case "hr":
		b.WriteString("\n\n---\n\n")
	case "strong", "b":
		wrapInline(b, n, c, "**")
	case "em", "i":
		wrapInline(b, n, c, "*")
	case "code":
		wrapInline(b, n, c, "`")
	case "del", "s":
		wrapInline(b, n, c, "~~")
	case "ul", "ol":
		inner := c
		inner.listDepth = c.listDepth + 1
		inner.ordered = n.name == "ol"
		if c.listDepth == 0 && !c.inCell {
			b.WriteString("\n")
		}
		kids(b, n, inner)
		if c.listDepth == 0 && !c.inCell {
			b.WriteString("\n")
		}
	case "li":
		mark := "- "
		if c.ordered {
			mark = "1. "
		}
		b.WriteString("\n" + strings.Repeat("  ", max(c.listDepth-1, 0)) + mark)
		kids(b, n, c)
	case "a":
		renderLink(b, n, c)
	case "table":
		renderTable(b, n, c)
	case "pre":
		b.WriteString("\n\n```\n")
		kids(b, n, ctx{inCode: true})
		b.WriteString("\n```\n\n")
	default:
		kids(b, n, c)
	}
}

func kids(b *strings.Builder, n *node, c ctx) {
	for _, k := range n.kids {
		render(b, k, c)
	}
}

// wrapInline печатает выделение (жирное, курсив, код).
//
// Пробелы выносятся наружу знаков выделения. В Confluence сплошь и рядом
// встречается «**SUZ_3 – **» — жирным выделено вместе с хвостовым пробелом.
// Markdown такое выделение не закрывает: по спецификации знак закрытия
// не может идти после пробела, и вся строка уезжает в жирный шрифт.
func wrapInline(b *strings.Builder, n *node, c ctx, mark string) {
	var inner strings.Builder
	kids(&inner, n, c)
	s := inner.String()
	if strings.TrimSpace(s) == "" {
		return // пустое выделение только мусорит разметку
	}
	lead := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
	tail := s[len(strings.TrimRight(s, " \t")):]
	b.WriteString(lead + mark + strings.TrimSpace(s) + mark + tail)
}

func renderLink(b *strings.Builder, n *node, c ctx) {
	href := n.attr["href"]
	label := strings.TrimSpace(text(n))
	switch {
	case href == "":
		b.WriteString(label)
	case label == "" || label == href:
		b.WriteString(href)
	default:
		fmt.Fprintf(b, "[%s](%s)", label, href)
	}
}

// renderMacro печатает макросы Atlassian.
//
// Свёрнутые блоки раскрываются всегда. Это решение, а не мелочь: в них лежит
// то, что автор счёл громоздким, — обычно как раз данные, ради которых
// страницу и читают.
func renderMacro(b *strings.Builder, n *node, c ctx) {
	switch n.name {
	case "structured-macro":
		switch n.attr["ac:name"] {
		case "code", "noformat":
			lang := macroParam(n, "language")
			body := strings.Trim(plainBody(n), "\n")
			b.WriteString("\n\n```" + lang + "\n" + body + "\n```\n\n")
		case "expand", "collapse", "info", "note", "warning", "tip", "panel":
			title := macroParam(n, "title")
			if title == "" {
				title = macroTitle(n.attr["ac:name"])
			}
			if title != "" {
				fmt.Fprintf(b, "\n\n**%s**\n", title)
			}
			for _, k := range n.kids {
				if k.name == "rich-text-body" {
					kids(b, k, c)
				}
			}
			b.WriteString("\n")
		case "toc":
			// Оглавление Confluence строит сам; в markdown оно бессмысленно.
		default:
			// Неизвестный макрос: печатаем то, что внутри, и помечаем — иначе
			// содержимое пропало бы молча, а это худший исход.
			body := strings.TrimSpace(text(n))
			if body != "" {
				fmt.Fprintf(b, "\n\n_[макрос %s]_ %s\n", n.attr["ac:name"], body)
			}
		}
	case "image":
		renderImage(b, n)
	case "link":
		renderMacroLink(b, n)
	case "plain-text-body", "rich-text-body", "parameter":
		// Печатаются владельцем макроса.
	default:
		kids(b, n, c)
	}
}

func renderImage(b *strings.Builder, n *node) {
	for _, k := range n.kids {
		if k.space != "ri" {
			continue
		}
		if f := k.attr["ri:filename"]; f != "" {
			fmt.Fprintf(b, "\n\n![%s](%s)\n\n", f, f)
			return
		}
		if u := k.attr["ri:value"]; u != "" {
			fmt.Fprintf(b, "\n\n![](%s)\n\n", u)
			return
		}
	}
}

func renderMacroLink(b *strings.Builder, n *node) {
	label, target := "", ""
	for _, k := range n.kids {
		switch {
		case k.space == "ri" && k.attr["ri:content-title"] != "":
			target = k.attr["ri:content-title"]
		case k.space == "ri" && k.attr["ri:filename"] != "":
			target = k.attr["ri:filename"]
		case k.space == "ac" && k.name == "link-body", k.name == "plain-text-link-body":
			label = strings.TrimSpace(text(k))
		}
	}
	if label == "" {
		label = target
	}
	if target == "" {
		b.WriteString(label)
		return
	}
	// Ссылка внутрь Confluence остаётся названием страницы: куда она приведёт
	// в репозитории, знает только тот, кто раскладывает файлы.
	fmt.Fprintf(b, "[%s](%s)", label, target)
}

func macroParam(n *node, name string) string {
	for _, k := range n.kids {
		if k.space == "ac" && k.name == "parameter" && k.attr["ac:name"] == name {
			return strings.TrimSpace(text(k))
		}
	}
	return ""
}

func plainBody(n *node) string {
	for _, k := range n.kids {
		if k.space == "ac" && k.name == "plain-text-body" {
			return text(k)
		}
	}
	return text(n)
}

func macroTitle(name string) string {
	switch name {
	case "expand", "collapse":
		return "Развёрнутый блок"
	case "info":
		return "Справка"
	case "note":
		return "Заметка"
	case "warning":
		return "Внимание"
	case "tip":
		return "Совет"
	}
	return ""
}

// text собирает весь текст поддерева, без разметки.
func text(n *node) string {
	if n.text != "" {
		return n.text
	}
	var b strings.Builder
	for _, k := range n.kids {
		b.WriteString(text(k))
	}
	return b.String()
}
