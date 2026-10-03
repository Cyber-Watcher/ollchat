package redact

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/pdf"
)

// Все имена, номера и адреса здесь выдуманы: настоящие персональные данные
// в отслеживаемые файлы не попадают никогда, даже в тесты.

// doc — строки распознавания: слова через пробел, у каждого слова своя рамка
// по порядку. Уверенность у всех высокая — это печатный текст.
func doc(page int, lines ...string) []Word {
	var out []Word
	for ln, text := range lines {
		x := 50.0
		for _, f := range strings.Fields(text) {
			w := float64(len(f)) * 5
			out = append(out, Word{Page: page, Block: 1, Par: 1, Line: ln + 1, Text: f, Conf: 95,
				Box: Rect{x, 50 + float64(ln)*12, x + w, 58 + float64(ln)*12}})
			x += w + 4
		}
	}
	return out
}

func detect(words []Word, opt Options) []line {
	lines := buildLines(words)
	byLabels(words, lines)
	byCells(words, lines)
	byPatterns(words, lines)
	byNameDate(words, lines, 2026)
	byRepeat(words, byHints(words, lines, opt))
	byUnreadable(words, lines)
	return lines
}

func kindOf(t *testing.T, words []Word, text string) Kind {
	t.Helper()
	for _, w := range words {
		if w.Text == text {
			return w.Kind
		}
	}
	t.Fatalf("слова %q нет", text)
	return KindNone
}

func TestDetectFieldsAndRoles(t *testing.T) {
	words := doc(0,
		"Patient Name: DOE, JANE Q Exam: CT ABDOMEN",
		"MRN: AB1234567",
		"DOB: 01 Jan 1970",
		"Referring Provider: Rick Roe, Provider",
		"Phone (555) 010-0199 email jane.doe@example.com",
		"12 Example Ln Springfield, ZZ 00000",
		"Patient; DOE, JANE Q DOB: 01/01/1970 Exam Date: 9/29/2026 Acc No: XY7654321",
		"Electronically signed by: John Roe MD 01/02/2026 01:02 PM Workstation: WSX000001",
		"Reported: 02 Jan 2026 13:02 Roe, John, MD",
		"The findings were discussed with DOE by phone.",
	)
	detect(words, Options{})

	want := map[string]Kind{
		"DOE,": KindClient, "JANE": KindClient, "Q": KindClient,
		"AB1234567": KindID, "XY7654321": KindID, "WSX000001": KindID,
		"01/01/1970": KindBirth, "1970": KindBirth,
		"(555)": KindPhone, "jane.doe@example.com": KindEmail,
		"Example": KindAddress, "Springfield,": KindAddress, "00000": KindAddress,
		"John": KindDoctor, "Rick": KindDoctor,
		// не персональное: дата исследования и сам текст
		"9/29/2026": KindNone, "CT": KindNone, "findings": KindNone,
		// повтор: фамилия в свободном тексте, где подписи поля нет
		"DOE": KindClient,
	}
	for text, k := range want {
		if got := kindOf(t, words, text); got != k {
			t.Errorf("%q: %v, а должно быть %v", text, got, k)
		}
	}
	// Подпись поля с убранным значением уходит из .md вместе с ним, а подпись
	// имени — нет: на её месте встанет CLIENT.
	for _, w := range words {
		switch w.Text {
		case "MRN:":
			if w.LabelOf != KindID {
				t.Errorf("подпись MRN: не помечена как подпись убранного номера")
			}
		case "Name:":
			if w.LabelOf != KindNone {
				t.Errorf("подпись имени помечена к удалению")
			}
		}
	}
}

func TestNameStopsAtDigitsAndLimit(t *testing.T) {
	words := doc(0, "Signed by: John Roe MD 01/02/2026 01:02 PM")
	detect(words, Options{})
	if k := kindOf(t, words, "01/02/2026"); k != KindNone {
		t.Errorf("дата подписи стала %v: имя должно кончаться на первом слове с цифрой", k)
	}
	if k := kindOf(t, words, "Roe"); k != KindDoctor {
		t.Errorf("Roe: %v", k)
	}
}

