package redact

import (
	"strings"
	"testing"
)

// Все имена и номера здесь выдуманы.

// cell — слово в заданном месте страницы и в заданном блоке tesseract.
func cell(block int, x, y float64, text string) []Word {
	var out []Word
	for _, f := range strings.Fields(text) {
		w := float64(len([]rune(f))) * 5
		out = append(out, Word{Page: 0, Block: block, Par: 1, Line: 1, Text: f, Conf: 95,
			Box: Rect{x, y, x + w, y + 8}})
		x += w + 3
	}
	return out
}

// labTable — таблица без линеек, как в лабораторном бланке: каждый столбец
// tesseract отдал отдельным блоком, и прежде в .md выходили три столбика.
func labTable() []Word {
	var words []Word
	rows := [][3]string{
		{"NAME", "VALUE", "REFERENCE RANGE"},
		{"GLUCOSE", "212 H", "65-99 (mg/dL)"},
		{"SODIUM", "136", "135-146 (mmol/L)"},
	}
	for i, r := range rows {
		y := 100 + float64(i)*18
		words = append(words, cell(1+i, 40, y, r[0])...)
	}
	for i, r := range rows {
		y := 100 + float64(i)*18
		words = append(words, cell(10+i, 200, y, r[1])...)
	}
	for i, r := range rows {
		y := 100 + float64(i)*18
		words = append(words, cell(20+i, 320, y, r[2])...)
	}
	return words
}

func TestMarkdownRebuildsTable(t *testing.T) {
	words := labTable()
	md := buildMD("образец", words, nil, 1, false)
	for _, want := range []string{
		"| NAME | VALUE | REFERENCE RANGE |",
		"| --- | --- | --- |",
		"| GLUCOSE | 212 H | 65-99 (mg/dL) |",
		"| SODIUM | 136 | 135-146 (mmol/L) |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в .md нет строки таблицы %q:\n%s", want, md)
		}
	}
}

// Строка шапки с другими столбцами не слипается с полями под ней: у соседних
// строк таблицы должно быть хотя бы два общих левых края.
func TestTableSplitsOnOtherColumns(t *testing.T) {
	var words []Word
	words = append(words, cell(1, 30, 80, "H: 555-0100")...)
	words = append(words, cell(2, 470, 80, "FINAL RESULT")...)
	words = append(words, cell(3, 30, 100, "Order Date: 01/02/2026")...)
	words = append(words, cell(4, 300, 100, "Received: 01/02/2026")...)
	words = append(words, cell(3, 30, 118, "Collection Date: 01/02/2026")...)
	words = append(words, cell(4, 300, 118, "Report: 01/03/2026")...)
	md := buildMD("образец", words, nil, 1, true)
	if !strings.Contains(md, "| Order Date: 01/02/2026 | Received: 01/02/2026 |") {
		t.Errorf("поля не стали таблицей в два столбца:\n%s", md)
	}
	if strings.Contains(md, "| H: 555-0100") {
		t.Errorf("строка шапки слиплась с таблицей полей:\n%s", md)
	}
}

// Флаг у края стоит к названию то дальше порога клетки, то ближе: во второй
// строке «F PROTEIN» распознаётся одной клеткой, но слова всё равно
// расходятся по своим столбцам.
func TestFlagColumnSplitsWords(t *testing.T) {
	var words []Word
	words = append(words, cell(1, 20, 100, "F")...)
	words = append(words, cell(2, 45, 100, "CREATININE")...)
	words = append(words, cell(3, 200, 100, "0.40 L")...)
	words = append(words, cell(1, 30, 118, "F")...)
	words = append(words, cell(2, 45, 118, "PROTEIN")...)
	words = append(words, cell(3, 200, 118, "6.3")...)
	md := buildMD("образец", words, nil, 1, true)
	for _, want := range []string{"| F | CREATININE | 0.40 L |", "| F | PROTEIN | 6.3 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("в .md нет %q:\n%s", want, md)
		}
	}
}

// Пунктир линейки tesseract читает «словами» с нулевой уверенностью
// и высотой в пару точек: в .md их нет, а неуверенное слово обычной высоты
// остаётся — это может быть и настоящий текст.
func TestJunkWordsDropped(t *testing.T) {
	words := doc(0, "Order Date: 09/09/2026", "Collection Date: 09/09/2026")
	words = append(words,
		Word{Page: 0, Block: 9, Par: 1, Line: 1, Text: "kAR", Conf: 4, Box: Rect{300, 52, 320, 53}},
		Word{Page: 0, Block: 9, Par: 1, Line: 1, Text: "Failsre", Conf: 25, Box: Rect{50, 80, 85, 88}})
	// Пунктир рамки: строка «слов» обычной высоты, но почти без уверенности.
	for i, t := range []string{"A", "A", "R", "SRR", "AL"} {
		x := 50 + float64(i)*20
		words = append(words, Word{Page: 0, Block: 10, Par: 1, Line: 1, Text: t, Conf: 7, Box: Rect{x, 110, x + 10, 118}})
	}
	md := buildMD("образец", words, nil, 1, true)
	if strings.Contains(md, "kAR") || strings.Contains(md, "SRR") {
		t.Errorf("мусор линейки попал в .md:\n%s", md)
	}
	if !strings.Contains(md, "Failsre") {
		t.Errorf("неуверенное слово обычной высоты пропало:\n%s", md)
	}
}

