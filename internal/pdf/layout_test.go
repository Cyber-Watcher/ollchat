package pdf

import (
	"math"
	"strings"
	"testing"
)

// TestLayoutTableRows закрепляет главное, ради чего появилась сборка по
// координатам: ячейки одной строки таблицы обязаны оказаться в одной строке
// текста и друг под другом по столбцам. Раньше каждая ячейка уезжала на свою
// строку, потому что нарисована отдельным блоком BT…ET.
func TestLayoutTableRows(t *testing.T) {
	cell := func(x, y float64, text string) frag {
		return frag{x: x, y: y, w: float64(len(text)) * 5, size: 10, text: text}
	}
	got := layout([]frag{
		cell(50, 700, "Параметр"), cell(150, 700, "Тип"), cell(250, 700, "Обяз."),
		cell(50, 686, "count"), cell(150, 686, "integer"), cell(250, 686, "+"),
		cell(50, 672, "list"), cell(150, 672, "array"), cell(250, 672, "-"),
	})
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("строк %d, ожидалось 3:\n%s", len(lines), got)
	}
	for _, l := range lines {
		if strings.Count(l, "  ") == 0 {
			t.Fatalf("столбцы не разделены отступами: %q", l)
		}
	}
	// Столбцы должны стоять друг под другом. Позиция считается в символах:
	// в байтах кириллический заголовок вдвое длиннее и сравнение врёт.
	col := runeIndex(lines[0], "Тип")
	for i, want := range []string{"integer", "array"} {
		if at := runeIndex(lines[1+i], want); at != col {
			t.Fatalf("столбец %q в позиции %d, а заголовок в %d:\n%s", want, at, col, got)
		}
	}
}

// runeIndex возвращает позицию подстроки в символах, а не в байтах.
func runeIndex(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(s[:i]))
}

// TestLayoutKeepsWordsTogether проверяет, что куски одного слова не разъезжаются
// пробелами: шрифт часто режет слово на части ради кернинга.
func TestLayoutKeepsWordsTogether(t *testing.T) {
	got := layout([]frag{
		{x: 50, y: 700, w: 28, size: 10, text: "Опис"},
		{x: 78, y: 700, w: 25, size: 10, text: "ание"},
		{x: 103.4, y: 700, w: 6, size: 10, text: ":"},
	})
	if got != "Описание:" {
		t.Fatalf("получено %q, ожидалось %q", got, "Описание:")
	}
}

// TestLayoutSeparatesTouchingCells — обратный случай: соседние ячейки стоят
// вплотную, но склеивать их нельзя.
func TestLayoutSeparatesTouchingCells(t *testing.T) {
	got := layout([]frag{
		{x: 50, y: 700, w: 40, size: 10, text: "array of"},
		{x: 95, y: 700, w: 5, size: 10, text: "-"},
	})
	if got != "array of -" {
		t.Fatalf("получено %q, ожидалось %q", got, "array of -")
	}
}

// TestLayoutParagraphBreak проверяет, что заметный вертикальный пропуск даёт
// пустую строку — по ней видно границу абзаца.
func TestLayoutParagraphBreak(t *testing.T) {
	got := layout([]frag{
		{x: 50, y: 700, w: 30, size: 10, text: "первый"},
		{x: 50, y: 688, w: 30, size: 10, text: "абзац"},
		{x: 50, y: 640, w: 30, size: 10, text: "второй"},
	})
	if !strings.Contains(got, "абзац\n\nвторой") {
		t.Fatalf("граница абзаца не отмечена:\n%q", got)
	}
}

// TestLayoutTwoColumns закрепляет разделение колонок: без него строка левой
// колонки склеивается со строкой правой и обе фразы рвутся.
func TestLayoutTwoColumns(t *testing.T) {
	var frags []frag
	for i := 0; i < 30; i++ {
		y := 700 - float64(i)*12
		frags = append(frags,
			frag{x: 50, y: y, w: 100, size: 10, text: "левая"},
			frag{x: 320, y: y, w: 100, size: 10, text: "правая"})
	}
	got := layout(frags)
	lines := strings.Split(got, "\n")
	for _, l := range lines {
		if strings.Contains(l, "левая") && strings.Contains(l, "правая") {
			t.Fatalf("колонки склеены в одну строку: %q", l)
		}
	}
	if !strings.Contains(got, "левая") || !strings.Contains(got, "правая") {
		t.Fatalf("текст колонок потерян:\n%s", got)
	}
	// Левая колонка идёт целиком, потом правая.
	if strings.Index(got, "правая") < strings.LastIndex(got, "левая") {
		t.Fatalf("порядок колонок нарушен:\n%s", got)
	}
}

// TestLayoutSingleColumnNotSplit — страница обычного текста делиться не должна.
func TestLayoutSingleColumnNotSplit(t *testing.T) {
	var frags []frag
	for i := 0; i < 30; i++ {
		y := 700 - float64(i)*12
		frags = append(frags, frag{x: 50, y: y, w: 400, size: 10, text: "сплошная строка текста"})
	}
	if len(columns(frags)) != 1 {
		t.Fatal("страница с одной колонкой разделена надвое")
	}
}

