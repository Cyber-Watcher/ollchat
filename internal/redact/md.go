package redact

import (
	"context"
	"fmt"
	"image"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var headingRe = regexp.MustCompile(`^\p{Lu}[\p{Lu}\d ,/&()'-]+:$`)

// buildMD собирает .md из распознанных слов, с таблицами (layout.go).
//
// Обезличенный (plain = false): имена заменены ролью, прочее персональное
// убрано вместе с подписью поля. Слово, легшее под область чернил, тоже
// убирается: в .md не должно быть ничего из замазанного в PDF.
//
// Распознанный (plain = true): весь текст как есть, с персональными данными.
// Его просят пользователи — читаемая копия скана для того, кому исходник
// и так доступен. Модели он не отдаётся никогда (tools/scanredact.go).
func buildMD(title string, words []Word, ink []Region, npages int, plain bool) string {
	drop := func(w Word) bool {
		if w.Kind.removed() || w.LabelOf != KindNone {
			return true
		}
		for _, r := range ink {
			if r.Page == w.Page && r.Box.Overlaps(w.Box) {
				return true
			}
		}
		return false
	}
	render := func(idx []int) string {
		var parts []string
		last := ""
		for _, i := range idx {
			w := words[i]
			if plain {
				parts = append(parts, w.Text)
				continue
			}
			if r := w.Kind.Replacement(); r != "" {
				if last != r {
					parts = append(parts, r)
				}
				last = r
				continue
			}
			last = ""
			if !drop(w) {
				parts = append(parts, w.Text)
			}
		}
		return strings.Join(parts, " ")
	}

	// Служебные пометки — на языке документа (слово владельца 06.10.2026):
	// в английском документе русские «Страница» и пояснения чужие.
	note := notesRU
	if english(words) {
		note = notesEN
	}
	// Заголовок обезличенного .md — нейтральный, а не имя файла: сканы
	// называют по фамилии пациента («Иванова М.П. выписка.pdf»), и имя
	// уходило в .md, который отдают наружу, и в текст для модели. Копии
	// со всеми данными имя файла не вредит.
	intro, heading := note.introRedacted, note.titleRedacted
	if plain {
		intro, heading = note.introPlain, title
	}
	md := []string{"# " + heading, "", intro}

	byPage := make([][]int, npages)
	for i, w := range words {
		if w.Page >= 0 && w.Page < npages {
			byPage[w.Page] = append(byPage[w.Page], i)
		}
	}
	for page := 0; page < npages; page++ {
		md = append(md, "", fmt.Sprintf(note.page, page+1), "")
		md = append(md, pageMD(words, byPage[page], render)...)
		n := 0
		for _, r := range ink {
			if r.Page == page {
				n++
			}
		}
		switch {
		case n == 0:
		case plain:
			md = append(md, "", fmt.Sprintf(note.inkPlain, n))
		default:
			md = append(md, "", fmt.Sprintf(note.inkRedacted, n))
		}
	}
	out := strings.Join(md, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out) + "\n"
}

// mdNotes — служебные строки .md: заголовок обезличенного документа,
// пояснение под названием, заголовок листа (с %d — номером) и пометка
// о скрытых областях (с %d — их числом).
type mdNotes struct {
	titleRedacted             string
	introRedacted, introPlain string
	page                      string
	inkRedacted, inkPlain     string
}

var notesRU = mdNotes{
	titleRedacted: "Обезличенный документ",
	introRedacted: "_Текст распознан с картинки (tesseract), возможны ошибки распознавания. " +
		"Имя клиента заменено на CLIENT, имя врача — на DOCTOR, имя с неясной ролью — на PERSON; " +
		"адреса, телефоны, почта, номера документов и даты рождения убраны._",
	introPlain: "_Текст распознан с картинки (tesseract), возможны ошибки распознавания. " +
		"Персональные данные НЕ скрыты: это полная копия документа, передавать её можно " +
		"только тому, кому доступен и сам исходник._",
	page:        "## Страница %d",
	inkRedacted: "_На странице скрыто областей без распознанного текста (почерк, подпись, печать, логотип): %d._",
	inkPlain:    "_На странице областей без распознанного текста (почерк, подпись, печать, логотип): %d._",
}

var notesEN = mdNotes{
	titleRedacted: "Redacted document",
	introRedacted: "_Text recognized from the image (tesseract); recognition errors are possible. " +
		"The client's name is replaced with CLIENT, the doctor's with DOCTOR, a name of unclear role with PERSON; " +
		"addresses, phone numbers, e-mail, document numbers and dates of birth are removed._",
	introPlain: "_Text recognized from the image (tesseract); recognition errors are possible. " +
		"Personal data is NOT hidden: this is a full copy of the document; share it only with " +
		"those who may see the original._",
	page:        "## Page %d",
	inkRedacted: "_Hidden areas without recognized text on this page (handwriting, signature, stamp, logo): %d._",
	inkPlain:    "_Areas without recognized text on this page (handwriting, signature, stamp, logo): %d._",
}

// leftoversMax — больше слов в подсказке модели не выписывается.
const leftoversMax = 40

var (
	leftWordRe = regexp.MustCompile(`[\p{L}][\p{L}'’-]*[.,;:!?)»"]*`)
	leftSkip   = func() map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.Fields("January February March April May June July August September " +
			"October November December Monday Tuesday Wednesday Thursday Friday Saturday Sunday " +
			"Январь Февраль Март Апрель Май Июнь Июль Август Сентябрь Октябрь Ноябрь Декабрь " +
			"Страница Page CLIENT DOCTOR PERSON " +
			// обращения в письмах: стоят рядом с ролью («Dear DOCTOR»), но не имена
			"Dear Hello Hi Уважаемый Уважаемая Уважаемые Здравствуйте") {
			m[w] = true
		}
		return m
	}()
)

