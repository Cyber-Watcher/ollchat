package kb

import "testing"

// Повтор выбрасывается, а порядок ранжирования сохраняется (этап 105, Ж4).
//
// Векторы подставляются прямо: близость — арифметика, и городить вокруг неё
// настоящий счёт эмбеддингов значило бы мерить не то.
//
// Сквозного теста здесь намеренно НЕТ. Повторы из выдачи убирают текстовые
// правила поиска — и они уже закреплены своими тестами
// (`TestSearchDropsNearDuplicates`, `TestSearchDropsAdjacentChunks`,
// `TestSearchDropsSameParagraphFromTwoEditions`), а векторной проверке после
// них делать нечего: замер на золотом наборе при пороге 0,90 не изменил ни
// одного числа. Писать четвёртый тест о том же значило бы дублировать их.
func TestDedupeSimilarKeepsHigherRanked(t *testing.T) {
	dim := 4
	// Три куска: первый и третий почти одинаковы, второй — про другое.
	vecs := &Vectors{
		meta: VecMeta{Count: 3, Dim: dim},
		data: append(append(append([]int8{},
			127, 0, 0, 0), // 0: направление A
			0, 127, 0, 0), // 1: направление B
			120, 40, 0, 0), // 2: почти A
	}
	c := &Collection{vectors: vecs}
	hits := []Result{
		{ID: "b/1#0", Chunk: 0, Score: 9},
		{ID: "b/1#1", Chunk: 1, Score: 8},
		{ID: "b/1#2", Chunk: 2, Score: 7},
	}

	out, dropped := c.DedupeSimilar(hits, 0.90)
	if dropped != 1 || len(out) != 2 {
		t.Fatalf("ожидался один выброшенный из трёх, получено dropped=%d, осталось %d", dropped, len(out))
	}
	if out[0].ID != "b/1#0" || out[1].ID != "b/1#1" {
		t.Errorf("порядок ранжирования не сохранён: %s, %s", out[0].ID, out[1].ID)
	}

	// Порог выше близости — не выбрасывается ничего.
	if out, dropped := c.DedupeSimilar(hits, 0.99); dropped != 0 || len(out) != 3 {
		t.Errorf("при пороге 0.99 ничего выбрасывать не должно: dropped=%d, осталось %d", dropped, len(out))
	}
	// Ноль — выключатель.
	if out, dropped := c.DedupeSimilar(hits, 0); dropped != 0 || len(out) != 3 {
		t.Errorf("ноль обязан выключать проверку: dropped=%d, осталось %d", dropped, len(out))
	}
}

// Кусок без вектора остаётся: молча выбрасывать то, чего не измерил, нельзя.
// Так бывает у свежих книг, пока не прошёл счёт смыслов.
func TestDedupeKeepsChunksWithoutVectors(t *testing.T) {
	vecs := &Vectors{
		meta: VecMeta{Count: 1, Dim: 4},
		data: []int8{127, 0, 0, 0},
	}
	c := &Collection{vectors: vecs}
	hits := []Result{
		{ID: "b/1#0", Chunk: 0},
		{ID: "b/1#7", Chunk: 7}, // вектора нет
		{ID: "b/1#9", Chunk: 9}, // и тут нет
	}
	out, dropped := c.DedupeSimilar(hits, 0.5)
	if dropped != 0 || len(out) != 3 {
		t.Fatalf("куски без векторов обязаны остаться: dropped=%d, осталось %d", dropped, len(out))
	}
	// Без векторов вовсе проверка тоже не должна ничего ломать.
	empty := &Collection{}
	if out, dropped := empty.DedupeSimilar(hits, 0.5); dropped != 0 || len(out) != 3 {
		t.Errorf("без векторов проверка обязана молчать: dropped=%d, осталось %d", dropped, len(out))
	}
}
