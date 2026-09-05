package pdfout

import (
	"testing"

	"github.com/signintech/gopdf"
)

// TestServiceGlyphsExist проверяет, что все служебные знаки, которые мы сами
// подставляем в документ, есть в обоих встроенных шрифтах.
//
// Тест написан не из вежливости, а по следам настоящей ошибки: меткой переноса
// длинной строки был выбран знак «↩», которого в Liberation нет. В документе он
// молча превращался в «□» — то есть механизм подмены отсутствующего глифа
// сработал на нашей же служебной метке. Заметить это можно было только чтением
// готового PDF обратно.
func TestServiceGlyphsExist(t *testing.T) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: 595, H: 842}})

	missing := map[rune]bool{}
	if _, err := registerFonts(pdf, missing); err != nil {
		t.Fatalf("шрифты не загрузились: %v", err)
	}
	pdf.AddPage()

	// Всё, что пакет печатает от себя, а не берёт из ответа модели.
	service := map[string]rune{
		"замена отсутствующего глифа": noGlyph,
		"метка переноса строки":       wrapMark,
		"маркер списка 1 уровня":      '•',
		"маркер списка 2 уровня":      '◦',
		"маркер списка 3 уровня":      '–',
		"тире в номере страницы":      '—',
		"разделитель в подписи":       '·',
	}

	for _, family := range []string{familySans, familyMono} {
		if err := pdf.SetFont(family, "", 10); err != nil {
			t.Fatalf("шрифт %s: %v", family, err)
		}
		for name, r := range service {
			ok, err := pdf.IsCurrFontContainGlyph(r)
			if err != nil {
				t.Errorf("%s: проверка %s (%q): %v", family, name, string(r), err)
				continue
			}
			if !ok {
				t.Errorf("%s: нет глифа для %s (%q) — в документе он превратится в %q",
					family, name, string(r), string(noGlyph))
			}
		}
	}
}

// TestFontForPicksFamily закрепляет разбор начертаний: моноширинный побеждает
// всё остальное, жирный побеждает курсив.
func TestFontForPicksFamily(t *testing.T) {
	cases := []struct {
		style          runStyle
		family, weight string
	}{
		{0, familySans, ""},
		{styleBold, familySans, "B"},
		{styleItalic, familySans, "I"},
		{styleBold | styleItalic, familySans, "B"},
		{styleCode, familyMono, ""},
		{styleCode | styleBold, familyMono, ""},
		{styleDim, familySans, ""},
	}
	for _, c := range cases {
		family, weight := fontFor(c.style)
		if family != c.family || weight != c.weight {
			t.Errorf("начертание %d: получено (%s,%q), ожидалось (%s,%q)",
				c.style, family, weight, c.family, c.weight)
		}
	}
}

// TestFallbackCoversMissingGlyphs: знаки, которых нет в Liberation, обязаны
// найтись в резервном шрифте.
//
// Повод — знак рубля: модель писала «≈ 250 000 ₽», а в документе стоял пустой
// прямоугольник. Заодно проверяются соседи по беде: галочки, звёзды, двойные
// и жирные рамки псевдографики, которыми модель рисует таблицы в блоках кода.
func TestFallbackCoversMissingGlyphs(t *testing.T) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: 595, H: 842}})
	missing := map[rune]bool{}
	covers, err := registerFonts(pdf, missing)
	if err != nil {
		t.Fatalf("шрифты не загрузились: %v", err)
	}

	// Знаки, ради которых резерв и заведён. Рубль, тенге, рупия и турецкая
	// лира — те валюты, что реально встречаются в ответах. Лари (₾), манат (₼)
	// и ещё три знака не покрывает и DejaVu 2016 года: их взять неоткуда,
	// и они честно попадут в отчёт «символов без глифа».
	want := []rune{'₽', '₸', '₹', '₺', '✓', '✔', '✗', '★', '⇒', '⌀', '⏎', '≠'}

	for _, style := range []runStyle{0, styleBold, styleItalic, styleCode} {
		mf, ms := fontFor(style)
		sf, ss := fallbackFor(style)
		main, spare := covers[fontKey{mf, ms}], covers[fontKey{sf, ss}]
		if len(main) == 0 || len(spare) == 0 {
			t.Fatalf("начертание %d: покрытие не собрано (основной %d, резерв %d)",
				style, len(main), len(spare))
		}
		for _, r := range want {
			if !main[r] && !spare[r] {
				t.Errorf("начертание %d: %q нет ни в основном шрифте, ни в резервном",
					style, string(r))
			}
		}
	}
}

// TestCoverageMatchesGopdf сверяет нашу карту покрытия с ответом самой
// библиотеки.
//
// Карта строится разбором cmap из internal/pdf, а рисует по ней gopdf. Если
// разборщик чего-то не понимает (формат 12, например), мы будем уверены,
// что глиф есть, а в документе окажется □ — и заметить это можно будет только
// глазами. Поэтому карта проверяется независимым источником.
func TestCoverageMatchesGopdf(t *testing.T) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: 595, H: 842}})
	missing := map[rune]bool{}
	covers, err := registerFonts(pdf, missing)
	if err != nil {
		t.Fatalf("шрифты не загрузились: %v", err)
	}
	pdf.AddPage()

	probe := []rune("Ая Zz 0 ₽₴₸€ ✓✗★ →⇒ ─│├╔┏ ░▒█ №℃ ≈≠≤ ⌀⏎ …—«»")
	for key, cover := range covers {
		if err := pdf.SetFont(key.family, key.style, 10); err != nil {
			t.Fatalf("шрифт %s%s: %v", key.family, key.style, err)
		}
		for _, r := range probe {
			real, err := pdf.IsCurrFontContainGlyph(r)
			if err != nil {
				t.Fatalf("%s%s: проверка %q: %v", key.family, key.style, string(r), err)
			}
			if real != cover[r] {
				t.Errorf("%s%s: про %q карта говорит %v, а библиотека %v",
					key.family, key.style, string(r), cover[r], real)
			}
		}
	}
}
