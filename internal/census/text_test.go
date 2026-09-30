// Тесты переписей порчи текста. Проверяется то, что можно проверить без
// библиотеки на диске: признаки строк, которые ищет каждый род порчи,
// и отказ, когда коллекции не существует. Числа берутся из самих строк-
// образцов и из документированного порога каждого признака, а не подгоняются
// под код (internal/CLAUDE.md, «тест не подгоняется под код»).
package census

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// collectKinds — какие роды нашёл textScanCorruption на тексте, по разу на род.
func collectKinds(text string) map[textKind]int {
	found := map[textKind]int{}
	textScanCorruption(text, func(k textKind, at int) { found[k]++ })
	return found
}

// Мягкий перенос (U+00AD) на конце строки, за которым следует строчная буква —
// такой перенос безопасно склеивать в одно слово.
func TestScanFindsSoftHyphenAtEOL(t *testing.T) {
	text := "прог­\nрамма работает"
	found := collectKinds(text)
	if found[textShyEOL] != 1 {
		t.Errorf("shyEOL = %d, ожидался 1: %q", found[textShyEOL], text)
	}
	if found[textShyMid] != 0 {
		t.Errorf("shyMid = %d, ожидался 0 (перенос стоит на границе строки)", found[textShyMid])
	}
}

// Тот же знак посреди строки между буквами — слово не разорвано переносом
// строки, это опечатка внутри слова, а не перенос.
func TestScanFindsSoftHyphenMidWord(t *testing.T) {
	text := "abc­def обычный текст"
	found := collectKinds(text)
	if found[textShyMid] != 1 {
		t.Errorf("shyMid = %d, ожидался 1: %q", found[textShyMid], text)
	}
	if found[textShyEOL] != 0 {
		t.Errorf("shyEOL = %d, ожидался 0 (знак не на конце строки)", found[textShyEOL])
	}
}

// Обычный дефис в конце строки перед строчной буквой — LaTeX и вёрстка часто
// переносят так, без специальных юникод-знаков.
func TestScanFindsAsciiHyphenEOL(t *testing.T) {
	text := "algo-\nrithm работает"
	found := collectKinds(text)
	if found[textAsciiHypEOL] != 1 {
		t.Errorf("asciiHypEOL = %d, ожидался 1: %q", found[textAsciiHypEOL], text)
	}
}

// Дефис в конце строки перед ЗАГЛАВНОЙ буквой — это конец предложения перед
// новым, а не перенос слова: признак не должен сработать.
func TestScanDoesNotTakeSentenceBreakForHyphen(t *testing.T) {
	text := "конец фразы-\nСледующая начинается заглавной"
	found := collectKinds(text)
	if found[textAsciiHypEOL] != 0 {
		t.Errorf("asciiHypEOL = %d, ожидался 0: дефис перед заглавной буквой — не перенос слова", found[textAsciiHypEOL])
	}
}

// Знак переноса, пробел, строчная буква внутри строки — «Func‐ tionality»:
// перенос вставлен туда, где вёрстка сама решила разбить длинное слово.
func TestScanFindsHyphenSpaceMid(t *testing.T) {
	text := "Func‐ tionality работает"
	found := collectKinds(text)
	if found[textHypSpaceMid] != 1 {
		t.Errorf("hypSpaceMid = %d, ожидался 1: %q", found[textHypSpaceMid], text)
	}
}

// Три и больше подряд идущих «слово.слово» без пробела между — цепочка,
// которую ищет textDotRun. Меньше трёх подряд — не считается: обычная точка
// в тексте (`fmt.Println`, конец предложения) не должна портить счёт.
func TestScanFindsDotRunOnlyAtThreeOrMore(t *testing.T) {
	short := "слово.слово нормальный текст, тут одна точка между буквами."
	if found := collectKinds(short); found[textDotRun] != 0 {
		t.Errorf("dotRun = %d на тексте с одной точкой между буквами, ожидался 0: %q", found[textDotRun], short)
	}
	long := "возможности.выполнить.действие.с.помощью такого приёма"
	found := collectKinds(long)
	if found[textDotRun] == 0 {
		t.Errorf("dotRun = 0 на цепочке из четырёх точек подряд: %q", long)
	}
}

