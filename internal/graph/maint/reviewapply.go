package maint

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Очередь разбора без окна: выгрузить пары со всем, что видит человек
// в /graph review (`--graph-review-export`), и записать решения из файла
// (`--graph-review-apply`). Заведено по слову владельца: в очереди скопилось
// больше тысячи пар, и разбирать их за человека берётся Claude — под своим
// именем автора (graph.LinkByClaude), чтобы решения были видны и снимались.

// reviewSnippetRunes — сколько знаков текста вокруг упоминания класть в выгрузку.
const reviewSnippetRunes = 280

// ReviewExport печатает очередь разбора в TSV — ядро `--graph-review-export`.
// Только читает.
//
// Колонки: «id_a», «id_b» (номера из журнала — ими же адресуется решение
// в `--graph-review-apply`), имя, тип и число упоминаний каждой стороны, cos,
// причина арбитра, выдержка по каждой стороне (книга и текст вокруг имени)
// и «состояние»: пусто, «один узел» (стороны уже склеены) или «поглощён»
// (сторона ушла в третий узел — имя в журнале и имя узла разошлись).
// Порядок — как в окне: сперва самые близкие.
func ReviewExport(stdout io.Writer, cfg *config.Config, name string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "открываю граф для выгрузки очереди разбора…")
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	l := g.Links()
	if l == nil {
		return fmt.Errorf("у графа коллекции %s нет журнала связываний", name)
	}
	queue := l.Queue()
	fmt.Fprintln(stdout, strings.Join([]string{"id_a", "id_b", "имя_a", "тип_a", "упом_a", "имя_b", "тип_b", "упом_b",
		"cos", "арбитр", "выдержка_a", "выдержка_b", "состояние"}, "\t"))
	for i, r := range queue {
		if i%100 == 0 {
			fmt.Fprintf(os.Stderr, "\r  %d/%d пар   ", i, len(queue))
		}
		from := r.From
		if from == 0 {
			if ent, ok := g.Entities().Lookup(r.Name); ok {
				from = ent.ID
			}
		}
		a, b := reviewSide(g, coll, from, r.Name), reviewSide(g, coll, r.Cand, r.CandName)
		state := ""
		liveA, liveB := g.Merges().Resolve(from), g.Merges().Resolve(r.Cand)
		switch {
		case from != 0 && liveA == liveB:
			state = "один узел"
		case liveA != from || liveB != r.Cand:
			state = "поглощён"
		}
		fmt.Fprintf(stdout, "%d\t%d\t%s\t%s\t%d\t%s\t%s\t%d\t%.3f\t%s\t%s\t%s\t%s\n",
			r.From, r.Cand, tsvCell(r.Name), a.typ, a.count, tsvCell(r.CandName), b.typ, b.count,
			r.Cos, tsvCell(r.Why), a.evidence, b.evidence, state)
	}
	fmt.Fprintf(os.Stderr, "\r  %d/%d пар   \n", len(queue), len(queue))
	return nil
}

// reviewSideInfo — что известно об одной стороне пары.
type reviewSideInfo struct {
	typ      string
	count    int
	evidence string
}

// reviewSide собирает тип, число упоминаний и выдержку по стороне пары.
// Выдержка — первое читаемое упоминание, как в окне разбора (там её берёт
// firstEvidence): пример употребления, а не «лучший» кусок.
func reviewSide(g *graph.Graph, coll *kb.Collection, id uint32, name string) reviewSideInfo {
	var s reviewSideInfo
	if id == 0 {
		return s
	}
	if ent, ok := g.Entities().Get(id); ok {
		s.typ, s.count = tsvCell(ent.Type), int(ent.Count)
	}
	for i, key := range g.Mentions().Of(id) {
		if i >= 5 {
			break
		}
		ci, ok := coll.ChunkByRef(key.Doc, key.Ord)
		if !ok {
			continue
		}
		s.evidence = tsvCell(ci.Book.Title + " «" + snippetAround(ci.Text, name, reviewSnippetRunes) + "»")
		break
	}
	return s
}

// snippetAround вырезает из текста окно в n знаков вокруг первого вхождения
// имени (без учёта регистра); имени в тексте нет — начало текста.
func snippetAround(text, name string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	at := 0
	if name != "" {
		if i := strings.Index(strings.ToLower(text), strings.ToLower(name)); i >= 0 {
			at = len([]rune(text[:i]))
		}
	}
	start := at - n/2
	if start < 0 {
		start = 0
	}
	end := start + n
	if end > len(runes) {
		end = len(runes)
		start = end - n
	}
	return string(runes[start:end])
}

// tsvCell делает из строки ячейку TSV: табуляции и переводы строк — пробелами.
func tsvCell(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ReviewApply пишет решения по парам очереди разбора из TSV — ядро
// `--graph-review-apply`. Автор решений — graph.LinkByClaude.
//
// Порядок — как у всякой правки графа: сухой прогон (`--kb-dry-run`), архив,
// запись, доктор со сверкой вторым путём. «ДА» — склейка в merges.jsonl,
// обратимая: `--graph-unmerge` по списку пар. После склеек устаревают векторы
// выживших и разбиение на темы — команда называет, чем их долечить.
func ReviewApply(stdout io.Writer, cfg *config.Config, name, tsvPath string, dry bool) error {
	f, err := os.Open(tsvPath)
	if err != nil {
		return err
	}
	defer f.Close()
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "открываю граф для записи решений разбора…")
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	if g.Links() == nil {
		return fmt.Errorf("у графа коллекции %s нет журнала связываний", name)
	}
	if !dry {
		unmark, err := markWork(g, "решения разбора пар")
		if err != nil {
			return err
		}
		defer unmark()
	}
	before, merged := g.Links().Queued(), g.Merges().Count()
	res, err := g.ApplyReviewTSV(f, graph.LinkByClaude, dry)
	verb := "записано"
	if dry {
		verb = "было бы записано"
	}
	fmt.Fprintf(stdout, "коллекция %s: в очереди было %d; %s «ДА» %d (из них уже один узел %d), «НЕТ» %d\n",
		name, before, verb, res.Yes, res.OneNode, res.No)
	fmt.Fprintf(stdout, "  оставлено человеку: «?» в файле %d, сторона поглощена другим узлом %d; нет в очереди %d\n",
		res.Left, res.Stale, res.NotInQueue)
	if err != nil {
		return err
	}
	if dry {
		fmt.Fprintln(stdout, "сухой прогон: ничего не записано")
		return nil
	}
	fmt.Fprintf(stdout, "в очереди стало %d; поглощено понятий: было %d, стало %d; автор решений в журнале — %s\n",
		g.Links().Queued(), merged, g.Merges().Count(), graph.LinkByClaude)
	if res.Yes > res.OneNode {
		fmt.Fprintf(stdout, "дальше: ollchat --graph-embed-stale %s (векторы выживших), "+
			"ollchat --graph-communities %s (темы), ollchat --graph-doctor %s\n", name, name, name)
	}
	return nil
}
