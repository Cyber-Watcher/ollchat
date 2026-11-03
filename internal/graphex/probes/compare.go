// compare.go — режим `-only compare`: два построчных итога `stability -axis temp`
// (TSV из -out) по одним и тем же кускам — с разных серверов или до и после
// обновления Ollama. Карта не нужна: читаются только два файла.
//
// **Зачем.** Сборку графа собрались вести несколькими серверами (этап 95), а на
// серверах разные карты и разные версии Ollama: подсказку для RENDERER собирает
// код самой Ollama, расчёт идёт её ядрами. Одни ли и те же понятия извлекаются
// из одного куска — вопрос замера, а не веры в одинаковые веса.
//
// **Мера.** Та же, что внутри одного итога: Жаккар состава понятий (и пар
// связанных понятий) между прогонами. На каждый кусок и температуру — три
// пары: два прогона внутри A, два внутри B и прогоны A и B между собой
// (первый с первым, второй со вторым). При температуре 0 два прогона на
// одном сервере совпадали целиком (замер 18.09.2026, Жаккар 1,000), поэтому
// расхождение «A против B» при 0 — от сервера, а не от случая; при рабочей
// температуре его надо сравнивать с разбросом внутри сервера.
package probes

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// listSep — разделитель элементов в колонках «имена» и «связи»; relSep —
// между двумя понятиями пары внутри колонки «связи».
const (
	listSep = " | "
	relSep  = " ~ "
)

// runKey — одна строка итога: кусок, температура, номер прогона.
type runKey struct {
	chunk, temp string
	pass        int
}

// runSet — что извлёк один прогон: имена понятий и пары связанных понятий.
type runSet struct {
	names, rels map[string]bool
	hasRels     bool // в итоге есть колонка «связи» (итоги до 03.11.2026 её не писали)
}

// stabilityRun — разобранный итог одного прогона замера.
type stabilityRun struct {
	path   string
	runs   map[runKey]runSet
	chunks map[string]bool
	temps  map[string]bool
}

// readStabilityTSV читает итог `stability -axis temp -out`.
func readStabilityTSV(path string) (*stabilityRun, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := &stabilityRun{path: path, runs: map[runKey]runSet{}, chunks: map[string]bool{}, temps: map[string]bool{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		p := strings.Split(sc.Text(), "\t")
		if line == 1 && len(p) > 0 && p[0] == "кусок" {
			continue
		}
		if len(p) < 8 {
			return nil, fmt.Errorf("%s:%d: %d колонок, ждали не меньше 8 — это не итог stability -axis temp", path, line, len(p))
		}
		pass, err := strconv.Atoi(p[3])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: номер прогона %q", path, line, p[3])
		}
		set := runSet{names: splitList(p[7]), rels: map[string]bool{}}
		if len(p) > 8 {
			set.rels, set.hasRels = splitList(p[8]), true
		}
		k := runKey{chunk: p[0], temp: p[2], pass: pass}
		r.runs[k] = set
		r.chunks[k.chunk] = true
		r.temps[k.temp] = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return r, nil
}

func splitList(s string) map[string]bool {
	out := map[string]bool{}
	for _, x := range strings.Split(s, listSep) {
		if x = strings.TrimSpace(x); x != "" {
			out[x] = true
		}
	}
	return out
}

// pairTally — сходство одной пары прогонов по всем кускам.
type pairTally struct {
	pairs, sameNames, sameRels int
	jacNames, jacRels          float64
	withRels                   int
}

func (t *pairTally) add(a, b runSet) {
	t.pairs++
	jn := jaccard(a.names, b.names)
	t.jacNames += jn
	if jn == 1 {
		t.sameNames++
	}
	if a.hasRels && b.hasRels {
		t.withRels++
		jr := jaccard(a.rels, b.rels)
		t.jacRels += jr
		if jr == 1 {
			t.sameRels++
		}
	}
}

func (t pairTally) line(label string) string {
	if t.pairs == 0 {
		return fmt.Sprintf("  %-26s нет пар", label)
	}
	s := fmt.Sprintf("  %-26s %7.3f  %4d из %-4d", label, t.jacNames/float64(t.pairs), t.sameNames, t.pairs)
	if t.withRels > 0 {
		s += fmt.Sprintf("   %7.3f  %4d из %-4d", t.jacRels/float64(t.withRels), t.sameRels, t.withRels)
	} else {
		s += "   связей в итоге нет"
	}
	return s
}

// runCompare сравнивает два итога по общим кускам и печатает по температурам.
func runCompare(stdout io.Writer, pathA, pathB string) error {
	if pathA == "" || pathB == "" {
		return fmt.Errorf("режиму compare нужны два итога stability: -a <файл.tsv> -b <файл.tsv>")
	}
	a, err := readStabilityTSV(pathA)
	if err != nil {
		return err
	}
	b, err := readStabilityTSV(pathB)
	if err != nil {
		return err
	}
	var common []string
	for c := range a.chunks {
		if b.chunks[c] {
			common = append(common, c)
		}
	}
	sort.Strings(common)
	fmt.Fprintf(stdout, "A: %s — кусков %d\nB: %s — кусков %d\nобщих кусков: %d\n",
		a.path, len(a.chunks), b.path, len(b.chunks), len(common))
	if len(common) == 0 {
		return fmt.Errorf("общих кусков нет: итоги сняты по разным выборкам")
	}
	var temps []string
	for t := range a.temps {
		if b.temps[t] {
			temps = append(temps, t)
		}
	}
	sort.Strings(temps)
	for _, temp := range temps {
		var inA, inB, cross1, cross2 pairTally
		var namesA, namesB, relsA, relsB, missA, missB int
		for _, c := range common {
			get := func(r *stabilityRun, pass int, miss *int) (runSet, bool) {
				s, ok := r.runs[runKey{c, temp, pass}]
				if !ok {
					*miss++
				}
				return s, ok
			}
			a1, okA1 := get(a, 1, &missA)
			a2, okA2 := get(a, 2, &missA)
			b1, okB1 := get(b, 1, &missB)
			b2, okB2 := get(b, 2, &missB)
			if okA1 && okA2 {
				inA.add(a1, a2)
			}
			if okB1 && okB2 {
				inB.add(b1, b2)
			}
			if okA1 && okB1 {
				cross1.add(a1, b1)
			}
			if okA2 && okB2 {
				cross2.add(a2, b2)
			}
			for _, s := range []runSet{a1, a2} {
				namesA += len(s.names)
				relsA += len(s.rels)
			}
			for _, s := range []runSet{b1, b2} {
				namesB += len(s.names)
				relsB += len(s.rels)
			}
		}
		fmt.Fprintf(stdout, "\nтемпература %s\n", temp)
		fmt.Fprintf(stdout, "  %-26s %7s  %-12s   %7s  %s\n", "пара прогонов", "понятия", "тот же состав", "связи", "тот же состав")
		fmt.Fprintln(stdout, inA.line("A: 1-й / 2-й"))
		fmt.Fprintln(stdout, inB.line("B: 1-й / 2-й"))
		fmt.Fprintln(stdout, cross1.line("A против B, 1-й / 1-й"))
		fmt.Fprintln(stdout, cross2.line("A против B, 2-й / 2-й"))
		fmt.Fprintf(stdout, "  всего за два прогона: понятий A %d, B %d; пар связей A %d, B %d; нет строки (сбой прогона) A %d, B %d\n",
			namesA, namesB, relsA, relsB, missA, missB)
	}
	fmt.Fprintln(stdout, "\n«тот же состав» — у скольких кусков множества совпали целиком; Жаккар — среднее по кускам.")
	return nil
}
