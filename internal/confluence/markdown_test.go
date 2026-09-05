package confluence

import (
	"strings"
	"testing"
)

func md(t *testing.T, storage string) string {
	t.Helper()
	out, err := ToMarkdown([]byte(storage))
	if err != nil {
		t.Fatalf("перевод не удался: %v", err)
	}
	return out
}

// Блок кода доезжает побайтово вместе с CDATA: на странице про XmlConfiguration
// весь смысл в управляющем символе внутри примера, и «поправить» его нельзя.
func TestCodeMacroKeepsBytes(t *testing.T) {
	out := md(t, `<ac:structured-macro ac:name="code">`+
		`<ac:parameter ac:name="language">xml</ac:parameter>`+
		`<ac:plain-text-body><![CDATA[<x>0104&#x1D;93</x>]]></ac:plain-text-body>`+
		`</ac:structured-macro>`)
	if !strings.Contains(out, "```xml") {
		t.Errorf("язык блока кода потерян: %q", out)
	}
	if !strings.Contains(out, "<x>0104&#x1D;93</x>") {
		t.Errorf("содержимое блока изменилось: %q", out)
	}
}

// Свёрнутый блок раскрывается: в нём лежит то, ради чего страницу и читают.
func TestExpandIsUnfolded(t *testing.T) {
	out := md(t, `<p>до</p><ac:structured-macro ac:name="expand">`+
		`<ac:parameter ac:name="title">Показать справочник</ac:parameter>`+
		`<ac:rich-text-body><p>внутри свёрнутого</p></ac:rich-text-body>`+
		`</ac:structured-macro>`)
	if !strings.Contains(out, "внутри свёрнутого") {
		t.Errorf("содержимое свёрнутого блока потеряно: %q", out)
	}
	if !strings.Contains(out, "Показать справочник") {
		t.Errorf("заголовок свёрнутого блока потерян: %q", out)
	}
}

// Таблица переводится в markdown, а многострочная ячейка склеивается
// в одну строку: данные важнее вёрстки.
func TestTableFlattensCells(t *testing.T) {
	out := md(t, `<table><tbody>`+
		`<tr><th>Поле</th><th>Значения</th></tr>`+
		`<tr><td>Proxy</td><td><p>Объект вида:</p><ul><li>Host</li><li>Password</li></ul></td></tr>`+
		`</tbody></table>`)
	if !strings.Contains(out, "| Поле | Значения |") {
		t.Errorf("шапка таблицы не собралась: %q", out)
	}
	if !strings.Contains(out, "| --- | --- |") {
		t.Errorf("разделитель таблицы не выставлен: %q", out)
	}
	row := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "| Proxy") {
			row = l
		}
	}
	if row == "" {
		t.Fatalf("строка таблицы потеряна: %q", out)
	}
	for _, want := range []string{"Объект вида:", "Host", "Password"} {
		if !strings.Contains(row, want) {
			t.Errorf("из ячейки пропало %q: %s", want, row)
		}
	}
	if strings.Count(row, "|") != 3 {
		t.Errorf("ячейка разъехалась по столбцам: %s", row)
	}
}

// Вертикальная черта внутри ячейки экранируется, иначе таблица разъезжается.
func TestTableEscapesPipe(t *testing.T) {
	out := md(t, `<table><tbody><tr><td>a|b</td><td>c</td></tr></tbody></table>`)
	if !strings.Contains(out, `a\|b`) {
		t.Errorf("черта в ячейке не экранирована: %q", out)
	}
}

// Пробелы выносятся наружу знаков выделения: «**текст **» markdown не закроет.
func TestEmphasisTrimsSpaces(t *testing.T) {
	out := md(t, `<p><strong>SUZ_3 – </strong>взаимодействие</p>`)
	if strings.Contains(out, "– **") {
		t.Errorf("пробел остался внутри выделения: %q", out)
	}
	if !strings.Contains(out, "**SUZ_3 –**") {
		t.Errorf("выделение потеряно: %q", out)
	}
}

// Ссылка, у которой подпись совпадает с адресом, печатается голым адресом:
// иначе внутри примера JSON появляется markdown-ссылка и ломает пример.
func TestLinkWithSameLabelStaysPlain(t *testing.T) {
	out := md(t, `<p>"Host": "<a href="https://suz.example/">https://suz.example/</a>"</p>`)
	if strings.Contains(out, "](") {
		t.Errorf("адрес превращён в ссылку внутри примера: %q", out)
	}
	if !strings.Contains(out, "https://suz.example/") {
		t.Errorf("адрес потерян: %q", out)
	}
}

// Заголовки, списки и картинки переводятся узнаваемо.
func TestBasicBlocks(t *testing.T) {
	out := md(t, `<h2>Описание полей</h2><ul><li>раз</li><li>два</li></ul>`+
		`<ac:image><ri:attachment ri:filename="shot.png"/></ac:image>`)
	for _, want := range []string{"## Описание полей", "- раз", "- два", "![shot.png](shot.png)"} {
		if !strings.Contains(out, want) {
			t.Errorf("не нашлось %q в: %q", want, out)
		}
	}
}

// Неизвестный макрос не проглатывается молча: его содержимое печатается
// с пометкой. Тихая пропажа куска страницы — худший исход из возможных.
func TestUnknownMacroKeepsBody(t *testing.T) {
	out := md(t, `<ac:structured-macro ac:name="jira"><ac:rich-text-body>`+
		`<p>задача PROJ-42</p></ac:rich-text-body></ac:structured-macro>`)
	if !strings.Contains(out, "PROJ-42") {
		t.Errorf("содержимое неизвестного макроса потеряно: %q", out)
	}
	if !strings.Contains(out, "макрос jira") {
		t.Errorf("макрос не помечен: %q", out)
	}
}
