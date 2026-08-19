package pdfout

import (
	_ "embed"
	"fmt"

	"github.com/signintech/gopdf"

	ttf "github.com/Cyber-Watcher/ollchat/internal/pdf"
)

// Шрифты встроены в бинарь, а не берутся из системы: приложение — один файл,
// и на машине пользователя нужного шрифта может не оказаться вовсе. Кириллицу
// стандартные шрифты PDF не покрывают, поэтому без встроенного TrueType
// русский текст в документе превратился бы в пустоту.
//
// Liberation выбран за лицензию (SIL OFL 1.1 разрешает встраивание) и за то,
// что в нём есть и кириллица, и латиница, и нужные знаки. Текст лицензии
// лежит рядом со шрифтами в fonts/OFL.txt.

//go:embed fonts/LiberationSans-Regular.ttf
var sansRegular []byte

//go:embed fonts/LiberationSans-Bold.ttf
var sansBold []byte

//go:embed fonts/LiberationSans-Italic.ttf
var sansItalic []byte

//go:embed fonts/LiberationMono-Regular.ttf
var monoRegular []byte

// Резервные шрифты. В Liberation нет знака рубля (U+20BD), галочек, звёзд,
// двойных и жирных рамок псевдографики — модель их пишет, а в документе на
// их месте оказывались пустые прямоугольники. Основной шрифт от этого не
// меняется: резерв подставляется по одному символу, там где основной пуст.
//
// Это урезанный DejaVu: из него вырезано только то, чего нет в Liberation
// (около тысячи знаков вместо тридцати тысяч, 424 КБ вместо 1.8 МБ).
// Собирается скриптом fonts/make_fallback.py — при сборке проекта не нужен
// ни он, ни fontTools. Гарнитура переименована: лицензия Bitstream Vera
// запрещает распространять изменённые шрифты под именем DejaVu. Текст
// лицензии — в fonts/DEJAVU-LICENSE.txt.

//go:embed fonts/OllchatFallback-Regular.ttf
var fallbackRegular []byte

//go:embed fonts/OllchatFallback-Bold.ttf
var fallbackBold []byte

//go:embed fonts/OllchatFallback-Mono.ttf
var fallbackMono []byte

// Имена семейств внутри документа.
const (
	familySans = "sans"
	familyMono = "mono"

	familyFallback     = "fallback"
	familyFallbackMono = "fallbackmono"
)

// wrapMark ставится в конце строки, которую пришлось разорвать по символам:
// длинная строка кода или ссылка не обрезана, а продолжается ниже.
//
// Знак выбран не на вкус: «↩», который просится по смыслу, в Liberation
// отсутствует и сам превращался бы в □ — проверено, см. TestServiceGlyphsExist.
const wrapMark = '»'

// noGlyph рисуется вместо символа, которого в шрифте нет.
//
// Умолчание gopdf здесь вредное — DefaultOnGlyphNotFoundSubstitute
// подставляет пробел, и, например, эмодзи из ответа исчезает бесследно.
// Видимый прямоугольник вместе с отчётом «символов без глифа: N» честнее:
// пользователь хотя бы знает, что потерял.
const noGlyph = '□'

// registerFonts грузит встроенные шрифты в документ.
//
// «Жирный» и «курсив» — отдельные файлы: gopdf не синтезирует ни утолщение,
// ни наклон, а требует шрифт, загруженный со своим Style (см. комментарий
// к SetFontWithStyle в gopdf). Сочетание «жирный курсив» вырождается
// в жирный — четвёртое начертание стоило бы ещё 400 КБ бинаря ради редкого
// случая.
func registerFonts(pdf *gopdf.GoPdf, missing map[rune]bool) (map[fontKey]map[rune]bool, error) {
	note := func(r rune) { missing[r] = true }
	sub := func(rune) rune { return noGlyph }

	covers := map[fontKey]map[rune]bool{}
	for _, f := range []struct {
		family string
		style  string
		data   []byte
		ttf    int
	}{
		{familySans, "", sansRegular, gopdf.Regular},
		{familySans, "B", sansBold, gopdf.Bold},
		{familySans, "I", sansItalic, gopdf.Italic},
		{familyMono, "", monoRegular, gopdf.Regular},

		{familyFallback, "", fallbackRegular, gopdf.Regular},
		{familyFallback, "B", fallbackBold, gopdf.Bold},
		{familyFallbackMono, "", fallbackMono, gopdf.Regular},
	} {
		opt := gopdf.TtfOption{
			Style:                     f.ttf,
			OnGlyphNotFound:           note,
			OnGlyphNotFoundSubstitute: sub,
		}
		if err := pdf.AddTTFFontDataWithOption(f.family, f.data, opt); err != nil {
			return nil, fmt.Errorf("встроенный шрифт %s: %w", f.family, err)
		}
		// Покрытие читаем из того же файла, что отдали gopdf: спросить
		// у библиотеки, умеет ли шрифт рисовать символ, до печати нельзя,
		// а знать это надо заранее — иначе не выбрать шрифт и не померить
		// ширину. Разбор cmap переиспользован из internal/pdf.
		covers[fontKey{f.family, f.style}] = ttf.TrueTypeRunes(f.data)
	}
	return covers, nil
}

// fontKey — пара «семейство, начертание», как их различает gopdf.
type fontKey struct{ family, style string }

// runStyle — начертание куска текста. Флаги складываются: жирный курсив
// возможен в разметке, даже если шрифт для него один.
type runStyle uint8

const (
	styleBold runStyle = 1 << iota
	styleItalic
	styleCode // моноширинный
	styleDim  // тусклый: подписи, адреса ссылок, язык блока кода
)

// fontFor переводит начертание в пару «семейство, стиль» для SetFont.
//
// Моноширинный побеждает: код внутри жирного заголовка остаётся кодом.
// Жирный побеждает курсив, потому что отдельного жирного курсива у нас нет.
func fontFor(s runStyle) (family, style string) {
	switch {
	case s&styleCode != 0:
		return familyMono, ""
	case s&styleBold != 0:
		return familySans, "B"
	case s&styleItalic != 0:
		return familySans, "I"
	default:
		return familySans, ""
	}
}

// fallbackFor подбирает резервный шрифт под то же начертание.
//
// Курсивного резерва нет: знак валюты в наклонном тексте берётся прямым.
// Разницу заметить трудно, а ещё одно начертание стоило бы 150 КБ бинаря.
func fallbackFor(s runStyle) (family, style string) {
	switch {
	case s&styleCode != 0:
		return familyFallbackMono, ""
	case s&styleBold != 0:
		return familyFallback, "B"
	default:
		return familyFallback, ""
	}
}