// TestLayoutSurvivesBrokenCoordinates закрепляет исправленный дефект, найденный
// обстрелом испорченных книг: у повреждённого файла координата бывает NaN или
// бесконечностью. Преобразование такого числа в целое даёт минимальное int64,
// разность номеров колонок переполняется в максимальное положительное, и
// strings.Repeat запрашивает петабайт памяти — программа падала целиком.
func TestLayoutSurvivesBrokenCoordinates(t *testing.T) {
	inf := math.Inf(1)
	nan := math.NaN()
	cases := []struct {
		name  string
		frags []frag
	}{
		{"бесконечность в X", []frag{{x: 50, y: 700, w: 20, size: 10, text: "начало"}, {x: inf, y: 700, w: 10, size: 10, text: "хвост"}}},
		{"NaN в X", []frag{{x: 50, y: 700, w: 20, size: 10, text: "начало"}, {x: nan, y: 700, w: 10, size: 10, text: "хвост"}}},
		{"NaN в ширине", []frag{{x: 50, y: 700, w: nan, size: 10, text: "а"}, {x: 90, y: 700, w: 10, size: 10, text: "б"}}},
		{"минус бесконечность", []frag{{x: -inf, y: 700, w: 10, size: 10, text: "а"}, {x: 90, y: 700, w: 10, size: 10, text: "б"}}},
		{"нулевой кегль", []frag{{x: 50, y: 700, w: 10, size: 0, text: "а"}, {x: 5000, y: 700, w: 10, size: 0, text: "б"}}},
		{"огромная координата", []frag{{x: 0, y: 700, w: 10, size: 10, text: "а"}, {x: 1e300, y: 700, w: 10, size: 10, text: "б"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := layout(c.frags) // паника здесь провалит тест сама по себе
			if len(got) > 10000 {
				t.Fatalf("строка неразумной длины: %d символов", len(got))
			}
		})
	}
}

// Отдельно нарисованная точка остаётся точкой (этап 104, П6.6).
//
// 17.09.2026 раскладка один день превращала в пробел любую узкую точку,
// пришедшую отдельным куском, — так лечились книги, где пробел нарисован
// глифом-точкой. Признак оказался ложным: точка в любом шрифте у́же трети
// кегля (0,25–0,28), а отдельным куском она приходит всякий раз, когда перед
// ней стоит кернинг или текст выводится по знаку. Замер на десяти книгах:
// в «Artificial Intelligence: A Modern Approach» из ~11 тысяч точек на 225
// страницах уцелело бы 230. Настоящая причина тех книг — ActualText, она
// разобрана в marked.go; здесь закреплено, что раскладка точек не трогает.
func TestJoinLineKeepsLoneDots(t *testing.T) {
	cases := []struct {
		name string
		line []frag
		want string
	}{
		{"конец предложения после кернинга", []frag{
			{x: 10, w: 40, size: 10, text: "Hello wor"},
			{x: 50, w: 5, size: 10, text: "d"},
			{x: 55, w: 2.78, size: 10, text: "."},
		}, "Hello word."},
		{"десятичное число", []frag{
			{x: 10, w: 5.5, size: 10, text: "3"},
			{x: 15.5, w: 2.78, size: 10, text: "."},
			{x: 18.3, w: 11, size: 10, text: "14"},
		}, "3.14"},
		{"имя файла", []frag{
			{x: 10, w: 11, size: 10, text: "go"},
			{x: 21, w: 2.78, size: 10, text: "."},
			{x: 23.8, w: 17, size: 10, text: "mod"},
		}, "go.mod"},
	}
	for _, c := range cases {
		if got := joinLine(c.line, 10, 5, wordGapMax); got != c.want {
			t.Errorf("%s: получено %q, ожидалось %q", c.name, got, c.want)
		}
	}
}

// Порог пробела между словами выбирается по странице (этап 104, П6.10).
//
// gapPage собирает страницу из строк по десять слов; внутри слова куски стоят
// с разрывом inner, между словами — с разрывом outer (в долях кегля).
func gapPage(inner, outer float64) []frag {
	const size = 10.0
	var out []frag
	for row := 0; row < 8; row++ {
		x := 50.0
		for w := 0; w < 10; w++ {
			for part := 0; part < 2; part++ {
				out = append(out, frag{x: x, y: 700 - float64(row)*14, w: 12, size: size, text: "ab"})
				x += 12 + inner*size
			}
			x += (outer - inner) * size
		}
	}
	return out
}

func TestWordGapFollowsPage(t *testing.T) {
	// Вёрстка TeX: кернинг до 0,03 кегля, пробел ужат до 0,12. Прежний жёсткий
	// порог 0,2 склеивал такие слова в одно.
	tex := layout(gapPage(0.03, 0.12))
	if strings.Contains(tex, "abababab") || !strings.Contains(tex, "abab abab") {
		t.Fatalf("слова слиплись: %q", strings.SplitN(tex, "\n", 2)[0])
	}
	// Буквы по одной с шагом 0,08 кегля, пробел 0,32: долины под 0,2 нет,
	// и рвать слова на куски нельзя.
	spaced := layout(gapPage(0.08, 0.32))
	if strings.Contains(spaced, "ab ab ab") || !strings.Contains(spaced, "abab abab") {
		t.Fatalf("слова разорваны: %q", strings.SplitN(spaced, "\n", 2)[0])
	}
	// Мало данных — прежнее поведение.
	if got := wordGapOf(gapPage(0.03, 0.12)[:6], 4); got != wordGapMax {
		t.Fatalf("порог по шести кускам: %v", got)
	}
}
