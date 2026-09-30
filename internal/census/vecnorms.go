// Перепись длины квантованных векторов и расхождения двух косинусов проекта —
// прибор для долга Д15 (docs/plan/technical_debts.md).
//
// **Почему в проекте вообще два косинуса.** `kb.Cosine` (internal/kb/vectors.go)
// делит скалярное произведение на постоянную 127² и потому верен только для
// векторов, нормированных при записи; `vecstand.CosineRaw`
// (internal/graph/vecstand) считает полный косинус с настоящими нормами обоих
// векторов. Нормировка обещана и кускам коллекции (`kb.Quantize` перед записью),
// и понятиям графа (`internal/graph/embed.go`), но округление до int8 могло эту
// нормировку слегка испортить — вопрос в том, насколько. Замер, а не правка:
// оба косинуса остаются как есть.
package census

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph/vecstand"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// vecNormsPairs — сколько пар брать на сравнение формул в каждом хранилище.
// Число из задания Д15: маленькая выборка уже дважды подводила проект
// (my-mistakes.md), поэтому фиксировано большим и не подбирается.
const vecNormsPairs = 20000

// vecNormsSeed — сид случайной выборки пар: фиксирован ради воспроизводимости
// (повторный прогон обязан давать те же числа).
const vecNormsSeed = 20260930

// vecNorms — режим `-only vec-norms`: длина квантованных векторов кусков
// коллекции и понятий графа, и на скольких настоящих парах расходятся формулы
// `kb.Cosine` и `vecstand.CosineRaw`.
func vecNorms(stdout io.Writer, cfg *config.Config, collName string) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintln(stdout, "== векторы кусков коллекции ==")
	chunkVecs, err := collectChunkVectors(c)
	if err != nil {
		return err
	}
	printVecNormStats(stdout, "куски", c.VecMeta().Dim, computeVecNormStats(chunkVecs))

	fmt.Fprintln(stdout, "\n== векторы понятий графа ==")
	gdir := filepath.Join(c.Dir(), "graph")
	gv, gerr := vecstand.Load(gdir)
	var entVecs [][]int8
	if gerr != nil {
		fmt.Fprintf(stdout, "векторы понятий не прочитаны: %v\n", gerr)
	} else {
		entVecs = entityVectors(gv)
		printVecNormStats(stdout, "понятия", gv.Dim, computeVecNormStats(entVecs))
	}

	fmt.Fprintln(stdout, "\n== расхождение kb.Cosine и vecstand.CosineRaw на настоящих парах ==")
	if len(chunkVecs) >= 2 {
		printCosineGap(stdout, "куски", computeCosineGap(chunkVecs, vecNormsSeed, vecNormsPairs))
	} else {
		fmt.Fprintln(stdout, "куски: векторов меньше двух, пары не из чего брать")
	}
	if len(entVecs) >= 2 {
		printCosineGap(stdout, "понятия", computeCosineGap(entVecs, vecNormsSeed, vecNormsPairs))
	} else {
		fmt.Fprintln(stdout, "понятия: векторов меньше двух, пары не из чего брать")
	}
	return nil
}

// collectChunkVectors читает векторы всех кусков коллекции.
//
// Прямого доступа по сквозному номеру у census нет: поле `vectors` приватно
// пакету kb, наружу отдаётся только `ChunkVectorByRef` по устойчивой ссылке
// «книга, номер внутри книги» (internal/kb/iterate.go). Ссылки собираются
// отдельным проходом — и только потом, вне замка `EachChunkRef`, читаются
// векторы: `ChunkVectorByRef` сам берёт `RLock`, а вложенный `RLock` внутри
// уже занятого `RLock` того же `sync.RWMutex` документацией Go не гарантирован
// и может застрять, если между двумя вызовами вклинится писатель.
func collectChunkVectors(c *kb.Collection) ([][]int8, error) {
	total := c.ChunkCount()
	fmt.Fprintln(os.Stderr, "читаю ссылки на куски коллекции…")
	refs := make([]kb.ChunkRef, 0, total)
	if err := c.EachChunkRef(kb.ChunkFilter{}, func(ref kb.ChunkRef) error {
		refs = append(refs, ref)
		return nil
	}); err != nil {
		return nil, err
	}

	out := make([][]int8, 0, len(refs))
	started := time.Now()
	warned := false
	for i, ref := range refs {
		if v, ok := c.ChunkVectorByRef(ref.Doc, ref.Ord); ok {
			out = append(out, v)
		}
		if !warned && time.Since(started) > 2*time.Second {
			warned = true
		}
		if warned && i%50000 == 0 {
			fmt.Fprintf(os.Stderr, "\r  векторы кусков: %d/%d   ", i, len(refs))
		}
	}
	if warned {
		fmt.Fprintln(os.Stderr)
	}
	return out, nil
}

