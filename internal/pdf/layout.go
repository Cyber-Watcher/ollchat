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
		out = append(out, joinLine(line, originX, unit))
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
func joinLine(line []frag, originX, unit float64) string {
	sort.Slice(line, func(i, j int) bool { return line[i].x < line[j].x })

	// maxCols ограничивает длину строки: страницы бывают широкие, а строка
	// в тысячу пробелов не помогает никому.
	const maxCols = 300

	line = dropDotSpacers(line)

	var b strings.Builder
	var prevEnd float64
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
		case gap < unit*0.4:
			// Разрыва нет: это одно слово, разрезанное кернингом или сменой
			// шрифта. Ставить пробел по одной лишь арифметике колонок нельзя —
			// получится «Опис ание».
		default:
			// Разрыв есть: тянем кусок к его колонке, но не меньше пробела,
			// иначе соседние столбцы слипаются в «array of-».
			b.WriteString(strings.Repeat(" ", max(1, want-cur)))
		}
		b.WriteString(f.text)
		prevEnd = f.x + f.w
	}
	return strings.TrimRight(b.String(), " ")
}

// dropDotSpacers убирает точки, нарисованные вместо пробелов.
//
// **Что это за беда.** Встречаются PDF, где пробел между словами нарисован
// отдельной точкой: строка состоит из кусков «Систему», «.», «Java», «.»,
// «регламентируют». Наш разбор честно читает нарисованное, и текст выходит
// как «Систему.Java.регламентируют» — а такой текст ломает всё сразу: поиск
// по словам не найдёт «Систему Java», модель извлечения читает слипшийся
// текст, эмбеддер строит вектор по неверным токенам.
//
// **Замер 17.09.2026:** так испорчено 0,65% кусков библиотеки, но в отдельных
// книгах — от 67% до 89% («Облачные архитектуры», «LLM на практике», «Java для
// опытных разработчиков»). Сторонний pdftotext такие точки не показывает.
//
// **Как отличить от настоящей точки.** Настоящая точка в конце предложения
// приклеена к слову и приходит одним куском с ним («циями.»). Точка-заменитель
// приходит ОТДЕЛЬНЫМ куском и узка: 2.7 при кегле 10.2, то есть около
// четверти кегля. Поэтому убираются только куски, состоящие ровно из точки
// и уже́ трети кегля.
//
// Кусок не выбрасывается, а **становится пробелом**: выбросить его мало —
// слова тогда слипаются («языкасредуобщегоназначения»), потому что разрыв
// между соседями оказывается меньше порога, по которому joinLine ставит
// пробел. Проверено на той же странице 17.09.2026.
//
// **Точки-выноски в оглавлении** («Глава 1 . . . . 15») тоже уйдут, и это
// к лучшему: в тексте куска они не нужны, а номер страницы останется.
func dropDotSpacers(line []frag) []frag {
	out := make([]frag, 0, len(line))
	for _, f := range line {
		if f.text == "." && f.size > 0 && f.w > 0 && f.w < f.size/3 {
			f.text = " "
		}
		out = append(out, f)
	}
	return out
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
