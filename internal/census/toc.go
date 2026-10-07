// Переписи про оглавления и предметные указатели. Три режима одного предмета,
// перенесённые 30.09.2026 из `privatescripts/` (этап 114, пункт Г4):
// `toccensus` → `-only toc`, `tocprobe` → `-only toc-calibrate`,
// `epubtocprobe` → `-only toc-epub`.
//
// **Зачем предмет вообще.** Оглавление — это куски, где рядом стоят понятия,
// не связанные по смыслу; на них граф получал «всё со всем» (этап 99). Вопрос
// владельца 07.09.2026 — не резать ли оглавления при нарезке — решается
// счётом: сколько таких кусков и какая доля графа из них извлечена.

package census

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/epub"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Размеры записей в журналах графа: читаются напрямую, чтобы не открывать граф
// целиком (открытие стоит разбора реестра, а здесь нужны только ссылки).
const (
	mentionRec  = 12 // понятие, книга, кусок
	edgeRec     = 24 // связь: два понятия, тип, книга, кусок
	progressRec = 12 // книга, кусок, отметка
)

// Отметки в progress.log: 1 — разобран с понятиями, 2 — пустой.
const (
	markDone  = 1
	markEmpty = 2
)

// tocCensus — сколько в коллекции кусков похожих на оглавление и какая доля
// упоминаний и связей графа извлечена именно из них.
func tocCensus(stdout io.Writer, cfg *config.Config, collName string) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintln(os.Stderr, "читаю куски коллекции…")
	toc := map[uint64]bool{}
	total, tocN := 0, 0
	perBook := map[uint32][2]int{} // книга → [кусков, оглавлений]
	if err := c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		total++
		pb := perBook[ci.Doc]
		pb[0]++
		if kb.LooksLikeTOC(ci.Text) {
			tocN++
			pb[1]++
			toc[chunkKey(ci.Doc, ci.Ord)] = true
		}
		perBook[ci.Doc] = pb
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "кусков %d, похожих на оглавление %d (%.1f%%)\n",
		total, tocN, 100*float64(tocN)/float64(max(total, 1)))
	worst, worstN := uint32(0), 0
	// При равных числах — меньший номер книги: обход карты иначе называл
	// разные книги на разных прогонах.
	for d, pb := range perBook {
		if pb[1] > worstN || (pb[1] == worstN && worstN > 0 && d < worst) {
			worst, worstN = d, pb[1]
		}
	}
	if worstN > 0 {
		fmt.Fprintf(stdout, "больше всего у книги %d: %d из %d кусков\n", worst, worstN, perBook[worst][0])
	}

	// Каталог графа — по настройке, как у самого ollchat: у именованного графа
	// (graph.name = lab → graph-lab) он свой, и прежний жёсткий «graph» молча
	// считал не тот граф или не находил никакого.
	gdir := cfg.Graph.Rules().Dir(c.Dir())
	if _, err := os.Stat(filepath.Join(gdir, "graph.meta")); err != nil {
		fmt.Fprintf(stdout, "графа в %s нет — доли упоминаний и связей не считаются\n", gdir)
	}
	if data, err := os.ReadFile(filepath.Join(gdir, "mentions.log")); err == nil {
		n, in := len(data)/mentionRec, 0
		for i := 0; i < n; i++ {
			doc := binary.LittleEndian.Uint32(data[i*mentionRec+4:])
			ord := binary.LittleEndian.Uint32(data[i*mentionRec+8:])
			if toc[chunkKey(doc, ord)] {
				in++
			}
		}
		fmt.Fprintf(stdout, "упоминаний %d, из оглавлений %d (%.1f%%)\n", n, in, 100*float64(in)/float64(max(n, 1)))
	}
	if data, err := os.ReadFile(filepath.Join(gdir, "edges.log")); err == nil {
		n, in := len(data)/edgeRec, 0
		for i := 0; i < n; i++ {
			doc := binary.LittleEndian.Uint32(data[i*edgeRec+16:])
			ord := binary.LittleEndian.Uint32(data[i*edgeRec+20:])
			if toc[chunkKey(doc, ord)] {
				in++
			}
		}
		fmt.Fprintf(stdout, "связей (подтверждений) %d, из оглавлений %d (%.1f%%)\n", n, in, 100*float64(in)/float64(max(n, 1)))
	}
	if data, err := os.ReadFile(filepath.Join(gdir, "progress.log")); err == nil {
		// Журнал отметок читается как у графа (graph.Progress): последняя
		// запись о куске побеждает. Счёт по записям учитывал перезаписанный
		// кусок дважды.
		n, tocDone, tocEmpty := len(data)/progressRec, 0, 0
		last := map[uint64]uint32{}
		for i := 0; i < n; i++ {
			doc := binary.LittleEndian.Uint32(data[i*progressRec:])
			ord := binary.LittleEndian.Uint32(data[i*progressRec+4:])
			if k := chunkKey(doc, ord); toc[k] {
				last[k] = binary.LittleEndian.Uint32(data[i*progressRec+8:])
			}
		}
		for _, mark := range last {
			switch mark {
			case markDone:
				tocDone++
			case markEmpty:
				tocEmpty++
			}
		}
		fmt.Fprintf(stdout, "оглавлений разобрано моделью %d, из них пустых %d\n", tocDone+tocEmpty, tocEmpty)
	}
	return nil
}

