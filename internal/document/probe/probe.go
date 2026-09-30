// Пакет probe — посмотреть глазами, что наш разбор достаёт из файла книги:
// хвост текста, прочитанного до нужной единицы, или одну страницу с признаками
// порчи. Только читает: ни граф, ни коллекция не открываются, карта не нужна.
//
//	ollchat --doc-probe "книга.pdf"                 хвост текста до 40-й единицы
//	ollchat --doc-probe "книга.pdf" -- -unit 120    та же проба, но до 120-й
//	ollchat --doc-probe "книга.pdf" -- -page 40     страница 40 и признаки порчи
//
// Свои ключи идут ПОСЛЕ «--»: иначе их разбирает сам ollchat и отказывает.
//
// **Зачем.** 6,34 % кусков библиотеки выходили с точками вместо пробелов
// («В.этом.процессе.бизнес-аспекты»), и это ломает поиск по словам, извлечение
// понятий и векторы разом (этап 104, П6.5). Вопрос «наш это разбор или сам
// файл такой» решается только глазами на одной странице, поэтому прибор
// печатает текст, а не одни числа.
//
// **История.** До 30.09.2026 это были две программы в `privatescripts/`:
// `docprobe` (35 строк, хвост текста) и `pagedump` (75 строк, страница
// с признаками порчи). Перенесены ключом по слову владельца 30.09.2026
// (этап 114, пункт Г5): отдельный бинарь Go — только по его решению, а две
// программы над одним пакетом `internal/document` означали бы вторую сборку
// и второй повод прибор не найти. Так уже вышло с `graphstats`.
//
// **Поправка к плану этапа 114.** В таблице групп сказано, что `pagedump`
// «добавляет лишь сверку со сторонним средством». На деле он ничего не сверяет
// сам: печатает НАШ разбор одной страницы и считает два признака порчи. Сверяет
// человек, открыв ту же страницу сторонним просмотрщиком. Поэтому ключ зовётся
// `-page`, а не `-compare-external`, как предполагал план.
package probe

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// Пределы чтения файла. Числа перенесены как были в двух программах: проба
// текста читала до 256 МБ, разбор по частям — до 512 МБ. Разница намеренная:
// проба останавливается на нужной единице, разбор идёт по всему файлу.
const (
	probeLimit = 256 << 20
	partsLimit = 512 << 20
)

// tailRunes — сколько знаков хвоста печатать: примерно последняя прочитанная
// страница, чтобы глазами было видно порчу и не листать вывод.
const tailRunes = 900

// dotRun — буква, точка, буква: признак «точки вместо пробелов».
var dotRun = regexp.MustCompile(`[а-яa-zА-ЯA-Z]\.[а-яa-zА-ЯA-Z]`)

// softHyphens — знаки, которые в PDF означают перенос, а не обычный дефис.
const softHyphens = "­‐‑⁃"

// Run — проба одного файла. Без ключей печатает хвост текста до 40-й единицы;
// с `-page N` — одну страницу и признаки порчи на ней.
func Run(stdout io.Writer, path string, args []string) error {
	fs := flag.NewFlagSet("doc-probe", flag.ContinueOnError)
	fs.SetOutput(stdout)
	unit := fs.Int("unit", 40, "до какой единицы (страницы, раздела) читать: печатается хвост текста")
	page := fs.Int("page", 0, "показать одну страницу и признаки порчи текста вместо хвоста")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("нужен файл: --doc-probe <файл> [-- -unit N | -page N]")
	}
	if *page > 0 {
		return onePage(stdout, path, *page)
	}
	return tail(stdout, path, *unit)
}

// tail — хвост текста, прочитанного до единицы unit: лечит ли нынешний разбор
// порчу, видно на последней прочитанной единице.
func tail(stdout io.Writer, path string, unit int) error {
	// Разбор большого PDF идёт дольше двух секунд — говорим о шаге заранее.
	fmt.Fprintf(os.Stderr, "разбираю %s до единицы %d…\n", filepath.Base(path), unit)
	doc, err := document.Probe(path, probeLimit, unit+1)
	if err != nil {
		return fmt.Errorf("не разобрать %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "вид %v, единиц %d (%s), год %d, знаков %d\n",
		doc.Kind, doc.Units, doc.Unit, doc.Year, len(doc.Text))
	r := []rune(doc.Text)
	if len(r) > tailRunes {
		r = r[len(r)-tailRunes:] // хвост — последняя прочитанная единица
	}
	fmt.Fprintf(stdout, "── хвост текста ──\n%s\n", string(r))
	return nil
}

// onePage — одна страница нашим разбором и признаки порчи на ней. Сверять
// с посторонним просмотрщиком глазами: страница названа своим номером.
func onePage(stdout io.Writer, path string, page int) error {
	fmt.Fprintf(os.Stderr, "разбираю %s по частям…\n", filepath.Base(path))
	_, parts, err := document.Parts(path, partsLimit)
	if err != nil {
		return fmt.Errorf("не прочитать %s: %w", path, err)
	}
	for _, p := range parts {
		if p.Number != page {
			continue
		}
		fmt.Fprintf(stdout, "=== НАШ разбор, страница %d, знаков %d ===\n", p.Number, len([]rune(p.Text)))
		fmt.Fprintln(stdout, cut(strings.Join(strings.Fields(p.Text), " "), 900))
		dots, soft, around := flaws(p.Text)
		fmt.Fprintf(stdout, "\nточек между буквами: %d\n", dots)
		fmt.Fprintf(stdout, "мягких переносов: %d\n", soft)
		if around != "" {
			fmt.Fprintf(stdout, "вокруг первого: %q\n", around)
		}
		return nil
	}
	return fmt.Errorf("страница %d не найдена, всего частей %d", page, len(parts))
}

// flaws — два признака порчи на тексте страницы: сколько точек стоит между
// буквами, сколько мягких переносов и что написано вокруг первого из них.
// Отдельной функцией, чтобы проверялась тестом без файла книги.
func flaws(text string) (dots, soft int, around string) {
	dots = len(dotRun.FindAllString(text, -1))
	for _, r := range text {
		if strings.ContainsRune(softHyphens, r) {
			soft++
		}
	}
	if soft == 0 {
		return dots, 0, ""
	}
	// Окно вокруг первого переноса считается в ЗНАКАХ, а не в байтах: в
	// `pagedump` оно резалось по байтам (`text[i-60:i+60]`) и на русской
	// странице разрубало букву надвое, отчего в выводе появлялись «\xd0».
	r := []rune(text)
	first := 0
	for k, c := range r {
		if strings.ContainsRune(softHyphens, c) {
			first = k
			break
		}
	}
	lo, hi := first-60, first+60
	if lo < 0 {
		lo = 0
	}
	if hi > len(r) {
		hi = len(r)
	}
	return dots, soft, string(r[lo:hi])
}

// cut — обрезать строку по знакам, а не по байтам: в тексте книг не латиница.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
