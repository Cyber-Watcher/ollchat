// hublist — выписать хабы графа вместе с контекстом, из которого модель
// сможет написать им описание (этап 103, Ш2).
//
// **Зачем.** У понятия в нашем графе есть имя, синонимы и выдержки, но
// описания нет вовсе: поле `Summary` заведено только у темы
// (`graph.Community`), у `graph.Entity` его нет. «Essential GraphRAG»
// (Bratanic, Hane, 2025, стр. 116) описывает, как MS GraphRAG сливает
// несколько упоминаний сущности моделью в краткую сводку. Для 264 тысяч
// понятий это больше двух суток карты, для 242 хабов — минуты.
//
// **Почему именно хабы.** Через них идёт вход в граф: понятие с сотнями
// связей попадает в выдачу почти на любой вопрос своей области.
//
// Ничего не меняет: граф и коллекция открыты на чтение, результат — TSV.
package stats

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// hubRow — одна строка выдачи: хаб и всё, что о нём известно без модели.
type hubRow struct {
	ID      uint32
	Name    string
	Type    string
	Docs    int
	Count   int
	Degree  int
	Aliases []string
	Near    []string // соседи по убыванию веса: «имя (вес)»
	Quotes  []string // выдержки из книг: «Книга, стр. N: текст»
}

// hubListOpts — что считать хабом и сколько контекста собирать.
type hubListOpts struct {
	MinDeg    int // порог связей; 0 — взять Rules().ChainHubLimit
	Limit     int // сколько хабов выписать (0 — все)
	Aliases   int // сколько синонимов в строку
	Neighbors int // сколько соседей в строку
	Quotes    int // сколько выдержек на хаб
	QuoteLen  int // предел длины выдержки в знаках
	Scan      int // сколько кусков просмотреть в поисках годных выдержек
	Out       string
}

func (o hubListOpts) norm(g *graph.Graph) hubListOpts {
	if o.MinDeg <= 0 {
		o.MinDeg = g.Rules().ChainHubLimit
	}
	if o.MinDeg <= 0 {
		o.MinDeg = graph.DefaultChainHubLimit
	}
	if o.Aliases <= 0 {
		o.Aliases = 8
	}
	if o.Neighbors <= 0 {
		o.Neighbors = 12
	}
	if o.Quotes <= 0 {
		o.Quotes = 3
	}
	if o.QuoteLen <= 0 {
		o.QuoteLen = 600
	}
	if o.Scan <= 0 {
		o.Scan = 120
	}
	return o
}

func hubListStats(g *graph.Graph, c *kb.Collection, o hubListOpts) {
	o = o.norm(g)
	live := g.Entities().Live()

	// Множество живых: соседями могут оказаться понятия, поглощённые
	// склейкой, и их имена в контексте были бы обманом — после склейки
	// такого узла в графе уже нет.
	alive := make(map[uint32]bool, len(live))
	for _, e := range live {
		alive[e.ID] = true
	}

	rows := make([]hubRow, 0, 512)
	for _, e := range live {
		nb := g.Edges().Neighbors(e.ID)
		if len(nb) < o.MinDeg {
			continue
		}
		rows = append(rows, hubRow{ID: e.ID, Name: e.Name, Type: e.Type,
			Docs: e.Docs, Count: e.Count, Degree: len(nb)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Degree > rows[j].Degree })
	if o.Limit > 0 && len(rows) > o.Limit {
		rows = rows[:o.Limit]
	}

	fmt.Printf("\nШ2. Хабы графа: связей %d и больше — %d понятий\n", o.MinDeg, len(rows))
	if len(rows) == 0 {
		return
	}

	for i := range rows {
		ent, ok := g.Entities().Get(rows[i].ID)
		if !ok {
			continue
		}
		rows[i].Aliases = first(g.Entities().SafeAliases(ent), o.Aliases)
		rows[i].Near = hubNeighbors(g, alive, rows[i].ID, o.Neighbors)
		rows[i].Quotes = hubQuotes(g, c, ent, o)
	}

	if o.Out == "" {
		for _, r := range rows[:min(10, len(rows))] {
			fmt.Printf("  %-34s связей %5d, книг %3d, выдержек %d\n",
				cut(r.Name, 34), r.Degree, r.Docs, len(r.Quotes))
		}
		fmt.Println("\n  (ключ -hublist-out файл.tsv выпишет все с контекстом)")
		return
	}
	die(writeHubTSV(o.Out, rows))
	noQuotes := 0
	for _, r := range rows {
		if len(r.Quotes) == 0 {
			noQuotes++
		}
	}
	fmt.Printf("  записано %d строк в %s (без выдержек %d)\n", len(rows), o.Out, noQuotes)
}

// hubNeighbors — соседи хаба по убыванию веса, только живые.
func hubNeighbors(g *graph.Graph, alive map[uint32]bool, id uint32, limit int) []string {
	nb := g.Edges().Neighbors(id)
	sort.Slice(nb, func(i, j int) bool { return nb[i].Weight > nb[j].Weight })
	out := make([]string, 0, limit)
	for _, n := range nb {
		if !alive[n.ID] {
			continue
		}
		e, ok := g.Entities().Get(n.ID)
		if !ok || e.Name == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s (%.0f)", e.Name, n.Weight))
		if len(out) >= limit {
			break
		}
	}
	return out
}

// hubQuotes — выдержки из книг про этот хаб.
//
// Отбор строгий, потому что мусорная выдержка хуже отсутствующей: кусок
// не должен быть оглавлением, списком литературы или листингом, а имя
// понятия должно в нём буквально встречаться — иначе модель опишет
// не то понятие, а соседнее по куску. Из одной книги берётся одна
// выдержка: три выдержки из одной главы не добавляют знания.
func hubQuotes(g *graph.Graph, c *kb.Collection, e graph.Entity, o hubListOpts) []string {
	keys := g.Mentions().Of(e.ID)
	seen := map[uint32]bool{}
	out := make([]string, 0, o.Quotes)
	needle := strings.ToLower(e.Name)
	for i, k := range keys {
		if i >= o.Scan || len(out) >= o.Quotes {
			break
		}
		if seen[k.Doc] {
			continue
		}
		ci, ok := c.ChunkByRef(k.Doc, k.Ord)
		if !ok || ci.TOC || ci.Refs || ci.Code {
			continue
		}
		if !strings.Contains(strings.ToLower(ci.Text), needle) {
			continue
		}
		seen[k.Doc] = true
		out = append(out, fmt.Sprintf("%s, %s %d: %s",
			ci.Book.Title, unitOr(ci.Unit), ci.UnitFrom, cut(oneLine(ci.Text), o.QuoteLen)))
	}
	return out
}

func unitOr(u string) string {
	if u == "" {
		return "стр."
	}
	return u
}

func writeHubTSV(path string, rows []hubRow) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, strings.Join([]string{"id", "имя", "тип", "книг", "упоминаний",
		"связей", "синонимы", "соседи", "выдержки"}, "\t"))
	for _, r := range rows {
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n",
			r.ID, tsv(r.Name), tsv(r.Type), r.Docs, r.Count, r.Degree,
			tsv(strings.Join(r.Aliases, "; ")),
			tsv(strings.Join(r.Near, "; ")),
			tsv(strings.Join(r.Quotes, " ¶ ")))
	}
	return w.Flush()
}

// tsv обезвреживает знаки, которые разорвали бы строку TSV.
func tsv(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// first — первые n элементов среза (обрезка строк — cut в main.go).
func first(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}
