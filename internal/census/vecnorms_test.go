// Тесты прибора долга Д15: счётчик длины вектора и расхождение kb.Cosine
// с vecstand.CosineRaw. Векторы строятся руками — ожидаемые числа посчитаны
// из самого построения (long division и теорема Пифагора), а не подсмотрены
// в выводе прибора.
package census

import (
	"math"
	"testing"
)

// closeEnough — сравнение float64 с запасом на округление double.
func closeEnough(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

// TestVecLenIsEuclideanNorm проверяет счётчик длины на классическом
// прямоугольном треугольнике 3-4-5: сумма квадратов 9+16=25, корень — 5.
func TestVecLenIsEuclideanNorm(t *testing.T) {
	got := vecLen([]int8{3, 4, 0, 0})
	if !closeEnough(got, 5, 1e-9) {
		t.Fatalf("vecLen([3,4,0,0]) = %v, ожидалось 5", got)
	}
	if got := vecLen([]int8{0, 0, 0, 0}); got != 0 {
		t.Fatalf("vecLen нулевого вектора = %v, ожидалось 0", got)
	}
}

// TestComputeVecNormStats проверяет распределение длины на пяти построенных
// векторах: два ровно 127 (нормированы верно), один заметно короче (10),
// один чуть короче 127 (126 — отклонение 0,79%, между порогами 0,5% и 1%),
// один нулевой (у понятия нет вектора).
//
// Отсортированные длины: 0, 10, 126, 127, 127. Среднее (0+10+126+127+127)/5=78.
// Процентиль — линейная интерполяция по рангу p/100*(n-1):
//
//	p1:  ранг 0,04 → между sorted[0]=0 и sorted[1]=10 на 4% пути → 0,4
//	p50: ранг 2,0  → sorted[2] → 126
//	p99: ранг 3,96 → между sorted[3]=127 и sorted[4]=127 → 127
func TestComputeVecNormStats(t *testing.T) {
	vecs := [][]int8{
		{127, 0, 0, 0},  // длина 127 — точная нормировка
		{0, -127, 0, 0}, // длина 127 — точная нормировка, отрицательная ось
		{10, 0, 0, 0},   // длина 10 — заведомо короткий
		{126, 0, 0, 0},  // длина 126 — отклонение 0,79%
		{0, 0, 0, 0},    // нулевой вектор
	}
	st := computeVecNormStats(vecs)

	if st.count != 5 {
		t.Fatalf("count = %d, ожидалось 5", st.count)
	}
	if st.zero != 1 {
		t.Errorf("zero = %d, ожидалось 1 (один нулевой вектор)", st.zero)
	}
	// >0,5%: 10 (92%), 126 (0,79%), 0 (100%) — три вектора.
	if st.over05 != 3 {
		t.Errorf("over05 = %d, ожидалось 3", st.over05)
	}
	// >1%: 10 и 0 — вектор 126 (0,79%) в этот порог не попадает.
	if st.over1 != 2 {
		t.Errorf("over1 = %d, ожидалось 2", st.over1)
	}
	// >5%: 10 и 0 — те же два.
	if st.over5 != 2 {
		t.Errorf("over5 = %d, ожидалось 2", st.over5)
	}
	if !closeEnough(st.min, 0, 1e-9) {
		t.Errorf("min = %v, ожидалось 0", st.min)
	}
	if !closeEnough(st.max, 127, 1e-9) {
		t.Errorf("max = %v, ожидалось 127", st.max)
	}
	if !closeEnough(st.mean, 78, 1e-9) {
		t.Errorf("mean = %v, ожидалось 78", st.mean)
	}
	if !closeEnough(st.p1, 0.4, 1e-9) {
		t.Errorf("p1 = %v, ожидалось 0,4", st.p1)
	}
	if !closeEnough(st.median, 126, 1e-9) {
		t.Errorf("median = %v, ожидалось 126", st.median)
	}
	if !closeEnough(st.p99, 127, 1e-9) {
		t.Errorf("p99 = %v, ожидалось 127", st.p99)
	}
}

// TestComputeCosineGapAgreesOnNormalizedVectors — на векторах, у каждого из
// которых длина РОВНО 127 (оси координат), у kb.Cosine и vecstand.CosineRaw
// один и тот же знаменатель 127×127 для любой пары, поэтому разница обязана
// быть точно нулевой на всех парах выборки.
func TestComputeCosineGapAgreesOnNormalizedVectors(t *testing.T) {
	vecs := [][]int8{
		{127, 0, 0, 0},
		{0, 127, 0, 0},
		{0, 0, 127, 0},
		{0, 0, 0, 127},
		{0, -127, 0, 0},
	}
	st := computeCosineGap(vecs, 1, 200)
	if st.pairs != 200 {
		t.Fatalf("pairs = %d, ожидалось 200", st.pairs)
	}
	if st.maxAbs != 0 {
		t.Errorf("maxAbs = %v, ожидалось 0 на нормированных векторах", st.maxAbs)
	}
	if st.median != 0 || st.p99 != 0 {
		t.Errorf("median=%v p99=%v, ожидались нули", st.median, st.p99)
	}
	if st.over001 != 0 || st.over01 != 0 || st.over05 != 0 {
		t.Errorf("over001=%d over01=%d over05=%d, ожидались нули", st.over001, st.over01, st.over05)
	}
}

// TestComputeCosineGapDivergesOnShortVectors — на заведомо ненормированной
// паре расхождение большое и посчитано заранее по определению обеих формул.
//
// a=(10,0), b=(10,10): dot=100, |a|=10, |b|=√200=10√2.
//
//	kb.Cosine  = dot/127² = 100/16129 ≈ 0,0062017
//	CosineRaw  = dot/(|a|·|b|) = 100/(10·10√2) = 1/√2 ≈ 0,7071068 (угол 45°)
//	|разница|  ≈ 0,7009051
//
// Оба вектора короче 127 в разы, поэтому kb.Cosine занижает многократно.
func TestComputeCosineGapDivergesOnShortVectors(t *testing.T) {
	vecs := [][]int8{
		{10, 0, 0, 0},
		{10, 10, 0, 0},
	}
	want := math.Abs(100.0/(127*127) - 1/math.Sqrt2)
	st := computeCosineGap(vecs, 1, 5)
	if !closeEnough(st.maxAbs, want, 1e-6) {
		t.Errorf("maxAbs = %v, ожидалось %v", st.maxAbs, want)
	}
	if !closeEnough(st.median, want, 1e-6) {
		t.Errorf("median = %v, ожидалось %v (пара всего одна)", st.median, want)
	}
	if st.over05 != st.pairs {
		t.Errorf("over05 = %d, ожидалось %d (разница %.4f больше 0,05 на каждой паре)", st.over05, st.pairs, want)
	}
}

// TestComputeVecNormStatsEmpty — на пустом хранилище прибор не должен падать.
func TestComputeVecNormStatsEmpty(t *testing.T) {
	st := computeVecNormStats(nil)
	if st.count != 0 {
		t.Fatalf("count = %d, ожидалось 0 на пустом хранилище", st.count)
	}
}
