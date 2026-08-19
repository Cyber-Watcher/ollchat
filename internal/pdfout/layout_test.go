package pdfout

import (
	"strings"
	"testing"
)

// newTestPainter готовит рисовальщика для проверки арифметики раскладки.
func newTestPainter(t *testing.T) *painter {
	t.Helper()
	p, err := newPainter(defaultTheme(), Options{})
	if err != nil {
		t.Fatalf("подготовка документа: %v", err)
	}
	return p
}

// lineWidth считает ширину готовой строки так же, как её печатает drawLine.
func lineWidth(line []token) float64 {
	w := 0.0
	for i, t := range line {
		w += t.w
		if i < len(line)-1 {
			w += t.space
		}
	}
	return w
}

// TestWrapNeverExceedsWidth — главная проверка переноса: ни одна строка
// не смеет вылезти за колонку, иначе текст уедет за поле страницы.
func TestWrapNeverExceedsWidth(t *testing.T) {
	p := newTestPainter(t)
	const width = 300.0

	texts := []string{
		"Короткий текст.",
		strings.Repeat("слово ", 200),
		"Слова разной длины: а, аб, абвгдеёжзийклмнопрстуфхцчшщъыьэюя, ещё.",
		"Mixed кириллица and latin вперемешку for проверки переноса.",
	}
	for _, src := range texts {
		lines := p.wrapTokens(p.tokenize([]run{{text: src}}, p.th.textSize), width, p.th.textSize)
		if len(lines) == 0 {
			t.Errorf("текст %q не дал ни одной строки", src[:min(20, len(src))])
			continue
		}
		for i, line := range lines {
			if w := lineWidth(line); w > width+0.01 {
				t.Errorf("строка %d шире колонки: %.1f при пределе %.1f", i, w, width)
			}
		}
	}
}

// TestSplitLongWordFitsColumn: слово длиннее колонки режется, а не выпадает.
func TestSplitLongWordFitsColumn(t *testing.T) {
	p := newTestPainter(t)
	const width = 120.0

	long := "https://example.com/" + strings.Repeat("verylongpath/", 20)
	parts := p.splitLong(long, 0, p.th.textSize, width)

	if len(parts) < 2 {
		t.Fatalf("длинное слово не разрезано: %d кусков", len(parts))
	}
	for i, part := range parts {
		if w := p.measure(part, 0, p.th.textSize); w > width+0.01 {
			t.Errorf("кусок %d шире колонки: %.1f при пределе %.1f", i, w, width)
		}
	}
	// Ни один символ не должен потеряться — иначе пропажу не заметить.
	if got := strings.Join(parts, ""); got != long {
		t.Errorf("текст изменился при разрезании:\nбыло:  %q\nстало: %q", long, got)
	}
}

// TestWrapKeepsAllWords: перенос не должен терять слова.
func TestWrapKeepsAllWords(t *testing.T) {
	p := newTestPainter(t)
	src := strings.Repeat("альфа бета гамма дельта ", 30)

	lines := p.wrapTokens(p.tokenize([]run{{text: src}}, p.th.textSize), 200, p.th.textSize)

	var got []string
	for _, line := range lines {
		for _, tk := range line {
			if tk.text != "" {
				got = append(got, tk.text)
			}
		}
	}
	want := strings.Fields(src)
	if len(got) != len(want) {
		t.Fatalf("слов после переноса %d, было %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("слово %d: %q, ожидалось %q", i, got[i], want[i])
		}
	}
}

// TestHardBreakStartsNewLine: жёсткий перенос обязан начать новую строку,
// даже если место в текущей ещё есть.
func TestHardBreakStartsNewLine(t *testing.T) {
	p := newTestPainter(t)
	runs := []run{
		{text: "первая", brk: true},
		{text: "вторая"},
	}
	lines := p.wrapTokens(p.tokenize(runs, p.th.textSize), 400, p.th.textSize)
	if len(lines) != 2 {
		t.Fatalf("ожидалось две строки, получено %d: %+v", len(lines), lines)
	}
}