// В обезличенном .md имя в клетке становится ролью, номер пропадает;
// в распознанном всё остаётся как есть.
func TestTableRedactedAndPlain(t *testing.T) {
	var words []Word
	words = append(words, cell(1, 40, 100, "Patient")...)
	words = append(words, cell(2, 200, 100, "Jane Example")...)
	words = append(words, cell(1, 40, 118, "Member")...)
	words = append(words, cell(2, 200, 118, "ZX0001234")...)
	words = append(words, cell(1, 40, 136, "Glucose")...)
	words = append(words, cell(2, 200, 136, "98")...)
	for i := range words {
		switch words[i].Text {
		case "Jane", "Example":
			words[i].Kind = KindClient
		case "ZX0001234":
			words[i].Kind = KindID
		}
	}
	red := buildMD("образец", words, nil, 1, false)
	for _, want := range []string{"| Patient | CLIENT |", "| Glucose | 98 |"} {
		if !strings.Contains(red, want) {
			t.Errorf("в обезличенном .md нет %q:\n%s", want, red)
		}
	}
	for _, bad := range []string{"Jane", "Example", "ZX0001234"} {
		if strings.Contains(red, bad) {
			t.Errorf("в обезличенном .md осталось %q:\n%s", bad, red)
		}
	}
	plain := buildMD("образец", words, nil, 1, true)
	for _, want := range []string{"| Patient | Jane Example |", "| Member | ZX0001234 |", "is NOT hidden"} {
		if !strings.Contains(plain, want) {
			t.Errorf("в распознанном .md нет %q:\n%s", want, plain)
		}
	}
}

// Служебные пометки — на языке документа: «Page» и английские пояснения
// у английского, «Страница» и русские — у русского (слово владельца
// 06.10.2026). Это касается обоих .md, а значит, и текстового PDF.
func TestNotesFollowDocumentLanguage(t *testing.T) {
	en := doc(0, "No significant abnormality is seen in the examined region.")
	ru := doc(0, "Патологических изменений в исследованной области не выявлено.")
	ink := []Region{{Page: 0, Box: Rect{0, 500, 600, 560}}}
	for _, plain := range []bool{false, true} {
		md := buildMD("образец", en, ink, 1, plain)
		for _, want := range []string{"## Page 1", "_Text recognized from the image", "without recognized text on this page"} {
			if !strings.Contains(md, want) {
				t.Errorf("английский документ (plain=%v): нет %q:\n%s", plain, want, md)
			}
		}
		if strings.ContainsAny(strings.TrimPrefix(md, "# образец"), "абвгдеёжзийклмнопрстуфхцчшщъыьэюя") {
			t.Errorf("в английском документе русские пометки (plain=%v):\n%s", plain, md)
		}
		md = buildMD("образец", ru, ink, 1, plain)
		for _, want := range []string{"## Страница 1", "_Текст распознан с картинки", "без распознанного текста"} {
			if !strings.Contains(md, want) {
				t.Errorf("русский документ (plain=%v): нет %q:\n%s", plain, want, md)
			}
		}
	}
}

// Обычный абзац в таблицу не превращается, и строки абзаца склеиваются,
// как прежде.
func TestParagraphStaysText(t *testing.T) {
	words := doc(0, "No significant abnormality is seen in the",
		"examined region of the chest.")
	md := buildMD("образец", words, nil, 1, false)
	if strings.Contains(md, "|") {
		t.Errorf("абзац стал таблицей:\n%s", md)
	}
	if !strings.Contains(md, "seen in the examined region") {
		t.Errorf("строки абзаца не склеены:\n%s", md)
	}
}

// Имя крупной шапкой становится заголовком, и подсказка модели его всё
// равно видит: прежде строки «#» пропускались целиком.
func TestLeftoversSeeHeadingsAndCells(t *testing.T) {
	md := "# скан\n\n## Страница 1\n\n### Report for Corwin Example\n\n| Doctor | Example, Oriana |\n"
	got := strings.Join(Leftovers(md), " ")
	for _, want := range []string{"Corwin", "Oriana"} {
		if !strings.Contains(got, want) {
			t.Errorf("в подсказке нет %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Страница") {
		t.Errorf("своё служебное попало в подсказку: %q", got)
	}
}

func TestParseFormats(t *testing.T) {
	f, err := ParseFormats("")
	if err != nil || f != (Formats{true, true, true, true}) {
		t.Errorf("пусто — все четыре, а вышло %+v, %v", f, err)
	}
	f, err = ParseFormats("ocr-pdf")
	if err != nil || f != (Formats{OCRPDF: true}) {
		t.Errorf("ocr-pdf прочитан как %+v, %v", f, err)
	}
	f, err = ParseFormats("PDF, md")
	if err != nil || f != (Formats{PDF: true, MD: true}) {
		t.Errorf("«PDF, md» прочитан как %+v, %v", f, err)
	}
	if _, err := ParseFormats("pdf,docx"); err == nil {
		t.Error("docx принят")
	}
	if got := Stem("/x/15-006-test .pdf"); got != "/x/15-006-test" {
		t.Errorf("Stem = %q", got)
	}
}