// chunkKey — ссылка на кусок одним числом: книга в старших знаках, кусок
// в младших. Так журналы графа сверяются с кусками коллекции без строк.
func chunkKey(doc, ord uint32) uint64 { return uint64(doc)<<32 | uint64(ord) }

// tocCalibrate — калибровка kb.LooksLikeTOC на названных кусках: на каждом
// печатает долю строк, кончающихся числом, и приговор правила. Так порог
// проверяется на настоящих оглавлениях и настоящих абзацах, а не на догадке.
func tocCalibrate(stdout io.Writer, cfg *config.Config, collName string, refs []string) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		var doc, ord uint32
		if _, err := fmt.Sscanf(ref, "%d#%d", &doc, &ord); err != nil {
			fmt.Fprintf(stdout, "%-9s не разобрать ссылку (нужно книга#кусок)\n", ref)
			continue
		}
		info, ok := c.ChunkByRef(doc, ord)
		if !ok {
			fmt.Fprintf(stdout, "%-9s нет\n", ref)
			continue
		}
		lines, ends := digitEndShare(info.Text)
		fmt.Fprintf(stdout, "%-9s строк %3d, кончаются числом %3d (%3.0f%%)  toc=%v code=%v  %.60q\n",
			ref, lines, ends, 100*float64(ends)/float64(max(lines, 1)),
			kb.LooksLikeTOC(info.Text), info.Code, strings.Join(strings.Fields(info.Text), " "))
	}
	return nil
}

// digitEndShare — сколько непустых строк в куске и сколько из них кончаются
// цифрой. Именно цифрой, а не «номером страницы» по правилу kb: прибор для
// калибровки и должен показывать более широкий признак, иначе он покажет
// ровно то же, что решило правило, и калибровать будет нечего.
func digitEndShare(text string) (lines, ends int) {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines++
		if c := l[len(l)-1]; c >= '0' && c <= '9' {
			ends++
		}
	}
	return lines, ends
}

var (
	// Имя файла главы EPUB, похожее на служебную.
	navNameRe = regexp.MustCompile(`(?i)\b(toc|contents|nav|index|ind|idx)\b|toc\.|index\.|contents\.|nav\.`)
	// Заголовок служебной главы.
	navTitleRe = regexp.MustCompile(`(?i)^(table of )?contents$|^(subject |alphabetical )?index$|^brief contents$|оглавление|содержание|предметный указатель|алфавитный указатель`)
	// Строка указателя: «term, 12, 45» или «term 12» — короткая, с числами через запятую.
	indexLineRe = regexp.MustCompile(`^.{1,80}?[,\s]\s*\d{1,4}((,|–|-)\s*\d{1,4})*$`)
)

// lineStats — признаки строения главы EPUB. У оглавления EPUB номеров страниц
// нет, и kb.LooksLikeTOC его не видит (18.09.2026), поэтому признак ищется
// по строению: короткие строки без знаков препинания, начатые номером главы.
type lineStats struct {
	lines, chars   int
	pageEnds       int // строк, кончающихся номером страницы (правило kb)
	indexLike      int // строк вида «term, 12, 45»
	short          int // строк короче 60 знаков
	noPunct        int // строк без точки/запятой/двоеточия в конце
	numbered       int // строк, начинающихся с номера главы или слова «Chapter»
	longestLineLen int
}

// measure — признаки строения одной главы.
func measure(text string) lineStats {
	var s lineStats
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		s.lines++
		n := len([]rune(l))
		s.chars += n // знаки, не байты: у русской главы байтов вдвое больше
		if n < 60 {
			s.short++
		}
		if n > s.longestLineLen {
			s.longestLineLen = n
		}
		// Правило берётся у kb, а не копируется: своя копия отстала на случай
		// `�` и мерила по правилу старше того, которое калибровала.
		if kb.LineEndsWithPageNumber(l) {
			s.pageEnds++
		}
		if indexLineRe.MatchString(l) {
			s.indexLike++
		}
		last := []rune(l)
		if r := last[len(last)-1]; r != '.' && r != ',' && r != ':' && r != ';' && r != '!' && r != '?' {
			s.noPunct++
		}
		if startsNumbered(l) {
			s.numbered++
		}
	}
	return s
}

// startsNumbered — начинается ли строка номером главы или раздела.
func startsNumbered(l string) bool {
	lo := strings.ToLower(l)
	if strings.HasPrefix(lo, "chapter ") || strings.HasPrefix(lo, "глава ") ||
		strings.HasPrefix(lo, "part ") || strings.HasPrefix(lo, "appendix ") {
		return true
	}
	i := 0
	for i < len(l) && (l[i] >= '0' && l[i] <= '9' || l[i] == '.') {
		i++
	}
	return i > 0 && i < len(l) && l[i] == ' ' && l[0] != '.'
}