func TestHintsFromModel(t *testing.T) {
	// Строка для hide подобрана так, чтобы её не ловило ни одно правило:
	// проверяется сама подсказка, а не шаблоны.
	words := doc(0, "Seen today by Anna Smirnova at Blue Door Partners.",
		"Smirnova recommends follow-up.")
	detect(words, Options{Doctors: []string{"Anna Smirnova"}, Hide: []string{"Blue Door Partners"}})
	if k := kindOf(t, words, "Anna"); k != KindDoctor {
		t.Errorf("Anna: %v", k)
	}
	if k := kindOf(t, words, "Smirnova"); k != KindDoctor {
		t.Errorf("Smirnova во второй строке: %v", k)
	}
	if k := kindOf(t, words, "Partners."); k != KindOther {
		t.Errorf("Partners.: %v", k)
	}
}

func TestRussianFields(t *testing.T) {
	words := doc(0, "Пациент: Иванова Мария Петровна", "Дата рождения: 01.01.1970",
		"Полис ОМС: 1234567890123456", "Лечащий врач: Петров П.П.", "Телефон: +7 (900) 000-00-00")
	detect(words, Options{})
	for text, k := range map[string]Kind{
		"Иванова": KindClient, "01.01.1970": KindBirth, "1234567890123456": KindID,
		"Петров": KindDoctor, "+7": KindPhone,
	} {
		if got := kindOf(t, words, text); got != k {
			t.Errorf("%q: %v, а должно быть %v", text, got, k)
		}
	}
}

// Свободный русский текст без подписей полей: ФИО по отчеству в падеже,
// адрес с улицей в падеже, дата рождения оборотом. Все значения выдуманы.
func TestRussianFreeText(t *testing.T) {
	words := doc(0,
		"Справка выдана гражданину Сидорову Ивану Кузьмичу, проживающему на",
		"Вишнёвой улице, дом 3, квартира 12. Наблюдается у доктора Орловой.",
		"Прописана: г. Примерск, ул. Полевая, д. 4, кв. 8",
		"Пациентка 1980 года рождения, со слов родилась 5 июня 1980 г.",
		"Born: 1970-01-31 Exam: 2026-09-01",
		"Полис ДМС: ЖЖ-0000001. Услуги оказал кардиолог.",
	)
	detect(words, Options{})
	for text, k := range map[string]Kind{
		"Сидорову": KindPerson, "Ивану": KindPerson, "Кузьмичу,": KindPerson,
		"Вишнёвой": KindAddress, "дом": KindAddress, "12.": KindAddress,
		"Примерск,": KindAddress, "Полевая,": KindAddress, "8": KindAddress,
		"Орловой.": KindDoctor,
		"1980":     KindBirth, "июня": KindBirth,
		"1970-01-31": KindBirth, "2026-09-01": KindNone,
		"ЖЖ-0000001.": KindID,
		// значение номера кончается на первом слове без цифр
		"Услуги": KindNone, "кардиолог.": KindNone, "Справка": KindNone,
	} {
		if got := kindOf(t, words, text); got != k {
			t.Errorf("%q: %v, а должно быть %v", text, got, k)
		}
	}
}

// Оставшиеся имена для подсказки модели: посреди предложения и рядом с ролью;
// начало предложения, аббревиатуры и месяцы — нет. Имена выдуманы.
func TestLeftovers(t *testing.T) {
	md := "# doc\n\n_Пояснение с Заглавной._\n\n## Страница 1\n\n" +
		"Dear DOCTOR\n\nI am writing about my patient Ada CLIENT who has CT findings.\n" +
		"On Monday Ada had tests. Thank you.\n\nOriel Nash, consultant\n" +
		"Наблюдается у Громовой. Пациентка жалоб не имеет."
	got := strings.Join(Leftovers(md), " ")
	for _, want := range []string{"Ada", "Nash,", "Громовой"} {
		w := strings.TrimRight(want, ",")
		if !strings.Contains(" "+got+" ", " "+w+" ") {
			t.Errorf("нет %q в %q", w, got)
		}
	}
	for _, not := range []string{"CT", "Monday", "Thank", "Пациентка", "Заглавной", "Dear", "On"} {
		if strings.Contains(" "+got+" ", " "+not+" ") {
			t.Errorf("лишнее %q в %q", not, got)
		}
	}
}