// entityVectors — векторы понятий графа списком; пропущенных номеров внутри
// [0, Count) не бывает (гарантия `vecstand.Vectors`), но проверка второго
// значения дешева и снимает вопрос.
func entityVectors(gv *vecstand.Vectors) [][]int8 {
	out := make([][]int8, 0, gv.Count)
	for i := 0; i < gv.Count; i++ {
		if v, ok := gv.Vector(i); ok {
			out = append(out, v)
		}
	}
	return out
}

// vecLen — настоящая (евклидова) длина квантованного вектора.
func vecLen(v []int8) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// vecNormStats — числа одного распределения длины векторов, отдельно от
// печати: тест проверяет числа, а не текст.
type vecNormStats struct {
	count                 int
	min, p1, median, mean float64
	p99, max              float64
	over05, over1, over5  int // отклонение от 127 больше чем на 0,5% / 1% / 5%
	zero                  int
}

// computeVecNormStats считает распределение длины по всем переданным векторам.
func computeVecNormStats(vecs [][]int8) vecNormStats {
	st := vecNormStats{count: len(vecs)}
	if len(vecs) == 0 {
		return st
	}
	lens := make([]float64, len(vecs))
	var sum float64
	for i, v := range vecs {
		l := vecLen(v)
		lens[i] = l
		sum += l
		if l == 0 {
			st.zero++
		}
		dev := math.Abs(l-127) / 127
		if dev > 0.05 {
			st.over5++
		}
		if dev > 0.01 {
			st.over1++
		}
		if dev > 0.005 {
			st.over05++
		}
	}
	sort.Float64s(lens)
	st.min = lens[0]
	st.max = lens[len(lens)-1]
	st.mean = sum / float64(len(lens))
	st.p1 = percentileAt(lens, 1)
	st.median = percentileAt(lens, 50)
	st.p99 = percentileAt(lens, 99)
	return st
}

// printVecNormStats печатает распределение длины одного хранилища.
func printVecNormStats(stdout io.Writer, label string, dim int, st vecNormStats) {
	fmt.Fprintf(stdout, "%s: векторов %d, размерность %d\n", label, st.count, dim)
	if st.count == 0 {
		return
	}
	fmt.Fprintf(stdout, "  длина: мин %.3f, 1-й процентиль %.3f, медиана %.3f, среднее %.3f, 99-й процентиль %.3f, макс %.3f\n",
		st.min, st.p1, st.median, st.mean, st.p99, st.max)
	fmt.Fprintf(stdout, "  отклонение от 127: >0,5%% — %d, >1%% — %d, >5%% — %d; нулевых векторов — %d\n",
		st.over05, st.over1, st.over5, st.zero)
}

// cosineGapStats — числа расхождения двух формул на выборке пар.
type cosineGapStats struct {
	pairs                   int
	median, p99, maxAbs     float64
	maxI, maxJ              int
	over001, over01, over05 int
}

// computeCosineGap считает разницу `kb.Cosine - vecstand.CosineRaw` на
// воспроизводимой случайной выборке из `pairs` пар векторов одного хранилища.
func computeCosineGap(vecs [][]int8, seed int64, pairs int) cosineGapStats {
	n := len(vecs)
	rnd := rand.New(rand.NewSource(seed))
	diffs := make([]float64, 0, pairs)
	var st cosineGapStats
	for k := 0; k < pairs; k++ {
		i := rnd.Intn(n)
		j := rnd.Intn(n)
		for j == i {
			j = rnd.Intn(n)
		}
		a, b := vecs[i], vecs[j]
		d := math.Abs(kb.Cosine(a, b) - vecstand.CosineRaw(a, b))
		diffs = append(diffs, d)
		if d > st.maxAbs {
			st.maxAbs, st.maxI, st.maxJ = d, i, j
		}
		if d > 0.001 {
			st.over001++
		}
		if d > 0.01 {
			st.over01++
		}
		if d > 0.05 {
			st.over05++
		}
	}
	sort.Float64s(diffs)
	st.pairs = len(diffs)
	st.median = percentileAt(diffs, 50)
	st.p99 = percentileAt(diffs, 99)
	return st
}

// printCosineGap печатает расхождение формул одного хранилища.
func printCosineGap(stdout io.Writer, label string, st cosineGapStats) {
	fmt.Fprintf(stdout, "%s: пар %d, |разница|: медиана %.6f, 99-й процентиль %.6f, максимум %.6f (пара индексов %d/%d)\n",
		label, st.pairs, st.median, st.p99, st.maxAbs, st.maxI, st.maxJ)
	fmt.Fprintf(stdout, "  разница больше 0,001 — %d, больше 0,01 — %d, больше 0,05 — %d\n",
		st.over001, st.over01, st.over05)
}

// percentileAt — значение p-го процентиля (0..100) в ОТСОРТИРОВАННОМ срезе,
// линейная интерполяция между соседними точками. Готовой функции процентиля
// в проекте нет (проверено при написании этого прибора, 30.09.2026, Д15).
func percentileAt(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
