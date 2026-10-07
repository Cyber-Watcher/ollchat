// Package redact скрывает персональные данные в сканах документов.
//
// **Зачем.** Пользователи присылают PDF, которые на деле сканы: страница —
// картинка, текстового слоя нет. Из такого документа просят сделать точно
// такой же PDF, где персональные данные замазаны чёрным, или текст .md без
// них: имя клиента заменено на CLIENT, имя врача — на DOCTOR, а адреса,
// телефоны, почта и номера документов убраны вовсе.
//
// **Цепочка.** Картинка страницы → внешняя программа tesseract (слова
// с рамками) → поиск персональных данных: подписи полей («MRN:», «DOB:»,
// «Пациент:»), шаблоны (телефон, почта, адрес, номер), подсказки модели,
// повтор найденного по всему документу, неразборчивые строки и чернила вне
// распознанного текста (почерк, подпись, печать, логотип) → чёрные
// прямоугольники в пикселях картинки и новый PDF из картинок, под которыми
// ничего не остаётся → .md из распознанных строк → проверка вторым прибором:
// повторное распознавание итога и поиск в нём и в .md всего скрытого.
//
// Своего распознавания здесь нет и не будет: tesseract — внешняя программа,
// её отсутствие объясняется человеку словами, а не падением.
//
// Исследование 03.10.2026 на образце (два листа: ч/б 400 dpi и цветной
// 200 dpi) началось прототипом на Python с Presidio; сюда перенесены его
// правила, кроме имён по смыслу (spaCy): на образце они дали только ложные
// находки («Multiplanar», «PT»), а имена в свободном тексте подсказывает
// модель — она видит обезличенный текст и замечает оставшееся.
package redact

import (
	"context"
	"fmt"
	"image"
	"os"
	"sort"
	"strings"
	"time"
)

// Page — страница скана: её размер и картинка целиком.
type Page struct {
	Width, Height float64 // размер страницы, pt
	Image         image.Image
}

// Rect — прямоугольник в точках страницы, начало в левом верхнем углу.
type Rect struct{ X0, Y0, X1, Y1 float64 }

// Overlaps — пересекаются ли прямоугольники.
func (r Rect) Overlaps(o Rect) bool {
	return r.X0 < o.X1 && o.X0 < r.X1 && r.Y0 < o.Y1 && o.Y0 < r.Y1
}

// Region — замазанная область без распознанного текста.
type Region struct {
	Page int
	Box  Rect
}

// Options — настройки обработки.
type Options struct {
	Lang      string // языки tesseract: «eng», «rus», «eng+rus»; пусто — решают первые листы (chooseLang)
	Tesseract string // путь к программе; пусто — искать в PATH

	// Подсказки: имена и строки, которые надо скрыть сверх найденного.
	// Их передаёт модель, заметив в обезличенном тексте оставшееся.
	Clients []string
	Doctors []string
	Hide    []string

	Progress func(string) // шаги для человека; nil — молча
	TempDir  string       // где держать картинки для tesseract; пусто — системный
}

// Result — итог обработки.
type Result struct {
	Words    []Word
	Ink      []Region
	Redacted []image.Image // страницы с замазанным, в разрешении исходника
	MD       string        // обезличенный текст
	OCRMD    string        // распознанный текст целиком, С персональными данными
	Lang     string        // какими языками распознано
	Check    Check
}

// Check — итог проверки вторым прибором. Значений в нём нет, только виды:
// он уходит модели, а та не должна видеть скрытое.
type Check struct {
	Values   int      // сколько скрытых значений проверено
	LeaksPDF []string // виды значений, найденных повторным распознаванием PDF
	LeaksMD  []string // виды значений, оставшихся в .md
}

// OK — ничего скрытого не нашлось ни в PDF, ни в .md.
func (c Check) OK() bool { return len(c.LeaksPDF) == 0 && len(c.LeaksMD) == 0 }

