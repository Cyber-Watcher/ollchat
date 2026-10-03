package stats

// Ш3.1а: сколько двойников не видит отбор — оценка по выборке.
//
// Отбор кандидатов на склейку (`ResolveCandidates`) берёт пару, **только если
// модель при извлечении написала имя одного понятия в синонимы другого**;
// близость векторов служит фильтром поверх. Отсюда 4 828 кандидатов и 1,9%
// склеек при ориентире книг ~25%. Вопрос: велика ли невидимая часть.
//
// **Почему выборка, а не полный перебор.** Полный перебор 271 980 понятий —
// 36,9 млрд пар и около 25 часов процессора (замер 14.09.2026: за 12 минут
// сделано ~4%, пять ядер из восьми, при этом мешал ночной сборке). Для оценки
// ДОЛИ это лишнее: выборка в тысячу-две понятий даёт тот же ответ с погрешностью
// в доли процента и за минуты. Полный перебор остаётся для случая, когда нужен
// точный список пар, а не оценка.
//
// Карта не нужна; граф читается, файл векторов читается напрямую.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

type vecMeta struct {
	Magic string `json:"magic"`
	Model string `json:"model"`
	Dim   int    `json:"dim"`
	Count int    `json:"count"`
}

// loadEntityVectors читает entities.vec рядом с графом: плоский int8,
// вектор понятия с номером id лежит по смещению (id-1)*dim.
func loadEntityVectors(gdir string) ([]int8, int, error) {
	raw, err := os.ReadFile(filepath.Join(gdir, "entities.vecmeta"))
	if err != nil {
		return nil, 0, err
	}
	var m vecMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, 0, err
	}
	if m.Dim <= 0 {
		return nil, 0, fmt.Errorf("в паспорте векторов не указана размерность")
	}
	data, err := os.ReadFile(filepath.Join(gdir, "entities.vec"))
	if err != nil {
		return nil, 0, err
	}
	out := make([]int8, len(data))
	for i, b := range data {
		out[i] = int8(b)
	}
	return out, m.Dim, nil
}

