package maint

// Замер входа в граф: находит ли он понятия, о которых спрашивают.
//
// **Зачем отдельный замер.** Четыре работы этапа 90 трогают вход в граф —
// порог близости, порядок синонимов, отбор двойников, новая схема извлечения, —
// а мерить вход нам до сих пор было нечем. `kb_golden.toml` меряет
// поиск по книгам и графа не видит вовсе, а слепое судейство моделью отвечает
// на другой вопрос и стоит часа карты.
//
// **Что меряется.** У каждого вопроса набора записаны ДВА понятия, о связи
// которых спрашивают (`concept_a`, `concept_b`) — правильный ответ входа известен
// заранее. Значит можно считать без модели и без судьи:
//
//	нашлись ли оба названных понятия и каким по счёту;
//	сколько в выдаче постороннего — того, о чём не спрашивали.
//
// Это не оценка «хорошо или плохо»: постороннее понятие бывает уместным.
// Это мера ПОВТОРЯЕМАЯ, и по ней видно, что дала правка — на том же наборе,
// теми же числами.
//
// Набор собирается `ollscripts/relationquestions.py` из совстречаемости понятий
// в кусках книг, а НЕ из связей графа: иначе замер спрашивал бы ровно о том,
// что граф знает по построению.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// entryCase — один вопрос набора.
type entryCase struct {
	Query     string `toml:"query"`
	ConceptA  string `toml:"concept_a"`
	ConceptB  string `toml:"concept_b"`
	GraphEdge bool   `toml:"graph_edge"`
	ChunkID   string `toml:"chunk_id"`
	Note      string `toml:"note"`
}

// entryScore — что вышло по одному вопросу.
type entryScore struct {
	RankA, RankB int // место понятия в выдаче, 1 — первое; 0 — не найдено
	Foreign      int // сколько выдано того, о чём прямо не спрашивали
	Total        int // всего понятий в выдаче

	// ForeignBySense — сколько постороннего пришло смысловым входом, а не
	// словесным. Разделение нужно затем, что чинятся они по-разному: у
	// смыслового отбор относительный (см. internal/graph/vectors.go), у
	// словесного — синонимы и основы слов. Без этого числа правку выбирают
	// на глаз, а глаз тут ошибается.
	ForeignBySense int
}

// scoreEntry считает места названных понятий и число постороннего.
//
// Сравниваются НОМЕРА понятий, а не имена: склейка двойников ведёт к выжившему,
// и «goroutines», спрошенное по имени, законно возвращается узлом «goroutine».
// Ноль в ожидаемом номере означает, что понятия в графе нет вовсе, — такой
// вопрос из счёта исключается, а не считается промахом входа.
func scoreEntry(want [2]uint32, got []uint32) entryScore {
	return scoreEntryWithOrigin(want, got, nil)
}

// scoreEntryWithOrigin — то же, но с пометкой, каким путём найдено каждое
// понятие: bySense[i] == true означает смысловой вход.
func scoreEntryWithOrigin(want [2]uint32, got []uint32, bySense []bool) entryScore {
	s := entryScore{Total: len(got)}
	for i, id := range got {
		switch {
		case want[0] != 0 && id == want[0] && s.RankA == 0:
			s.RankA = i + 1
		case want[1] != 0 && id == want[1] && s.RankB == 0:
			s.RankB = i + 1
		default:
			if (want[0] == 0 || id != want[0]) && (want[1] == 0 || id != want[1]) {
				s.Foreign++
				if i < len(bySense) && bySense[i] {
					s.ForeignBySense++
				}
			}
		}
	}
	return s
}

type EntryEvalOpts struct {
	Collection string
	Limit      int
	Show       int
	// Entities — сколько мест в карте понятий. 0 — как в настройках.
	// Ключом, а не правкой конфига: ручку надо мерить перебором, а замер,
	// требующий редактировать конфиг между прогонами, никто не повторит.
	Entities int
}

