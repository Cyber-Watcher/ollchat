package stats

// Три переписи этапа 101 (Г9–Г11), все по открытому на чтение графу, без карты:
//   -corroboration  — подтверждения связей по ИСТОЧНИКАМ, а не по кускам
//   -typecheck      — тройки «тип источника, связь, тип цели»
//   -summarycheck   — чужие понятия в описаниях тем

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// ── Г9. Источники подтверждений ──────────────────────────────────────────────

// loadWorks читает пары «часть названия A <TAB> часть названия B» и отдаёт
// отображение «книга → произведение»: обе книги пары получают одно произведение.
func loadWorks(c *kb.Collection, path string) (map[uint32]uint32, int) {
	works := map[uint32]uint32{}
	if path == "" {
		return works, 0
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "пары произведений: %v\n", err)
		return works, 0
	}
	defer f.Close()
	books := c.Books()
	find := func(part string) []uint32 {
		part = strings.ToLower(strings.TrimSpace(part))
		var out []uint32
		for _, b := range books {
			if strings.Contains(strings.ToLower(b.Title), part) || strings.Contains(strings.ToLower(b.Path), part) {
				out = append(out, b.ID)
			}
		}
		return out
	}
	pairs := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 {
			continue
		}
		a, b := find(cols[0]), find(cols[1])
		if len(a) == 0 || len(b) == 0 {
			fmt.Fprintf(os.Stderr, "пара «%s» ↔ «%s»: в коллекции нет одной из книг (найдено %d и %d)\n", cols[0], cols[1], len(a), len(b))
			continue
		}
		work := a[0]
		for _, id := range append(a, b...) {
			works[id] = work
		}
		pairs++
	}
	return works, pairs
}

type pairKey struct{ a, b uint32 }

