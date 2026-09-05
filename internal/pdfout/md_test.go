package pdfout

import (
	"strings"
	"testing"

	east "github.com/yuin/goldmark/extension/ast"
)

// Разбор разметки проверяется без единой строчки PDF: это чистое
// преобразование текста в список блоков.

// textOf склеивает текст всех кусков блока.
func textOf(b blockNode) string {
	var sb strings.Builder
	for _, r := range b.runs {
		sb.WriteString(r.text)
	}
	return strings.TrimSpace(sb.String())
}

// findKind возвращает блоки указанного вида.
func findKind(bs []blockNode, k blockKind) []blockNode {
	var out []blockNode
	for _, b := range bs {
		if b.kind == k {
			out = append(out, b)
		}
	}
	return out
}

func TestParseHeadingLevels(t *testing.T) {
	bs := parseMarkdown("# Первый\n\n## Второй\n\n### Третий\n")
	hs := findKind(bs, blkHeading)
	if len(hs) != 3 {
		t.Fatalf("ожидалось три заголовка, получено %d", len(hs))
	}
	for i, want := range []struct {
		level int
		text  string
	}{{1, "Первый"}, {2, "Второй"}, {3, "Третий"}} {
		if hs[i].level != want.level || textOf(hs[i]) != want.text {
			t.Errorf("заголовок %d: уровень %d текст %q, ожидалось %d %q",
				i, hs[i].level, textOf(hs[i]), want.level, want.text)
		}
	}
}

func TestParseInlineStyles(t *testing.T) {
	bs := parseMarkdown("Обычный **жирный** и *курсив* и `код` тут.")
	if len(bs) != 1 {
		t.Fatalf("ожидался один абзац, получено %d", len(bs))
	}

	styles := map[string]runStyle{}
	for _, r := range bs[0].runs {
		styles[strings.TrimSpace(r.text)] = r.style
	}
	for text, want := range map[string]runStyle{
		"жирный": styleBold,
		"курсив": styleItalic,
		"код":    styleCode,
	} {
		if got := styles[text]; got&want == 0 {
			t.Errorf("кусок %q: начертание %d, ожидался флаг %d", text, got, want)
		}
	}
}

func TestParseNestedListsDepthAndMarkers(t *testing.T) {
	bs := parseMarkdown("- верхний\n  - вложенный\n    - глубокий\n")
	items := findKind(bs, blkItem)
	if len(items) != 3 {
		t.Fatalf("ожидалось три пункта, получено %d", len(items))
	}
	for i, want := range []struct {
		depth  int
		marker string
		text   string
	}{
		{0, "•", "верхний"},
		{1, "◦", "вложенный"},
		{2, "–", "глубокий"},
	} {
		got := items[i]
		if got.depth != want.depth || got.marker != want.marker || textOf(got) != want.text {
			t.Errorf("пункт %d: глубина %d метка %q текст %q; ожидалось %d %q %q",
				i, got.depth, got.marker, textOf(got), want.depth, want.marker, want.text)
		}
	}
}

func TestParseOrderedListNumbering(t *testing.T) {
	// Нумерация берётся из разметки: список может начинаться не с единицы.
	bs := parseMarkdown("3. третий\n4. четвёртый\n5. пятый\n")
	items := findKind(bs, blkItem)
	if len(items) != 3 {
		t.Fatalf("ожидалось три пункта, получено %d", len(items))
	}
	for i, want := range []string{"3.", "4.", "5."} {
		if items[i].marker != want {
			t.Errorf("пункт %d: метка %q, ожидалась %q", i, items[i].marker, want)
		}
	}
}

func TestParseCodeBlockVerbatim(t *testing.T) {
	src := "```go\nfunc main() {\n\tfmt.Println(\"привет\")\n}\n```"
	bs := parseMarkdown(src)
	cs := findKind(bs, blkCode)
	if len(cs) != 1 {
		t.Fatalf("ожидался один блок кода, получено %d", len(cs))
	}
	if cs[0].lang != "go" {
		t.Errorf("язык блока: %q", cs[0].lang)
	}
	want := []string{"func main() {", "\tfmt.Println(\"привет\")", "}"}
	if len(cs[0].code) != len(want) {
		t.Fatalf("строк в блоке %d, ожидалось %d: %q", len(cs[0].code), len(want), cs[0].code)
	}
	for i := range want {
		if cs[0].code[i] != want[i] {
			// Отступы в коде обязаны сохраняться дословно, иначе пример
			// на Python или YAML перестанет быть правильным.
			t.Errorf("строка %d: %q, ожидалось %q", i, cs[0].code[i], want[i])
		}
	}
}

