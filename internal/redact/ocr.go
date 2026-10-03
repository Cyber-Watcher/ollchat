package redact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
)

// Word — слово, распознанное tesseract, и что о нём решено.
type Word struct {
	Page             int // с нуля
	Block, Par, Line int // строка распознавания: слова одной строки идут подряд
	Text             string
	Conf             float64 // уверенность tesseract, 0..100
	Box              Rect    // в точках страницы
	Kind             Kind    // что это за данные; KindNone — обычный текст
	Why              string  // каким правилом найдено — для отчёта
	// LabelOf — слово входит в подпись поля («MRN:»), чьё значение убрано
	// из .md: подпись без значения ничего не сообщает и уходит вместе с ним.
	LabelOf Kind
}

// ocrDPI — до какого разрешения поднимать картинку перед распознаванием:
// на 200 dpi tesseract заметно ошибается в мелком шрифте, на 300 — нет.
const ocrDPI = 300

func findTesseract(path string) (string, error) {
	if path == "" {
		path = "tesseract"
	}
	bin, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("для сканов нужна программа распознавания tesseract, а её в системе нет (%s): "+
			"установите пакет tesseract-ocr и нужные языки, например tesseract-ocr-rus", path)
	}
	return bin, nil
}

// pickLangs проверяет, что все языки установлены: иначе tesseract падает
// с сообщением, по которому не понять, чего не хватает. Пустой выбор —
// английский и русский из тех, что есть.
func pickLangs(ctx context.Context, bin, lang string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "--list-langs").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tesseract --list-langs: %v", err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n")[1:] {
		have[strings.TrimSpace(l)] = true
	}
	if lang == "" {
		var auto []string
		for _, l := range []string{"eng", "rus"} {
			if have[l] {
				auto = append(auto, l)
			}
		}
		if len(auto) == 0 {
			return "", errors.New("у tesseract нет ни английского, ни русского языка: " +
				"установите пакеты tesseract-ocr-eng и tesseract-ocr-rus")
		}
		return strings.Join(auto, "+"), nil
	}
	for _, l := range strings.Split(lang, "+") {
		if !have[l] {
			var list []string
			for h := range have {
				if h != "" && h != "osd" {
					list = append(list, h)
				}
			}
			sort.Strings(list)
			return "", fmt.Errorf("у tesseract нет языка %q (есть: %s): установите пакет tesseract-ocr-%s",
				l, strings.Join(list, ", "), l)
		}
	}
	return lang, nil
}

// ocrPage распознаёт страницу и возвращает слова с рамками в точках страницы.
func ocrPage(ctx context.Context, bin, lang, tmp string, p Page, page int) ([]Word, error) {
	img, pxPerPt := grayAt(p, ocrDPI)
	tsv, err := runTesseract(ctx, bin, lang, tmp, img, "3")
	if err != nil {
		return nil, err
	}
	return parseTSV(tsv, page, pxPerPt), nil
}

// Вторая попытка для непрочитанного. Разбор всей страницы (psm 3) пропускает
// клетки таблиц и колонтитулы: на синтетическом наборе 03.10.2026 из клеток
// таблицы направления на анализы не вернулось ни слова, на образце — «Page 1
// of 2». Такое место ловилось как «чернила вне текста» и уходило под чёрное
// вместе с нужным. Здесь каждая такая область высотой со строку читается
// отдельно как одна строка (psm 7). Уверенно прочитанное становится словами
// и проходит все правила поиска; почерк читается неуверенно и остаётся
// чернилами.
const (
	rereadMinH     = 4.0  // pt; ниже — не строка текста
	rereadLineH    = 24.0 // pt; до этой высоты область читается как одна строка (psm 7)
	rereadMaxH     = 80.0 // pt; до этой — как блок строк (psm 6: две-три клетки таблицы подряд)
	rereadPad      = 3.0  // pt вокруг области: рамка чернил режет края букв
	rereadLineConf = 70.0 // средняя уверенность, с которой строка принимается
	rereadWordConf = 50.0 // слово неуверенней — отбрасывается и остаётся чернилами
	rereadBlock    = 10000
)

