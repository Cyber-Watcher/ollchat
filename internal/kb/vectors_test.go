// Сторож нормировки векторов. Заведён 30.09.2026 по итогам замера долга Д15:
// `Cosine` делит скалярное произведение на постоянную 127² и потому верен
// ТОЛЬКО пока `Quantize` нормирует вектор к единице. Если нормировка когда-нибудь
// уплывёт — от нового эмбеддера, от правки квантователя, от смены размерности, —
// молча поедет ранжирование смыслового поиска (`fusion.go`, `searchVectors`),
// пороги `kb.min_cosine`, склейка похожих выдач и вход в граф. Этот тест падает
// раньше, чем это случится.
package kb

import (
	"math"
	"math/rand"
	"testing"
)

// normOf — настоящая длина квантованного вектора.
func normOf(q []int8) float64 {
	var sum float64
	for _, x := range q {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// Квантованная длина обязана лежать у 127 — и не «примерно», а в пределах,
// которые ДИКТУЕТ округление: округление каждого из dim чисел до целого
// добавляет к квадрату длины дисперсию равномерной ошибки 1/12 на измерение,
// то есть ожидаемая длина равна sqrt(127² + dim/12). Замер 30.09.2026 на живой
// библиотеке подтвердил это до четвёртого знака: медиана 127,334 при
// предсказанных 127,3355 (555 211 векторов кусков, 391 400 векторов понятий,
// ни одного с отклонением больше 5 %).
func TestQuantizedNormStaysNear127(t *testing.T) {
	const dim = 1024
	want := math.Sqrt(127*127 + float64(dim)/12)

	rnd := rand.New(rand.NewSource(20260930))
	worst := 0.0
	for range 200 {
		v := make([]float32, dim)
		for i := range v {
			// Направление произвольное, длина произвольная: Quantize обязан
			// снять и то и другое, оставив только направленность.
			v[i] = float32(rnd.NormFloat64()) * float32(1+rnd.Float64()*50)
		}
		q := Quantize(v)
		if len(q) != dim {
			t.Fatalf("размерность %d вместо %d", len(q), dim)
		}
		got := normOf(q)
		if off := math.Abs(got-want) / want; off > worst {
			worst = off
		}
	}
	// 1 % — с запасом больше, чем разброс на живых данных (там 1-й и 99-й
	// процентили дают 126,661 и 128,004, то есть ±0,53 %), и заметно меньше
	// любого настоящего сбоя нормировки: без неё длина зависела бы от длины
	// исходного вектора и уехала бы в разы.
	if worst > 0.01 {
		t.Errorf("длина квантованного вектора уехала от %.4f на %.2f%% — нормировка сломана; "+
			"Cosine делит на 127² и станет врать, а с ним поедут пороги min_cosine и склейка", want, worst*100)
	}
}

// Длина исходного вектора на итог не влияет: тот же вектор, умноженный на 1000,
// обязан дать те же числа. Это и есть «нормировка обязательна» из комментария
// к Quantize, проверенное, а не обещанное.
func TestQuantizeIgnoresInputLength(t *testing.T) {
	const dim = 64
	v := make([]float32, dim)
	rnd := rand.New(rand.NewSource(1))
	for i := range v {
		v[i] = float32(rnd.NormFloat64())
	}
	big := make([]float32, dim)
	for i := range v {
		big[i] = v[i] * 1000
	}
	a, b := Quantize(v), Quantize(big)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("измерение %d: %d против %d — длина исходного вектора просочилась в итог", i, a[i], b[i])
		}
	}
}

// Косинус на квантованных векторах обязан совпадать с косинусом на исходных
// с точностью до шума округления. Тест идёт ТЕМ ЖЕ путём, что работа: через
// Quantize и Cosine, а не через свою арифметику.
func TestCosineOnQuantizedMatchesFloat(t *testing.T) {
	const dim = 1024
	rnd := rand.New(rand.NewSource(7))
	mkVec := func() []float32 {
		v := make([]float32, dim)
		for i := range v {
			v[i] = float32(rnd.NormFloat64())
		}
		return v
	}
	cosFloat := func(a, b []float32) float64 {
		var dot, na, nb float64
		for i := range a {
			dot += float64(a[i]) * float64(b[i])
			na += float64(a[i]) * float64(a[i])
			nb += float64(b[i]) * float64(b[i])
		}
		return dot / math.Sqrt(na*nb)
	}
	worst := 0.0
	check := func(a, b []float32) {
		got := Cosine(Quantize(a), Quantize(b))
		want := cosFloat(a, b)
		if d := math.Abs(got - want); d > worst {
			worst = d
		}
	}
	for range 100 {
		check(mkVec(), mkVec())
	}
	// Независимые случайные векторы дают косинус около нуля, а там ошибка
	// нормы почти не видна: она умножается на сам косинус. Пороги склейки
	// и поиска лежат в 0,7–0,9, поэтому проверяются и пары с заданной
	// близостью: b = c·a + √(1−c²)·шум.
	for _, c := range []float64{0.3, 0.5, 0.7, 0.8, 0.9, 0.95} {
		for range 20 {
			a, noise := mkVec(), mkVec()
			b := make([]float32, dim)
			for i := range b {
				b[i] = float32(c*float64(a[i]) + math.Sqrt(1-c*c)*float64(noise[i]))
			}
			check(a, b)
		}
	}
	// Замер 30.09.2026: на 40 000 живых пар расхождение двух способов счёта
	// не превысило 0,0090 ни разу. Порог 0,02 оставляет запас на выборку
	// и на постоянный сдвиг 0,526 %, но ловит настоящую поломку.
	if worst > 0.02 {
		t.Errorf("косинус на квантованных векторах разошёлся с косинусом на исходных на %.4f", worst)
	}
}
