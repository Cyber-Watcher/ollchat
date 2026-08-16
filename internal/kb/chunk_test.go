package kb

import (
	"fmt"
	"strings"
	"testing"

	"github.com/itpro/ollchat/internal/document"
)

func page(n int, text string) document.Part {
	return document.Part{Number: n, Text: text}
}

// para возвращает абзац примерно нужной длины.
func para(prefix string, chars int) string {
	s := prefix + " "
	for len([]rune(s)) < chars {
		s += "слово текста для набора нужной длины абзаца "
	}
	return strings.TrimSpace(s)
}

// TestChunksKnowTheirPage — главное требование: по куску должно быть видно
// страницу, иначе ссылку в ответе модели нельзя проверить.
func TestChunksKnowTheirPage(t *testing.T) {
	parts := []document.Part{
		page(7, para("первая", 600)),
		page(8, para("вторая", 600)),
		page(9, para("третья", 600)),
	}
	chunks := Split(parts, DefaultChunkOpts())
	if len(chunks) == 0 {
		t.Fatal("кусков не вышло вовсе")
	}
	for _, c := range chunks {
		if c.UnitFrom < 7 || c.UnitTo > 9 || c.UnitFrom > c.UnitTo {
			t.Fatalf("страницы куска неверны: %d–%d", c.UnitFrom, c.UnitTo)
		}
	}
	if chunks[0].UnitFrom != 7 {
		t.Fatalf("первый кусок начинается со страницы %d, ожидалась 7", chunks[0].UnitFrom)
	}
}

// TestChunkSizeAndOverlap — куски держат заданный размер и перекрываются:
// определение, разорванное границей, обязано целиком попасть хотя бы в один.
func TestChunkSizeAndOverlap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&sb, "%s\n\n", para(fmt.Sprintf("абзац%d", i), 300))
	}
	chunks := Split([]document.Part{page(1, sb.String())}, DefaultChunkOpts())
	if len(chunks) < 3 {
		t.Fatalf("кусков всего %d — перекрытие не работает", len(chunks))
	}
	for _, c := range chunks {
		if n := len([]rune(c.Text)); n > DefaultChunkOpts().Chars*2 {
			t.Fatalf("кусок длиной %d символов — вдвое больше цели", n)
		}
	}
	// Перекрытие: хвост одного куска должен встречаться в начале следующего.
	overlaps := 0
	for i := 0; i+1 < len(chunks); i++ {
		tailWords := strings.Fields(chunks[i].Text)
		if len(tailWords) < 6 {
			continue
		}
		tail := strings.Join(tailWords[len(tailWords)-6:], " ")
		if strings.Contains(chunks[i+1].Text, tail) {
			overlaps++
		}
	}
	if overlaps == 0 {
		t.Fatal("соседние куски нигде не перекрываются")
	}
}

// TestCodeNotSplitMidLine — половина команды не читается ни человеком,
// ни моделью.
func TestCodeNotSplitMidLine(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Пример программы:\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&sb, "    func handler%d(w http.ResponseWriter, r *http.Request) { log.Println(\"ok\") }\n", i)
	}
	chunks := Split([]document.Part{page(1, sb.String())}, DefaultChunkOpts())

	found := false
	for _, c := range chunks {
		if c.Flags&FlagCode == 0 {
			continue
		}
		found = true
		for _, line := range strings.Split(c.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "func handler") {
				continue
			}
			if !strings.HasSuffix(line, "}") {
				t.Fatalf("строка кода разрезана: %q", line)
			}
		}
	}
	if !found {
		t.Fatal("код не опознан: ни у одного куска нет пометки")
	}
}

