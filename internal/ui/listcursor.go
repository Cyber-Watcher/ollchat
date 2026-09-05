package ui

// listCursor — положение в списке панели: выбранная строка и первая видимая.
//
// Одна на пять панелей (выбор сервера и модели, файлы по «@», команды по «/»,
// вложения, найденное): до этапа 91 (R6.8) каждая держала свою копию move
// и ensureVisible, и правка одной не доходила до остальных.
//
// Списки в панелях короткие, и стрелка с края уходит на другой край —
// так быстрее добраться до конца, чем листать через всё.
type listCursor struct {
	cursor int
	offset int
}

// ensure сдвигает окно так, чтобы выбранная строка была видна;
// visible — сколько строк помещается.
func (c *listCursor) ensure(visible int) {
	if c.cursor < c.offset {
		c.offset = c.cursor
	}
	if c.cursor >= c.offset+visible {
		c.offset = c.cursor - visible + 1
	}
	if c.offset < 0 {
		c.offset = 0
	}
}

// step двигает выбор на delta строк по кругу: с начала — в конец, с конца — в начало.
func (c *listCursor) step(delta, count, visible int) {
	if count == 0 {
		return
	}
	c.cursor += delta
	if c.cursor < 0 {
		c.cursor = count - 1
	}
	if c.cursor >= count {
		c.cursor = 0
	}
	c.ensure(visible)
}

// clamp удерживает выбор в границах списка, который мог стать короче.
func (c *listCursor) clamp(count, visible int) {
	if count == 0 {
		c.cursor, c.offset = 0, 0
		return
	}
	if c.cursor >= count {
		c.cursor = count - 1
	}
	if c.cursor < 0 {
		c.cursor = 0
	}
	c.ensure(visible)
}

// panelInner — ширина содержимого панели при ширине окна width: рамка
// занимает 2 колонки, отступы стиля — ещё 2, маркер строки — 2.
// Раньше литерал `width - 6` повторялся в пяти видах.
func panelInner(width int) int {
	inner := width - 6
	if inner < 10 {
		inner = 10
	}
	return inner
}