// addStats — сложить признаки двух глав, чтобы вывести средние по виду.
func addStats(a *lineStats, b lineStats) {
	a.lines += b.lines
	a.chars += b.chars
	a.pageEnds += b.pageEnds
	a.indexLike += b.indexLike
	a.short += b.short
	a.noPunct += b.noPunct
	a.numbered += b.numbered
}

// pct — доля в процентах без деления на ноль.
func pct(a, b int) string {
	if b == 0 {
		return "  -"
	}
	return fmt.Sprintf("%3d", a*100/b)
}

// tocEPUB — разведка по книгам EPUB: какие главы похожи на оглавление или
// предметный указатель и как они выглядят как текст. Читает книги с диска,
// индекс и граф не нужны.
func tocEPUB(stdout io.Writer, cfg *config.Config, root string, show int, all bool) error {
	if root == "" {
		if len(cfg.KB.Roots) == 0 {
			return fmt.Errorf("в конфиге нет kb.roots — укажите каталог: -root <каталог>")
		}
		root = cfg.KB.Roots[0]
	}
	var files []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".epub") {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(files)
	fmt.Fprintf(os.Stderr, "книг EPUB под %s: %d, разбираю…\n", root, len(files))

	books, withNav, navInSpine, byName, byTitle, suspects := 0, 0, 0, 0, 0, 0
	var sumSuspect, sumProse lineStats
	prose, susp := 0, 0
	for _, f := range files {
		res, err := epub.ExtractFile(f, epub.Options{})
		if err != nil {
			fmt.Fprintf(stdout, "!! %s: %v\n", filepath.Base(f), err)
			continue
		}
		books++
		fmt.Fprintf(stdout, "\n== %s (%d разделов)\n", filepath.Base(f), res.TotalSections)
		hasNav := false
		for _, sec := range res.Sections {
			base := filepath.Base(sec.Href)
			byN := navNameRe.MatchString(strings.TrimSuffix(base, filepath.Ext(base)))
			byT := navTitleRe.MatchString(strings.TrimSpace(sec.Title))
			if sec.Nav {
				hasNav = true
				navInSpine++
			}
			st := measure(sec.Text)
			suspect := sec.Nav || byN || byT
			switch {
			case suspect:
				suspects++
				if byN {
					byName++
				}
				if byT {
					byTitle++
				}
				susp++
				addStats(&sumSuspect, st)
			case st.lines >= 8:
				prose++
				addStats(&sumProse, st)
			}
			if !suspect && !sec.Service && !all {
				continue
			}
			// Отметка: «S » — служебная и подозрительная по имени или заголовку,
			// «S+» — служебная только по правилу, «-!» — подозрительная, но
			// служебной не признана: это и есть случай, который ищет прибор.
			mark := "  "
			switch {
			case sec.Service && suspect:
				mark = "S "
			case sec.Service:
				mark = "S+"
			case suspect:
				mark = "-!"
			}
			fmt.Fprintf(stdout, "%s §%-3d %-28.28s nav=%-5v %-40.40q строк %5d знаков %7d | стр%% %s указ%% %s кор%% %s безпункт%% %s нум%% %s\n",
				mark, sec.Number, base, sec.Nav, sec.Title, st.lines, st.chars,
				pct(st.pageEnds, st.lines), pct(st.indexLike, st.lines), pct(st.short, st.lines),
				pct(st.noPunct, st.lines), pct(st.numbered, st.lines))
			if suspect && show > 0 {
				n := 0
				for _, l := range strings.Split(sec.Text, "\n") {
					l = strings.TrimSpace(l)
					if l == "" {
						continue
					}
					fmt.Fprintf(stdout, "       │ %.100s\n", l)
					if n++; n >= show {
						break
					}
				}
			}
		}
		if hasNav {
			withNav++
		}
	}
	fmt.Fprintf(stdout, "\nкниг %d, с nav-документом в spine %d (глав nav %d); подозрительных глав %d: по имени файла %d, по заголовку %d\n",
		books, withNav, navInSpine, suspects, byName, byTitle)
	avg := func(s lineStats, n int) string {
		if s.lines == 0 {
			return "-"
		}
		return fmt.Sprintf("глав %d, строк %d, ср. длина строки %d, стр%% %s указ%% %s кор%% %s безпункт%% %s нум%% %s",
			n, s.lines, s.chars/s.lines, pct(s.pageEnds, s.lines), pct(s.indexLike, s.lines),
			pct(s.short, s.lines), pct(s.noPunct, s.lines), pct(s.numbered, s.lines))
	}
	fmt.Fprintf(stdout, "подозрительные в среднем: %s\n", avg(sumSuspect, susp))
	fmt.Fprintf(stdout, "остальные (≥8 строк):     %s\n", avg(sumProse, prose))
	return nil
}