func corroborationStats(c *kb.Collection, g *graph.Graph, pairsPath string) {
	works, npairs := loadWorks(c, pairsPath)
	workOf := func(doc uint32) uint32 {
		if w, ok := works[doc]; ok {
			return w
		}
		return doc
	}

	// Подтверждения каждой пары понятий: множество кусков.
	conf := map[pairKey]map[graph.ChunkKey]bool{}
	for _, ent := range g.Entities().Live() {
		for _, ed := range g.Edges().Of(ent.ID) {
			k := pairKey{ed.Src, ed.Dst}
			if k.a > k.b {
				k.a, k.b = k.b, k.a
			}
			if ed.Evidence.Doc == 0 {
				continue
			}
			if conf[k] == nil {
				conf[k] = map[graph.ChunkKey]bool{}
			}
			conf[k][ed.Evidence] = true
		}
	}

	var (
		total, single, multi         int
		overlapOnly, translationOnly int
		bothKinds, singleOrigin      int
		confBefore, confAfter        int
		examples                     []string
	)
	type ex struct {
		k    pairKey
		n    int
		kind string
	}
	var exs []ex
	type adj struct {
		k   pairKey
		doc uint32
		ord uint32 // меньший из двух соседних
	}
	var adjSample []adj
	for k, chunks := range conf {
		total++
		n := len(chunks)
		confBefore += n
		if n == 1 {
			single++
			confAfter++
			continue
		}
		multi++
		// Куски по книгам, соседние (ord и ord+1) — один источник.
		byDoc := map[uint32][]uint32{}
		for ck := range chunks {
			byDoc[ck.Doc] = append(byDoc[ck.Doc], ck.Ord)
		}
		clusters := 0
		docs := 0
		wset := map[uint32]bool{}
		for doc, ords := range byDoc {
			docs++
			wset[workOf(doc)] = true
			sort.Slice(ords, func(i, j int) bool { return ords[i] < ords[j] })
			clusters++
			for i := 1; i < len(ords); i++ {
				if ords[i] != ords[i-1]+1 {
					clusters++
				}
			}
		}
		// Источники: кластеры внутри произведения считаем по книге с наибольшим
		// их числом — перевод повторяет оригинал, а не добавляет к нему.
		perWork := map[uint32]int{}
		for doc, ords := range byDoc {
			sort.Slice(ords, func(i, j int) bool { return ords[i] < ords[j] })
			cl := 1
			for i := 1; i < len(ords); i++ {
				if ords[i] != ords[i-1]+1 {
					cl++
				}
			}
			w := workOf(doc)
			if cl > perWork[w] {
				perWork[w] = cl
			}
		}
		origins := 0
		for _, cl := range perWork {
			origins += cl
		}
		confAfter += origins
		isOverlap := docs == 1 && clusters == 1
		isTranslation := docs >= 2 && len(wset) == 1 && origins == 1
		switch {
		case isOverlap:
			overlapOnly++
			if len(adjSample) < 4000 {
				for doc, ords := range byDoc {
					adjSample = append(adjSample, adj{k, doc, ords[0]})
				}
			}
		case isTranslation:
			translationOnly++
		}
		if origins == 1 {
			singleOrigin++
			if isOverlap && isTranslation {
				bothKinds++
			}
			if len(exs) < 400 {
				kind := "соседние куски"
				if isTranslation {
					kind = "пара перевода"
				}
				exs = append(exs, ex{k, n, kind})
			}
		}
	}
	sort.Slice(exs, func(i, j int) bool { return exs[i].n > exs[j].n })
	for i, e := range exs {
		if i >= 12 {
			break
		}
		a, _ := g.Entities().Get(e.k.a)
		b, _ := g.Entities().Get(e.k.b)
		examples = append(examples, fmt.Sprintf("  %2d кусков, %s: %s — %s", e.n, e.kind, a.Name, b.Name))
	}

	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return 100 * float64(n) / float64(d)
	}
	fmt.Printf("Г9. Подтверждения связей по источникам (пар произведений задано: %d)\n", npairs)
	fmt.Printf("  пар понятий со связью: %d\n", total)
	fmt.Printf("  на одном куске: %d (%.1f%%)\n", single, pct(single, total))
	fmt.Printf("  на двух и более кусках: %d (%.1f%%), из них:\n", multi, pct(multi, total))
	fmt.Printf("    только соседние куски одной книги: %d (%.1f%% от многокусочных)\n", overlapOnly, pct(overlapOnly, multi))
	fmt.Printf("    только пара «оригинал — перевод»: %d (%.1f%%)\n", translationOnly, pct(translationOnly, multi))
	fmt.Printf("  итого связей с ОДНИМ источником: %d (%.1f%%) против %d (%.1f%%) на одном куске\n",
		single+singleOrigin, pct(single+singleOrigin, total), single, pct(single, total))
	fmt.Printf("  подтверждений всего: %d кусками → %d источниками (−%.1f%%)\n",
		confBefore, confAfter, pct(confBefore-confAfter, confBefore))
	if len(examples) > 0 {
		fmt.Println("  самые «подтверждённые» связи с одним источником:")
		for _, e := range examples {
			fmt.Println(e)
		}
	}

	// Соседние куски — копия из перекрытия или два настоящих упоминания?
	// Проверяется по тексту: перекрытие — общий хвост первого куска и голова
	// второго; если оба имени стоят внутри него, второе подтверждение — копия.
	copies, genuine, unknown := 0, 0, 0
	for _, a := range adjSample {
		c1, ok1 := c.ChunkByRef(a.doc, a.ord)
		c2, ok2 := c.ChunkByRef(a.doc, a.ord+1)
		if !ok1 || !ok2 {
			unknown++
			continue
		}
		t1, t2 := strings.ToLower(c1.Text), strings.ToLower(c2.Text)
		ea, _ := g.Entities().Get(a.k.a)
		eb, _ := g.Entities().Get(a.k.b)
		na, nb := strings.ToLower(ea.Name), strings.ToLower(eb.Name)
		if !(strings.Contains(t1, na) && strings.Contains(t1, nb) && strings.Contains(t2, na) && strings.Contains(t2, nb)) {
			unknown++ // названо синонимом — по имени не проверить
			continue
		}
		k := min(min(len(t1), len(t2)), 1200)
		for ; k >= 40; k-- {
			if t1[len(t1)-k:] == t2[:k] {
				break
			}
		}
		if k < 40 {
			unknown++
			continue
		}
		ov := t2[:k]
		if strings.Contains(ov, na) && strings.Contains(ov, nb) {
			copies++
		} else {
			genuine++
		}
	}
	fmt.Printf("  проверка по тексту %d соседних пар: копия из перекрытия %d (%.1f%%), два упоминания %d (%.1f%%), не проверить %d\n",
		len(adjSample), copies, pct(copies, len(adjSample)), genuine, pct(genuine, len(adjSample)), unknown)
	if copies+genuine > 0 {
		fmt.Printf("  среди проверяемых: копий %.1f%%\n", pct(copies, copies+genuine))
	}
}

// ── Г10. Тройки «тип — связь — тип» ─────────────────────────────────────────