// TestTableRowsKept — строки таблицы тоже нельзя рвать.
func TestTableRowsKept(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Параметры:\n\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&sb, "параметр%d | целое | обязательный | описание параметра номер %d\n", i, i)
	}
	chunks := Split([]document.Part{page(1, sb.String())}, DefaultChunkOpts())
	tagged := false
	for _, c := range chunks {
		if c.Flags&FlagTable != 0 {
			tagged = true
		}
		for _, line := range strings.Split(c.Text, "\n") {
			if strings.HasPrefix(line, "параметр") && !strings.Contains(line, "описание") {
				t.Fatalf("строка таблицы разрезана: %q", line)
			}
		}
	}
	if !tagged {
		t.Fatal("таблица не опознана")
	}
}

// TestHeadersAndFootersDropped — типовая надпись со всех страниц становится
// одним из самых частых термов и портит ранжирование, ничего не значая.
func TestHeadersAndFootersDropped(t *testing.T) {
	var parts []document.Part
	for i := 1; i <= 20; i++ {
		text := fmt.Sprintf("Глава 7 · Конкурентность · %d\n\n%s\n\nwww.example.com | %d",
			i, para(fmt.Sprintf("содержательный текст страницы %d", i), 500), i)
		parts = append(parts, page(i, text))
	}
	chunks := Split(parts, DefaultChunkOpts())
	all := ""
	for _, c := range chunks {
		all += c.Text + "\n"
	}
	if strings.Contains(all, "Конкурентность ·") {
		t.Error("колонтитул попал в куски")
	}
	if strings.Contains(all, "www.example.com") {
		t.Error("нижний колонтитул попал в куски")
	}
	if !strings.Contains(all, "содержательный текст") {
		t.Error("вместе с колонтитулами вырезано содержимое")
	}
}

// TestTableOfContentsSkipped — страницы оглавления не несут смысла, но забивают
// индекс названиями всех разделов книги.
func TestTableOfContentsSkipped(t *testing.T) {
	toc := "Содержание\n\n" +
		"Введение .......... 5\n" +
		"Глава 1. Основы ................ 12\n" +
		"Глава 2. Каналы ................ 45\n" +
		"Глава 3. Горутины .............. 78\n" +
		"Глава 4. Планировщик ........... 96\n" +
		"Глава 5. Отладка ............... 120\n"
	parts := []document.Part{
		page(3, toc),
		page(4, para("настоящий текст книги", 600)),
	}
	chunks := Split(parts, DefaultChunkOpts())
	for _, c := range chunks {
		if strings.Contains(c.Text, "..........") {
			t.Fatalf("страница оглавления попала в индекс: %q", c.Text[:60])
		}
		if c.UnitFrom == 3 {
			t.Fatalf("кусок со страницы оглавления: %q", c.Text[:60])
		}
	}
}

// TestJunkDropped — номера страниц, обломки распознавания и строки из одних
// знаков в индексе не нужны.
func TestJunkDropped(t *testing.T) {
	parts := []document.Part{
		page(1, "42\n\n· · · · ·\n\n1.2  3.4  5.6  7.8  9.0\n\n"+para("осмысленный текст", 400)),
	}
	chunks := Split(parts, DefaultChunkOpts())
	for _, c := range chunks {
		if strings.Contains(c.Text, "· · ·") {
			t.Errorf("мусор попал в кусок: %q", c.Text)
		}
	}
	if len(chunks) == 0 {
		t.Fatal("вместе с мусором выброшен и текст")
	}
}

// TestSentenceBoundaries — длинный абзац режется по предложениям, не спотыкаясь
// о сокращения и номера версий.
func TestSentenceBoundaries(t *testing.T) {
	got := sentences("Версия v1.2 вышла в срок. См. рис. 5 и табл. 3. Далее идёт вывод.")
	if len(got) != 3 {
		t.Fatalf("предложений %d, ожидалось 3: %q", len(got), got)
	}
	if !strings.Contains(got[1], "рис. 5") || !strings.Contains(got[1], "табл. 3") {
		t.Fatalf("сокращения приняты за конец предложения: %q", got)
	}
}

