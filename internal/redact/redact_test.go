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
	byNameDate(words, lines, 2026)
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

// Растровый фон — отдельные чёрные точки через две — после очистки белый,
// а штрих буквы толщиной в четыре точки остаётся чёрным.
func TestDespeckleKeepsStrokesDropsDots(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 60, 30))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < 30; y += 3 {
		for x := 0; x < 30; x += 3 {
			img.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	for y := 5; y < 25; y++ {
		for x := 40; x < 44; x++ {
			img.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	out := despeckle(img, img.Bounds())
	dots := 0
	for y := 0; y < 30; y++ {
		for x := 0; x < 30; x++ {
			if out.GrayAt(x, y).Y == 0 {
				dots++
			}
		}
	}
	if dots != 0 {
		t.Errorf("после очистки осталось точек растра: %d", dots)
	}
	if out.GrayAt(41, 15).Y != 0 || out.GrayAt(42, 15).Y != 0 {
		t.Error("штрих буквы стёрт вместе с растром")
	}
}

// Рамка повторного чтения задевает соседнее прочитанное слово: второй раз
// оно в текст не попадает, а новое слово рядом — попадает.
func TestRereadSkipsReadWords(t *testing.T) {
	ws := []Word{{Block: 1, Par: 1, Line: 1, Text: "1234", Box: Rect{50, 100, 70, 110}}}
	more := []Word{
		{Block: rereadBlock, Par: 1, Line: 1, Text: "1234", Box: Rect{51, 100, 71, 110}},
		{Block: rereadBlock, Par: 1, Line: 1, Text: "Lane", Box: Rect{75, 100, 95, 110}},
	}
	var got []string
	for _, w := range mergeReread(ws, more) {
		got = append(got, w.Text)
	}
	if s := strings.Join(got, " "); s != "1234 Lane" {
		t.Errorf("после повторного чтения: %q", s)
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

// Шапки факса и карточки приёма: подписи без двоеточия и возраст с полом
// между именем и датой рождения (образец 06.10.2026; значения выдуманы).
func TestBareLabelsAndAgeBeforeBirth(t *testing.T) {
	words := doc(0,
		"Appt. Date/Time 08/15/2026 (48yo, F) ID# 404040 DOB 03/04/1971 Service Dept.",
		"Patient: Roe, Anna, 29 Y, F 02/03/1997 Accession ID: QX123",
		"Faxed copy Acc No. 50505 DOS: 06/14/2026 for (id #606060, Type 2 diabetes",
		"The ID 12 form, No. 7 on the list.",
		"Report for Roe, Anna, 31Y,F 04/05/1995 Example Center",
		"12 Example Maple Ave, Springfield, Example County, NV, 99999",
		"PERFORMING LAB: Example Labs - 4321 Maple, other",
		"77 ELM STREET NORTH SPRINGFIELD NV 99998 Ph (555) 010-0000")
	detect(words, Options{})
	for text, want := range map[string]Kind{
		"404040": KindID, "03/04/1971": KindBirth, "02/03/1997": KindBirth,
		"50505": KindID, "#606060,": KindID, "04/05/1995": KindBirth,
		"Springfield,": KindAddress, "County,": KindAddress, "99999": KindAddress,
		"STREET": KindAddress, "SPRINGFIELD": KindAddress, "99998": KindAddress, "4321": KindAddress,
		"Service": KindNone, "08/15/2026": KindNone, "06/14/2026": KindNone, "12": KindNone, "7": KindNone,
	} {
		if k := kindOf(t, words, text); k != want {
			t.Errorf("%q: %v, а должно быть %v", text, k, want)
		}
	}
}

// Шапка крупным кеглем: имя распознано обрывками, дата рождения — без
// подписи. Обрывки склеиваются и сверяются с найденным именем, дата
// закрывается повтором найденной по подписи. Обычные слова, похожие
// на имя, не склеиваются: среди них нет обрывков в одну-две буквы.
// Русские бланки пишут подпись без двоеточия: «Дата рождения 01.02.1970»,
// «Паспорт серия 4508 № 123456», «Тел. (4822) 12-34-56», — и до 07.10.2026
// такие значения оставались в .md. Анализы, нормы, даты исследований и
// числа после похожих слов («Fax 2 pages», «кетоновых тел 0,5») остаются.
func TestRussianBareLabels(t *testing.T) {
	words := doc(0,
		"Дата рождения 01.02.1970 Д.Р. 03.04.1972 д.р. 05.06.1973",
		"Date of birth 07/08/1974, Петрова 1968 г.р.",
		"Паспорт серия 4508 № 123456 выдан ОВД",
		"паспорт серия 45 09 номер 234567, серия 46 10 № 345678",
		"Полис ОМС 1234 5678 9012 3456 СНИЛС 123-456-789 01 ИНН 771234567890",
		"Тел. (4822) 12-34-56, Телефон +7 912 345-67-89",
		"Гемоглобин 135 г/л 120-160 Глюкоза 5,4 ммоль/л 3,9-6,1",
		"Дата анализа 05.09.2026, Fax 2 pages, кетоновых тел 0,5",
		"MRN 98765 No new findings, pH 7.4",
		"Справка от 12.03.2015 г. р-н Центральный",
	)
	detect(words, Options{})
	for text, want := range map[string]Kind{
		"01.02.1970": KindBirth, "03.04.1972": KindBirth, "05.06.1973": KindBirth,
		"07/08/1974,": KindBirth, "1968": KindBirth,
		"4508": KindID, "№": KindID, "123456": KindID, "234567,": KindID, "345678": KindID,
		"1234": KindID, "3456": KindID, "123-456-789": KindID, "01": KindID, "771234567890": KindID,
		"(4822)": KindPhone, "12-34-56,": KindPhone, "+7": KindPhone, "345-67-89": KindPhone, "98765": KindID,
		"Паспорт": KindNone, "СНИЛС": KindNone, "выдан": KindNone, "ОВД": KindNone,
		"135": KindNone, "120-160": KindNone, "5,4": KindNone, "3,9-6,1": KindNone,
		"05.09.2026,": KindNone, "2": KindNone, "0,5": KindNone, "No": KindNone, "7.4": KindNone,
		"12.03.2015": KindNone,
	} {
		if k := kindOf(t, words, text); k != want {
			t.Errorf("%q: %v, а должно быть %v", text, k, want)
		}
	}
}

// Адрес без «д.» — номер дома сразу за улицей: до 07.10.2026 он оставался
// в .md. «Площадь» в описании очага, «пл.» перед числом и улица без
// названия — не адрес.
func TestRussianAddressWithoutHouseWord(t *testing.T) {
	words := doc(0,
		"Проживает г. Тверь, ул. Примерная, 5, кв. 12",
		"Ул. 8 Марта 14/2 корп. 3, пр-т Мира, 21а",
		"Почтовый 170000, г. Примерск",
		"Площадь поражения 7 см, пл. 2,5 см2, ул. не указана 9",
		"Гемоглобин 135 г/л",
	)
	detect(words, Options{})
	for text, want := range map[string]Kind{
		"Тверь,": KindAddress, "Примерная,": KindAddress, "5,": KindAddress, "12": KindAddress,
		"Марта": KindAddress, "14/2": KindAddress, "3,": KindAddress, "Мира,": KindAddress, "21а": KindAddress,
		"170000,": KindAddress, "Примерск": KindAddress,
		"Площадь": KindNone, "поражения": KindNone, "7": KindNone, "2,5": KindNone, "9": KindNone,
		"135": KindNone,
	} {
		if k := kindOf(t, words, text); k != want {
			t.Errorf("%q: %v, а должно быть %v", text, k, want)
		}
	}
}

func TestNameFragmentsAndRepeatedBirth(t *testing.T) {
	words := doc(0,
		"Patient: Roe, Annabel DOB: 02/03/1997",
		"Ro e, An nab el, 29Y,F 02/03/1997",
		"Annabelle Street fair was held on 02/03/2026.",
		"Seen with Annabel MD 11/12/2026 09:41 AM")
	detect(words, Options{})
	for text, want := range map[string]Kind{
		"An": KindClient, "nab": KindClient, "el,": KindClient, "Ro": KindClient, "e,": KindClient,
		"Street": KindNone, "02/03/2026.": KindNone, "11/12/2026": KindNone, "09:41": KindNone,
	} {
		if k := kindOf(t, words, text); k != want {
			t.Errorf("%q: %v, а должно быть %v", text, k, want)
		}
	}
	births := 0
	for _, w := range words {
		if w.Text == "02/03/1997" && w.Kind == KindBirth {
			births++
		}
	}
	if births != 2 {
		t.Errorf("дата рождения закрыта %d раз из 2", births)
	}

	// Дата рождения, прочитанная с ошибкой в цифре, закрывается повтором.
	words = doc(0, "Patient: Roe, Annabel DOB: 02/03/1997", "Sex DOB Age F 02/\\63/1997 29yo")
	detect(words, Options{})
	if k := kindOf(t, words, `02/\63/1997`); k != KindBirth {
		t.Errorf("дата рождения с ошибкой распознавания: %v", k)
	}
}

// Факс направления: врач заглавными с припиской MD, клиника с признаком
// в названии, номер после подписи с апострофом вместо двоеточия. «Patient's»
// подписью не считается. Все названия выдуманы.
func TestFaxDoctorsOrgsApostrophe(t *testing.T) {
	words := doc(0,
		"To Provider JANE Q ROE MD From MEI-LIN DOE, MD",
		"Northwind Cancer Center 12 Example Ln",
		"Accession ID' QX7654321 Ref",
		"The patient's chart was reviewed.")
	detect(words, Options{})
	for text, want := range map[string]Kind{
		"JANE": KindDoctor, "ROE": KindDoctor, "MEI-LIN": KindDoctor, "DOE,": KindDoctor,
		"Northwind": KindOrg, "Cancer": KindOrg, "QX7654321": KindID,
		"chart": KindNone, "reviewed.": KindNone, "Provider": KindNone,
	} {
		if k := kindOf(t, words, text); k != want {
			t.Errorf("%q: %v, а должно быть %v", text, k, want)
		}
	}
}

// Слово адреса повторяется только точно: улица «Dancer» не закрывает
// аллерген «Dander», а имя с ошибкой распознавания закрывается.
func TestRepeatAddressExactNameNear(t *testing.T) {
	words := doc(0,
		"Patient: Corwin Example",
		"12 Dancer Ave, Springfield, NV 99999",
		"Allergies: cat Dander, pollen. Seen with Corwln today.")
	detect(words, Options{})
	if k := kindOf(t, words, "Dander,"); k != KindNone {
		t.Errorf("аллерген закрыт как %v", k)
	}
	if k := kindOf(t, words, "Corwln"); k != KindClient {
		t.Errorf("имя с ошибкой распознавания: %v", k)
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
	detect(words, Options{})
	md := buildMD("образец", words, nil, 1, false)
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

// Заголовок обезличенного .md — нейтральный, а не имя файла скана: сканы
// называют по фамилии пациента. В распознанной копии имя файла остаётся.
func TestMarkdownTitleNeutral(t *testing.T) {
	const name = "Иванова Мария Петровна выписка"
	ru := doc(0, "Патологических изменений в исследованной области не выявлено.")
	md := buildMD(name, ru, nil, 1, false)
	if strings.Contains(md, "Иванова") || !strings.HasPrefix(md, "# Обезличенный документ\n") {
		t.Errorf("заголовок обезличенного .md:\n%s", md)
	}
	en := doc(0, "No significant abnormality is seen in the examined region.")
	if md := buildMD(name, en, nil, 1, false); !strings.HasPrefix(md, "# Redacted document\n") {
		t.Errorf("заголовок английского документа:\n%s", md)
	}
	if plain := buildMD(name, ru, nil, 1, true); !strings.HasPrefix(plain, "# "+name+"\n") {
		t.Errorf("у распознанной копии заголовок — имя файла:\n%s", plain)
	}
}

func TestMarkdownDropsWordsUnderInk(t *testing.T) {
	words := doc(0, "Requested measurements review")
	detect(words, Options{})
	ink := []Region{{Page: 0, Box: Rect{0, 40, 600, 70}}}
	md := buildMD("образец", words, ink, 1, false)
	if strings.Contains(md, "measurements") {
		t.Errorf("слово под областью чернил попало в .md:\n%s", md)
	}
	// Документ английский — и пометка английская.
	if !strings.Contains(md, "Hidden areas without recognized text") {
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

// Слово-признак само в ПДн не уходит: «доктора» — не имя врача, «Clinical» —
// не клиника, «Marital» — не месяц. До 03.10.2026 каждое из них помечалось,
// разносилось повтором по документу и закрашивалось везде. Имена выдуманы.
func TestSignalWordsAreNotData(t *testing.T) {
	words := doc(0, "Наблюдается у доктора Орловой с весны.",
		"Clinical indication: cough",
		"DOB: 03/04/1970 Marital status: married",
		"Мнение второго доктора не запрашивалось.")
	detect(words, Options{})
	for text, k := range map[string]Kind{
		"Орловой": KindDoctor, "доктора": KindNone, "Clinical": KindNone,
		"03/04/1970": KindBirth, "Marital": KindNone,
	} {
		if got := kindOf(t, words, text); got != k {
			t.Errorf("%q: %v, а должно быть %v", text, got, k)
		}
	}
}

// Месяц словом внутри даты рождения по-прежнему часть даты.
func TestDateWordIsWholeMonth(t *testing.T) {
	for _, s := range []string{"March", "mar", "Sept.", "декабря", "мая", "г.", "года", "June,"} {
		if !dateWordRe.MatchString(s) {
			t.Errorf("%q не признано словом даты", s)
		}
	}
	for _, s := range []string{"Marital", "Decreased", "Мария", "Junior", "годовалый", "Maybe"} {
		if dateWordRe.MatchString(s) {
			t.Errorf("%q признано словом даты", s)
		}
	}
}

// Скан с разным разрешением по осям (факс): картинка для распознавания
// обязана иметь пропорции страницы, иначе единый масштаб врёт по вертикали.
func TestGrayAtKeepsPageProportions(t *testing.T) {
	// Страница 612×792 pt; картинка 1224×792 — по ширине вдвое плотнее.
	p := Page{Width: 612, Height: 792, Image: image.NewGray(image.Rect(0, 0, 1224, 792))}
	img, k := grayAt(p, 72)
	b := img.Bounds()
	if got, want := float64(b.Dy())/k, p.Height; got < want-1 || got > want+1 {
		t.Errorf("высота картинки %d при масштабе %.3f — это %.1f pt, а страница %.0f pt", b.Dy(), k, got, want)
	}
	// Обычный скан (точки квадратные) остаётся как был.
	p = Page{Width: 612, Height: 792, Image: image.NewGray(image.Rect(0, 0, 2550, 3300))}
	img, _ = grayAt(p, 72)
	if img.Bounds().Dx() != 2550 || img.Bounds().Dy() != 3300 {
		t.Errorf("обычный скан пересчитан: %v", img.Bounds())
	}
}
