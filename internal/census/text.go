// Переписи порчи текста в сохранённых кусках: испорченные переносы, точки
// вместо пробелов, слипшиеся слова. Пять программ, перенесённых 30.09.2026
// из `privatescripts/` (этап 114, пункт Г2) режимами одного семейства.
//
// **Состав перенесён не в один общий счёт, а пятью отдельными функциями.**
// План предполагал, что `textcensus` перекрывает `hyphcheck` и `dotcheck`
// целиком, но по коду это не так: у каждой программы свой порог и свой обход.
//
//   - `hyphcheck` смотрит КАЖДЫЙ ПЯТЫЙ кусок и считает мягкий перенос без
//     проверки соседних букв, деля находки на «конец строки» / «внутри
//     строки» по тому, есть ли после знака ещё текст в строке.
//   - `textCorruption` (бывший `textcensus`) обходит ВСЕ куски и признаёт
//     перенос только когда по обе стороны буквы — иначе, например, дефис
//     в списке или тире дают ложные находки.
//
// Оба вопроса правильные, но числа на одних и тех же данных разные:
// `textHyphens` сохранён СВОЕЙ функцией, а не сведён в `textCorruption`.
//
//   - `dotcheck` меряет ОБЩУЮ плотность точек в куске целиком: подозрителен
//     кусок, где точек между буквами больше, чем пробелов (10 и больше).
//     Это признак «пробелы заменены точками всюду».
//   - `textCorruption`'s `textDotRun` меряет ЛОКАЛЬНУЮ цепочку: три подряд
//     идущих «слово.слово» без пробела между. Кусок может пройти по одному
//     признаку и не пройти по другому — предметы разные, не подмножества.
//
// `textLongWords` и `textDotPrefix` — тоже свои функции: первая отсеивает
// законные длинные идентификаторы (без заглавных внутри, не в листинге),
// чего у `textCorruption`'s `textLongWordKind` нет вовсе; вторая ищет
// «пробел, точка, слово» — род порчи, который ни один из первых не видит.
//
// `reparsecensus` (`privatescripts/reparsecensus`) сюда НЕ перенесён: он
// перечитывает книги заново с диска и сравнивает с хранимыми кусками —
// предмет другой (не признак в уже сохранённом тексте, а сверка разбора).
package census

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/document"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// --- textCorruption (бывший privatescripts/textcensus): все роды разом ---

// textKind — род находки. Порядок задаёт порядок строк отчёта.
type textKind int

const (
	textShyEOL       textKind = iota // U+00AD на конце строки, дальше строчная буква
	textShyMid                       // U+00AD внутри строки между буквами
	textUniHypEOL                    // U+2010/2011/2043 на конце строки
	textUniHypMid                    // они же внутри строки между буквами
	textAsciiHypEOL                  // обычный «-» на конце строки, дальше строчная буква
	textHypSpaceMid                  // «Func\u2010 tionality»: знак переноса, пробел, строчная — внутри строки
	textLigature                     // U+FB00–FB06
	textZeroWidth                    // U+200B–200D, U+2060, U+FEFF внутри текста
	textCombining                    // разложенные знаки: буква + U+0300–036F
	textNbsp                         // U+00A0, U+2007, U+202F, U+2000–200A
	textPrivateUse                   // U+E000–F8FF: глиф без юникода
	textReplacement                  // U+FFFD
	textDotRun                       // «слово.слово.слово»: три и больше подряд
	textLongWordKind                 // буквенное слово длиннее 30 знаков: слиплось
	textKindsCount
)

var textKindName = [textKindsCount]string{
	"U+00AD на конце строки",
	"U+00AD внутри строки",
	"U+2010/2011/2043 на конце строки",
	"U+2010/2011/2043 внутри слова",
	"обычный «-» на конце строки + строчная",
	"знак переноса + пробел + строчная внутри строки",
	"лигатуры U+FB00–FB06",
	"невидимые знаки нулевой ширины",
	"разложенные знаки (буква + U+03xx)",
	"неразрывные и типографские пробелы",
	"знаки частного пользования U+E000–F8FF",
	"знак замены U+FFFD",
	"цепочки «слово.слово.слово»",
	"буквенные слова длиннее 30 знаков",
}

