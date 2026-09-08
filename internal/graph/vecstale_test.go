package graph

import "testing"

// Отпечаток текста ловит изменение и не срабатывает на совпадении.
func TestTextStampChangesWithText(t *testing.T) {
	a := textStamp("горутина")
	if a != textStamp("горутина") {
		t.Fatal("один и тот же текст дал разные отпечатки")
	}
	if a == textStamp("горутина, goroutine") {
		t.Fatal("добавленный синоним не изменил отпечаток")
	}
}

// Устаревшим считается понятие, чей текст изменился после счёта вектора.
func TestStaleEntitiesFindsChangedText(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	for _, n := range []string{"горутина", "канал", "рантайм"} {
		if _, _, err := g.Entities().Add(n, TypeConcept); err != nil {
			t.Fatal(err)
		}
	}

	// Векторы «посчитаны»: три понятия по два числа, отпечатки записаны.
	if err := g.SaveEntityVectors("проба", "", 2, []int8{1, 0, 0, 1, 1, 1}); err != nil {
		t.Fatal(err)
	}
	if ids, have := g.StaleEntities(); !have || len(ids) != 0 {
		t.Fatalf("сразу после счёта устаревших быть не может: %v (отпечатки: %v)", ids, have)
	}

	// У второго понятия появился синоним — текст изменился.
	if _, _, err := g.Entities().Add("канал", TypeConcept, "channel"); err != nil {
		t.Fatal(err)
	}
	ids, have := g.StaleEntities()
	if !have {
		t.Fatal("отпечатки пропали")
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("ожидалось устаревшее понятие №2, получено %v", ids)
	}
}

// Без отпечатков вопрос «что устарело» ответа не имеет — и это надо отличать
// от «всё в порядке», иначе пересчёт молча не сделает ничего.
func TestStaleEntitiesWithoutStamps(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, _, err := g.Entities().Add("горутина", TypeConcept); err != nil {
		t.Fatal(err)
	}
	if ids, have := g.StaleEntities(); have || len(ids) != 0 {
		t.Fatalf("без векторов и отпечатков ответа быть не должно: %v %v", ids, have)
	}
}
