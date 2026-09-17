package maint

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Unmerge снимает склейки по списку — ядро команды `--graph-unmerge`.
//
// Строка списка: «поглощённое<TAB>выживший<TAB>причина»; выживший может быть
// нулём («куда бы ни вело»), строки с `//` и заголовок пропускаются. Годится
// и файл переписи `chaincensus.py`: номера в нём ищутся по именам столбцов
// `from` и `to`.
//
// Порядок — как у всякой чистки графа: сухой прогон (`--kb-dry-run`), архив,
// правка, доктор со сверкой вторым путём. После снятия устаревают вектор
// выжившего и разбиение на темы — команда называет, чем их долечить.
func Unmerge(stdout io.Writer, cfg *config.Config, name, file, why string, dry bool) error {
	pairs, err := readUnmergeList(file)
	if err != nil {
		return err
	}
	if len(pairs) == 0 {
		return fmt.Errorf("%s: в списке нет ни одной пары", file)
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	if !dry {
		unmark, err := markWork(g, "снятие склеек")
		if err != nil {
			return err
		}
		defer unmark()
	}
	if why == "" {
		why = "снято по списку " + file
	}
	before := g.Merges().Count()
	res, err := g.UndoMerges(pairs, why, dry)
	if err != nil {
		return err
	}

	verb := "снято"
	if dry {
		verb = "было бы снято"
	}
	fmt.Fprintf(stdout, "коллекция %s: пар в списке %d, %s склеек %d (поглощено понятий: было %d, станет %d)\n",
		name, res.Asked, verb, len(res.Undone), before, before-len(res.Undone))
	for i, r := range res.Undone {
		if i >= 60 {
			fmt.Fprintf(stdout, "  …и ещё %d\n", len(res.Undone)-60)
			break
		}
		from, _ := g.Entities().RawEntity(r.From)
		to, _ := g.Entities().RawEntity(r.To)
		fmt.Fprintf(stdout, "  %-34s ⇠ отделяется от ⇢ %-34s (записанная близость %.3f)\n",
			cutName(from.Name, 34), cutName(to.Name, 34), r.Cos)
	}
	if res.Carried > 0 {
		fmt.Fprintf(stdout, "  вернувшиеся понятия уносят с собой поглощённых ими раньше: %d\n", res.Carried)
	}
	for _, p := range res.Missing {
		fmt.Fprintf(stdout, "  НЕТ В ЖУРНАЛЕ: %d → %d — склейки уже нет, строка пропущена\n", p[0], p[1])
	}
	for _, p := range res.Mismatch {
		fmt.Fprintf(stdout, "  ВЕДЁТ НЕ ТУДА: %d в списке → %d, в журнале → %d — не снято, проверьте список\n", p[0], p[1], p[2])
	}
	if dry {
		fmt.Fprintln(stdout, "сухой прогон: ничего не записано")
		return nil
	}
	if len(res.Undone) == 0 {
		return nil
	}
	fmt.Fprintf(stdout, "прежний журнал: %s\n", res.Backup)
	fmt.Fprintln(stdout, "снятые решения дописаны в merges-undone.jsonl")
	fmt.Fprintf(stdout, "дальше: ollchat --graph-embed-stale %s (векторы выживших), "+
		"ollchat --graph-communities %s (темы вернувшихся понятий), ollchat --graph-doctor %s\n", name, name, name)
	return nil
}

func cutName(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// readUnmergeList читает пары «поглощённое, выживший». Столбцы ищутся
// по заголовку (`from`, `to`), а без заголовка берутся первый и второй.
func readUnmergeList(file string) ([][2]uint32, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][2]uint32
	fromCol, toCol := 0, 1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "//") || strings.HasPrefix(text, "#") {
			continue
		}
		p := strings.Split(sc.Text(), "\t")
		if _, err := strconv.ParseUint(strings.TrimSpace(p[0]), 10, 32); err != nil && len(out) == 0 {
			// Заголовок: ищем в нём столбцы from и to.
			for i, h := range p {
				switch strings.TrimSpace(h) {
				case "from":
					fromCol = i
				case "to":
					toCol = i
				}
			}
			continue
		}
		if fromCol >= len(p) {
			return nil, fmt.Errorf("%s, строка %d: нет столбца с номером поглощённого понятия", file, line)
		}
		from, err := strconv.ParseUint(strings.TrimSpace(p[fromCol]), 10, 32)
		if err != nil || from == 0 {
			return nil, fmt.Errorf("%s, строка %d: номер поглощённого понятия %q", file, line, p[fromCol])
		}
		var to uint64
		if toCol < len(p) {
			if to, err = strconv.ParseUint(strings.TrimSpace(p[toCol]), 10, 32); err != nil {
				return nil, fmt.Errorf("%s, строка %d: номер выжившего понятия %q", file, line, p[toCol])
			}
		}
		out = append(out, [2]uint32{uint32(from), uint32(to)})
	}
	return out, sc.Err()
}
