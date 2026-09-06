package graph

import "testing"

// twoGroupVectors — векторы для сорока понятий двух заведомо разных смыслов.
//
// Понятия 1..20 смотрят в одну сторону пространства, 21..40 — в другую,
// перпендикулярную. Внутри группы близость около единицы, между группами —
// ноль. Такая расстановка и есть проверочный стенд меры: разбиение, положившее
// группы в разные темы, обязано получить большой зазор, а перемешавшее их —
// нулевой.
func twoGroupVectors(count int) *EntityVectors {
	const dim = 4
	data := make([]int8, count*dim)
	for i := 0; i < count; i++ {
		noise := int8(i % 7) // чтобы понятия не были копиями друг друга
		if i < count/2 {
			data[i*dim] = 127
			data[i*dim+1] = noise
		} else {
			data[i*dim+2] = 127
			data[i*dim+3] = noise
		}
	}
	return &EntityVectors{
		meta: entVecMeta{Magic: entVecMagic, Model: "test", Dim: dim, Count: count},
		data: data,
	}
}

// theme собирает тему из перечисленных понятий.
func theme(id int, ids ...uint32) Community {
	return Community{ID: id, Level: 0, Members: ids}
}

// Мера обязана отличать тему от каши — ради этого она и заводится.
//
// Проверка повторяет в малом замер 26.08.2026: разбиение с одним γ дало «тему»
// из 3 662 понятий, и по числам она выглядела как обычная крупная тема. Числа
// связности этого не ловят: разбиение оптимизирует именно связи. Векторы ловят.
func TestThemeCosineTellsThemeFromMush(t *testing.T) {
	g := &Graph{vecs: twoGroupVectors(40)}

	// Разбиение по смыслу: каждая группа — своя тема.
	good := &Communities{List: []Community{
		theme(1, members(1, 20)...),
		theme(2, members(21, 40)...),
	}}
	// Каша: в каждой теме половина одной группы и половина другой.
	mush := &Communities{List: []Community{
		theme(1, append(members(1, 10), members(21, 30)...)...),
		theme(2, append(members(11, 20), members(31, 40)...)...),
	}}

	gotGood := g.ThemeCosineOf(good, 0)
	gotMush := g.ThemeCosineOf(mush, 0)

	if !gotGood.Ready() || !gotMush.Ready() {
		t.Fatalf("мера не посчиталась: good=%+v mush=%+v", gotGood, gotMush)
	}
	if gotGood.Gap < 0.7 {
		t.Errorf("зазор осмысленного разбиения %.3f, ожидался близкий к единице (%+v)",
			gotGood.Gap, gotGood)
	}
	if gotMush.Gap > 0.2 {
		t.Errorf("зазор каши %.3f, ожидался около нуля (%+v)", gotMush.Gap, gotMush)
	}
	if gotGood.Gap <= gotMush.Gap {
		t.Errorf("мера не отличила тему от каши: %.3f против %.3f", gotGood.Gap, gotMush.Gap)
	}
}

// Два запуска на одних данных дают одно и то же.
//
// Разбиение в этом пакете детерминировано намеренно (см. community.go), и мера,
// плавающая от запуска к запуску, обесценила бы это свойство: две колонки чисел
// стало бы нельзя сравнивать.
func TestThemeCosineIsDeterministic(t *testing.T) {
	g := &Graph{vecs: twoGroupVectors(40)}
	c := &Communities{List: []Community{
		theme(1, members(1, 20)...),
		theme(2, members(21, 40)...),
	}}

	first := g.ThemeCosineOf(c, 0)
	for i := 0; i < 3; i++ {
		again := g.ThemeCosineOf(c, 0)
		if again != first {
			t.Fatalf("запуск %d дал другое: %+v против %+v", i+2, again, first)
		}
	}
}

// Нет векторов — нет и меры, и это говорится, а не выдаётся за ноль.
//
// Ноль означал бы «темы бессвязны», то есть приговор разбиению, тогда как
// на деле судить нечем. Граф без векторов — обычное состояние, а не поломка.
func TestThemeCosineWithoutVectorsSaysNothing(t *testing.T) {
	c := &Communities{List: []Community{theme(1, members(1, 20)...)}}

	for name, g := range map[string]*Graph{
		"векторов нет вовсе": {},
		"пустой паспорт":     {vecs: &EntityVectors{}},
	} {
		got := g.ThemeCosineOf(c, 0)
		if got.Ready() {
			t.Errorf("%s: мера объявила себя годной: %+v", name, got)
		}
		if got.Themes != 0 || got.Gap != 0 || got.Covered != 0 {
			t.Errorf("%s: вместо молчания выдан ответ: %+v", name, got)
		}
	}
}

