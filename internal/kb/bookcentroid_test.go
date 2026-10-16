package kb

import "testing"

// Книги по близости вопроса к вектору книги (этап 112, У2): ближайшая
// первой, при равном косинусе — меньший номер, n обрезает список.
func TestRankByCentroid(t *testing.T) {
	cents := map[uint32][]float32{
		7: {1, 0, 0},
		3: {0, 1, 0},
		9: {0.6, 0.8, 0},
		5: {0, 1, 0}, // та же близость, что у книги 3: порядок по номеру
	}
	for _, v := range cents {
		unitize(v)
	}
	q := []int8{0, 127, 0}
	got := rankByCentroid(q, cents, 0)
	want := []uint32{3, 5, 9, 7}
	if len(got) != len(want) {
		t.Fatalf("книг %d, ждали %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок %v, ждали %v", got, want)
		}
	}
	if top := rankByCentroid(q, cents, 2); len(top) != 2 || top[0] != 3 || top[1] != 5 {
		t.Errorf("первые две: %v, ждали [3 5]", top)
	}
	// Вектор другой длины (книга посчитана другой моделью) не участвует.
	cents[11] = []float32{1, 0}
	if got := rankByCentroid(q, cents, 0); len(got) != 4 {
		t.Errorf("книга с вектором другой длины попала в порядок: %v", got)
	}
}

func TestUnitize(t *testing.T) {
	v := []float32{3, 4}
	unitize(v)
	if v[0] != 0.6 || v[1] != 0.8 {
		t.Errorf("вектор (3, 4) после нормировки %v, ждали (0.6, 0.8)", v)
	}
	zero := []float32{0, 0}
	unitize(zero)
	if zero[0] != 0 || zero[1] != 0 {
		t.Errorf("нулевой вектор испорчен: %v", zero)
	}
}

func TestDocOfChunkID(t *testing.T) {
	if doc, ok := DocOfChunkID("books/98#173"); !ok || doc != 98 {
		t.Errorf("books/98#173 → %d, %v; ждали 98", doc, ok)
	}
	for _, bad := range []string{"", "books", "books/x#1", "books/98"} {
		if _, ok := DocOfChunkID(bad); ok {
			t.Errorf("%q разобрана как ссылка на кусок", bad)
		}
	}
}