// Буквенное слово длиннее 30 знаков — признак слипшегося текста
// (textLongWordKind срабатывает на 31-й букве подряд).
func TestScanFindsLongWord(t *testing.T) {
	word := strings.Repeat("а", 31)
	found := collectKinds(word)
	if found[textLongWordKind] == 0 {
		t.Errorf("longWord не нашёлся на слове длиной 31: %q", word)
	}
	short := strings.Repeat("а", 29)
	if found := collectKinds(short); found[textLongWordKind] != 0 {
		t.Errorf("longWord сработал на слове длиной 29, порог — от 31 знака")
	}
}

// Невидимые знаки нулевой ширины (U+200B, U+2060) не должны теряться внутри
// обычного слова — признак textZeroWidth их находит.
func TestScanFindsZeroWidth(t *testing.T) {
	text := "нор​мальное слово"
	found := collectKinds(text)
	if found[textZeroWidth] != 1 {
		t.Errorf("zeroWidth = %d, ожидался 1: %q", found[textZeroWidth], text)
	}
}

// textIsSoftHyphen (использует textCountBare) — ровно четыре знака переноса,
// не больше и не меньше: обычный дефис '-' и типографское тире в их число
// не входят, иначе счёт распухнет обычной пунктуацией.
func TestIsSoftHyphenExactSet(t *testing.T) {
	yes := []rune{'­', '‐', '‑', '⁃'}
	for _, r := range yes {
		if !textIsSoftHyphen(r) {
			t.Errorf("%U не признан мягким переносом", r)
		}
	}
	no := []rune{'-', '—', '–', 'a', '.', ' '}
	for _, r := range no {
		if textIsSoftHyphen(r) {
			t.Errorf("%U зря признан мягким переносом", r)
		}
	}
}

// textDotBetweenLetters (использует textDots) — точка между буквами кириллицы
// и латиницы, а не между цифрами или знаками: адрес из цифр не должен считаться.
func TestDotBetweenLettersIgnoresDigits(t *testing.T) {
	if n := len(textDotBetweenLetters.FindAllString("версия 1.2.3 и адрес 198.51.100.10", -1)); n != 0 {
		t.Errorf("точки между цифрами дали %d находок, ожидалось 0", n)
	}
	if n := len(textDotBetweenLetters.FindAllString("fmt.Println и os.Open", -1)); n != 2 {
		t.Errorf("точки между буквами дали %d находок, ожидалось 2 (fmt.P, os.O)", n)
	}
}

// dotPrefixShare (общий предикат textDotPrefix и textDotPrefixFresh) —
// счёт слов и дот-префиксных слов проверяется на построенной строке, где
// оба числа известны заранее по самому построению, а не подогнаны под код:
// "используя .данный .подход .можно .быстро " четыре раза даёт по 5 слов
// на повтор (4 из них с точкой впереди), плюс два обычных слова в конце.
func TestDotPrefixShareCounts(t *testing.T) {
	text := strings.Repeat("используя .данный .подход .можно .быстро ", 4) + "закончить работу"
	words, dotted := dotPrefixShare(text)
	if words != 22 {
		t.Fatalf("words = %d, ожидалось 22 (4×5 + 2 хвостовых слова): %q", words, text)
	}
	if dotted != 16 {
		t.Fatalf("dotted = %d, ожидалось 16 (4×4 дот-префиксных слова на повтор): %q", dotted, text)
	}
}

// Строка того рода порчи, что искал утраченный dotprefixcensus/fresh
// («.выполнить .действие .с .помощью»), признаётся плохим куском: слов
// не меньше 20, и с точкой впереди — не меньше четверти. Та же строка без
// точек (обычная проза, то же число слов) — не признаётся: разница только
// в наличии точек перед словами, остальное строение фразы одинаковое.
func TestDotPrefixCorruptedLineIsBad(t *testing.T) {
	corrupted := strings.Repeat("используя .данный .подход .можно .быстро ", 4) + "закончить работу"
	words, dotted := dotPrefixShare(corrupted)
	if !(words >= 20 && dotted*4 >= words) {
		t.Errorf("строка с точками перед словами не признана плохим куском: words=%d dotted=%d", words, dotted)
	}

	normal := strings.Repeat("используя данный подход можно быстро ", 4) + "закончить работу"
	words, dotted = dotPrefixShare(normal)
	if words >= 20 && dotted*4 >= words {
		t.Errorf("обычная проза (те же слова без точек) ошибочно признана испорченной: words=%d dotted=%d", words, dotted)
	}
	if dotted != 0 {
		t.Errorf("в строке без точек перед словами dotted = %d, ожидался 0", dotted)
	}
}

