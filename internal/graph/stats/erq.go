package stats

// ERQ — качество разрешения сущностей на готовой разметке (этап 90, Н1).
//
// Мера из книги «Advanced retrieval-augmented generation: bridging large language
// models and knowledge graphs» (2026, стр. 410): Entity Resolution Quality — доля
// пар, где решение системы совпало с истинным. Считать её можно **сегодня и без
// карты**: рядом с графом лежит реестр `doubles-judged.tsv`, где у каждой пары
// стоит вердикт арбитра и близость векторов, по которой её вообще нашли.
//
// **Граф здесь не открывается.** Читается один текстовый файл: рабочий граф —
// недели работы карты, и мера, которой хватает реестра, не имеет права держать
// его открытым (правило владельца 12.09.2026).
//
// **Что именно считается.** «Системой» выступает порог близости без арбитра —
// то самое `graph.link_min_cos`, с которым связывание при вставке решало бы
// судьбу пары само. «Истиной» — вердикт из реестра. Поэтому число, которое
// печатает эта мера, есть **согласие порога с прежним арбитром, а не истина**:
// разметку выносил арбитр (частью человек), и настоящая ERQ требует выборки,
// разобранной глазами через `/graph review`. Эта оговорка печатается вместе
// с числами намеренно — без неё цифру процитируют как точность склейки.

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// erqPair — одна разобранная пара из реестра.
type erqPair struct {
	cos     float64
	verdict string // «ДА», «НЕТ», «?»
	judge   string // кто разбирал: модель или имя прежнего разбора
}

// erqCounts — четыре клетки таблицы согласия при заданном пороге.
type erqCounts struct {
	truePos  int // порог сказал «склеить», арбитр — «ДА»
	falsePos int // порог сказал «склеить», арбитр — «НЕТ»
	trueNeg  int // порог сказал «развести», арбитр — «НЕТ»
	falseNeg int // порог сказал «развести», арбитр — «ДА»
}

func (c erqCounts) total() int { return c.truePos + c.falsePos + c.trueNeg + c.falseNeg }

// erq — доля совпадений с разметкой.
func (c erqCounts) erq() float64 {
	if c.total() == 0 {
		return math.NaN()
	}
	return float64(c.truePos+c.trueNeg) / float64(c.total())
}

// onYes — согласие на парах, где арбитр сказал «ДА».
func (c erqCounts) onYes() float64 {
	if c.truePos+c.falseNeg == 0 {
		return math.NaN()
	}
	return float64(c.truePos) / float64(c.truePos+c.falseNeg)
}

// onNo — согласие на парах, где арбитр сказал «НЕТ».
func (c erqCounts) onNo() float64 {
	if c.trueNeg+c.falsePos == 0 {
		return math.NaN()
	}
	return float64(c.trueNeg) / float64(c.trueNeg+c.falsePos)
}

// readJudged читает реестр разобранных пар.
//
// Формат: id_a, id_b, вердикт, cos, модель, когда — с заголовком в первой
// строке. Строки с неразобранным cos пропускаются и считаются отдельно:
// пара без близости для порога немая.
func readJudged(path string) (pairs []erqPair, noCos int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		if first { // заголовок
			first = false
			continue
		}
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 4 {
			continue
		}
		cos, e := strconv.ParseFloat(strings.TrimSpace(p[3]), 64)
		if e != nil {
			noCos++
			continue
		}
		judge := ""
		if len(p) >= 5 {
			judge = strings.TrimSpace(p[4])
		}
		pairs = append(pairs, erqPair{cos: cos, verdict: strings.TrimSpace(p[2]), judge: judge})
	}
	return pairs, noCos, sc.Err()
}

// countAt раскладывает пары по клеткам при пороге min.
//
// Вердикт «?» (арбитр усомнился) в счёт не идёт: у пары нет истины, с которой
// сравнивать, — такие пары ждут человека в `/graph review`.
func countAt(pairs []erqPair, min float64) erqCounts {
	var c erqCounts
	for _, p := range pairs {
		yes := p.cos >= min
		switch p.verdict {
		case "ДА":
			if yes {
				c.truePos++
			} else {
				c.falseNeg++
			}
		case "НЕТ":
			if yes {
				c.falsePos++
			} else {
				c.trueNeg++
			}
		}
	}
	return c
}

// quantile — значение на доле q отсортированного среза.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}

