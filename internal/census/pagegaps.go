package census

// Перепись пустых страниц (этап 112, Л5): «есть в корпусе ≠ находится». Доктор
// делит книги целиком — скан, тощая, битая, пропала (`kb/doctor.go`), — а книга,
// у которой текстовый слой есть, но пропали отдельные страницы или целая глава
// (вставленный скан, страницы-картинки), проходит проверку и молча теряет их:
// поиску они не видны. Страница считается пустой, если ни один кусок её не
// покрывает. Карта не нужна: читается готовый индекс.
//
// Счёт снизу: страница внутри куска, начатого раньше и кончившегося позже,
// засчитана покрытой, даже если сама пуста.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// pageCover — какие страницы книги покрыты кусками.
type pageCover struct {
	book    kb.BookRec
	covered []bool // индекс — номер страницы, 1…Units
}

// gaps — пустых страниц и самая длинная подряд идущая пустота (с какой страницы).
func (p pageCover) gaps() (empty, run, runFrom int) {
	cur := 0
	for pg := 1; pg < len(p.covered); pg++ {
		if p.covered[pg] {
			cur = 0
			continue
		}
		empty++
		cur++
		if cur > run {
			run, runFrom = cur, pg-cur+1
		}
	}
	return empty, run, runFrom
}

func pageGaps(stdout io.Writer, cfg *config.Config, collName string, top int) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()
	fmt.Fprintln(os.Stderr, "читаю куски коллекции…")
	books := map[uint32]*pageCover{}
	err = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		if strings.ToLower(filepath.Ext(ci.Book.Path)) != ".pdf" || ci.Book.Units <= 0 {
			return nil
		}
		p := books[ci.Doc]
		if p == nil {
			p = &pageCover{book: ci.Book, covered: make([]bool, ci.Book.Units+1)}
			books[ci.Doc] = p
		}
		from, to := max(ci.UnitFrom, 1), min(max(ci.UnitTo, ci.UnitFrom), ci.Book.Units)
		for pg := from; pg <= to; pg++ {
			p.covered[pg] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, pageGapsReport(books, top))
	return nil
}

// pageGapsReport — итог переписи: всего, распределение книг по доле пустых
// страниц, худшие книги. Отдельно от обхода, чтобы проверяться без коллекции.
func pageGapsReport(books map[uint32]*pageCover, top int) string {
	type row struct {
		title                    string
		pages, empty, run, runAt int
	}
	var rows []row
	var pages, empty, long int
	// Корзины доли пустых страниц у книги: ровно 0, до 5 %, до 20 %, до 50 %, больше.
	edges := []float64{0, 0.05, 0.20, 0.50}
	bins := make([]int, len(edges)+1)
	for _, p := range books {
		e, run, at := p.gaps()
		n := p.book.Units
		pages += n
		empty += e
		if run >= 10 {
			long++
		}
		share := float64(e) / float64(n)
		b := len(edges)
		for i, edge := range edges {
			if share <= edge {
				b = i
				break
			}
		}
		bins[b]++
		title := p.book.Title
		if title == "" {
			title = filepath.Base(p.book.Path)
		}
		rows = append(rows, row{title, n, e, run, at})
	}
	var sb strings.Builder
	share := func(a, b int) float64 { return float64(a) / float64(max(b, 1)) }
	fmt.Fprintf(&sb, "книг PDF с числом страниц: %d; страниц %d, из них без единого куска %d (%.3f)\n",
		len(books), pages, empty, share(empty, pages))
	fmt.Fprintf(&sb, "книг с пустотой подряд от 10 страниц: %d\n", long)
	names := []string{"ровно 0", "до 5 %", "5–20 %", "20–50 %", "больше 50 %"}
	fmt.Fprintf(&sb, "\nкниг по доле пустых страниц:\n")
	for i, n := range bins {
		fmt.Fprintf(&sb, "  %-12s %6d\n", names[i], n)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].run != rows[j].run {
			return rows[i].run > rows[j].run
		}
		return rows[i].title < rows[j].title
	})
	fmt.Fprintf(&sb, "\nсамые длинные пустоты подряд (книга: пусто из страниц, пустота подряд — с какой страницы):\n")
	for i, r := range rows {
		if i >= top || r.run == 0 {
			break
		}
		fmt.Fprintf(&sb, "  %5d из %5d  подряд %4d с стр. %-5d %s\n", r.empty, r.pages, r.run, r.runAt, cut(r.title, 70))
	}
	return sb.String()
}