func typecheckStats(g *graph.Graph) {
	type triple struct {
		src, dst string
		rel      uint8
	}
	count := map[triple]int{}
	pairs := map[triple]map[pairKey]bool{}
	sample := map[triple][]string{}
	total := 0
	for _, ent := range g.Entities().Live() {
		for _, ed := range g.Edges().Of(ent.ID) {
			dst, ok := g.Entities().Get(ed.Dst)
			if !ok {
				continue
			}
			t := triple{graph.NormalizeType(ent.Type), graph.NormalizeType(dst.Type), ed.Type}
			count[t]++
			total++
			if pairs[t] == nil {
				pairs[t] = map[pairKey]bool{}
			}
			pk := pairKey{ed.Src, ed.Dst}
			if !pairs[t][pk] && len(sample[t]) < 3 {
				sample[t] = append(sample[t], ent.Name+" → "+dst.Name)
			}
			pairs[t][pk] = true
		}
	}
	list := make([]triple, 0, len(count))
	for t := range count {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return count[list[i]] > count[list[j]] })
	fmt.Printf("Г10. Тройки «тип источника — связь — тип цели»: сочетаний %d, записей %d\n", len(list), total)
	fmt.Printf("  %-14s %-12s %-14s %9s %7s %9s\n", "источник", "связь", "цель", "записей", "доля", "пар")
	for _, t := range list {
		fmt.Printf("  %-14s %-12s %-14s %9d %6.2f%% %9d\n", t.src, graph.RelName(t.rel), t.dst,
			count[t], 100*float64(count[t])/float64(total), len(pairs[t]))
	}
	fmt.Println("  редкие сочетания (меньше 0.2%) с примерами:")
	for _, t := range list {
		if 100*float64(count[t])/float64(total) >= 0.2 {
			continue
		}
		fmt.Printf("    %s —%s→ %s (%d): %s\n", t.src, graph.RelName(t.rel), t.dst, count[t], strings.Join(sample[t], "; "))
	}
}

// ── Г11. Описание темы против её состава ─────────────────────────────────────

func summarycheckStats(g *graph.Graph, show int) {
	comms, err := g.LoadCommunities()
	if err != nil {
		die(err)
	}
	ents := g.Entities()
	merges := g.Merges()
	// Только по ИМЕНАМ понятий, не по синонимам: Lookup нашёл бы «Тема»
	// (первое слово каждого описания) как синоним понятия «topic», и перепись
	// считала бы чужим понятием слово рамки.
	byName := map[string]uint32{}
	for _, e := range ents.Live() {
		if k := graph.Normalize(e.Name); k != "" {
			byName[k] = e.ID
		}
	}
	var (
		withSummary, withForeign, withMultiword int
		foreignTotal, withUnlinkedMulti         int
	)
	type ex struct {
		title   string
		foreign []string
	}
	var exs []ex
	for _, com := range comms.List {
		if com.Level != 0 || strings.TrimSpace(com.Summary) == "" {
			continue
		}
		withSummary++
		members := map[uint32]bool{}
		linked := map[uint32]bool{}
		for _, id := range com.Members {
			members[merges.Resolve(id)] = true
			for _, nb := range g.Edges().Neighbors(id) {
				linked[merges.Resolve(nb.ID)] = true
			}
		}
		words := splitWords(com.Summary)
		seen := map[uint32]bool{}
		var foreign []string
		multi := false
		unlinkedMulti := false
		for n := 3; n >= 1; n-- {
			for i := 0; i+n <= len(words); i++ {
				phrase := strings.Join(words[i:i+n], " ")
				eid, ok := byName[graph.Normalize(phrase)]
				if !ok {
					continue
				}
				e, _ := ents.Get(eid)
				id := merges.Resolve(e.ID)
				if members[id] || seen[id] {
					continue
				}
				// Редкое понятие в описании — скорее совпадение слова, чем ссылка.
				if e.Count < 5 || len([]rune(e.Name)) < 4 {
					continue
				}
				seen[id] = true
				if linked[id] {
					continue // сосед участника: описание вправе его назвать
				}
				foreign = append(foreign, e.Name)
				if n >= 2 {
					multi = true
					if e.Count >= 5 {
						unlinkedMulti = true
					}
				}
			}
		}
		if len(foreign) > 0 {
			withForeign++
			foreignTotal += len(foreign)
			if multi {
				withMultiword++
			}
			if unlinkedMulti {
				withUnlinkedMulti++
			}
			exs = append(exs, ex{com.Title, foreign})
		}
	}
	sort.Slice(exs, func(i, j int) bool { return len(exs[i].foreign) > len(exs[j].foreign) })
	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return 100 * float64(n) / float64(d)
	}
	fmt.Printf("Г11. Описания тем против состава: тем с описанием %d\n", withSummary)
	fmt.Printf("  описаний, где названо понятие НЕ из темы и НЕ сосед её участников: %d (%.1f%%), из них с многословным именем: %d (%.1f%%)\n",
		withForeign, pct(withForeign, withSummary), withMultiword, pct(withMultiword, withSummary))
	_ = withUnlinkedMulti
	fmt.Printf("  чужих понятий на такое описание в среднем: %.1f\n", float64(foreignTotal)/float64(max(withForeign, 1)))
	fmt.Println("  примеры (тема: чужие понятия):")
	for i, e := range exs {
		if i >= show {
			break
		}
		fmt.Printf("    %s: %s\n", e.title, strings.Join(e.foreign, ", "))
	}
}