// textTally — счёт одного рода: в скольких кусках встретился и сколько всего находок.
type textTally struct {
	chunks [textKindsCount]int
	hits   [textKindsCount]int
}

func textIsUniHyphen(r rune) bool { return r == '\u2010' || r == '\u2011' || r == '\u2043' }

func textIsZeroWidth(r rune) bool {
	return (r >= '\u200b' && r <= '\u200d') || r == '\u2060' || r == '\ufeff'
}

func textIsOddSpace(r rune) bool {
	return r == '\u00a0' || r == '\u2007' || r == '\u202f' || (r >= '\u2000' && r <= '\u200a')
}

// textNextLineStart — первая непробельная руна после конца строки at (индекс
// '\n' или конца), либо 0. Пустая строка между — конец абзаца, переноса нет.
func textNextLineStart(r []rune, i int) rune {
	j := i
	for j < len(r) && (r[j] == ' ' || r[j] == '\t' || r[j] == '\r') {
		j++
	}
	if j >= len(r) || r[j] != '\n' {
		return 0
	}
	j++
	for j < len(r) && (r[j] == ' ' || r[j] == '\t' || r[j] == '\r') {
		j++
	}
	if j >= len(r) || r[j] == '\n' {
		return 0
	}
	return r[j]
}

// textScanCorruption обходит текст куска и зовёт hit на каждой находке.
func textScanCorruption(text string, hit func(k textKind, at int)) {
	r := []rune(text)
	wordLen, dots := 0, 0
	for i, ch := range r {
		prevLetter := i > 0 && unicode.IsLetter(r[i-1])
		nextLetter := i+1 < len(r) && unicode.IsLetter(r[i+1])
		switch {
		case ch == '\u00ad':
			if prevLetter && unicode.IsLetter(textNextLineStart(r, i+1)) {
				hit(textShyEOL, i)
			} else if prevLetter && nextLetter {
				hit(textShyMid, i)
			} else if prevLetter && i+2 < len(r) && r[i+1] == ' ' && unicode.IsLower(r[i+2]) {
				hit(textHypSpaceMid, i)
			}
		case textIsUniHyphen(ch):
			if prevLetter && unicode.IsLetter(textNextLineStart(r, i+1)) {
				hit(textUniHypEOL, i)
			} else if prevLetter && nextLetter {
				hit(textUniHypMid, i)
			} else if prevLetter && i+2 < len(r) && r[i+1] == ' ' && unicode.IsLower(r[i+2]) {
				hit(textHypSpaceMid, i)
			}
		case ch == '-':
			if prevLetter && unicode.IsLower(textNextLineStart(r, i+1)) {
				hit(textAsciiHypEOL, i)
			}
		case ch >= '\ufb00' && ch <= '\ufb06':
			hit(textLigature, i)
		case textIsZeroWidth(ch):
			hit(textZeroWidth, i)
		case ch >= '\u0300' && ch <= '\u036f':
			if prevLetter {
				hit(textCombining, i)
			}
		case textIsOddSpace(ch):
			hit(textNbsp, i)
		case ch >= '\ue000' && ch <= '\uf8ff':
			hit(textPrivateUse, i)
		case ch == '\ufffd':
			hit(textReplacement, i)
		}
		// Цепочки «слово.слово.слово» и слипшиеся слова.
		if unicode.IsLetter(ch) {
			wordLen++
			if wordLen == 31 {
				hit(textLongWordKind, i)
			}
		} else {
			wordLen = 0
			if ch == '.' && prevLetter && nextLetter {
				dots++
				if dots == 3 {
					hit(textDotRun, i)
				}
			} else if ch == ' ' || ch == '\n' {
				dots = 0
			}
		}
	}
}

func textAround(text string, at, span int) string {
	r := []rune(text)
	lo, hi := at-span, at+span
	if lo < 0 {
		lo = 0
	}
	if hi > len(r) {
		hi = len(r)
	}
	return fmt.Sprintf("%q", string(r[lo:hi]))
}

func textCutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// textCorruption — перепись всех родов порчи разом и по всем кускам,
// с разрезом по формату и книгам (бывший `privatescripts/textcensus`).
// top — сколько книг показать на род, examples — сколько примеров на род.
func textCorruption(stdout io.Writer, cfg *config.Config, collName string, top, examples int) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintln(os.Stderr, "читаю куски коллекции…")
	total := map[string]*textTally{}   // по формату
	perBook := map[string]*textTally{} // по книге
	bookChunks := map[string]int{}
	formatChunks := map[string]int{}
	examplesByKind := [textKindsCount][]string{}
	exBook := [textKindsCount]map[string]bool{}
	for k := range exBook {
		exBook[k] = map[string]bool{}
	}
	all := 0
	// Знаки переноса САМИ ПО СЕБЕ, без проверки соседних букв: так считал
	// `hyphcheck` (удалён 30.09.2026, этап 114, Г2). Роды выше требуют букв
	// с обеих сторон, поэтому знак между цифрой и пробелом они не видят —
	// это число сохранено, чтобы с удалением прибора сигнал не потерялся.
	bareHyphens, bareChunks := 0, 0

	err = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		all++
		if n := textCountBare(ci.Text); n > 0 {
			bareHyphens += n
			bareChunks++
		}
		format := strings.ToLower(strings.TrimPrefix(filepath.Ext(ci.Book.Path), "."))
		book := filepath.Base(ci.Book.Path)
		formatChunks[format]++
		bookChunks[book]++
		var seen [textKindsCount]bool
		textScanCorruption(ci.Text, func(k textKind, at int) {
			for _, key := range []string{format, "всего"} {
				t := total[key]
				if t == nil {
					t = &textTally{}
					total[key] = t
				}
				t.hits[k]++
				if !seen[k] {
					t.chunks[k]++
				}
			}
			t := perBook[book]
			if t == nil {
				t = &textTally{}
				perBook[book] = t
			}
			t.hits[k]++
			if !seen[k] {
				t.chunks[k]++
			}
			seen[k] = true
			// Примеры — из разных книг, иначе одна книга займёт все места.
			if len(examplesByKind[k]) < examples && !exBook[k][book] {
				exBook[k][book] = true
				examplesByKind[k] = append(examplesByKind[k], textAround(ci.Text, at, 22)+"  ← "+textCutRunes(book, 50))
			}
		})
		return nil
	})
	if err != nil {
		return err
	}
	formatChunks["всего"] = all

	fmt.Fprintf(stdout, "кусков просмотрено: %d (все)\n", all)
	fmt.Fprintf(stdout, "знаков переноса без проверки соседей: %d в %d кусках (счёт бывшего hyphcheck)\n",
		bareHyphens, bareChunks)
	formats := make([]string, 0, len(formatChunks))
	for f := range formatChunks {
		formats = append(formats, f)
	}
	// Порядок при равных числах задаётся вторым признаком: имя формата. Без него
	// порядок брался из обхода карты и МЕНЯЛСЯ между прогонами — старый
	// `dotprefixcensus` дал два разных ответа на двух прогонах подряд (проверено
	// 30.09.2026). Прибор обязан быть воспроизводим, иначе его вывод
	// нельзя ни сверить, ни процитировать.
	sort.Slice(formats, func(i, j int) bool {
		if formatChunks[formats[i]] != formatChunks[formats[j]] {
			return formatChunks[formats[i]] > formatChunks[formats[j]]
		}
		return formats[i] < formats[j]
	})
	for k := textKind(0); k < textKindsCount; k++ {
		fmt.Fprintf(stdout, "\n== %s\n", textKindName[k])
		for _, f := range formats {
			t := total[f]
			if t == nil || t.chunks[k] == 0 {
				continue
			}
			fmt.Fprintf(stdout, "   %-6s кусков %7d из %7d (%5.2f%%), находок %d\n", f, t.chunks[k], formatChunks[f],
				100*float64(t.chunks[k])/float64(formatChunks[f]), t.hits[k])
		}
		type row struct {
			book string
			n    int
		}
		var rows []row
		for b, t := range perBook {
			if t.chunks[k] > 0 {
				rows = append(rows, row{b, t.chunks[k]})
			}
		}
		// Второй признак — имя книги: см. пояснение выше про воспроизводимость.
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].n != rows[j].n {
				return rows[i].n > rows[j].n
			}
			return rows[i].book < rows[j].book
		})
		fmt.Fprintf(stdout, "   книг с этим родом: %d\n", len(rows))
		for i, rw := range rows {
			if i >= top {
				break
			}
			fmt.Fprintf(stdout, "     %6d из %6d (%5.1f%%)  %s\n", rw.n, bookChunks[rw.book],
				100*float64(rw.n)/float64(bookChunks[rw.book]), textCutRunes(rw.book, 80))
		}
		for _, e := range examplesByKind[k] {
			fmt.Fprintln(stdout, "   пример:", e)
		}
	}
	return nil
}