// TestNeedStartsNewPage: когда место кончилось, начинается страница.
func TestNeedStartsNewPage(t *testing.T) {
	p := newTestPainter(t)
	if p.pages != 1 {
		t.Fatalf("документ начинается с %d страниц", p.pages)
	}

	p.y = p.th.bottom() - 5
	if !p.need(p.th.textLead) {
		t.Error("места не хватало, но страница не началась")
	}
	if p.pages != 2 {
		t.Errorf("страниц после разрыва: %d", p.pages)
	}
	if p.y != p.th.marginT {
		t.Errorf("после разрыва вывод продолжился с %.1f, а не с верхнего поля %.1f",
			p.y, p.th.marginT)
	}

	// Место есть — страница начинаться не должна.
	p.y = p.th.marginT
	if p.need(p.th.textLead) {
		t.Error("страница началась, хотя места хватало")
	}
}

// TestMeasureUsesRequestedStyle: мерить надо тем начертанием, каким печатаем.
//
// Жирный текст шире обычного, и если померить его обычным шрифтом, строка
// вылезет за поле страницы.
func TestMeasureUsesRequestedStyle(t *testing.T) {
	p := newTestPainter(t)
	const s = "Одинаковый текст для сравнения"

	plain := p.measure(s, 0, p.th.textSize)
	bold := p.measure(s, styleBold, p.th.textSize)
	mono := p.measure(s, styleCode, p.th.textSize)

	if plain <= 0 || bold <= 0 || mono <= 0 {
		t.Fatalf("нулевая ширина: обычный %.1f жирный %.1f моно %.1f", plain, bold, mono)
	}
	if bold <= plain {
		t.Errorf("жирный текст не шире обычного: %.1f против %.1f", bold, plain)
	}
	if mono == plain {
		t.Errorf("моноширинный совпал по ширине с обычным: %.1f", mono)
	}
}

// TestMeasureCached: повторное измерение берётся из кеша.
func TestMeasureCached(t *testing.T) {
	p := newTestPainter(t)
	const s = "повторяющееся слово"

	first := p.measure(s, 0, p.th.textSize)
	if len(p.wcache) == 0 {
		t.Fatal("измерение не попало в кеш")
	}
	if second := p.measure(s, 0, p.th.textSize); second != first {
		t.Errorf("кеш вернул другое значение: %.3f против %.3f", second, first)
	}
}

// TestIndentStopsGrowing: списки не сдвигаются бесконечно вправо.
func TestIndentStopsGrowing(t *testing.T) {
	th := defaultTheme()
	deep := th.indentFor(th.maxDepth)
	if got := th.indentFor(th.maxDepth + 5); got != deep {
		t.Errorf("на глубине %d отступ %.1f, ожидался предел %.1f",
			th.maxDepth+5, got, deep)
	}
	if deep >= th.contentWidth() {
		t.Errorf("предельный отступ %.1f не оставляет места под текст (%.1f)",
			deep, th.contentWidth())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestGapAfterList: блок после списка не должен прилипать к последнему пункту.
//
// Пункты списка идут вплотную, отбивку после списка добавляет следующий блок —
// проверяем, что он её действительно добавляет.
func TestGapAfterList(t *testing.T) {
	measure := func(src string) float64 {
		q := newTestPainter(t)
		start := q.y
		q.drawBlocks(parseMarkdown(src))
		return q.y - start
	}

	tight := measure("- первый\n- второй\n- третий\n")
	loose := measure("- первый\n- второй\n- третий\n\nабзац после списка\n")
	single := measure("абзац после списка\n")

	if gap := loose - tight - single; gap < 1 {
		t.Errorf("после списка нет отбивки: лишних %.1f пунктов", gap)
	}
}
