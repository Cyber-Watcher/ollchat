// Package confluence — чтение страниц Confluence и перевод их в markdown.
//
// Зачем свой разбор. Confluence отдаёт не HTML, а storage format: XHTML
// с макросами Atlassian. Снять теги регулярками недостаточно — в макросах
// живёт самое ценное. Разведка на живом пространстве 25.08.2026 показала:
// актуальный вид справочника лежал внутри свёрнутого блока `expand`, JSON
// примеров — в `code` с CDATA, а описание полей — таблицей, где ячейка
// содержит вложенный объект на пять строк. Разбор, который этого не знает,
// вынесет в файл описание без данных.
package confluence

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// node — узел разобранного дерева.
//
// Дерево, а не потоковый разбор: таблицы нельзя перевести в markdown,
// не собрав строку целиком, а вложенные списки — не зная глубины.
type node struct {
	name  string // локальное имя тега; пусто у текста
	space string // пространство имён: ac, ri или пусто
	attr  map[string]string
	text  string
	kids  []*node
}

// parse разбирает storage format в дерево.
//
// Разбор нестрогий (`Strict = false`, `AutoClose = HTMLAutoClose`): storage
// format порождает сам Confluence и он правилен, но встречаются одиночные
// теги вроде <br> и сущности HTML, на которых строгий разбор споткнётся.
func parse(data []byte) (*node, error) {
	d := xml.NewDecoder(strings.NewReader(xmlRoot(string(data))))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = entities

	root := &node{name: "root"}
	stack := []*node{root}
	for {
		tok, err := d.Token()
		if err != nil {
			break // до конца или до места, дальше которого не разобрать
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name.Local, space: nsOf(t.Name.Space), attr: map[string]string{}}
			for _, a := range t.Attr {
				key := a.Name.Local
				if ns := nsOf(a.Name.Space); ns != "" {
					key = ns + ":" + key
				}
				n.attr[key] = a.Value
			}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			s := string(t)
			if strings.TrimSpace(s) == "" {
				s = " " // пробел между словами терять нельзя
			}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, &node{text: s})
		}
	}
	if len(root.kids) == 0 {
		return nil, fmt.Errorf("страница пуста или не разобралась")
	}
	return root, nil
}

// wrap оборачивает содержимое в корень с объявлением пространств имён:
// storage format приходит куском без них, и разбор иначе спотыкается
// на первом же `ac:structured-macro`.
// xmlRoot оборачивает разметку страницы корневым элементом с пространствами
// имён Confluence: без него разборщик XML споткнётся на ac: и ri:.
func xmlRoot(s string) string {
	return `<root xmlns:ac="ac" xmlns:ri="ri">` + s + `</root>`
}

func nsOf(space string) string {
	switch space {
	case "ac", "ri":
		return space
	}
	return ""
}

// entities — сущности HTML, которых нет в XML. Список короткий намеренно:
// сюда попадает то, что встречается в живых страницах, а не весь HTML5.
var entities = map[string]string{
	"nbsp": " ", "mdash": "—", "ndash": "–", "laquo": "«", "raquo": "»",
	"hellip": "…", "rsquo": "’", "lsquo": "‘", "ldquo": "“", "rdquo": "”",
	"deg": "°", "times": "×", "middot": "·", "bull": "•", "copy": "©",
	"reg": "®", "trade": "™", "rarr": "→", "larr": "←", "shy": "",
}