// erqStats печатает разбор реестра: состав, распределение близости, согласие
// порога с арбитром по сетке порогов и отдельно при рабочем пороге.
func erqStats(graphDir string, minCos float64) {
	path := filepath.Join(graphDir, "doubles-judged.tsv")
	pairs, noCos, err := readJudged(path)
	die(err)

	byVerdict := map[string]int{}
	byJudge := map[string]int{}
	cosOf := map[string][]float64{}
	for _, p := range pairs {
		byVerdict[p.verdict]++
		byJudge[p.judge]++
		cosOf[p.verdict] = append(cosOf[p.verdict], p.cos)
	}
	for _, v := range cosOf {
		sort.Float64s(v)
	}

	fmt.Printf("\nERQ. Разрешение сущностей на готовой разметке (этап 90, Н1)\n")
	fmt.Printf("  реестр: %s\n", path)
	fmt.Printf("  пар с близостью: %d", len(pairs))
	if noCos > 0 {
		fmt.Printf(", без разобранной близости пропущено: %d", noCos)
	}
	fmt.Println()
	fmt.Printf("  вердикты: %v\n", byVerdict)

	fmt.Println("\n  Кто разбирал (источник разметки):")
	type kv struct {
		k string
		n int
	}
	var judges []kv
	for k, n := range byJudge {
		judges = append(judges, kv{k, n})
	}
	sort.Slice(judges, func(i, j int) bool { return judges[i].n > judges[j].n })
	for _, j := range judges {
		name := j.k
		if name == "" {
			name = "(не указан)"
		}
		fmt.Printf("    %-46s пар %d\n", name, j.n)
	}

	fmt.Println("\n  Близость по вердиктам (min · 5% · медиана · 95% · max):")
	for _, v := range []string{"ДА", "НЕТ", "?"} {
		s := cosOf[v]
		if len(s) == 0 {
			continue
		}
		fmt.Printf("    %-4s n=%-7d %.4f · %.4f · %.4f · %.4f · %.4f\n", v, len(s),
			s[0], quantile(s, 0.05), quantile(s, 0.5), quantile(s, 0.95), s[len(s)-1])
	}

	fmt.Println("\n  Согласие порога с арбитром (порог решает сам, без арбитра):")
	fmt.Println("    порог    ERQ     на «ДА»  на «НЕТ»   склеил бы   развёл бы")
	for t := 0.80; t <= 0.995; t += 0.01 {
		c := countAt(pairs, t)
		fmt.Printf("    %.2f   %6.2f%%  %6.2f%%  %7.2f%%   %9d   %9d\n",
			t, 100*c.erq(), 100*c.onYes(), 100*c.onNo(),
			c.truePos+c.falsePos, c.trueNeg+c.falseNeg)
	}

	// Опоры. Без них 62% читаются как «неплохо», хотя «склеивать всё подряд»
	// даёт почти столько же: доля «ДА» в реестре и есть цена бездумной склейки.
	yes, no := byVerdict["ДА"], byVerdict["НЕТ"]
	if yes+no > 0 {
		fmt.Println("\n  Опоры, с которыми только и можно сравнивать:")
		fmt.Printf("    «склеивать всё»  (порог 0):  ERQ %.2f%%\n", 100*float64(yes)/float64(yes+no))
		fmt.Printf("    «не склеивать»   (порог 1):  ERQ %.2f%%\n", 100*float64(no)/float64(yes+no))
	}

	// По источникам разметки: разбор 02.09 и свежий арбитр отбирали пары
	// разными полосами близости, и общая таблица их смешивает.
	fmt.Println("\n  По источникам разметки при рабочем пороге. «Опора» — доля большего")
	fmt.Println("  класса в этом же подмножестве: ERQ ниже опоры означает, что порог хуже,")
	fmt.Println("  чем решение не думая. Полоса cos показывает, какие пары источник вообще брал.")
	fmt.Println("    источник                                       пар     ERQ    опора  на «ДА»  на «НЕТ»  полоса cos")
	for _, j := range judges {
		var sub []erqPair
		for _, p := range pairs {
			if p.judge == j.k {
				sub = append(sub, p)
			}
		}
		sc := countAt(sub, minCos)
		if sc.total() == 0 {
			continue
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, p := range sub {
			lo, hi = math.Min(lo, p.cos), math.Max(hi, p.cos)
		}
		base := float64(sc.truePos+sc.falseNeg) / float64(sc.total()) // доля «ДА»
		if base < 0.5 {
			base = 1 - base
		}
		name := j.k
		if name == "" {
			name = "(не указан)"
		}
		fmt.Printf("    %-44s %6d  %6.2f%% %6.2f%%  %6.2f%%  %7.2f%%  %.3f–%.3f\n",
			name, sc.total(), 100*sc.erq(), 100*base, 100*sc.onYes(), 100*sc.onNo(), lo, hi)
	}

	c := countAt(pairs, minCos)
	fmt.Printf("\n  При рабочем пороге graph.link_min_cos = %.2f:\n", minCos)
	fmt.Printf("    ERQ (доля совпадений с разметкой): %.2f%% на %d парах\n", 100*c.erq(), c.total())
	fmt.Printf("    согласие на «ДА»:  %.2f%% (совпало %d, разошлось %d)\n", 100*c.onYes(), c.truePos, c.falseNeg)
	fmt.Printf("    согласие на «НЕТ»: %.2f%% (совпало %d, разошлось %d)\n", 100*c.onNo(), c.trueNeg, c.falsePos)
	fmt.Printf("    ложных склеек (порог «да», арбитр «нет»): %d\n", c.falsePos)

	fmt.Println("\n  Чем это НЕ является:")
	fmt.Println("    — это согласие порога с ПРЕЖНИМ АРБИТРОМ, а не истина: разметку выносил")
	fmt.Println("      арбитр (частью человек). Настоящая ERQ требует выборки, разобранной")
	fmt.Println("      глазами через /graph review.")
	fmt.Println("    — пары попали в реестр не случайно: их отбирали по близости, поэтому")
	fmt.Println("      слева от минимума таблицы данных нет и доля «НЕТ» здесь не такая,")
	fmt.Println("      как во всём графе.")
	fmt.Printf("    — вердикт «?» (%d пар) в счёт не идёт: у пары нет истины.\n", byVerdict["?"])
}
