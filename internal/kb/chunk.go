package kb

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/itpro/ollchat/internal/document"
)

// Разбиение книги на куски.
//
// Кусок — это единица поиска и единица цитирования. Отсюда три требования,
// которые и определили устройство:
//
//   - Кусок должен знать свою страницу, иначе ссылку в ответе модели нельзя
//     проверить. Поэтому режем постранично (document.Parts), а не по склеенному
//     тексту.
//   - Кусок должен быть понятен сам по себе: определение, разорванное границей,
//     бесполезно в обеих половинах. Отсюда перекрытие.
//   - Код и таблицы нельзя рвать посреди строки: половина команды не читается
//     ни человеком, ни моделью.
//
// Плюс отдельная забота — выбросить то, что засоряет индекс: колонтитулы,
// оглавления, обломки распознавания. Проверка на живой книге показала, что без
// этого частым термом становится не смысл, а типовая надпись со всех страниц.

// ChunkFlags помечает, что за текст в куске.
type ChunkFlags uint16

const (
	FlagCode    ChunkFlags = 1 << iota // код или вывод команд
	FlagTable                          // таблица
	FlagRussian                        // есть кириллица
	FlagEnglish                        // есть латиница
)

// Chunk — кусок книги.
type Chunk struct {
	Text     string
	UnitFrom int // страница или раздел, где кусок начинается
	UnitTo   int // где заканчивается
	Flags    ChunkFlags
}

// ChunkOpts задаёт размеры.
type ChunkOpts struct {
	Chars    int // целевая длина куска в символах
	Step     int // шаг: разница с Chars и есть перекрытие
	MinChars int // короче не индексируем
}

// DefaultChunkOpts — размеры по умолчанию.
//
// 1200 символов — это 350–400 токенов: у русского примерно 2.5 символа на
// токен, у английского около 4. Меньше — кусок теряет связность и даёт шум,
// заметно больше — размывается тема и падает точность. Перекрытие в 300
// символов стоит трети объёма индекса, но это дешевле промахов на определениях,
// разорванных границей.
func DefaultChunkOpts() ChunkOpts {
	return ChunkOpts{Chars: 1200, Step: 900, MinChars: 200}
}

// Split режет книгу на куски.
func Split(parts []document.Part, opt ChunkOpts) []Chunk {
	if opt.Chars <= 0 {
		opt = DefaultChunkOpts()
	}
	if opt.Step <= 0 || opt.Step > opt.Chars {
		opt.Step = opt.Chars * 3 / 4
	}

	drop := repeatedLines(parts)
	offset, hasOffset := pageNumberOffset(parts)
	var pieces []piece
	for _, p := range parts {
		printed := 0
		if hasOffset {
			printed = p.Number + offset
		}
		pieces = append(pieces, split(p, drop, printed, opt)...)
	}
	return pack(pieces, opt)
}

// piece — минимальная неделимая часть: абзац, блок кода или строка таблицы.
type piece struct {
	text  string
	unit  int
	flags ChunkFlags
	runes int
}

// tocLine ловит строку оглавления: текст, отточие, номер страницы.
var tocLine = regexp.MustCompile(`[.·•\x{2024}\x{2027}]{4,}\s*\d+\s*$`)