// --- счёт знаков переноса, оставшийся от hyphcheck ---

// textIsSoftHyphen — один из четырёх знаков переноса (U+00AD, U+2010, U+2011,
// U+2043) сам по себе, без проверки соседних букв.
func textIsSoftHyphen(r rune) bool {
	switch r {
	case '\u00ad', '\u2010', '\u2011', '\u2043':
		return true
	}
	return false
}

// textCountBare — сколько в куске знаков переноса без всякой проверки соседей.
// Единственное, что умел `hyphcheck` и чего не дают роды порчи выше: они
// требуют букв с обеих сторон знака, и знак между цифрой и пробелом в них
// не попадает. Прибор удалён 30.09.2026 (этап 114, Г2), число осталось здесь.
func textCountBare(text string) int {
	n := 0
	for _, r := range text {
		if textIsSoftHyphen(r) {
			n++
		}
	}
	return n
}

// --- textDots (бывший privatescripts/dotcheck): точки вместо пробелов ---

var (
	textDotBetweenLetters = regexp.MustCompile(`[а-яa-zА-ЯA-Z]\.[а-яa-zА-ЯA-Z]`)
	textSoftHyphenRun     = regexp.MustCompile("[\u00ad\u2010\u2011\u2043\u2010]")
)

// textDots — сколько кусков испорчено вёрсткой: точки вместо пробелов,
// мягкие переносы. Смотрит КАЖДЫЙ ТРЕТИЙ кусок (бывший `dotcheck`).
//
// Этот режим НЕ перекрыт общей переписью, хотя план этапа 114 так полагал:
// проверено 30.09.2026 по коду — предикаты разные (см. ниже).
//
// Признак «точки вместо пробелов» — это ОБЩАЯ плотность на весь кусок
// (точек между буквами не меньше 10 и больше, чем пробелов), а не цепочка
// подряд идущих точек, как у `textCorruption`'s `textDotRun`: кусок может
// пройти по одному признаку и не пройти по другому, предметы разные.
func textDots(stdout io.Writer, cfg *config.Config, collName string) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintln(os.Stderr, "читаю куски коллекции (каждый третий)…")
	// Разрезы: формат файла и язык книги — чтобы знать, беда в разборе PDF
	// или шире (вопрос владельца 17.09.2026).
	type slice struct{ total, dots int }
	byKind := map[string]*slice{}
	add := func(key string, bad bool) {
		s := byKind[key]
		if s == nil {
			s = &slice{}
			byKind[key] = s
		}
		s.total++
		if bad {
			s.dots++
		}
	}
	var total, dots, hyph, both int
	byBook := map[string]int{}
	allBook := map[string]int{}
	n := 0
	err = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		n++
		if n%3 != 0 { // каждый третий: нужен список книг, а не только доля
			return nil
		}
		total++
		allBook[ci.Book.Title]++
		t := ci.Text
		// **Признак уточнён 17.09.2026.** Считать просто точки между буквами
		// нельзя: в технических книгах `fmt.Println`, `os.Open`, `github.com`
		// — норма, и такой счёт ловил код, а не порчу (первый прогон дал
		// 6,2% и «EPUB тоже испорчен», хотя у EPUB шрифтов нет вовсе).
		//
		// Порча видна по ОТНОШЕНИЮ: там, где точки заменили пробелы, точек
		// между буквами больше, чем самих пробелов. В обычном тексте с кодом
		// отношение около 0,3; в испорченном — больше единицы.
		nd := len(textDotBetweenLetters.FindAllString(t, -1))
		nsp := strings.Count(t, " ")
		d := nd >= 10 && nd > nsp
		h := len(textSoftHyphenRun.FindAllString(t, -1)) >= 3
		if d {
			dots++
			byBook[ci.Book.Title]++
		}
		ext := "pdf"
		low := strings.ToLower(ci.Book.Path)
		switch {
		case strings.HasSuffix(low, ".epub"):
			ext = "epub"
		case strings.HasSuffix(low, ".md"), strings.HasSuffix(low, ".txt"):
			ext = "текст"
		}
		// Язык — по доле кириллицы в куске: имя файла врёт (русские издания
		// часто названы латиницей).
		cyr := 0
		for _, r := range t {
			if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' {
				cyr++
			}
		}
		lang := "англ"
		if len(t) > 0 && float64(cyr)/float64(len([]rune(t))) > 0.25 {
			lang = "рус"
		}
		add(ext, d)
		add(ext+" · "+lang, d)
		if h {
			hyph++
		}
		if d && h {
			both++
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Исходный `dotcheck` печатал здесь «каждый седьмой», а сэмплировал
	// каждый третий (`n%3 != 0`). Подпись исправлена 30.09.2026: единственная
	// строка, которой новый вывод отличается от прежнего.
	fmt.Fprintf(stdout, "кусков просмотрено (каждый третий): %d\n", total)
	fmt.Fprintf(stdout, "  точки вместо пробелов (10+ на кусок): %d (%.2f%%)\n", dots, 100*float64(dots)/float64(max(total, 1)))
	fmt.Fprintf(stdout, "  мягкие переносы (3+ на кусок):        %d (%.2f%%)\n", hyph, 100*float64(hyph)/float64(max(total, 1)))
	fmt.Fprintf(stdout, "  и то и другое:                        %d (%.2f%%)\n", both, 100*float64(both)/float64(max(total, 1)))

	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range byBook {
		list = append(list, kv{k, v})
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].v > list[i].v {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	fmt.Fprintln(stdout, "\nпо формату и языку:")
	for _, k := range []string{"pdf", "pdf · рус", "pdf · англ", "epub", "epub · рус", "epub · англ", "текст"} {
		if sl := byKind[k]; sl != nil && sl.total > 0 {
			fmt.Fprintf(stdout, "  %-12s кусков %6d, с точками %6d (%5.2f%%)\n",
				k, sl.total, sl.dots, 100*float64(sl.dots)/float64(sl.total))
		}
	}
	fmt.Fprintf(stdout, "\nкниг затронуто: %d из %d\n", len(byBook), len(allBook))
	fmt.Fprintln(stdout, "\nкниги с точками вместо пробелов (доля их кусков):")
	for i, x := range list {
		if i >= 25 {
			break
		}
		name := x.k
		if len([]rune(name)) > 54 {
			name = string([]rune(name)[:54]) + "…"
		}
		share := 100 * float64(x.v) / float64(allBook[x.k])
		fmt.Fprintf(stdout, "  %5d из %5d (%5.1f%%)  %s\n", x.v, allBook[x.k], share, strings.TrimSpace(name))
	}
	return nil
}

// --- textLongWords (бывший privatescripts/longwords): слипшиеся слова ---

// textLongWords — примеры слипшихся слов по книгам. `textCorruption`'s
// `textLongWordKind` считает буквенные слова длиннее 30 знаков и в счёт
// попадают законные идентификаторы (forceConsistentCasingInFileNames);
// здесь они отсеяны: слово без заглавных внутри и без цифр, не в листинге —
// это слипшийся текст, а не имя. minLen — с какой длины считать слипшимся,
// top — сколько книг показать.
func textLongWords(stdout io.Writer, cfg *config.Config, collName string, minLen, top int) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintln(os.Stderr, "читаю куски коллекции…")
	hits := map[string]int{}
	chunks := map[string]int{}
	total := map[string]int{}
	ex := map[string][]string{}
	all, bad := 0, 0
	err = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		all++
		book := filepath.Base(ci.Book.Path)
		total[book]++
		if ci.Code {
			return nil
		}
		r := []rune(ci.Text)
		found := false
		start := -1
		for i := 0; i <= len(r); i++ {
			if i < len(r) && unicode.IsLetter(r[i]) {
				if start < 0 {
					start = i
				}
				continue
			}
			if start >= 0 && i-start >= minLen {
				w := r[start:i]
				plain := true
				for k, x := range w {
					if k > 0 && unicode.IsUpper(x) {
						plain = false
						break
					}
				}
				if plain {
					hits[book]++
					found = true
					if len(ex[book]) < 3 {
						ex[book] = append(ex[book], string(w))
					}
				}
			}
			start = -1
		}
		if found {
			chunks[book]++
			bad++
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "кусков %d, со слипшимися словами (≥%d букв, без заглавных внутри, не листинг): %d (%.2f%%), книг %d\n",
		all, minLen, bad, 100*float64(bad)/float64(max(all, 1)), len(chunks))
	books := make([]string, 0, len(chunks))
	for b := range chunks {
		books = append(books, b)
	}
	// Второй признак — имя книги: порядок не должен зависеть от обхода карты.
	sort.Slice(books, func(i, j int) bool {
		if chunks[books[i]] != chunks[books[j]] {
			return chunks[books[i]] > chunks[books[j]]
		}
		return books[i] < books[j]
	})
	for i, b := range books {
		if i >= top {
			break
		}
		fmt.Fprintf(stdout, "%5d из %5d (%5.1f%%) %.70s\n", chunks[b], total[b], 100*float64(chunks[b])/float64(total[b]), b)
		for _, e := range ex[b] {
			fmt.Fprintf(stdout, "        %.90s\n", e)
		}
	}
	return nil
}