// EntryEval прогоняет набор вопросов через настоящий вход в граф.
func EntryEval(stdout io.Writer, cfg *config.Config, setPath string, o EntryEvalOpts) error {
	var set struct {
		Case []entryCase `toml:"case"`
	}
	if _, err := toml.DecodeFile(setPath, &set); err != nil {
		return fmt.Errorf("набор %s: %w", setPath, err)
	}
	if len(set.Case) == 0 {
		return fmt.Errorf("в наборе %s нет ни одного случая ([[case]])", setPath)
	}
	if o.Limit > 0 && o.Limit < len(set.Case) {
		set.Case = set.Case[:o.Limit]
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	name := o.Collection
	if name == "" {
		name = cfg.KB.Default
	}
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	printOpen(stdout, g, cfg)

	entities := cfg.Mix.Entities
	if o.Entities > 0 {
		entities = o.Entities
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	deps := find.Deps{
		Coll:     coll,
		Graph:    g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), fallback, 0, nil),
		Reranker: kbrerank.New(cfg.KB.RerankOptions()),
	}
	opts := find.Opts{
		Collection: name, TopK: cfg.KB.TopK, MaxPerBook: cfg.KB.MaxPerBook,
		MinCosine: cfg.KB.MinCosine, SemanticWeight: cfg.KB.SemanticWeight,
		Semantic: cfg.KB.Semantic, Rerank: deps.Reranker != nil,
		Entities: entities, Neighbors: cfg.Mix.Neighbors,
		Rank: NeighborRank(cfg), QueryTimeout: cfg.KB.QueryTimeoutDuration(),
		RerankOpts: kb.RerankOpts{Candidates: cfg.KB.RerankCandidates, Snippet: cfg.KB.RerankSnippet},
	}

	fmt.Fprintf(stdout, "\nзамер входа в граф: %s, коллекция %s, вопросов %d\n",
		setPath, name, len(set.Case))
	fmt.Fprintf(stdout, "понятий в выдаче входа: до %d (mix.entities)\n\n", entities)

	type row struct {
		c entryCase
		s entryScore
	}
	var rows []row
	var both, one, none, skipped, foreign, foreignSense, total int
	// Кто именно лезет в выдачу чаще всего: по этому списку видно, что чинить.
	type foreignStat struct {
		name  string
		count int
		sense int
	}
	foreignBy := map[uint32]*foreignStat{}
	var rankSum, rankCount int
	byEdge := map[bool][3]int{} // связь в графе → [оба, одно, ни одного]

	started := time.Now()
	for i := range set.Case {
		c := set.Case[i]
		var want [2]uint32
		if e, ok := g.Entities().Lookup(c.ConceptA); ok {
			want[0] = e.ID
		}
		if e, ok := g.Entities().Lookup(c.ConceptB); ok {
			want[1] = e.ID
		}
		if want[0] == 0 || want[1] == 0 {
			// Понятия нет в графе — вопрос не о входе, а о полноте графа.
			skipped++
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		res, err := find.Search(ctx, deps, c.Query, opts)
		cancel()
		if err != nil {
			return fmt.Errorf("вопрос %q: %w", c.Query, err)
		}
		got := make([]uint32, 0, len(res.Entities))
		bySense := make([]bool, 0, len(res.Entities))
		for _, e := range res.Entities {
			got = append(got, e.ID)
			bySense = append(bySense, e.Matched == "по смыслу")
		}
		s := scoreEntryWithOrigin(want, got, bySense)
		for i, e := range res.Entities {
			if e.ID == want[0] || e.ID == want[1] {
				continue
			}
			st := foreignBy[e.ID]
			if st == nil {
				st = &foreignStat{name: e.Name}
				foreignBy[e.ID] = st
			}
			st.count++
			if i < len(bySense) && bySense[i] {
				st.sense++
			}
		}
		rows = append(rows, row{c, s})

		total++
		foreign += s.Foreign
		foreignSense += s.ForeignBySense
		hit := 0
		for _, r := range []int{s.RankA, s.RankB} {
			if r > 0 {
				hit++
				rankSum += r
				rankCount++
			}
		}
		switch hit {
		case 2:
			both++
		case 1:
			one++
		default:
			none++
		}
		e := byEdge[c.GraphEdge]
		e[2-hit]++
		byEdge[c.GraphEdge] = e
	}

	if total == 0 {
		return fmt.Errorf("ни один вопрос не годен: все понятия набора отсутствуют в графе")
	}

	fmt.Fprintf(stdout, "оба понятия найдены:      %3d из %d (%.0f%%)\n", both, total, 100*float64(both)/float64(total))
	fmt.Fprintf(stdout, "одно из двух:             %3d (%.0f%%)\n", one, 100*float64(one)/float64(total))
	fmt.Fprintf(stdout, "ни одного:                %3d (%.0f%%)\n", none, 100*float64(none)/float64(total))
	if rankCount > 0 {
		fmt.Fprintf(stdout, "среднее место найденного: %.2f\n", float64(rankSum)/float64(rankCount))
	}
	// «Не названное» — не значит «лишнее»: смысловой вход приносит соседей
	// по предмету, и это его работа. Замер 03.09.2026 на вопросе про SNMP и JMX
	// дал `JMX exporter`, `SNMP-сервер`, `SNMP services` — всё по делу.
	// Мера полезна как СРАВНЕНИЕ до и после правки, а не как оценка сама по себе.
	fmt.Fprintf(stdout, "не названного в вопросе:  %d понятий на %d вопросов (%.1f на вопрос)\n",
		foreign, total, float64(foreign)/float64(total))
	if foreign > 0 {
		fmt.Fprintf(stdout, "  из них смысловым входом: %d (%.0f%%) — соседи по предмету, словесным: %d\n",
			foreignSense, 100*float64(foreignSense)/float64(foreign), foreign-foreignSense)
	}
	if skipped > 0 {
		fmt.Fprintf(stdout, "пропущено вопросов:       %d (понятия нет в графе — это о полноте, не о входе)\n", skipped)
	}

	// Половины набора: пары, о связи которых граф знает, и пары без связи.
	for _, edge := range []bool{true, false} {
		e := byEdge[edge]
		n := e[0] + e[1] + e[2]
		if n == 0 {
			continue
		}
		what := "со связью в графе"
		if !edge {
			what = "без связи в графе"
		}
		fmt.Fprintf(stdout, "  %-18s оба %3d, одно %3d, ни одного %3d  (из %d)\n", what, e[0], e[1], e[2], n)
	}

	// Что лезет чаще всего. Понятие, приходящее на каждый второй вопрос
	// независимо от предмета, — это дефект входа, а не уместный сосед:
	// сосед меняется вместе с вопросом, дефект — нет. Именно так поймали
	// «ИИ» (60 из 60) и «Связанность» (59 из 60).
	if len(foreignBy) > 0 {
		type fr struct {
			name         string
			count, sense int
		}
		list := make([]fr, 0, len(foreignBy))
		for _, st := range foreignBy {
			list = append(list, fr{st.name, st.count, st.sense})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].count != list[j].count {
				return list[i].count > list[j].count
			}
			return list[i].name < list[j].name
		})
		fmt.Fprintf(stdout, "\nчаще всего лезет в выдачу (из %d вопросов; повтор на каждом вопросе — дефект):\n", total)
		for i, f := range list {
			if i >= 12 {
				break
			}
			how := "словесно"
			if f.sense*2 >= f.count {
				how = "по смыслу"
			}
			fmt.Fprintf(stdout, "  %-42s %3d раз  (%s)\n", trimTo(f.name, 42), f.count, how)
		}
	}

	// Худшие случаи: по ним видно, что именно вход теряет.
	if o.Show > 0 {
		sort.SliceStable(rows, func(i, j int) bool {
			hi := btoi(rows[i].s.RankA > 0) + btoi(rows[i].s.RankB > 0)
			hj := btoi(rows[j].s.RankA > 0) + btoi(rows[j].s.RankB > 0)
			if hi != hj {
				return hi < hj
			}
			return rows[i].s.Foreign > rows[j].s.Foreign
		})
		fmt.Fprintf(stdout, "\nхуже всего (%d):\n", min(o.Show, len(rows)))
		for i, r := range rows {
			if i >= o.Show {
				break
			}
			fmt.Fprintf(stdout, "  %-52s a:%s b:%s постороннего %d\n",
				trimTo(r.c.Query, 52), place(r.s.RankA), place(r.s.RankB), r.s.Foreign)
		}
	}
	fmt.Fprintf(stdout, "\nза %.0f с\n", time.Since(started).Seconds())
	return nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func place(r int) string {
	if r == 0 {
		return "нет"
	}
	return fmt.Sprintf("%d", r)
}

func trimTo(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}