// split разбирает одну страницу на неделимые части. printed — печатный номер
// этой страницы, если его удалось определить; 0 — если нет.
func split(p document.Part, drop map[string]bool, printed int, opt ChunkOpts) []piece {
	lines := strings.Split(p.Text, "\n")
	edges := map[int]bool{}
	for _, i := range edgeIndexes(len(lines)) {
		edges[i] = true
	}

	// Страница оглавления целиком: пять строк с отточиями — верный признак.
	toc := 0
	for _, l := range lines {
		if tocLine.MatchString(l) {
			toc++
		}
	}
	if toc >= 5 {
		return nil
	}

	var out []piece
	var buf []string
	var bufFlags ChunkFlags

	flush := func() {
		if len(buf) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = buf[:0]
		f := bufFlags
		bufFlags = 0
		if !worthIndexing(text) {
			return
		}
		out = append(out, piece{text: text, unit: p.Number, flags: f | langFlags(text), runes: len([]rune(text))})
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if drop[normalizeLine(trimmed)] {
			continue // колонтитул, опознанный по повторяемости
		}
		if printed > 0 && edges[i] && isRunningHead(trimmed, printed) {
			continue // колонтитул, опознанный по номеру страницы
		}
		if tocLine.MatchString(line) {
			continue // одиночная строка оглавления
		}
		f := lineFlags(line)
		// Смена характера текста (проза ↔ код) — тоже граница части.
		if len(buf) > 0 && (f&FlagCode) != (bufFlags&FlagCode) {
			flush()
		}
		buf = append(buf, line)
		bufFlags |= f
	}
	flush()

	// Слишком длинные части режем: прозу по предложениям, код по строкам.
	var final []piece
	for _, pc := range out {
		if pc.runes <= opt.Chars*2 {
			final = append(final, pc)
			continue
		}
		final = append(final, cut(pc, opt)...)
	}
	return final
}

// cut режет слишком длинную часть, не ломая строки кода и предложения.
func cut(pc piece, opt ChunkOpts) []piece {
	var units []string
	if pc.flags&FlagCode != 0 || pc.flags&FlagTable != 0 {
		units = strings.Split(pc.text, "\n")
	} else {
		units = sentences(pc.text)
	}

	var out []piece
	var buf []string
	n := 0
	flush := func() {
		if len(buf) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(buf, sep(pc.flags)))
		buf, n = buf[:0], 0
		if worthIndexing(text) {
			out = append(out, piece{text: text, unit: pc.unit, flags: pc.flags, runes: len([]rune(text))})
		}
	}
	for _, u := range units {
		l := len([]rune(u))
		if n > 0 && n+l > opt.Chars {
			flush()
		}
		buf = append(buf, u)
		n += l
	}
	flush()
	return out
}

func sep(f ChunkFlags) string {
	if f&(FlagCode|FlagTable) != 0 {
		return "\n"
	}
	return " "
}

// pack собирает части в куски целевого размера с перекрытием.
func pack(pieces []piece, opt ChunkOpts) []Chunk {
	var out []Chunk
	i := 0
	for i < len(pieces) {
		var (
			buf   []string
			n     int
			flags ChunkFlags
			from  = pieces[i].unit
			to    = pieces[i].unit
			j     = i
		)
		for ; j < len(pieces); j++ {
			pc := pieces[j]
			if n > 0 && n+pc.runes > opt.Chars {
				break
			}
			buf = append(buf, pc.text)
			n += pc.runes
			flags |= pc.flags
			to = pc.unit
		}
		if len(buf) == 0 { // одна часть длиннее целевого размера
			pc := pieces[i]
			buf, n, flags, to, j = []string{pc.text}, pc.runes, pc.flags, pc.unit, i+1
		}
		text := strings.Join(buf, "\n\n")
		if n >= opt.MinChars || len(out) == 0 {
			out = append(out, Chunk{Text: text, UnitFrom: from, UnitTo: to, Flags: flags})
		}

		// Перекрытие: следующий кусок начинается так, чтобы захватить хвост
		// нынешнего длиной Chars-Step. Определение, разорванное границей,
		// целиком попадает хотя бы в один кусок.
		next := j
		if tail := opt.Chars - opt.Step; tail > 0 && j > i+1 {
			back, k := 0, j-1
			for ; k > i; k-- {
				back += pieces[k].runes
				if back >= tail {
					break
				}
			}
			if k > i && k < j {
				next = k
			}
		}
		if next <= i {
			next = i + 1
		}
		i = next
	}
	return out
}