// --- textDotPrefix (бывший privatescripts/dotprefixcensus): точка перед словом ---

// dotPrefixShare — считает буквенные слова куска и сколько из них начинаются
// с точки перед буквой: «.выполнить .действие». Общий предикат для
// textDotPrefix (куски уже в коллекции) и textDotPrefixFresh (файл книги,
// разобранный заново с диска) — вынесен в отдельную функцию, чтобы у одного
// признака не завелось две копии (правило 0 проекта).
func dotPrefixShare(text string) (words, dotted int) {
	for _, w := range strings.Fields(text) {
		r := []rune(w)
		if len(r) < 2 {
			continue
		}
		if unicode.IsLetter(r[0]) {
			words++
		} else if r[0] == '.' && unicode.IsLetter(r[1]) {
			words++
			dotted++
		}
	}
	return words, dotted
}

// textDotPrefix — куски, где слова начинаются с точки: «возможности
// .выполнить .действие .с .помощью» (найдено 18.09.2026 в «Паттерны
// проектирования API»). Род, которого не видят ни `textDotRun`, ни `textDots`:
// там точка СТОИТ МЕЖДУ буквами, здесь — ПЕРЕД словом после пробела.
func textDotPrefix(stdout io.Writer, cfg *config.Config, collName string) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintln(os.Stderr, "читаю куски коллекции…")
	type stat struct {
		path        string
		chunks, bad int
	}
	books := map[uint32]*stat{}
	docs := map[uint32]string{}
	for _, d := range c.MatchingDocs(kb.ChunkFilter{}) {
		docs[d.ID] = d.Path
	}
	total, bad := 0, 0
	err = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		total++
		words, dotted := dotPrefixShare(ci.Text)
		b := books[ci.Doc]
		if b == nil {
			b = &stat{path: docs[ci.Doc]}
			books[ci.Doc] = b
		}
		b.chunks++
		if words >= 20 && dotted*4 >= words { // от четверти слов с точкой впереди
			bad++
			b.bad++
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "кусков %d, с точкой перед словами (от четверти слов) %d (%.2f%%)\n", total, bad, 100*float64(bad)/float64(max(total, 1)))
	var list []*stat
	for _, b := range books {
		if b.bad > 0 {
			list = append(list, b)
		}
	}
	// Второй признак — путь книги: порядок не должен зависеть от обхода карты.
	sort.Slice(list, func(i, j int) bool {
		if list[i].bad != list[j].bad {
			return list[i].bad > list[j].bad
		}
		return list[i].path < list[j].path
	})
	fmt.Fprintf(stdout, "книг с таким родом: %d\n", len(list))
	for i, b := range list {
		if i >= 20 {
			break
		}
		p := b.path
		if k := strings.LastIndex(p, "/"); k >= 0 {
			p = p[k+1:]
		}
		fmt.Fprintf(stdout, "  %5d из %5d (%5.1f%%)  %s\n", b.bad, b.chunks, 100*float64(b.bad)/float64(b.chunks), p)
	}
	return nil
}