// Leftovers — слова обезличенного текста, похожие на оставшиеся имена: с
// заглавной буквы посреди предложения или рядом с CLIENT, DOCTOR, PERSON.
// Модель видит их и так — это тот же текст, — но одной просьбы «найди
// оставшееся» мало: в прогоне 03.10.2026 Qwen пересказала в ответе «Oriel
// Nash» и «Corwin CLIENT» и инструмент повторно не вызвала. Список делает
// проверку явной; решает по-прежнему модель.
func Leftovers(md string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		if !seen[w] && len(out) < leftoversMax {
			seen[w] = true
			out = append(out, w)
		}
	}
	nameLike := func(w string) bool {
		r := []rune(w)
		// skipWords — подписи полей и служебные слова: «Patient», «Exam», «Врач».
		if len(r) < 3 || !unicode.IsUpper(r[0]) || leftSkip[w] || skipWords[norm(w)] {
			return false
		}
		for _, c := range r[1:] {
			if unicode.IsLower(c) {
				return true
			}
		}
		return false // заглавные целиком — аббревиатура или заголовок
	}
	// Пропускаются только своё: название документа, «## Страница N» и
	// пояснения курсивом. Заголовок «###» — текст скана: крупная шапка
	// с именем клиента становится заголовком (layout.go), и пропуск таких
	// строк прятал бы от проверки самое заметное место листа.
	var cells []string
	for _, ln := range strings.Split(md, "\n") {
		if strings.HasPrefix(ln, "# ") || strings.HasPrefix(ln, "## ") || strings.HasPrefix(ln, "_") {
			continue
		}
		// Клетка таблицы начинается как предложение: «| Example, Oriana |».
		cells = append(cells, strings.Split(strings.TrimPrefix(ln, "### "), "|")...)
	}
	for _, ln := range cells {
		toks := leftWordRe.FindAllString(ln, -1)
		for i, t := range toks {
			w := strings.TrimRight(t, ".,;:!?)»\"")
			if !nameLike(w) {
				continue
			}
			startOfSentence := i == 0 || strings.ContainsAny(toks[i-1][len(toks[i-1])-1:], ".!?:")
			nearRole := (i > 0 && isRole(toks[i-1])) || (i+1 < len(toks) && isRole(toks[i+1]))
			if !startOfSentence || nearRole {
				add(w)
			}
		}
	}
	return out
}

func isRole(t string) bool {
	t = strings.TrimRight(t, ".,;:!?)»\"")
	return t == "CLIENT" || t == "DOCTOR" || t == "PERSON"
}

// secrets — скрытые значения, которые проверка ищет в итоге. Короткие
// и служебные слова не годятся: «Ln» или «2026» встречаются и законно.
func secrets(words []Word) map[string]Kind {
	out := map[string]Kind{}
	for _, w := range words {
		if w.Kind == KindNone || w.Kind == KindHidden {
			continue
		}
		n := norm(w.Text)
		if len([]rune(n)) < 4 || skipWords[n] || (allDigits(n) && len(n) < 6) {
			continue
		}
		out[n] = w.Kind
	}
	return out
}

// verify — второй прибор: распознаёт уже замазанные страницы заново и ищет
// в них и в .md всё скрытое. Находка значит, что прямоугольник лёг мимо
// или что значение встретилось там, где правило его не узнало.
func verify(ctx context.Context, bin, lang, tmp string, pages []Page, red []image.Image, words []Word, md string) (Check, error) {
	sec := secrets(words)
	check := Check{Values: len(sec)}
	inPDF := map[Kind]bool{}
	for i, p := range pages {
		ws, err := ocrPage(ctx, bin, lang, tmp, Page{Width: p.Width, Height: p.Height, Image: red[i]}, i)
		if err != nil {
			return check, fmt.Errorf("проверка, страница %d: %w", i+1, err)
		}
		for _, w := range ws {
			if k, ok := sec[norm(w.Text)]; ok {
				inPDF[k] = true
			}
		}
	}
	inMD := map[Kind]bool{}
	for _, t := range strings.Fields(md) {
		if k, ok := sec[norm(t)]; ok {
			inMD[k] = true
		}
	}
	check.LeaksPDF, check.LeaksMD = kindList(inPDF), kindList(inMD)
	return check, nil
}

func kindList(m map[Kind]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k.String())
	}
	sort.Strings(out)
	return out
}