// Слово повторного чтения встаёт в свою строку по месту, клетка без строки
// рядом — перед строкой ниже неё, а не в конец страницы.
func TestMergeReread(t *testing.T) {
	ws := []Word{
		{Block: 1, Par: 1, Line: 1, Text: "two", Box: Rect{50, 100, 70, 110}},
		{Block: 1, Par: 1, Line: 1, Text: "of", Box: Rect{150, 100, 160, 110}},
		{Block: 2, Par: 1, Line: 1, Text: "below", Box: Rect{50, 300, 80, 310}},
	}
	more := []Word{
		{Block: rereadBlock, Par: 1, Line: 1, Text: "episodes", Box: Rect{80, 100, 140, 110}},
		{Block: rereadBlock + 1, Par: 1, Line: 1, Text: "cell", Box: Rect{50, 200, 70, 210}},
		{Block: rereadBlock + 1, Par: 1, Line: 1, Text: "value", Box: Rect{75, 200, 100, 210}},
	}
	var got []string
	for _, w := range mergeReread(ws, more) {
		got = append(got, w.Text)
	}
	if s := strings.Join(got, " "); s != "two episodes of cell value below" {
		t.Errorf("порядок: %q", s)
	}
}

// Фамилия врача заглавными, как tesseract читает шапку бланка, — и её повтор
// обычным написанием в тексте. Имена выдуманы.
func TestDoctorInCaps(t *testing.T) {
	words := doc(0, "Кабинет неврологии доктора ОРЛОВОЙ", "Seen by Dr SMITHSON today.",
		"Прошу передать Орловой результат.")
	detect(words, Options{})
	for text, k := range map[string]Kind{
		"ОРЛОВОЙ": KindDoctor, "SMITHSON": KindDoctor, "Орловой": KindDoctor, "неврологии": KindNone,
	} {
		if got := kindOf(t, words, text); got != k {
			t.Errorf("%q: %v, а должно быть %v", text, got, k)
		}
	}
}

// Дата сразу после имени: давняя — дата рождения, свежая — дата визита.
func TestDateAfterName(t *testing.T) {
	words := doc(0, "Справка выдана гражданину Сидорову Ивану Кузьмичу, 01.02.1970",
		"Осмотрен Орлов Пётр Ильич, 05.09.2026, жалоб нет.")
	detect(words, Options{})
	if k := kindOf(t, words, "01.02.1970"); k != KindBirth {
		t.Errorf("давняя дата после имени: %v", k)
	}
	if k := kindOf(t, words, "05.09.2026,"); k != KindNone {
		t.Errorf("дата визита после имени: %v", k)
	}
}

// Таблица: подпись и значение — разные строки распознавания на одной высоте.
// Строка заголовков (подпись справа от подписи) значением не считается.
func TestCellLabels(t *testing.T) {
	cell := func(block int, x, y float64, text string) []Word {
		var out []Word
		for _, f := range strings.Fields(text) {
			w := float64(len(f)) * 5
			out = append(out, Word{Block: block, Par: 1, Line: 1, Text: f, Conf: 95,
				Box: Rect{x, y, x + w, y + 8}})
			x += w + 4
		}
		return out
	}
	var words []Word
	words = append(words, cell(1, 50, 100, "Date of birth")...)
	words = append(words, cell(2, 200, 100, "21 June 1901")...)
	words = append(words, cell(3, 50, 120, "Full name")...)
	words = append(words, cell(4, 200, 120, "Ada Example")...)
	words = append(words, cell(5, 50, 140, "Test")...)
	words = append(words, cell(6, 200, 140, "Fasting")...)
	detect(words, Options{})
	for text, k := range map[string]Kind{
		"June": KindBirth, "1901": KindBirth, "Ada": KindClient, "Example": KindClient,
		"Fasting": KindNone,
	} {
		if got := kindOf(t, words, text); got != k {
			t.Errorf("%q: %v, а должно быть %v", text, got, k)
		}
	}
}