// --- textDotPrefixFresh (восстановлен 30.09.2026 взамен утраченного privatescripts/dotprefixcensus/fresh) ---

// textDotPrefixFresh — тот же признак «точка перед словом», что и
// textDotPrefix, но не по кускам, уже лежащим в коллекции, а по файлу книги
// с диска, разобранному ЗАНОВО нынешним кодом извлечения — минуя индекс.
// Отвечает на вопрос «починит ли книгу перечитывание»: если в индексе кусков
// с этим родом порчи много, а свежий разбор даёт ноль, значит порча внесена
// старым кодом разбора и лечится перечитыванием, а не новым признаком.
//
// Путь разбора — тот же, что у индексации (internal/kb/index.go, parseBook):
// сперва дешёвая проба document.Probe отсеивает сканы, затем document.Parts
// делит файл на страницы или разделы, и для текстовых книг (.txt, .md) куски
// режет kb.SplitText, для остальных — kb.Split. Настройки нарезки — те же,
// что берёт коллекция при создании (kb.DefaultChunkOpts): собственного поля
// «размер куска» в конфиге нет, meta.Chunk коллекции всегда получает это же
// умолчание (internal/kb/kb.go, Base.Create). Предел размера файла —
// cfg.KB.MaxBookMB, как у IndexOpts.MaxBytes при индексации.
//
// 30.09.2026 сам инструмент (privatescripts/dotprefixcensus/fresh) удалён
// случайно вместе с каталогом; исходника не осталось нигде — ни в git
// (privatescripts/ не отслеживается), ни в архиве Resilio, ни в индексе
// projectdocs. Записанные им числа 18.09.2026 сохранены в
// docs/eval/audit-2026-09-17/17-epub-service-and-runheads.md: «Паттерны
// проектирования API» — 1 707 кусков, с точкой перед словами 0 (в индексе
// было 95%); «CLR via C#» — 2 529 кусков, 0.
func textDotPrefixFresh(stdout io.Writer, cfg *config.Config, files []string) error {
	maxBytes := int64(cfg.KB.MaxBookMB) << 20
	if maxBytes <= 0 {
		maxBytes = 512 << 20 // то же умолчание, что у kb.parseBook
	}
	opt := kb.DefaultChunkOpts()

	failed := 0
	for _, path := range files {
		fmt.Fprintf(os.Stderr, "разбираю %s…\n", path)
		// Дешёвая проба вперёд полного разбора — тем же порядком, что parseBook:
		// у скана отвечает по пяти страницам вместо разбора всей книги.
		if _, err := document.Probe(path, maxBytes, 5); err != nil {
			fmt.Fprintf(stdout, "%s: не читается (%v)\n", path, err)
			failed++
			continue
		}
		doc, parts, err := document.Parts(path, maxBytes)
		if err != nil {
			fmt.Fprintf(stdout, "%s: %v\n", path, err)
			failed++
			continue
		}
		var chunks []kb.Chunk
		if doc.Kind.Text() {
			chunks = kb.SplitText(parts, opt) // у текстовых книг своя нарезка
		} else {
			chunks = kb.Split(parts, opt)
		}

		total, bad := 0, 0
		var examples []string
		for _, ch := range chunks {
			total++
			words, dotted := dotPrefixShare(ch.Text)
			if words >= 20 && dotted*4 >= words { // от четверти слов с точкой впереди
				bad++
				if len(examples) < 3 {
					examples = append(examples, ch.Text)
				}
			}
		}
		fmt.Fprintf(stdout, "%s: кусков %d, с точкой перед словами (от четверти слов) %d (%.2f%%)\n",
			path, total, bad, 100*float64(bad)/float64(max(total, 1)))
		for _, e := range examples {
			fmt.Fprintf(stdout, "        %.90s\n", e)
		}
	}
	if failed == len(files) {
		return fmt.Errorf("ни один из %d названных файлов не разобрался", len(files))
	}
	return nil
}