// splitWords режет текст на слова: буквы и цифры, остальное — разделитель.
func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.')
	})
}

// ── Г9, шаг 2: разбиение по источникам против разбиения по кускам ───────────

// partitionSets — сообщества как множества понятий.
func partitionSets(assign map[uint32]uint32) map[uint32]map[uint32]bool {
	out := map[uint32]map[uint32]bool{}
	for id, c := range assign {
		if out[c] == nil {
			out[c] = map[uint32]bool{}
		}
		out[c][id] = true
	}
	return out
}

// comparePartitions — у каждого сообщества A лучший Жаккар с сообществом B;
// доли считаются по понятиям, а не по сообществам: тема из двух понятий
// и тема из шести тысяч весят по-разному.
func comparePartitions(a, b map[uint32]uint32) (same, close, far int, nodes int) {
	sa, sb := partitionSets(a), partitionSets(b)
	for _, members := range sa {
		// Кандидаты — сообщества B, куда попали понятия из этого сообщества A.
		cand := map[uint32]int{}
		for id := range members {
			if c, ok := b[id]; ok {
				cand[c]++
			}
		}
		best := 0.0
		for c, inter := range cand {
			union := len(members) + len(sb[c]) - inter
			if j := float64(inter) / float64(union); j > best {
				best = j
			}
		}
		n := len(members)
		nodes += n
		switch {
		case best >= 0.999:
			same += n
		case best >= 0.5:
			close += n
		default:
			far += n
		}
	}
	return
}

func originPartitionStats(g *graph.Graph) {
	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return 100 * float64(n) / float64(d)
	}
	show := func(label string, r graph.PartitionExperiment) {
		fmt.Printf("  %-22s понятий %d, связей %d, тем %d, одиночных %d, крупнейшая %d, медиана %d\n",
			label, r.Nodes, r.Edges, r.Themes, r.Singleton, r.Largest, r.Median)
	}
	fmt.Println("Г9, шаг 2. Разбиение тем: веса по кускам против весов по источникам (тот же Louvain, то же разрешение)")
	byChunks := g.ExperimentPartition(graph.PartitionOpts{KeepAssignment: true})
	show("по кускам", byChunks)
	byOrigins := g.ExperimentPartition(graph.PartitionOpts{ByOrigins: true, KeepAssignment: true})
	show("по источникам", byOrigins)

	same, close, far, nodes := comparePartitions(byChunks.Assign, byOrigins.Assign)
	fmt.Printf("  состав тем (по понятиям): без изменений %.1f%%, изменились слабо (Жаккар ≥ 0.5) %.1f%%, сильно %.1f%%\n",
		pct(same, nodes), pct(close, nodes), pct(far, nodes))

	// Контроль чувствительности: те же веса по кускам, но одиночные пары
	// ослаблены на 1%. Если и это перетасовывает темы так же, разница выше —
	// свойство Louvain, а не источников.
	control := g.ExperimentPartition(graph.PartitionOpts{OnceFactor: 0.99, KeepAssignment: true})
	show("контроль (×0.99)", control)
	s0, c0, f0, n0 := comparePartitions(byChunks.Assign, control.Assign)
	fmt.Printf("  контроль против «по кускам»: без изменений %.1f%%, слабо %.1f%%, сильно %.1f%%\n",
		pct(s0, n0), pct(c0, n0), pct(f0, n0))

	// Для ориентира — против рабочего разбиения на диске (оно старше графа
	// и прошло разрез несвязных тем, поэтому совпадать не обязано).
	comms, err := g.LoadCommunities()
	if err != nil || comms == nil {
		return
	}
	cur := map[uint32]uint32{}
	for _, c := range comms.List {
		if c.Level != 0 {
			continue
		}
		for _, id := range c.Members {
			cur[id] = uint32(c.ID)
		}
	}
	s1, c1, f1, n1 := comparePartitions(cur, byChunks.Assign)
	s2, c2, f2, n2 := comparePartitions(cur, byOrigins.Assign)
	fmt.Printf("  рабочее разбиение на диске (%d тем) против опыта по кускам: без изменений %.1f%%, слабо %.1f%%, сильно %.1f%%\n",
		len(partitionSets(cur)), pct(s1, n1), pct(c1, n1), pct(f1, n1))
	fmt.Printf("  рабочее разбиение на диске против опыта по источникам:      без изменений %.1f%%, слабо %.1f%%, сильно %.1f%%\n",
		pct(s2, n2), pct(c2, n2), pct(f2, n2))
}