// Порог 20 слов: доля с точкой впереди высокая (9 из 19, больше четверти),
// но слов меньше двадцати — кусок признаваться плохим не должен. Порог
// считается по обеим границам, а не только по доле.
func TestDotPrefixShareRequiresAtLeast20Words(t *testing.T) {
	text := strings.Repeat(".х аа ", 9) + "бб" // 9 дот-префиксных + 9 обычных + 1 хвостовое = 19 слов
	words, dotted := dotPrefixShare(text)
	if words != 19 {
		t.Fatalf("words = %d, ожидалось 19: %q", words, text)
	}
	if dotted != 9 {
		t.Fatalf("dotted = %d, ожидалось 9: %q", dotted, text)
	}
	if words >= 20 && dotted*4 >= words {
		t.Errorf("кусок с 19 словами прошёл порог 20 слов: words=%d dotted=%d", words, dotted)
	}
}

// Порог четверти на границе 20 слов: 4 из 20 (ровно 20%) — ниже порога,
// 5 из 20 (ровно 25%) — на пороге и признаётся плохим.
func TestDotPrefixShareQuarterBoundary(t *testing.T) {
	below := ".аб .вг .де .жз " + strings.Repeat("слово ", 16) // 4 дот-префиксных + 16 обычных = 20 слов
	words, dotted := dotPrefixShare(below)
	if words != 20 || dotted != 4 {
		t.Fatalf("below: words=%d dotted=%d, ожидалось 20 и 4: %q", words, dotted, below)
	}
	if words >= 20 && dotted*4 >= words {
		t.Errorf("4 из 20 (20%%) прошли порог четверти, а не должны были: words=%d dotted=%d", words, dotted)
	}

	atQuarter := ".аб .вг .де .жз .ёж " + strings.Repeat("слово ", 15) // 5 дот-префиксных + 15 обычных = 20 слов
	words, dotted = dotPrefixShare(atQuarter)
	if words != 20 || dotted != 5 {
		t.Fatalf("atQuarter: words=%d dotted=%d, ожидалось 20 и 5: %q", words, dotted, atQuarter)
	}
	if !(words >= 20 && dotted*4 >= words) {
		t.Errorf("5 из 20 (25%%, ровно порог) не прошли порог четверти: words=%d dotted=%d", words, dotted)
	}
}

// Без коллекции на диске все пять режимов обязаны отказать с ошибкой,
// а не молчать и не паниковать: openColl не находит "books" в пустой базе.
func TestModesRefuseOnMissingCollection(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.KB.Dir = dir

	var out strings.Builder
	cases := []struct {
		name string
		run  func() error
	}{
		{"textCorruption", func() error { return textCorruption(&out, cfg, "books", 12, 6) }},
		{"textDots", func() error { return textDots(&out, cfg, "books") }},
		{"textLongWords", func() error { return textLongWords(&out, cfg, "books", 25, 12) }},
		{"textDotPrefix", func() error { return textDotPrefix(&out, cfg, "books") }},
	}
	for _, c := range cases {
		if err := c.run(); err == nil {
			t.Errorf("%s: без коллекции на диске обязан отказать, а не пройти молча", c.name)
		}
	}
}

// textDotPrefixFresh читает файлы с диска, а не коллекцию: если ни один
// из названных файлов не разобрался (тут — файла попросту нет), режим
// обязан отказать с ошибкой, а не промолчать и не запаниковать.
func TestTextDotPrefixFreshRefusesWhenAllFilesFail(t *testing.T) {
	cfg := &config.Config{}
	var out strings.Builder
	err := textDotPrefixFresh(&out, cfg, []string{"/nonexistent/dir/no-such-book.pdf"})
	if err == nil {
		t.Fatal("textDotPrefixFresh с несуществующим файлом обязан отказать")
	}
}

// Название режима "books" в пустой базе не создаёт коллекцию сам по себе:
// проверка выше опирается именно на это поведение kb.Base.Open.
func TestMissingCollectionIsActuallyMissing(t *testing.T) {
	dir := t.TempDir()
	base, err := kb.OpenBase(dir)
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}
	defer base.Close()
	if _, err := base.Open("books"); err == nil {
		t.Fatal("Open('books') в пустой базе обязан отказать")
	}
}