// TestLanguageFlags — по кускам видно, на каком языке текст: это пригодится
// смысловому поиску, чтобы не смешивать языки в одном запросе.
func TestLanguageFlags(t *testing.T) {
	ru := Split([]document.Part{page(1, para("русский текст про каналы", 400))}, DefaultChunkOpts())
	if len(ru) == 0 || ru[0].Flags&FlagRussian == 0 {
		t.Error("русский текст не помечен")
	}
	en := Split([]document.Part{page(1, "The quick brown fox jumps over the lazy dog. "+
		strings.Repeat("This is a sentence of plain English prose. ", 12))}, DefaultChunkOpts())
	if len(en) == 0 || en[0].Flags&FlagEnglish == 0 {
		t.Error("английский текст не помечен")
	}
}

// TestEmptyAndTinyInput — разбиение не должно ломаться на пустых страницах.
func TestEmptyAndTinyInput(t *testing.T) {
	cases := [][]document.Part{
		nil,
		{page(1, "")},
		{page(1, "   \n\n\t")},
		{page(1, "коротко")},
		{page(1, strings.Repeat("а", 100000))},
	}
	for i, parts := range cases {
		chunks := Split(parts, DefaultChunkOpts())
		for _, c := range chunks {
			if strings.TrimSpace(c.Text) == "" {
				t.Errorf("случай %d: пустой кусок", i)
			}
		}
	}
}

// TestRunningHeadsWithPageNumbers закрепляет приём, найденный на живой книге:
// колонтитул опознаётся по тому, что содержит номер своей же страницы.
//
// Повторяемости не хватает: надпись меняется от раздела к разделу («Работа
// с переменными», «Глава 2 • Говорим на языке C#») и по отдельности встречается
// на десятке страниц из тысячи. В книге на 1019 страниц колонтитулы оставались
// в трети кусков, пока не появился счёт по номеру.
func TestRunningHeadsWithPageNumbers(t *testing.T) {
	var parts []document.Part
	for i := 1; i <= 60; i++ {
		// Надпись меняется каждые десять страниц — как настоящий раздел.
		head := fmt.Sprintf("Раздел %d. Подробности реализации  %d", i/10, i)
		if i%2 == 0 {
			head = fmt.Sprintf("%d  Глава %d • Другое название", i, i/10)
		}
		parts = append(parts, page(i, head+"\n\n"+para(fmt.Sprintf("содержимое страницы %d", i), 500)))
	}
	chunks := Split(parts, DefaultChunkOpts())
	all := ""
	for _, c := range chunks {
		all += c.Text + "\n"
	}
	if strings.Contains(all, "Подробности реализации") || strings.Contains(all, "Другое название") {
		t.Error("колонтитул с номером страницы попал в куски")
	}
	if !strings.Contains(all, "содержимое страницы") {
		t.Error("вместе с колонтитулами вырезано содержимое")
	}
}

// TestPageNumberOffsetFound — печатный номер отличается от порядкового, потому
// что вначале идут титул и оглавление. Смещение должно определяться само.
func TestPageNumberOffsetFound(t *testing.T) {
	const shift = 8
	var parts []document.Part
	for i := 1; i <= 40; i++ {
		parts = append(parts, page(i, fmt.Sprintf("Заголовок раздела  %d\n\n%s",
			i+shift, para("текст", 400))))
	}
	off, ok := pageNumberOffset(parts)
	if !ok || off != shift {
		t.Fatalf("смещение определено как %d (найдено: %v), ожидалось %d", off, ok, shift)
	}
}

// TestNoOffsetWhenNoPageNumbers — если номеров на страницах нет, приём не должен
// срабатывать на случайных числах в тексте.
func TestNoOffsetWhenNoPageNumbers(t *testing.T) {
	var parts []document.Part
	for i := 1; i <= 40; i++ {
		parts = append(parts, page(i, para("сплошной текст без колонтитулов", 500)))
	}
	if _, ok := pageNumberOffset(parts); ok {
		t.Fatal("смещение определено там, где номеров страниц нет")
	}
}