func rereadInk(ctx context.Context, bin, lang, tmp string, p Page, page int, words []Word) ([]Word, error) {
	regions := inkRegions(p, page, words)
	if len(regions) == 0 {
		return nil, nil
	}
	img, k := grayAt(p, ocrDPI)
	var out []Word
	for n, r := range regions {
		h := r.Box.Y1 - r.Box.Y0
		if h < rereadMinH || h > rereadMaxH || r.Box.X1-r.Box.X0 < h {
			continue
		}
		rect := image.Rect(int((r.Box.X0-rereadPad)*k), int((r.Box.Y0-rereadPad)*k),
			int((r.Box.X1+rereadPad)*k)+1, int((r.Box.Y1+rereadPad)*k)+1).Intersect(img.Bounds())
		crop := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		draw.Draw(crop, crop.Bounds(), img, rect.Min, draw.Src)
		psm := "7"
		if h > rereadLineH {
			psm = "6"
		}
		tsv, err := runTesseract(ctx, bin, lang, tmp, crop, psm)
		if err != nil {
			return nil, err
		}
		ws := parseTSV(tsv, page, k)
		sum := 0.0
		for _, w := range ws {
			sum += w.Conf
		}
		if len(ws) == 0 || sum/float64(len(ws)) < rereadLineConf {
			continue
		}
		dx, dy := float64(rect.Min.X)/k, float64(rect.Min.Y)/k
		for _, w := range ws {
			if w.Conf < rereadWordConf {
				continue
			}
			// Свой блок на каждую область: слова не смешиваются со строками
			// первого разбора; абзац и строка — от tesseract (в блоке psm 6
			// строк несколько).
			w.Block = rereadBlock + n
			w.Box = Rect{w.Box.X0 + dx, w.Box.Y0 + dy, w.Box.X1 + dx, w.Box.Y1 + dy}
			out = append(out, w)
		}
	}
	return out, nil
}

// grayAt переводит картинку страницы в серую и поднимает до dpi, если она
// мельче; крупнее — оставляет как есть. Возвращает и масштаб, точек картинки
// на точку страницы.
//
// Масштаб один на обе оси, поэтому высота берётся из пропорций СТРАНИЦЫ,
// а не картинки: у скана с разным разрешением по осям (факс 204×98 dpi)
// высота «как у картинки» сдвигала координаты слов по вертикали, и полосы
// внизу страницы ложились мимо текста.
func grayAt(p Page, dpi float64) (*image.Gray, float64) {
	b := p.Image.Bounds()
	scale := 1.0
	if native := float64(b.Dx()) / p.Width * 72; native < dpi*0.85 {
		scale = dpi / native
	}
	w, h := int(float64(b.Dx())*scale+0.5), int(float64(b.Dy())*scale+0.5)
	square := true // точки картинки квадратные: её пропорции совпадают со страницей
	if p.Height > 0 {
		if hp := int(float64(w)*p.Height/p.Width + 0.5); hp > h+1 || hp < h-1 {
			h, square = hp, false
		}
	}
	dst := image.NewGray(image.Rect(0, 0, w, h))
	if scale == 1 && square {
		draw.Draw(dst, dst.Bounds(), p.Image, b.Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), p.Image, b, draw.Src, nil)
	}
	return dst, float64(w) / p.Width
}

// scaleGray уменьшает или увеличивает картинку страницы до заданного dpi.
//
// Уменьшает — по самому тёмному: точка итога тёмная, если тёмен хоть один
// исходный пиксель под ней. Усреднение светлило тонкий штрих: росчерк ручкой
// в 300–400 dpi на сером скане становился светлее порога чернил и оставался
// незамазанным (синтетический набор 03.10.2026: 4 подписи из 8).
func scaleGray(p Page, dpi float64) (*image.Gray, float64) {
	w := int(p.Width/72*dpi + 0.5)
	h := int(p.Height/72*dpi + 0.5)
	dst := image.NewGray(image.Rect(0, 0, w, h))
	b := p.Image.Bounds()
	if b.Dx() <= w {
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), p.Image, b, draw.Src, nil)
		return dst, float64(w) / p.Width
	}
	src := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(src, src.Bounds(), p.Image, b.Min, draw.Src)
	for y := 0; y < h; y++ {
		sy0, sy1 := y*b.Dy()/h, max(y*b.Dy()/h+1, (y+1)*b.Dy()/h)
		for x := 0; x < w; x++ {
			sx0, sx1 := x*b.Dx()/w, max(x*b.Dx()/w+1, (x+1)*b.Dx()/w)
			m := uint8(255)
			for sy := sy0; sy < sy1; sy++ {
				row := src.Pix[sy*src.Stride:]
				for sx := sx0; sx < sx1; sx++ {
					m = min(m, row[sx])
				}
			}
			dst.Pix[y*dst.Stride+x] = m
		}
	}
	return dst, float64(w) / p.Width
}