// worthIndexing отсеивает мусор: номера страниц, обломки распознавания,
// строки из одних знаков.
func worthIndexing(text string) bool {
	var letters, total int
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if total < 12 {
		return false
	}
	// Доля букв ниже трети — это таблица чисел, формулы или мусор.
	return letters*3 >= total
}

// lineFlags определяет характер строки.
func lineFlags(line string) ChunkFlags {
	var f ChunkFlags
	if strings.Contains(line, " | ") {
		f |= FlagTable
	}
	if looksLikeCode(line) {
		f |= FlagCode
	}
	return f
}

var codePrefixes = []string{
	"func ", "class ", "def ", "package ", "import ", "#include", "public ",
	"private ", "var ", "const ", "let ", "return ", "if (", "for (", "while (",
	"$ ", "> ", "# ", "sudo ", "docker ", "kubectl ", "git ",
}

// looksLikeCode — признак строки кода или вывода команды.
func looksLikeCode(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}
	for _, p := range codePrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	// Отступ плюс знаки препинания программ.
	indent := len(line) - len(trimmed)
	var syms int
	for _, r := range trimmed {
		switch r {
		case '{', '}', ';', '(', ')', '=', '<', '>', '/', '*', '[', ']':
			syms++
		}
	}
	return indent >= 2 && syms*8 >= len([]rune(trimmed))
}

func langFlags(text string) ChunkFlags {
	var f ChunkFlags
	for _, r := range text {
		switch {
		case r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё':
			f |= FlagRussian
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			f |= FlagEnglish
		}
		if f == FlagRussian|FlagEnglish {
			break
		}
	}
	return f
}

// repeatedLines находит колонтитулы: строки, повторяющиеся на многих страницах.
//
// Без этого «Chapter 7 | Concurrency» и адрес издательства становятся одними
// из самых частых термов книги и портят ранжирование, ничего не значая.
func repeatedLines(parts []document.Part) map[string]bool {
	if len(parts) < 5 {
		return nil
	}
	count := map[string]int{}
	for _, p := range parts {
		lines := strings.Split(p.Text, "\n")
		seen := map[string]bool{}
		// Смотрим только края страницы: колонтитул стоит сверху или снизу.
		for _, idx := range edgeIndexes(len(lines)) {
			raw := strings.TrimSpace(lines[idx])
			// Длинная строка колонтитулом не бывает: там название главы
			// или адрес сайта, а не абзац.
			if len([]rune(raw)) > 90 {
				continue
			}
			l := normalizeLine(raw)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			count[l]++
		}
	}
	// Порог намеренно низкий. Колонтитул меняется от главы к главе
	// («Глава 7. Доступ к файлам», «Глава 8. Веб-приложения»), поэтому каждый
	// отдельный вариант встречается лишь на десятой части страниц книги — при
	// пороге в треть они все проходили в индекс. Найдено на живой книге.
	need := len(parts) / 20
	if need < 4 {
		need = 4
	}
	out := map[string]bool{}
	for l, n := range count {
		if n >= need {
			out[l] = true
		}
	}
	return out
}

// pageNumberOffset выясняет, на сколько печатный номер страницы отличается от
// порядкового: у книг вначале идут титул и оглавление, поэтому «страница 122»
// по счёту может быть 130-й.
//
// Признак колонтитула по номеру надёжнее повторяемости: типовая надпись меняется
// от раздела к разделу («Работа с переменными», «Глава 2 • Говорим на языке C#»)
// и по отдельности встречается на десятке страниц из тысячи, а номер страницы
// в ней есть всегда. Найдено на живой книге, где иначе колонтитулы оставались
// в трети кусков.
func pageNumberOffset(parts []document.Part) (int, bool) {
	if len(parts) < 10 {
		return 0, false
	}
	votes := map[int]int{}
	for _, p := range parts {
		lines := strings.Split(p.Text, "\n")
		for _, idx := range edgeIndexes(len(lines)) {
			l := strings.TrimSpace(lines[idx])
			if l == "" || len([]rune(l)) > 90 {
				continue
			}
			for _, n := range edgeNumbers(l) {
				votes[n-p.Number]++
			}
		}
	}
	best, bestN := 0, 0
	for off, n := range votes {
		if n > bestN {
			best, bestN = off, n
		}
	}
	// Смещение должно подтверждаться заметной долей страниц, иначе это
	// случайные числа в тексте.
	if bestN*4 < len(parts) {
		return 0, false
	}
	return best, true
}

