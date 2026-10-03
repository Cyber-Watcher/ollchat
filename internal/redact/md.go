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

// buildMD собирает .md из распознанных строк: имена заменены ролью, прочее
// персональное убрано вместе с подписью поля. Слово, легшее под область
// чернил, тоже убирается: в .md не должно быть ничего из замазанного в PDF.
func buildMD(title string, words []Word, lines []line, ink []Region, npages int) string {
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
	render := func(l line) string {
		var parts []string
		last := ""
		for _, i := range l.idx {
			w := words[i]
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

	md := []string{"# " + title, "",
		"_Текст распознан с картинки (tesseract), возможны ошибки распознавания. " +
			"Имя клиента заменено на CLIENT, имя врача — на DOCTOR, имя с неясной ролью — на PERSON; " +
			"адреса, телефоны, почта, номера документов и даты рождения убраны._"}

	type paraKey [3]int
	var order []paraKey
	paras := map[paraKey][]line{}
	for _, l := range lines {
		k := paraKey{l.key[0], l.key[1], l.key[2]}
		if _, ok := paras[k]; !ok {
			order = append(order, k)
		}
		paras[k] = append(paras[k], l)
	}
	for page := 0; page < npages; page++ {
		md = append(md, "", fmt.Sprintf("## Страница %d", page+1))
		for _, k := range order {
			if k[0] != page {
				continue
			}
			var buf []string
			flush := func() {
				if len(buf) > 0 {
					md = append(md, strings.Join(buf, " "))
					buf = nil
				}
			}
			md = append(md, "")
			for _, l := range paras[k] {
				t := render(l)
				switch {
				case strings.Trim(t, " ,.;:-/|") == "":
				case headingRe.MatchString(t):
					flush()
					md = append(md, "### "+strings.TrimSuffix(t, ":"), "")
				case labelRe.MatchString(t) && labelRe.FindStringIndex(t)[0] == 0:
					// Строка-поле («Exam: CT …») — отдельной строкой, а не в абзац.
					flush()
					md = append(md, t+"  ")
				default:
					buf = append(buf, t)
				}
			}
			flush()
		}
		n := 0
		for _, r := range ink {
			if r.Page == page {
				n++
			}
		}
		if n > 0 {
			md = append(md, "", fmt.Sprintf("_На странице скрыто областей без распознанного текста "+
				"(почерк, подпись, печать, логотип): %d._", n))
		}
	}
	out := strings.Join(md, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out) + "\n"
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
	for _, ln := range strings.Split(md, "\n") {
		if strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "_") {
			continue
		}
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