// mergeReread ставит слова повторного чтения на их место в тексте страницы.
// Дописанные в конец, они уходили в .md после всех строк страницы («episodes»
// из середины письма оказывалось под подписью, синтетический набор
// 03.10.2026). Слово на высоте строки первого разбора входит в эту строку
// по своему месту слева направо; строка, которой рядом нет (клетка таблицы),
// встаёт перед первой строкой ниже неё. Строки повторного чтения переносятся
// целиком, чтобы их слова не перемешались.
func mergeReread(ws, more []Word) []Word {
	out := append([]Word(nil), ws...)
	key := func(w Word) [3]int { return [3]int{w.Block, w.Par, w.Line} }
	var groups [][]Word
	for i, m := range more {
		if i == 0 || key(m) != key(more[i-1]) {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], m)
	}
	for _, g := range groups {
		box := g[0].Box
		for _, m := range g[1:] {
			box = Rect{min(box.X0, m.Box.X0), min(box.Y0, m.Box.Y0), max(box.X1, m.Box.X1), max(box.Y1, m.Box.Y1)}
		}
		h := box.Y1 - box.Y0
		host, best := [3]int{}, 0.0
		for _, w := range out {
			if w.Block >= rereadBlock {
				continue
			}
			if ov := min(w.Box.Y1, box.Y1) - max(w.Box.Y0, box.Y0); h > 0 && ov/h >= 0.5 && ov > best {
				host, best = key(w), ov
			}
		}
		if best > 0 {
			for _, m := range g {
				m.Block, m.Par, m.Line = host[0], host[1], host[2]
				pos, last := -1, -1
				for i, w := range out {
					if key(w) != host {
						continue
					}
					last = i
					if pos < 0 && w.Box.X0 > m.Box.X0 {
						pos = i
					}
				}
				if pos < 0 {
					pos = last + 1
				}
				out = append(out[:pos], append([]Word{m}, out[pos:]...)...)
			}
			continue
		}
		pos := len(out)
		for i, w := range out {
			if (i == 0 || key(out[i-1]) != key(w)) && w.Box.Y0 > box.Y0 {
				pos = i
				break
			}
		}
		out = append(out[:pos], append(append([]Word(nil), g...), out[pos:]...)...)
	}
	return out
}

// runTesseract — psm: «3» — страница целиком, «7» — одна строка.
func runTesseract(ctx context.Context, bin, lang, tmp string, img image.Image, psm string) (string, error) {
	f, err := os.CreateTemp(tmp, "page-*.png")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, f.Name(), "stdout", "-l", lang, "--psm", psm, "tsv")
	// Без предела tesseract занимает все ядра; на общей машине это чужое время.
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=2")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return "", fmt.Errorf("tesseract: %v: %s", err, msg)
	}
	return string(out), nil
}

// parseTSV разбирает вывод tesseract в формате tsv. Столбцы: level page_num
// block_num par_num line_num word_num left top width height conf text;
// слова — строки уровня 5.
func parseTSV(tsv string, page int, pxPerPt float64) []Word {
	var words []Word
	for _, ln := range strings.Split(tsv, "\n") {
		f := strings.Split(strings.TrimRight(ln, "\r"), "\t")
		if len(f) < 12 || f[0] != "5" {
			continue
		}
		text := strings.TrimSpace(f[11])
		if text == "" {
			continue
		}
		n := make([]int, 7)
		ok := true
		for i, col := range []int{2, 3, 4, 6, 7, 8, 9} {
			if n[i], ok = atoi(f[col]); !ok {
				break
			}
		}
		if !ok {
			continue
		}
		conf, _ := strconv.ParseFloat(f[10], 64)
		x, y, w, h := float64(n[3]), float64(n[4]), float64(n[5]), float64(n[6])
		words = append(words, Word{
			Page: page, Block: n[0], Par: n[1], Line: n[2],
			Text: text, Conf: conf,
			Box: Rect{x / pxPerPt, y / pxPerPt, (x + w) / pxPerPt, (y + h) / pxPerPt},
		})
	}
	return words
}

func atoi(s string) (int, bool) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	return v, err == nil
}
