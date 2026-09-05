package document

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Год издания книги.
//
// Зачем он нужен: знание из книги датировано, но в ответе модели выглядит
// вечным. «Ставится так-то» из книги 2018 года и из книги 2026-го — разные
// утверждения, и различить их можно только по году. С годом ответ получает
// честную оговорку, без года — не получает никакой.
//
// Ни один источник года не надёжен сам по себе, поэтому берутся три и в таком
// порядке:
//
//  1. **Имя файла.** Замер на библиотеке владельца (23.08.2026): год стоит
//     в имени у 216 файлов из 230, то есть у 94%. Имена собирает человек,
//     и в них обычно год того издания, которое он скачал.
//  2. **Копирайт с первых страниц.** «Copyright © 2019», «© 2019», «First
//     published 2019», «Издание 2019 года». Источник самый точный по смыслу,
//     но встречается не везде и путается на переизданиях, где перечислены
//     все годы сразу — берём наибольший.
//  3. **Метаданные файла.** `/CreationDate` у PDF и `<dc:date>` у EPUB. Это
//     дата **файла**: у скана старой книги она новее лет на двадцать, у
//     переизданной — новее издания. Поэтому идёт последней и помечается
//     как приблизительная.
//
// Год не используется для отбраковки книги — никогда. Только для оговорки.

// YearSource — откуда взялся год.
type YearSource string

const (
	YearNone     YearSource = ""           // года нет
	YearFromName YearSource = "имя файла"  //
	YearFromText YearSource = "копирайт"   //
	YearFromMeta YearSource = "метаданные" //
)

// Approximate сообщает, что году верить можно не вполне: он взят из даты файла.
func (s YearSource) Approximate() bool { return s == YearFromMeta }

var (
	// Год в имени файла: 1990–2039. Годы вне этого промежутка в именах книг
	// почти всегда часть названия («1984», «Проект 2000»), а не год издания.
	reNameYear = regexp.MustCompile(`(?:^|[^\d])((?:199|20[0-3])\d)(?:[^\d]|$)`)
	// Копирайт и первое издание на первых страницах.
	reCopyright = regexp.MustCompile(`(?i)(?:copyright|©|\(c\)|first published|published in|издание|подписано в печать)[^\n]{0,40}?((?:19|20)\d\d)`)
)

// YearFromFilename достаёт год из имени файла. 0 — не нашёлся.
//
// Когда чисел несколько («Книга 2-е издание (2019) 2021.pdf»), берётся
// наибольшее: переиздание новее оригинала, а номер издания в диапазон лет
// не попадает.
func YearFromFilename(path string) int {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	best := 0
	for _, m := range reNameYear.FindAllStringSubmatch(name, -1) {
		if y, err := strconv.Atoi(m[1]); err == nil && y > best {
			best = y
		}
	}
	return best
}

// YearFromCopyright ищет год в начале текста книги.
//
// text — первые страницы: дальше первых страниц копирайт не встречается,
// а «© 2011» в ссылке из середины книги — это чужой год.
func YearFromCopyright(text string) int {
	best := 0
	for _, m := range reCopyright.FindAllStringSubmatch(text, -1) {
		if y, err := strconv.Atoi(m[1]); err == nil && y > best {
			best = y
		}
	}
	return best
}

// PickYear выбирает год издания из трёх источников и говорит, откуда он взят.
//
// head — начало текста документа (первые страницы), meta — год из метаданных
// файла, path — путь к нему.
func PickYear(path, head string, meta int) (int, YearSource) {
	limit := time.Now().Year() + 1 // книга «следующего года» бывает, «через три» — нет
	ok := func(y int) bool { return y >= 1900 && y <= limit }

	if y := YearFromFilename(path); ok(y) {
		return y, YearFromName
	}
	if y := YearFromCopyright(head); ok(y) {
		return y, YearFromText
	}
	if ok(meta) {
		return meta, YearFromMeta
	}
	return 0, YearNone
}

// headOf отрезает начало текста для поиска копирайта.
func headOf(text string) string {
	const limit = 6000 // примерно первые три страницы
	r := []rune(text)
	if len(r) > limit {
		return string(r[:limit])
	}
	return text
}

// YearNote — оговорка о давности книги, годная для показа модели.
// Пустая строка означает, что оговорка не нужна.
//
// Порог в три года выбран так: за меньший срок в книгах по нашим темам мало
// что успевает устареть настолько, чтобы оговорка окупала место в контексте.
func YearNote(year int, now time.Time) string {
	if year <= 0 {
		return ""
	}
	age := now.Year() - year
	if age < 3 {
		return ""
	}
	return "книге " + strconv.Itoa(age) + " " + plural(age, "год", "года", "лет")
}

func plural(n int, one, few, many string) string {
	if mod := n % 100; mod >= 11 && mod <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// headParts собирает начало текста из первых частей документа: разбор по частям
// не строит общего текста, а копирайт лежит именно в начале.
func headParts(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if b.Len() > 6000 {
			break
		}
		b.WriteString(p.Text)
		b.WriteString("\n")
	}
	return headOf(b.String())
}
