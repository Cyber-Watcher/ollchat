package stats

// При каком γ понятия одного вопроса попадают в одну тему (этап 104, П9.2).
//
// **Зачем.** Тем нижнего уровня 56 тысяч на 264 тысячи понятий — пять понятий
// на тему, и книжные приёмы, опирающиеся на сообщества (local search MS
// GraphRAG), у нас не работают: преобладающая тема есть у единиц вопросов.
// Этап 103 (Ш1) показал, что γ «невиновен» в одиночных темах; здесь вопрос
// другой и по делу: собирает ли хоть какое-то γ понятия ОДНОГО ВОПРОСА вместе.
//
// **Опорная линия обязательна.** При малом γ сообщества становятся огромными,
// и «вместе» оказывается что угодно. Поэтому рядом считается доля СЛУЧАЙНЫХ пар
// понятий в одной теме: польза темы — в разнице между парами вопроса
// и случайными, а не в самой доле.
//
// Карта не нужна, граф не меняется: считает проекция `ExperimentPartition`.
// Понятия пары берутся по именам из набора, понятия методологического вопроса —
// словесным входом (без вектора вопроса; так в шапке и сказано).

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

func cohesionGrid(g *graph.Graph, c *kb.Collection, grid []float64, current, relatedWeight float64,
	byOrigins bool, pairsSet, methodSet string, seed int64) {

	// Пары набора → номера понятий.
	var pairs [][2]uint32
	named := readRelationPairs(pairsSet)
	for _, p := range named {
		a, ok1 := g.Entities().Lookup(p[0])
		b, ok2 := g.Entities().Lookup(p[1])
		if ok1 && ok2 && a.ID != b.ID {
			pairs = append(pairs, [2]uint32{a.ID, b.ID})
		}
	}
	direct := 0
	for _, p := range pairs {
		if len(g.Edges().Between(p[0], p[1])) > 0 {
			direct++
		}
	}

	// Понятия входа методологических вопросов — словами, без карты.
	deps := find.Deps{Coll: c, Graph: g}
	var questions [][]uint32
	for _, q := range readMethodQuestions(methodSet) {
		res, err := find.Search(context.Background(), deps, q, find.Opts{})
		if err != nil {
			continue
		}
		var ids []uint32
		for _, e := range res.Entities {
			ids = append(ids, e.ID)
		}
		if len(ids) >= 2 {
			questions = append(questions, ids)
		}
	}

	// Случайные пары понятий со связями — опорная линия.
	live := g.Entities().Live()
	rnd := rand.New(rand.NewSource(seed))
	var random [][2]uint32
	for tries := 0; len(random) < 20000 && tries < 400000; tries++ {
		a, b := live[rnd.Intn(len(live))].ID, live[rnd.Intn(len(live))].ID
		if a != b {
			random = append(random, [2]uint32{a, b})
		}
	}

	fmt.Printf("\nП9.2. При каком γ понятия одного вопроса попадают в одну тему (зерно %d)\n", seed)
	fmt.Printf("  пар набора %s: %d из %d нашлись по именам, из них с прямой связью %d\n",
		pairsSet, len(pairs), len(named), direct)
	fmt.Printf("  методологических вопросов с двумя и более понятиями входа (СЛОВЕСНЫЙ вход, без карты): %d\n", len(questions))
	fmt.Printf("  условия как у рабочего разбиения: вес «связано» %.2f, по источникам %v, рабочее γ = %.2f\n\n",
		relatedWeight, byOrigins, current)
	fmt.Println("      γ      тем  крупнейшая   пары вопроса   случайные пары   во сколько раз   вопросы с общей темой   время")
	fmt.Println("                                 в одной теме    в одной теме         чаще          у двух и более понятий")

	for _, res := range grid {
		start := time.Now()
		r := g.ExperimentPartition(graph.PartitionOpts{
			Weights:        map[uint8]float64{graph.RelRelated: relatedWeight},
			Resolution:     res,
			ByOrigins:      byOrigins,
			KeepAssignment: true,
		})
		same := func(list [][2]uint32) (int, int) {
			n, both := 0, 0
			for _, p := range list {
				ca, ok1 := r.Assign[p[0]]
				cb, ok2 := r.Assign[p[1]]
				if !ok1 || !ok2 {
					continue
				}
				both++
				if ca == cb {
					n++
				}
			}
			return n, both
		}
		pn, pall := same(pairs)
		rn, rall := same(random)
		withMain := 0
		for _, ids := range questions {
			count := map[uint32]int{}
			for _, id := range ids {
				if cm, ok := r.Assign[id]; ok {
					count[cm]++
				}
			}
			for _, n := range count {
				if n >= 2 {
					withMain++
					break
				}
			}
		}
		pShare := 100 * float64(pn) / float64(max(pall, 1))
		rShare := 100 * float64(rn) / float64(max(rall, 1))
		lift := "—"
		if rShare > 0 {
			lift = fmt.Sprintf("%.0f", pShare/rShare)
		}
		mark := "  "
		if res == current {
			mark = "→ "
		}
		fmt.Printf("%s%6.2f %8d %11d %7d/%d %4.1f%% %9d/%d %5.2f%% %12s %12d/%d %4.0f%% %9s\n",
			mark, res, r.Themes, r.Largest, pn, pall, pShare, rn, rall, rShare, lift,
			withMain, len(questions), 100*float64(withMain)/float64(max(len(questions), 1)),
			time.Since(start).Round(time.Second))
	}
	fmt.Println()
	fmt.Println("  Как читать. «Пары вопроса в одной теме» сами по себе ничего не значат: при малом γ")
	fmt.Println("  темы огромны, и в одну попадает что угодно — это видно по случайным парам.")
	fmt.Println("  Годное γ — то, где пары вопроса вместе хотя бы у половины, а случайные — редко.")
}