// Понятия без вектора не идут в счёт молча: их доля названа.
//
// Векторы считаются реже, чем растёт граф (замер 06.09.2026: вектор был
// у 174 233 понятий из 195 098), и тема из одних свежих понятий не даёт
// никакого числа. Если не сказать об этом, покрытие ниже сотни выглядит
// как ухудшение разбиения.
func TestThemeCosineReportsCoverage(t *testing.T) {
	// Векторы посчитаны первым сорока понятиям из шестидесяти.
	g := &Graph{vecs: twoGroupVectors(40)}
	c := &Communities{List: []Community{
		theme(1, members(1, 20)...),  // вектор есть у всех
		theme(2, members(21, 40)...), // и здесь тоже
		theme(3, members(41, 60)...), // а здесь ни у кого: понятия свежие
	}}

	got := g.ThemeCosineOf(c, 0)
	if got.Covered < 0.66 || got.Covered > 0.67 {
		t.Errorf("покрытие %.3f, ожидалось две трети (%+v)", got.Covered, got)
	}
	if got.Themes != 2 {
		t.Errorf("тем в счёте %d, ожидалось две: третья без векторов", got.Themes)
	}
}

// Тема мельче порога в счёт не идёт: у пары понятий близость говорит о паре.
func TestThemeCosineSkipsTinyThemes(t *testing.T) {
	g := &Graph{vecs: twoGroupVectors(40)}
	small := make([]Community, 0, 10)
	for i := 0; i < 10; i++ {
		id := uint32(i*4) + 1
		small = append(small, theme(i+1, id, id+1, id+2, id+3)) // по четыре, порог пять
	}
	got := g.ThemeCosineOf(&Communities{List: small}, 0)
	if got.Ready() {
		t.Errorf("посчитано по темам мельче %d понятий: %+v", CosMinMembers, got)
	}
	if got.Covered < 0.99 {
		t.Errorf("покрытие %.3f, а векторы есть у всех: покрытие не зависит от порога", got.Covered)
	}
}

// Перемешанное разбиение сохраняет размеры тем и весь состав целиком.
//
// Размеры — потому что зазор зависит от размера темы, и нулевая гипотеза
// с другими размерами сравнивалась бы не с тем. Состав целиком — потому что
// потерянное или удвоенное понятие тихо сдвинуло бы контрольное число.
func TestShuffledKeepsSizesAndMembers(t *testing.T) {
	c := &Communities{List: []Community{
		theme(1, members(1, 20)...),
		theme(2, members(21, 25)...),
		theme(3, members(26, 60)...),
		{ID: 4, Level: 1, Members: members(1, 60)}, // верхний уровень не берётся
	}}

	got := ShuffledLevel0(c)
	if len(got.List) != 3 {
		t.Fatalf("тем %d, ожидалось три: уровень 1 в перемешивание не идёт", len(got.List))
	}
	seen := map[uint32]int{}
	for i, com := range got.List {
		if want := len(c.List[i].Members); len(com.Members) != want {
			t.Errorf("тема %d: понятий %d, было %d", com.ID, len(com.Members), want)
		}
		for _, m := range com.Members {
			seen[m]++
		}
	}
	if len(seen) != 60 {
		t.Errorf("понятий в перемешанном разбиении %d, было 60", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("понятие %d встречается %d раз", id, n)
		}
	}
}

// Нулевая гипотеза обязана давать ноль: у случайного разбиения понятия темы
// похожи на своих ровно настолько же, насколько на посторонних.
//
// Это же и проверка самого счёта: систематический перекос в мере вылез бы
// здесь ненулевым числом. Именно этот столбец 06.09.2026 позволил прочитать
// живой замер: зазор 0.058 при фоне 0.410 выглядит шумом, пока рядом нет нуля.
//
// Тем берётся сорок, а не две: медиана из двух значений — это верхнее из двух,
// и на такой выборке проверялся бы разброс, а не мера.
func TestShuffledGivesNoGap(t *testing.T) {
	g := &Graph{vecs: twoGroupVectors(400)}

	// Сорок тем по десять понятий, и каждая целиком внутри своей группы.
	var real Communities
	for i := 0; i < 40; i++ {
		from := uint32(i*10) + 1
		real.List = append(real.List, theme(i+1, members(from, from+9)...))
	}

	gotReal := g.ThemeCosineOf(&real, 0)
	gotNull := g.ThemeCosineOf(ShuffledLevel0(&real), 0)

	// Фон здесь — весь граф, а половина графа своей группы, поэтому «снаружи»
	// около 0.5, а не ноль: зазор осмысленного разбиения около половины.
	if gotReal.Gap < 0.4 {
		t.Fatalf("осмысленное разбиение дало зазор %.3f, ожидался около 0.5 (%+v)",
			gotReal.Gap, gotReal)
	}
	if gotNull.Gap > 0.05 || gotNull.Gap < -0.05 {
		t.Errorf("случайное разбиение дало зазор %.3f, ожидался ноль (%+v)",
			gotNull.Gap, gotNull)
	}
	if gotNull.Themes != gotReal.Themes {
		t.Errorf("тем в счёте: настоящих %d, случайных %d — сравнивать надо равное",
			gotReal.Themes, gotNull.Themes)
	}
}
