package epub

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// Служебные главы: оглавление и предметный указатель.
//
// У PDF оглавление и указатель узнаются по строению куска — строка кончается
// номером страницы (kb.LooksLikeTOC). У EPUB номеров страниц нет: оглавление —
// это список заголовков, указатель — список терминов со ссылками, и по
// строению они неотличимы от списка команд или таблицы (пробник по 57 EPUB
// библиотеки, 18.09.2026: таблица прав доступа и «Command Summary» дают тот же
// профиль — короткие строки без знаков препинания). Поэтому глава узнаётся
// по трём признакам самой книги, и каждый из них подкрепляется строением:
//
//  1. манифест объявил главу навигационным документом (properties="nav");
//  2. файл главы зовётся toc/contents/index/ind/ix (56 глав из 62 по имени);
//  3. заголовок главы или одна из первых её строк — «Contents», «Table of
//     Contents», «Index», «Contents in detail», «Оглавление» — так выглядят
//     оглавления, спрятанные в part0003.xhtml, toc1.xhtml, c001.xhtml.
//
// Строение — доля строк без знака препинания на конце: у оглавлений
// и указателей 85–100 % (порог 80: заголовки-вопросы «Why Use The Shell?»
// тоже бывают), у прозы 61 % в среднем (это с кодом). Без этой подпорки
// «index.html» одной-файловой книги ушёл бы в служебные целиком.

var (
	// serviceName — имя файла главы без расширения и хвостовых цифр.
	serviceName = map[string]bool{
		"toc": true, "btoc": true, "contents": true, "nav": true,
		"index": true, "ind": true, "ix": true,
		"tableofcontents": true, "table_of_contents": true, "table-of-contents": true,
	}
	// serviceTitle — заголовок оглавления или указателя.
	serviceTitle = regexp.MustCompile(`(?i)^(brief |detailed )?(table of )?contents( in detail| at a glance)?$` +
		`|^(subject |alphabetical )?index$` +
		`|^(оглавление|содержание|предметный указатель|алфавитный указатель)$`)
	// headLines — сколько первых строк главы просматривается в поисках заголовка:
	// оглавление нередко идёт одним файлом с титульным листом.
	headLines = 15
)

// serviceSection решает, служебная ли глава: см. заголовок файла.
func serviceSection(href, title, text string, nav bool) bool {
	if nav {
		return true
	}
	lines, bare := listProfile(text)
	if lines < 8 || bare*100 < lines*80 {
		return false
	}
	base := strings.ToLower(strings.TrimSuffix(path.Base(href), path.Ext(href)))
	name := strings.TrimRightFunc(base, unicode.IsDigit)
	if serviceName[name] || strings.Contains(base, "_toc") || strings.Contains(base, "toc_") {
		return true
	}
	if serviceTitle.MatchString(strings.TrimSpace(title)) {
		return true
	}
	n := 0
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "•·-–—*"))
		if l == "" {
			continue
		}
		if serviceTitle.MatchString(l) {
			return true
		}
		if n++; n >= headLines {
			break
		}
	}
	return false
}

// listProfile считает непустые строки и те из них, что не кончаются знаком
// препинания предложения.
func listProfile(text string) (lines, bare int) {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines++
		switch l[len(l)-1] {
		case '.', ',', ':', ';', '!', '?':
		default:
			bare++
		}
	}
	return lines, bare
}