// vecSample оценивает по выборке, какую часть похожих пар отбор не видит.
func vecSample(g *graph.Graph, sample int, minCos float64, seed int64, show int) {
	gdir := g.Dir()
	data, dim, err := loadEntityVectors(gdir)
	die(err)

	live := g.Entities().Live()
	nameOf := make(map[uint32]string, len(live))
	// aliasLink — пара «понятие ↔ понятие», которую модель связала синонимом:
	// именно такие пары и попадают в нынешний отбор.
	aliasOwner := make(map[string][]uint32, len(live))
	for _, e := range live {
		nameOf[e.ID] = e.Name
		for _, a := range e.Aliases {
			key := strings.ToLower(strings.TrimSpace(a))
			if key != "" {
				aliasOwner[key] = append(aliasOwner[key], e.ID)
			}
		}
	}
	byNorm := make(map[string]uint32, len(live))
	for _, e := range live {
		if _, taken := byNorm[strings.ToLower(e.Norm)]; !taken {
			byNorm[strings.ToLower(e.Norm)] = e.ID
		}
	}
	// linkedByAlias(a,b) — есть ли у одного из них синоним, совпадающий
	// с именем другого.
	linkedByAlias := func(a, b uint32) bool {
		for _, e := range []struct{ x, y uint32 }{{a, b}, {b, a}} {
			name := strings.ToLower(nameOf[e.y])
			for _, owner := range aliasOwner[name] {
				if owner == e.x {
					return true
				}
			}
		}
		return false
	}

	at := func(id uint32) []int8 {
		off := int(id-1) * dim
		if off < 0 || off+dim > len(data) {
			return nil
		}
		return data[off : off+dim]
	}

	// Уже разобранные пары — чтобы отделить «видел и решил» от «не видел вовсе».
	judged := map[[2]uint32]string{}
	if f, err := os.Open(filepath.Join(gdir, "doubles-judged.tsv")); err == nil {
		defer f.Close()
		var a, b uint32
		var verdict string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue
			}
			p := strings.Split(sc.Text(), "\t")
			if len(p) < 3 {
				continue
			}
			if _, err := fmt.Sscan(p[0], &a); err != nil {
				continue
			}
			if _, err := fmt.Sscan(p[1], &b); err != nil {
				continue
			}
			verdict = p[2]
			if a > b {
				a, b = b, a
			}
			judged[[2]uint32{a, b}] = verdict
		}
	}

	ids := make([]uint32, 0, len(live))
	for _, e := range live {
		if len(at(e.ID)) == dim {
			ids = append(ids, e.ID)
		}
	}
	if sample <= 0 || sample > len(ids) {
		sample = len(ids)
	}
	rnd := rand.New(rand.NewSource(seed))
	picked := make([]uint32, len(ids))
	copy(picked, ids)
	rnd.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	picked = picked[:sample]

	type foundPair struct {
		a, b uint32
		cos  float64
	}
	var (
		pairs        []foundPair
		withNeighbor int
	)
	for _, id := range picked {
		va := at(id)
		best := 0.0
		var bestID uint32
		for _, other := range ids {
			if other == id {
				continue
			}
			c := kb.Cosine(va, at(other))
			if c >= minCos && c > best {
				best, bestID = c, other
			}
		}
		if bestID != 0 {
			withNeighbor++
			a, b := id, bestID
			if a > b {
				a, b = b, a
			}
			pairs = append(pairs, foundPair{a, b, best})
		}
	}

	var seen, unseen, judgedYes int
	for _, p := range pairs {
		key := [2]uint32{p.a, p.b}
		if v, ok := judged[key]; ok {
			seen++
			if v == "ДА" {
				judgedYes++
			}
			continue
		}
		if linkedByAlias(p.a, p.b) {
			seen++ // отбор такую пару видит, просто она ещё не дошла до арбитра
			continue
		}
		unseen++
	}

	fmt.Printf("\nШ3.1а. Невидимая часть двойников — оценка по выборке (этап 103)\n")
	fmt.Printf("  понятий с вектором: %d, выборка: %d (seed %d), порог близости %.2f\n",
		len(ids), sample, seed, minCos)
	fmt.Printf("  у скольких нашёлся сосед ближе порога: %d (%.1f%% выборки)\n",
		withNeighbor, 100*float64(withNeighbor)/float64(sample))
	if len(pairs) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  отбор такую пару видит (синоним или уже разобрана): %5d (%.1f%%)\n",
		seen, 100*float64(seen)/float64(len(pairs)))
	fmt.Printf("    из них решено «ДА» (склеить):         %5d\n", judgedYes)
	fmt.Printf("  ОТБОР НЕ ВИДИТ ВОВСЕ (нет синонима):    %5d (%.1f%%)\n",
		unseen, 100*float64(unseen)/float64(len(pairs)))
	fmt.Println()
	// Считаются ПОНЯТИЯ, а не пары: у каждого понятия выборки берётся один,
	// ближайший сосед. Взаимные ближайшие соседи при пересчёте на граф дают
	// одну пару дважды, а пары с не самым близким соседом не видны вовсе.
	fmt.Printf("  В пересчёте на весь граф: примерно %.0f понятий, у которых ближайший\n",
		float64(unseen)/float64(sample)*float64(len(ids)))
	fmt.Printf("  сосед выше %.2f отбору не виден (пар — от половины этого числа:\n", minCos)
	fmt.Println("  взаимные соседи считаются дважды). Арбитру их не предложат никогда.")

	if show > 0 {
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].cos > pairs[j].cos })
		fmt.Println("\n  Примеры пар, которых отбор не видит (глазами — это НЕ склейки):")
		n := 0
		for _, p := range pairs {
			if _, ok := judged[[2]uint32{p.a, p.b}]; ok {
				continue
			}
			if linkedByAlias(p.a, p.b) {
				continue
			}
			fmt.Printf("    %.4f  %-38s ↔ %s\n", p.cos, cut(nameOf[p.a], 38), cut(nameOf[p.b], 38))
			if n++; n >= show {
				break
			}
		}
	}
}
