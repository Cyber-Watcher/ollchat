package graph

import (
	"strings"
	"unicode"
)

// Текст куска для сверки «стоит ли это имя в тексте».
//
// Сверка строкой честна, только когда строки записаны одинаково, а текст,
// извлечённый из книги, записан как напечатан: слово разорвано переносом
// («Func\u2010\n   tionality»), внутри стоит мягкий перенос или невидимый знак,
// «fi» набрано лигатурой, дефис — типографский. Модель же называет понятие
// по-человечески: `Functionality`. До 17.09.2026 такие имена считались
// «не найденными в куске» — разбор 14 таких связей глазами показал, что
// выдумок среди них меньшинство, а две трети — дефекты записи текста.
//
// MatchText возвращает ДВА чтения куска, потому что перенос и составное слово
// внешне неразличимы: в joined разорванное слово склеено («алгоритмы»),
// в hyphened на месте разрыва оставлен дефис («специалистов-практиков»).
// Имя засчитывается, если нашлось в любом из двух.
func MatchText(text string) (joined, hyphened string) {
	var a, b strings.Builder
	a.Grow(len(text))
	b.Grow(len(text))
	both := func(s string) { a.WriteString(s); b.WriteString(s) }

	r := []rune(text)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\u00ad' || c == '\u2010' || c == '\u2011' || c == '\u2043' || c == '-':
			if next := wrapEnd(r, i); next > 0 {
				b.WriteByte('-')
				i = next - 1
				continue
			}
			if c == '\u00ad' {
				// Мягкий перенос посреди строки: в EPUB он невидим, в PDF так
				// бывает записан обычный дефис. Два чтения — два варианта.
				b.WriteByte('-')
				continue
			}
			both("-")
		case (c >= '\u200b' && c <= '\u200d') || c == '\u2060' || c == '\ufeff':
			// невидимые знаки нулевой ширины
		case c >= '\u0300' && c <= '\u036f':
			// ударение и прочие надстрочные знаки
		case c == '\ufb00':
			both("ff")
		case c == '\ufb01':
			both("fi")
		case c == '\ufb02':
			both("fl")
		case c == '\ufb03':
			both("ffi")
		case c == '\ufb04':
			both("ffl")
		case c == 'ё':
			both("е")
		case c == 'Ё':
			both("Е")
		case c == '\u00a0' || c == '\u2007' || c == '\u202f' || (c >= '\u2000' && c <= '\u200a'):
			both(" ")
		default:
			a.WriteRune(c)
			b.WriteRune(c)
		}
	}
	return strings.ToLower(collapseSpaces(a.String())), strings.ToLower(collapseSpaces(b.String()))
}

// MatchName приводит имя понятия к тому же виду, что MatchText — текст.
func MatchName(name string) string {
	joined, _ := MatchText(name)
	return joined
}

// wrapEnd — если знак i стоит в конце строки после буквы, а следующая строка
// начинается с буквы, возвращает место этой буквы; иначе 0. Пустая строка
// между ними — конец абзаца, там переноса нет.
func wrapEnd(r []rune, i int) int {
	if i == 0 || !unicode.IsLetter(r[i-1]) {
		return 0
	}
	newlines := 0
	for j := i + 1; j < len(r) && j < i+120; j++ {
		switch c := r[j]; {
		case c == '\n':
			newlines++
			if newlines > 1 {
				return 0
			}
		case c == ' ' || c == '\t' || c == '\r':
		case unicode.IsLetter(c):
			if newlines == 1 {
				return j
			}
			return 0
		default:
			return 0
		}
	}
	return 0
}

// SeenInText — стоит ли имя в тексте куска: фразой целиком, по границам слов,
// в любом из двух чтений. Имена короче трёх знаков находятся внутри чего
// угодно даже по границам («C», «R», «Go» в листинге), поэтому для них ответ
// «не знаю» выражен как false: вызывающий решает, считать ли такие вовсе.
func SeenInText(joined, hyphened, name string) bool {
	n := MatchName(name)
	if len([]rune(n)) < 3 {
		return false
	}
	return containsWord(joined, n) || containsWord(hyphened, n)
}