// edgeNumbers достаёт числа, стоящие с краю строки: номер страницы печатают
// в начале или в конце колонтитула, а не посередине.
func edgeNumbers(line string) []int {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	var out []int
	for _, f := range []string{fields[0], fields[len(fields)-1]} {
		if n, ok := atoiSmall(f); ok {
			out = append(out, n)
		}
	}
	return out
}

func atoiSmall(s string) (int, bool) {
	if s == "" || len(s) > 4 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n > 0
}

// isRunningHead сообщает, что строка на краю страницы — колонтитул: она коротка
// и содержит с краю печатный номер этой самой страницы.
func isRunningHead(line string, printed int) bool {
	if len([]rune(line)) > 90 {
		return false
	}
	for _, n := range edgeNumbers(line) {
		if n == printed {
			return true
		}
	}
	return false
}

func edgeIndexes(n int) []int {
	var idx []int
	for i := 0; i < 2 && i < n; i++ {
		idx = append(idx, i)
	}
	for i := n - 2; i < n; i++ {
		if i >= 2 {
			idx = append(idx, i)
		}
	}
	return idx
}

// normalizeLine убирает из строки номера страниц, чтобы колонтитул опознавался
// одинаково на всех страницах: «Глава 7 · 154» и «Глава 7 · 155» — одно и то же.
func normalizeLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// sentences режет прозу по границам предложений, не спотыкаясь о сокращения.
func sentences(text string) []string {
	runes := []rune(text)
	var out []string
	start := 0
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '.', '!', '?', '…':
		default:
			continue
		}
		if i+1 >= len(runes) {
			continue
		}
		if !unicode.IsSpace(runes[i+1]) {
			continue // «v1.2», «и.о.»
		}
		// Сокращения вроде «т.д.», «рис.», «Fig.» — не конец предложения.
		if abbrev(runes[:i+1]) {
			continue
		}
		// Следующее слово должно начинаться с заглавной или цифры.
		j := i + 1
		for j < len(runes) && unicode.IsSpace(runes[j]) {
			j++
		}
		if j < len(runes) && !unicode.IsUpper(runes[j]) && !unicode.IsDigit(runes[j]) {
			continue
		}
		out = append(out, strings.TrimSpace(string(runes[start:j])))
		start = j
		i = j - 1
	}
	if start < len(runes) {
		if s := strings.TrimSpace(string(runes[start:])); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var abbrevs = []string{"т.д", "т.п", "т.е", "рис", "табл", "гл", "см", "стр", "др",
	"fig", "eq", "ex", "vs", "etc", "e.g", "i.e", "no", "vol", "ch"}

func abbrev(upto []rune) bool {
	// Последнее слово перед точкой.
	end := len(upto) - 1
	start := end
	for start > 0 && !unicode.IsSpace(upto[start-1]) {
		start--
	}
	w := strings.ToLower(strings.TrimRight(string(upto[start:end]), "."))
	for _, a := range abbrevs {
		if w == a {
			return true
		}
	}
	// Одна БУКВА с точкой — инициал: «А. С. Пушкин». Одиночная цифра — нет:
	// «табл. 3.» заканчивает предложение, а «3» инициалом не бывает.
	r := []rune(w)
	return len(r) == 1 && unicode.IsLetter(r[0])
}
