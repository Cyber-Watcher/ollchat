package pdf

import (
	"math"
	"sort"
	"strings"
)

// Сборка страницы из кусков текста.
//
// Разбор содержимого не выводит текст сразу: он складывает куски вместе с их
// положением на странице, а строки собираются потом, по геометрии. Иначе
// таблицы рассыпаются — каждая ячейка нарисована отдельным блоком BT…ET, и при
// выводе по ходу разбора каждая уезжает на свою строку. Ячейки одной строки
// стоят на одной высоте, и собрать их обратно можно только зная координаты.

// frag — кусок текста с его положением.
type frag struct {
	x, y float64 // положение начала на странице
	w    float64 // ширина куска
	size float64 // кегль в единицах страницы
	text string
}

// layout собирает куски в текст страницы.
func layout(frags []frag) string {
	if len(frags) == 0 {
		return ""
	}
	var out []string
	for i, col := range columns(frags) {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, linesOf(col)...)
	}
	return strings.Join(out, "\n")
}

// linesOf группирует куски в строки по высоте и собирает каждую строку.
func linesOf(frags []frag) []string {
	if len(frags) == 0 {
		return nil
	}
	sort.Slice(frags, func(i, j int) bool {
		if math.Abs(frags[i].y-frags[j].y) > 0.01 {
			return frags[i].y > frags[j].y // страница считается сверху вниз
		}
		return frags[i].x < frags[j].x
	})

	median := medianSize(frags)
	tol := math.Max(1, median*0.4) // куски в пределах допуска считаются одной строкой

	// Шаг колонки берётся один на всю страницу: если считать его по кеглю
	// каждого куска, столбцы разъезжаются на строках с другим шрифтом.
	unit := math.Max(median*0.5, 0.5)
	originX := frags[0].x
	for _, f := range frags {
		originX = math.Min(originX, f.x)
	}

	// Порог пробела между словами выбирается по самой странице (см. wordGapOf).
	gap := wordGapOf(frags, tol)

	var out []string
	var line []frag
	lineY := frags[0].y
	prevY := math.NaN()

	flush := func() {
		if len(line) == 0 {
			return
		}
		// Пустая строка там, где по вертикали пропущено больше строки: так
		// сохраняется деление на абзацы и отбивка заголовков.
		if !math.IsNaN(prevY) && prevY-lineY > median*1.9 {
			out = append(out, "")
		}
		out = append(out, joinLine(line, originX, unit, gap))
		prevY = lineY
		line = line[:0]
	}

	for _, f := range frags {
		if len(line) > 0 && math.Abs(f.y-lineY) > tol {
			flush()
			lineY = f.y
		}
		if len(line) == 0 {
			lineY = f.y
		}
		line = append(line, f)
	}
	flush()
	return out
}

// joinLine собирает строку, ставя каждый кусок в ту колонку, где он стоит на
// странице. Единый шаг колонки на всю страницу — единственный способ, чтобы
// ячейки таблицы встали друг под другом; заодно сохраняются отступы и то,
// что продолжение ячейки остаётся под своей ячейкой.
func joinLine(line []frag, originX, unit, wordGap float64) string {
	sort.Slice(line, func(i, j int) bool { return line[i].x < line[j].x })

	// maxCols ограничивает длину строки: страницы бывают широкие, а строка
	// в тысячу пробелов не помогает никому.
	const maxCols = 300

	var b strings.Builder
	var prevEnd, prevSize float64
	for _, f := range line {
		if f.text == "" {
			continue
		}
		// Колонка зажимается с ОБЕИХ сторон. Верхняя граница — чтобы строка
		// не разъезжалась на широких страницах. Нижняя не менее важна:
		// у повреждённого файла координата бывает NaN или бесконечностью,
		// преобразование в целое даёт минимальное int64, и разность want-cur
		// переполняется в максимальное положительное — strings.Repeat тогда
		// просит петабайт памяти и валит программу. Найдено обстрелом
		// испорченных книг.
		want := min(max(int((f.x-originX)/unit+0.5), 0), maxCols)
		cur := runeLen(b.String())
		gap := f.x - prevEnd

		switch {
		case cur == 0:
			b.WriteString(strings.Repeat(" ", max(0, want)))
		case gap < wordGap*glyphSize(prevSize, f.size, unit*2):
			// Разрыва нет: это одно слово, разрезанное кернингом или сменой
			// шрифта. Ставить пробел по одной лишь арифметике колонок нельзя —
			// получится «Опис ание».
		case gap < unit*0.4 && (strings.HasSuffix(b.String(), " ") || strings.HasPrefix(f.text, " ")):
			// Узкий разрыв, а пробел уже есть в самом тексте: второй не нужен.
		default:
			// Разрыв есть: тянем кусок к его колонке, но не меньше пробела,
			// иначе соседние столбцы слипаются в «array of-».
			b.WriteString(strings.Repeat(" ", max(1, want-cur)))
		}
		b.WriteString(f.text)
		prevEnd, prevSize = f.x+f.w, f.size
	}
	return strings.TrimRight(b.String(), " ")
}

