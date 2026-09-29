package stats

// Калибровка resolution (γ) разбиения — этап 103, шаг Ш1.
//
// «Domain-Specific Small Language Models» (Iozzia, 2026, стр. 321): «параметр
// resolution управляет размером сообществ: чем выше значение, тем мельче
// сообщества». У нас γ стоит с самого начала (умолчание кода 5.0, в конфиге
// ноль = «взять умолчание») и ни разу не проверялся замером, а тем из одного
// понятия 14 311 из 55 935 — ровно тот признак, на который γ влияет.
//
// **Карта не нужна и граф не меняется.** Считает проекция
// `ExperimentPartition`: строит своё разбиение в памяти и ничего не пишет.
// Граф открывается один раз на всю сетку.
//
// **Условия берутся из конфига, а не выдумываются**: вес «связано»
// (`graph.related_weight`) и веса по источникам (`graph.weights_by_origins`) —
// те же, что у рабочего разбиения. Иначе сравнивать результат с нынешним
// состоянием графа нельзя: у Ф1 уже был такой промах, когда замер проекции
// считался по кускам, а рабочий путь шёл по источникам.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// parseGrid разбирает сетку значений «1,2,3.5» в числа.
func parseGrid(s string) ([]float64, error) {
	var out []float64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, fmt.Errorf("не понял значение %q: %w", part, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("значение %v: γ должно быть больше нуля", v)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("сетка пуста")
	}
	sort.Float64s(out)
	return out, nil
}

// resolutionGrid считает разбиение для каждого значения γ и печатает таблицу.
//
// current — рабочее значение (уже с раскрытым умолчанием), оно помечается
// в выдаче: сравнивать надо с тем, что стоит сейчас, а не с абстрактной
// единицей.
func resolutionGrid(g *graph.Graph, grid []float64, current, relatedWeight float64, byOrigins bool) {
	fmt.Printf("\nШ1. Калибровка resolution (этап 103). Условия как у рабочего разбиения:\n")
	fmt.Printf("  вес «связано» %.2f, веса по источникам %v, рабочее γ = %.2f\n",
		relatedWeight, byOrigins, current)
	fmt.Println("  Граф не изменяется: считает проекция ExperimentPartition.")
	fmt.Println()
	fmt.Println("      γ      тем   одиночных   доля   медиана  крупнейшая   понятий в темах   время")

	opts := func(res float64) graph.PartitionOpts {
		return graph.PartitionOpts{
			Weights:    map[uint8]float64{graph.RelRelated: relatedWeight},
			Resolution: res,
			ByOrigins:  byOrigins,
		}
	}

	for _, res := range grid {
		start := time.Now()
		r := g.ExperimentPartition(opts(res))
		share := 0.0
		if r.Themes > 0 {
			share = 100 * float64(r.Singleton) / float64(r.Themes)
		}
		mark := "  "
		if res == current {
			mark = "→ " // то, что стоит сейчас
		}
		fmt.Printf("%s%6.2f %8d %11d %6.1f%% %9d %11d %17d %7s\n",
			mark, res, r.Themes, r.Singleton, share, r.Median, r.Largest, r.Nodes,
			time.Since(start).Round(time.Second))
	}

	fmt.Println()
	fmt.Println("  Как читать: «одиночных» — темы из одного понятия, они не дают обзору")
	fmt.Println("  ничего и не получают описания. «понятий в темах» обязано быть одинаковым")
	fmt.Println("  во всех строках: γ не выбрасывает понятия, в отличие от порога веса.")
	fmt.Println("  Структурный выбор — только отсев; решает замер входа в граф")
	fmt.Println("  (--graph-entry-eval), см. docs/plan/stage103.md, шаг Ш1.4.")
}