// Серая страница из JPEG: декодер выравнивает строку до кратного 8
// (Stride больше ширины). До правки paint копировал Pix целиком, и каждая
// строка съезжала — страница выходила косой мешаниной, а повторное
// распознавание мешанины «утечек» не находило (синтетический набор
// 03.10.2026, все серые JPEG-страницы).
func TestPaintKeepsPaddedGray(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 101, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 101; x++ {
			src.SetGray(x, y, color.Gray{uint8((x * 7) ^ (y * 13))})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	dec, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	g, ok := dec.(*image.Gray)
	if !ok || g.Stride == g.Bounds().Dx() {
		t.Skipf("декодер дал %T со строкой %d — случай не воспроизводится", dec, g.Stride)
	}
	out := paint(Page{Width: 101, Height: 40, Image: g}, 0, nil, nil)
	for y := 0; y < 40; y++ {
		for x := 0; x < 101; x++ {
			if a, b := color.GrayModel.Convert(out.At(x, y)), g.At(x, y); a != b {
				t.Fatalf("точка %d,%d: %v, а в исходнике %v", x, y, a, b)
			}
		}
	}
}

func TestOrganization(t *testing.T) {
	words := doc(0, "NORTHWIND RADIOLOGY", "RADIOLOGY:", "Sent to Contoso Medical Center today")
	detect(words, Options{})
	if k := kindOf(t, words, "NORTHWIND"); k != KindOrg {
		t.Errorf("NORTHWIND: %v", k)
	}
	if k := kindOf(t, words, "Contoso"); k != KindOrg {
		t.Errorf("Contoso: %v", k)
	}
	if k := kindOf(t, words, "today"); k != KindNone {
		t.Errorf("today: %v", k)
	}
	if k := kindOf(t, words, "RADIOLOGY:"); k != KindNone {
		t.Errorf("заголовок раздела «RADIOLOGY:» стал %v", k)
	}
}

func TestMarkdownHidesEverything(t *testing.T) {
	words := doc(0,
		"Patient Name: DOE, JANE",
		"MRN: AB1234567",
		"FINDINGS:",
		"No significant abnormality. Discussed with DOE.",
		"Electronically signed by: John Roe MD 01/02/2026 Workstation: WSX000001",
		"12 Example Ln Springfield, ZZ 00000",
	)
	lines := detect(words, Options{})
	md := buildMD("образец", words, lines, nil, 1)
	for _, bad := range []string{"DOE", "JANE", "AB1234567", "MRN", "John", "Roe", "WSX000001",
		"Workstation", "Example", "Springfield", "00000"} {
		if strings.Contains(md, bad) {
			t.Errorf("в .md осталось %q:\n%s", bad, md)
		}
	}
	for _, good := range []string{"Patient Name: CLIENT", "### FINDINGS", "Discussed with CLIENT",
		"signed by: DOCTOR 01/02/2026", "No significant abnormality."} {
		if !strings.Contains(md, good) {
			t.Errorf("в .md нет %q:\n%s", good, md)
		}
	}
}

func TestMarkdownDropsWordsUnderInk(t *testing.T) {
	words := doc(0, "Requested measurements review")
	lines := detect(words, Options{})
	ink := []Region{{Page: 0, Box: Rect{0, 40, 600, 70}}}
	md := buildMD("образец", words, lines, ink, 1)
	if strings.Contains(md, "measurements") {
		t.Errorf("слово под областью чернил попало в .md:\n%s", md)
	}
	if !strings.Contains(md, "областей без распознанного текста") {
		t.Errorf("о скрытых областях не сказано:\n%s", md)
	}
}

func TestUnreadableLine(t *testing.T) {
	words := doc(0, "bgt ol fUL4S")
	for i := range words {
		words[i].Conf = 30
	}
	detect(words, Options{})
	for _, w := range words {
		if w.Kind != KindHidden {
			t.Errorf("%q в неразборчивой строке: %v", w.Text, w.Kind)
		}
	}
}

func TestNear(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"SMIRNOVO", "SMIRNOVA", true}, {"SMIRNOV", "SMIRNOVA", true}, {"XSMIRNOVA", "SMIRNOVA", true},
		{"SMIRN", "SMIRNOVA", false}, {"OMIRNOVO", "SMIRNOVA", false}, {"ABC", "ABC", true},
	} {
		if got := near(c.a, c.b); got != c.want {
			t.Errorf("near(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestParseTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t2550\t3300\t-1\t\n" +
		"5\t1\t2\t1\t3\t1\t300\t600\t150\t30\t91.5\tMRN:\n" +
		"5\t1\t2\t1\t3\t2\t\t\t\t\t\t \n"
	words := parseTSV(tsv, 0, 300.0/72)
	if len(words) != 1 {
		t.Fatalf("слов %d, а должно быть 1: %+v", len(words), words)
	}
	w := words[0]
	if w.Text != "MRN:" || w.Block != 2 || w.Line != 3 || w.Conf != 91.5 {
		t.Errorf("слово разобрано неверно: %+v", w)
	}
	if math.Abs(w.Box.X0-72) > 1e-9 || math.Abs(w.Box.Y0-144) > 1e-9 || math.Abs(w.Box.X1-108) > 1e-9 {
		t.Errorf("рамка в точках страницы неверна: %+v", w.Box)
	}
}

// page — белая страница Letter с нарисованным тёмным пятном в точках rect.
func page(dpi float64, rect Rect) Page {
	k := dpi / 72
	img := image.NewGray(image.Rect(0, 0, int(612*k), int(792*k)))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := int(rect.Y0 * k); y < int(rect.Y1*k); y++ {
		for x := int(rect.X0 * k); x < int(rect.X1*k); x++ {
			if (x+y)%3 != 0 { // штрихи с просветами, как у почерка
				img.SetGray(x, y, color.Gray{20})
			}
		}
	}
	return Page{Width: 612, Height: 792, Image: img}
}

func TestInkFindsUnrecognizedMarks(t *testing.T) {
	scribble := Rect{400, 500, 480, 530}
	got := inkRegions(page(200, scribble), 0, nil)
	if len(got) != 1 {
		t.Fatalf("областей %d, а должна быть одна: %+v", len(got), got)
	}
	if !got[0].Box.Overlaps(scribble) {
		t.Errorf("область %+v мимо пятна %+v", got[0].Box, scribble)
	}

	// То же пятно под уверенно прочитанным словом — это печатный текст.
	word := Word{Page: 0, Text: "Печать", Conf: 95, Box: scribble}
	if got := inkRegions(page(200, scribble), 0, []Word{word}); len(got) != 0 {
		t.Errorf("прочитанное слово принято за чернила: %+v", got)
	}

	// Росчерк, который tesseract прочёл «словом» из двух знаков: по рамке
	// это не буквы, и пятно под ним — чернила.
	wide := Rect{300, 600, 420, 630}
	fake := Word{Page: 0, Text: `\Ш`, Conf: 64, Box: wide}
	if got := inkRegions(page(200, wide), 0, []Word{fake}); len(got) != 1 {
		t.Errorf("росчерк под «словом» %q не найден: %+v", fake.Text, got)
	}

	// Тонкая линейка — не чернила.
	if got := inkRegions(page(200, Rect{50, 300, 560, 302}), 0, nil); len(got) != 0 {
		t.Errorf("линейка принята за чернила: %+v", got)
	}
}

// Сетка таблицы — не чернила, а почерк поверх таблицы — чернила. До правки
// сетка связывала все клетки в одну область, и таблица уходила под чёрное
// целиком вместе с прочитанным в ней.
func TestInkIgnoresTableGrid(t *testing.T) {
	const dpi = 300
	k := float64(dpi) / 72
	p := page(dpi, Rect{})
	img := p.Image.(*image.Gray)
	line := func(r Rect) {
		for y := int(r.Y0 * k); y < int(r.Y1*k); y++ {
			for x := int(r.X0 * k); x < int(r.X1*k); x++ {
				img.SetGray(x, y, color.Gray{0})
			}
		}
	}
	for y := 200.0; y <= 400; y += 25 { // строки таблицы, линии 0,8 pt
		line(Rect{60, y, 550, y + 0.8})
	}
	for _, x := range []float64{60, 250, 400, 550} { // столбцы
		line(Rect{x, 200, x + 0.8, 400.8})
	}
	if got := inkRegions(p, 0, nil); len(got) != 0 {
		t.Fatalf("сетка таблицы принята за чернила: %+v", got)
	}

	scribble := Rect{300, 300, 360, 330}
	pp := page(dpi, scribble)
	img = pp.Image.(*image.Gray)
	for y := 200.0; y <= 400; y += 25 {
		line(Rect{60, y, 550, y + 0.8})
	}
	got := inkRegions(pp, 0, nil)
	if len(got) != 1 || !got[0].Box.Overlaps(scribble) {
		t.Errorf("пятно поверх таблицы: %+v, ждали одну область на %+v", got, scribble)
	}
}

// Итоговый PDF: того же размера, без текста и сведений о документе,
// а замазанное и вправду чёрное — проверено обратным чтением своим разбором.
// Серая страница с полутонами уходит в JPEG (в PNG раздувалась вчетверо),
// чёрно-белая — в PNG; обе читаются обратно своим разбором.
func TestPDFGrayEncoding(t *testing.T) {
	half := image.NewGray(image.Rect(0, 0, 850, 1100))
	for i := range half.Pix {
		half.Pix[i] = uint8(200 + (i*7919)%40) // шум бумаги серого скана
	}
	bw := page(100, Rect{100, 100, 200, 130})
	for i, v := range bw.Image.(*image.Gray).Pix { // как у CCITT: только 0 и 255
		if v < 128 {
			bw.Image.(*image.Gray).Pix[i] = 0
		}
	}
	pages := []Page{{Width: 612, Height: 792, Image: half}, bw}
	data, err := PDF(pages, []image.Image{half, bw.Image})
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte("/DCTDecode")); n != 1 {
		t.Errorf("JPEG-страниц %d, ждали одну (серую с полутонами)", n)
	}
	back, err := pdf.ScanPages(data)
	if err != nil || len(back) != 2 || back[0].Image == nil || back[1].Image == nil {
		t.Fatalf("обратное чтение: %v, страниц %d", err, len(back))
	}
}

func TestPDFRoundTrip(t *testing.T) {
	gray := page(300, Rect{})
	rgb := image.NewRGBA(image.Rect(0, 0, 1700, 2200))
	for i := range rgb.Pix {
		rgb.Pix[i] = 255
	}
	pages := []Page{gray, {Width: 612, Height: 792, Image: rgb}}
	words := []Word{{Page: 0, Text: "AB1234567", Kind: KindID, Box: Rect{100, 100, 200, 112}}}
	red := []image.Image{paint(pages[0], 0, words, nil), paint(pages[1], 1, nil, nil)}

	data, err := PDF(pages, red)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("/Info")) || bytes.Contains(data, []byte("/Producer")) {
		t.Error("в PDF есть сведения о документе")
	}
	back, err := pdf.ScanPages(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("страниц %d", len(back))
	}
	for i, p := range back {
		if p.Image == nil || p.Width != 612 || p.Height != 792 {
			t.Fatalf("страница %d: %.0f×%.0f, картинка %v (%s)", i+1, p.Width, p.Height, p.Image != nil, p.Note)
		}
	}
	img := back[0].Image
	k := float64(img.Bounds().Dx()) / 612
	if r, _, _, _ := img.At(int(150*k), int(106*k)).RGBA(); r != 0 {
		t.Errorf("середина замазанного не чёрная: %d", r>>8)
	}
	if r, _, _, _ := img.At(int(300*k), int(400*k)).RGBA(); r>>8 != 255 {
		t.Errorf("незамазанное поле не белое: %d", r>>8)
	}
}