func TestParseBlockquoteDepth(t *testing.T) {
	bs := parseMarkdown("> первый уровень\n>\n> > второй уровень\n")
	var depths []int
	for _, b := range bs {
		if b.kind == blkParagraph {
			depths = append(depths, b.quote)
		}
	}
	if len(depths) < 2 {
		t.Fatalf("ожидалось два абзаца в цитатах, получено %d", len(depths))
	}
	if depths[0] != 1 {
		t.Errorf("первый уровень цитаты: %d", depths[0])
	}
	if depths[1] != 2 {
		t.Errorf("второй уровень цитаты: %d", depths[1])
	}
}

func TestParseTableAlignments(t *testing.T) {
	src := "| левый | центр | правый |\n|:---|:---:|---:|\n| a | b | c |"
	bs := parseMarkdown(src)
	ts := findKind(bs, blkTable)
	if len(ts) != 1 {
		t.Fatalf("ожидалась одна таблица, получено %d", len(ts))
	}
	tbl := ts[0].tbl
	if len(tbl.head) != 3 {
		t.Fatalf("колонок в шапке %d", len(tbl.head))
	}
	if len(tbl.rows) != 1 || len(tbl.rows[0]) != 3 {
		t.Fatalf("строк %d", len(tbl.rows))
	}
	want := []east.Alignment{east.AlignLeft, east.AlignCenter, east.AlignRight}
	for i, w := range want {
		if alignOf(tbl.align, i) != w {
			t.Errorf("колонка %d: выравнивание %v, ожидалось %v", i, tbl.align[i], w)
		}
	}
}

func TestParseHardLineBreak(t *testing.T) {
	// Две пробела в конце строки — жёсткий перенос, он обязан дожить
	// до раскладки, иначе строки стихов или адреса слипнутся.
	bs := parseMarkdown("первая строка  \nвторая строка")
	if len(bs) != 1 {
		t.Fatalf("ожидался один абзац, получено %d", len(bs))
	}
	var hasBreak bool
	for _, r := range bs[0].runs {
		if r.brk {
			hasBreak = true
		}
	}
	if !hasBreak {
		t.Errorf("жёсткий перенос потерян: %+v", bs[0].runs)
	}
}

func TestParseSoftBreakBecomesSpace(t *testing.T) {
	// Мягкий перенос — просто пробел: строку заново разложит наш перенос
	// по словам, иначе абзац остался бы разорванным как в исходнике.
	bs := parseMarkdown("первая строка\nвторая строка")
	got := textOf(bs[0])
	if strings.Contains(got, "\n") {
		t.Errorf("мягкий перенос дожил до модели: %q", got)
	}
	if !strings.Contains(got, "строка вторая") {
		t.Errorf("строки не склеились пробелом: %q", got)
	}
}

func TestParseLinkKeepsTextAndAddress(t *testing.T) {
	bs := parseMarkdown("см. [документацию](https://go.dev/doc) тут")
	got := textOf(bs[0])
	if !strings.Contains(got, "документацию") {
		t.Errorf("текст ссылки потерян: %q", got)
	}
	if !strings.Contains(got, "https://go.dev/doc") {
		// В PDF по ссылке не кликнешь, поэтому адрес печатается рядом:
		// иначе он пропал бы бесследно.
		t.Errorf("адрес ссылки потерян: %q", got)
	}
}

func TestParseHTMLBlockBecomesCode(t *testing.T) {
	bs := parseMarkdown("<div>\n  сырой html\n</div>\n")
	cs := findKind(bs, blkCode)
	if len(cs) == 0 {
		t.Fatal("сырой HTML потерян — он должен показываться как код")
	}
	if !strings.Contains(strings.Join(cs[0].code, "\n"), "сырой html") {
		t.Errorf("содержимое HTML потеряно: %q", cs[0].code)
	}
}

func TestParseEmptyGivesNothing(t *testing.T) {
	if bs := parseMarkdown("   \n\n\t\n"); len(bs) != 0 {
		t.Errorf("пустой текст дал %d блоков", len(bs))
	}
}

func TestParseThematicBreak(t *testing.T) {
	bs := parseMarkdown("текст\n\n---\n\nещё текст")
	if len(findKind(bs, blkRule)) != 1 {
		t.Errorf("горизонтальная линия потеряна: %+v", bs)
	}
}