// Разрыв между кусками строки, начиная с которого это пробел между словами,
// в долях кегля: пределы, между которыми порог ищется по странице.
//
// **Откуда числа.** TeX и часть издательских вёрсток пробел не рисуют вовсе:
// следующее слово просто сдвинуто. Замер 17.09.2026 на семи книгах (гистограмма
// разрывов между соседними кусками, в долях кегля) дал два горба с пустой
// долиной между ними: кернинг и разрядка капители — до 0,062, пробелы между
// словами — от 0,098 (в сжатых строках вёрстка ужимает пробел почти вдвое
// против обычных 0,24). Прежний жёсткий порог 0,2 принимал за кернинг всё, что
// у́же, и съедал в таких книгах около 14% пробелов: «Вэтойсистемевесьмаполно
// реализованытребованиястандарта». Так было испорчено 0,73% кусков библиотеки
// в 108 книгах, в отдельных — больше трети.
//
// **Почему не просто 0,085.** Проверка на 61 книге: у книги, где буквы
// выведены по одной с шагом 0,08 кегля, такой порог разорвал слова на буквы
// (слов стало втрое больше). Горбы у разных вёрсток стоят в разных местах,
// поэтому порог — это долина между ними на данной странице, а не число.
const (
	wordGapMin = 0.085 // ниже пробелов не бывает: тут кернинг и разрядка
	wordGapMax = 0.2   // прежний порог: остаётся, когда долины на странице нет
)

// wordGapOf ищет порог пробела для страницы: нижний край первой пустой долины
// в распределении разрывов между wordGapMin и wordGapMax.
//
// Долина — окно шириной valley, в которое попало не больше сотой доли узких
// разрывов страницы. Порогом становится верх окна: всё, что у́же, — кернинг.
// Нет долины (разрывы размазаны, как у распознанного скана, или горб букв
// стоит прямо в этой зоне) — нет и оснований менять прежнее поведение.
func wordGapOf(frags []frag, tol float64) float64 {
	const (
		step     = 0.005
		width    = 7  // ширина долины в шагах: 0,035 кегля
		minGaps  = 40 // на меньшем числе разрывов долина — случайность
		fromGap  = -0.1
		binCount = 61 // от fromGap до wordGapMax шагами step
	)
	var hist [binCount]int
	total := 0
	for i := 1; i < len(frags); i++ {
		a, b := frags[i-1], frags[i]
		if math.Abs(a.y-b.y) > tol || a.text == "" || b.text == "" {
			continue
		}
		// Пробел, уже стоящий в тексте, о пороге ничего не говорит.
		if strings.HasSuffix(a.text, " ") || strings.HasPrefix(b.text, " ") {
			continue
		}
		g := (b.x - (a.x + a.w)) / glyphSize(a.size, b.size, 0)
		if g < fromGap || g >= wordGapMax || !finite(g) {
			continue
		}
		if k := int((g - fromGap) / step); k >= 0 && k < binCount {
			hist[k]++
		}
		total++
	}
	if total < minGaps {
		return wordGapMax
	}
	allowed := max(1, total/100)
	first := int(math.Round((wordGapMin - fromGap) / step))
	for top := first; top < binCount; top++ {
		n := 0
		for k := top - width; k < top; k++ {
			n += hist[k]
		}
		if n <= allowed {
			return fromGap + float64(top)*step
		}
	}
	return wordGapMax
}

// glyphSize — кегль, которым мерить разрыв: меньший из двух соседних кусков,
// а когда он неизвестен — запасной (кегль страницы; ноль — единица).
func glyphSize(prev, cur, fallback float64) float64 {
	size := math.Min(prev, cur)
	if size <= 0 {
		size = math.Max(prev, cur)
	}
	if size <= 0 {
		size = fallback
	}
	if size <= 0 {
		size = 1
	}
	return size
}

// runeLen считает длину в символах: столбцы меряются символами, а кириллица
// в UTF-8 занимает по два байта.
func runeLen(s string) int { return len([]rune(s)) }

// medianSize возвращает средний кегль страницы — по нему меряются допуски.
func medianSize(frags []frag) float64 {
	sizes := make([]float64, 0, len(frags))
	for _, f := range frags {
		if f.size > 0 {
			sizes = append(sizes, f.size)
		}
	}
	if len(sizes) == 0 {
		return 12
	}
	sort.Float64s(sizes)
	m := sizes[len(sizes)/2]
	if m <= 0 {
		return 12
	}
	return m
}

// columns делит страницу на колонки, если между ними идёт сплошная пустая
// полоса. Без этого текст двух колонок склеивается построчно: строка левой
// колонки и строка правой оказываются на одной высоте и попадают в одну строку,
// разрывая обе фразы.
func columns(frags []frag) [][]frag {
	if len(frags) < 40 {
		return [][]frag{frags}
	}
	minX, maxX := frags[0].x, frags[0].x+frags[0].w
	for _, f := range frags {
		minX = math.Min(minX, f.x)
		maxX = math.Max(maxX, f.x+f.w)
	}
	width := maxX - minX
	if width <= 0 {
		return [][]frag{frags}
	}

	// Ищем разрез в средней трети страницы: там, где его не пересекает ни один
	// кусок, а по обе стороны остаётся заметная доля текста.
	const steps = 24
	best, bestScore := 0.0, 0.0
	for i := 1; i < steps; i++ {
		x := minX + width*(0.35+0.3*float64(i)/steps)
		crossing, left, right := 0, 0, 0
		for _, f := range frags {
			switch {
			case f.x+f.w <= x:
				left++
			case f.x >= x:
				right++
			default:
				crossing++
			}
		}
		if crossing > 0 {
			continue
		}
		share := math.Min(float64(left), float64(right)) / float64(len(frags))
		if share > bestScore {
			best, bestScore = x, share
		}
	}
	if bestScore < 0.25 {
		return [][]frag{frags}
	}

	var left, right []frag
	for _, f := range frags {
		if f.x < best {
			left = append(left, f)
		} else {
			right = append(right, f)
		}
	}
	return [][]frag{left, right}
}