// Process распознаёт страницы, находит персональные данные, замазывает их
// и собирает .md. title — заголовок распознанной копии (имя документа без
// расширения); у обезличенного .md заголовок нейтральный (buildMD).
func Process(ctx context.Context, title string, pages []Page, opt Options) (*Result, error) {
	if len(pages) == 0 {
		return nil, fmt.Errorf("в документе нет страниц")
	}
	bin, err := findTesseract(opt.Tesseract)
	if err != nil {
		return nil, err
	}
	lang, err := pickLangs(ctx, bin, strings.TrimSpace(opt.Lang))
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(opt.TempDir, "ollchat-redact-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	say := func(format string, a ...any) {
		if opt.Progress != nil {
			opt.Progress(fmt.Sprintf(format, a...))
		}
	}

	// Языки не заданы — решают первые листы (ocr.go: chooseLang).
	var sample map[int][]Word
	if strings.TrimSpace(opt.Lang) == "" && lang == "eng+rus" {
		say("пробую языки по первым листам")
		if lang, sample, err = chooseLang(ctx, bin, lang, tmp, pages); err != nil {
			return nil, err
		}
		say("язык распознавания: %s", lang)
	}

	var words []Word
	for i, p := range pages {
		say("распознаю страницу %d из %d", i+1, len(pages))
		ws, ok := sample[i]
		if !ok {
			if ws, err = ocrPage(ctx, bin, lang, tmp, p, i); err != nil {
				return nil, fmt.Errorf("страница %d: %w", i+1, err)
			}
		}
		if ws, err = rereadJunk(ctx, bin, lang, tmp, p, i, ws); err != nil {
			return nil, fmt.Errorf("страница %d: %w", i+1, err)
		}
		more, err := rereadInk(ctx, bin, lang, tmp, p, i, ws)
		if err != nil {
			return nil, fmt.Errorf("страница %d: %w", i+1, err)
		}
		words = append(words, mergeReread(ws, more)...)
	}

	say("ищу персональные данные")
	lines := buildLines(words)
	byLabels(words, lines)
	byCells(words, lines)
	byPatterns(words, lines)
	byNameDate(words, lines, time.Now().Year())
	seeds := byHints(words, lines, opt)
	byRepeat(words, seeds)
	// Ещё раз после повтора: имя в шапке бланка часто узнаётся только
	// повтором найденного, а дата рождения стоит сразу за ним.
	byNameDate(words, lines, time.Now().Year())
	byUnreadable(words, lines)
	var ink []Region
	for i, p := range pages {
		ink = append(ink, inkRegions(p, i, words)...)
	}

	red := make([]image.Image, len(pages))
	for i, p := range pages {
		red[i] = paint(p, i, words, ink)
	}
	md := buildMD(title, words, ink, len(pages), false)

	say("проверяю итог повторным распознаванием")
	check, err := verify(ctx, bin, lang, tmp, pages, red, words, md)
	if err != nil {
		return nil, err
	}
	return &Result{Words: words, Ink: ink, Redacted: red, MD: md,
		OCRMD: buildMD(title, words, ink, len(pages), true), Lang: lang, Check: check}, nil
}

// LeakMark — начало строки проверки, когда скрытое нашлось в итоге: по нему
// ключ --scan-redact-llm узнаёт утечку в ответе инструмента.
const LeakMark = "ВНИМАНИЕ, проверка нашла скрытое в итоге"

// Line — итог проверки одной строкой: для человека и модели.
func (c Check) Line() string {
	if c.OK() {
		return fmt.Sprintf("Проверка повторным распознаванием: скрытого не найдено ни в PDF, ни в .md "+
			"(проверено значений: %d).\n", c.Values)
	}
	none := func(s []string) string {
		if len(s) == 0 {
			return "ничего"
		}
		return strings.Join(s, ", ")
	}
	return fmt.Sprintf(LeakMark+": в PDF — %s; в .md — %s. "+
		"Обезличенные файлы записаны с пометкой UNVERIFIED в имени: показывать их наружу нельзя, "+
		"пока это не исправлено.\n",
		none(c.LeaksPDF), none(c.LeaksMD))
}

// CountsLine — сколько скрыто по видам, одной строкой.
func (r *Result) CountsLine() string {
	c := r.Counts()
	kinds := make([]Kind, 0, len(c))
	for k, n := range c {
		if n > 0 {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return "Персональных данных не найдено.\n"
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s — %d", k, c[k]))
	}
	return "Скрыто (слов или областей): " + strings.Join(parts, ", ") + ".\n"
}

// Report — разбор для исследования: каждое слово, что о нём решено и каким
// правилом. **В нём сами персональные данные**: только в файл, по ключу,
// и наружу его не выносить.
func (r *Result) Report() any {
	type word struct {
		Page             int
		Line             [3]int
		Text             string
		Conf             float64
		Box              Rect
		Kind, Why, Label string `json:",omitempty"`
	}
	out := struct {
		Words []word
		Ink   []Region
		Check Check
	}{Ink: r.Ink, Check: r.Check}
	for _, w := range r.Words {
		ww := word{Page: w.Page + 1, Line: [3]int{w.Block, w.Par, w.Line}, Text: w.Text, Conf: w.Conf, Box: w.Box, Why: w.Why}
		if w.Kind != KindNone {
			ww.Kind = w.Kind.String()
		}
		if w.LabelOf != KindNone {
			ww.Label = w.LabelOf.String()
		}
		out.Words = append(out.Words, ww)
	}
	return out
}

// Counts — сколько скрыто по видам: для отчёта человеку и модели.
func (r *Result) Counts() map[Kind]int {
	out := map[Kind]int{}
	for _, w := range r.Words {
		if w.Kind != KindNone {
			out[w.Kind]++
		}
	}
	out[KindHidden] += len(r.Ink)
	return out
}
