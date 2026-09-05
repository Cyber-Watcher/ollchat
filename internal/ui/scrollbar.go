package ui

import "strings"

// Символы полосы прокрутки.
const (
	scrollTrackChar = "│"
	scrollThumbChar = "█"
)

// scrollbar описывает геометрию полосы прокрутки ленты диалога.
//
// Вычисления вынесены из отрисовки, чтобы их можно было проверить тестами
// без терминала: ошибка в пересчёте «строка экрана ↔ позиция в тексте»
// проявляется как рывки бегунка, которые глазами ловить долго.
type scrollbar struct {
	height int // высота видимой области в строках
	total  int // всего строк в ленте
	offset int // текущая позиция первой видимой строки
}

// visible сообщает, нужна ли полоса: при коротком тексте её не показываем.
func (s scrollbar) visible() bool {
	return s.height > 0 && s.total > s.height
}

// maxOffset — наибольшее возможное значение offset.
func (s scrollbar) maxOffset() int {
	if !s.visible() {
		return 0
	}
	return s.total - s.height
}

// thumb возвращает начало и размер бегунка в строках.
func (s scrollbar) thumb() (start, size int) {
	if !s.visible() {
		return 0, 0
	}
	size = s.height * s.height / s.total
	if size < 1 {
		size = 1
	}
	if size > s.height {
		size = s.height
	}

	travel := s.height - size
	if travel <= 0 {
		return 0, size
	}

	offset := min(max(s.offset, 0), s.maxOffset())
	// Округление к ближайшему: иначе бегунок не доходит до низа.
	start = (offset*travel + s.maxOffset()/2) / s.maxOffset()
	return min(max(start, 0), travel), size
}

// offsetForRow переводит строку экрана в позицию прокрутки так, чтобы бегунок
// встал серединой на эту строку. Используется при щелчке и перетаскивании.
func (s scrollbar) offsetForRow(row int) int {
	if !s.visible() {
		return 0
	}
	_, size := s.thumb()
	travel := s.height - size
	if travel <= 0 {
		return 0
	}
	start := min(max(row-size/2, 0), travel)
	return min(max((start*s.maxOffset()+travel/2)/travel, 0), s.maxOffset())
}

// render рисует колонку полосы прокрутки: по одной ячейке на строку.
func (s scrollbar) render() []string {
	if s.height <= 0 {
		return nil
	}
	out := make([]string, s.height)
	if !s.visible() {
		// Полосы нет, но колонку сохраняем: иначе при появлении прокрутки
		// вся лента дёргалась бы на одну колонку.
		for i := range out {
			out[i] = " "
		}
		return out
	}

	start, size := s.thumb()
	for i := 0; i < s.height; i++ {
		if i >= start && i < start+size {
			out[i] = styScrollThumb.Render(scrollThumbChar)
			continue
		}
		out[i] = styScrollTrack.Render(scrollTrackChar)
	}
	return out
}

// attachScrollbar приклеивает колонку полосы прокрутки к правому краю ленты.
func attachScrollbar(view string, bar []string) string {
	if len(bar) == 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	for i := range lines {
		if i < len(bar) {
			lines[i] += bar[i]
		}
	}
	return strings.Join(lines, "\n")
}

// trimLineEnds убирает добивку пробелами в конце каждой строки.
//
// Лента приходит из viewport выровненной по ширине окна, и эти пробелы
// терминал добросовестно кладёт в буфер обмена вместе с выделенным текстом.
// Обрезаем только хвостовые пробелы: управляющие последовательности идут
// не после них, поэтому оформление строки не страдает.
func trimLineEnds(view string) string {
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
