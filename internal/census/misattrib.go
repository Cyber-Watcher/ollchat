// Переписи ложных приписываний: упоминание попало не тому понятию. Три
// режима одного предмета, перенесённые 30.09.2026 из `privatescripts/`
// (этап 114, Г1): `acronymcensus` → misattribAcronym, `aliascensus` →
// misattribAlias, `mentioncensus` → misattribMention. Решение владельца
// 30.09.2026 — «отдельный бинарь не надо», всё ключом внутрь ollchat.
//
// **Общий признак у всех трёх.** Векторы кусков и понятий уже посчитаны
// одной моделью и лежат на диске (`entities.vec`) — карта не нужна, модель
// не зовётся. Упоминание, приписанное не тому понятию, в среднем лежит по
// смыслу заметно дальше настоящих упоминаний того же понятия. Расходится
// то, ЧЕМ каждая перепись делит упоминания на «свои» и «подозрительные»:
// аббревиатура-омоним (misattribAcronym), синоним-ключ реестра, работающий
// как ложная воронка (misattribAlias), или собственное имя понятия, вовсе
// не встреченное в тексте куска (misattribMention).
package census

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// entVecDim — размерность вектора понятия; та же модель и та же размерность,
// что у векторов кусков (kb.Collection.ChunkVectorByRef).
const entVecDim = 1024

// ── общее для всех трёх переписей ──────────────────────────────────────────

// readEntityVectors читает сырые векторы понятий с диска: entities.vec лежит
// подряд по номеру понятия, вектор id=1 — с нулевого байта.
func readEntityVectors(g *graph.Graph) ([]byte, error) {
	return os.ReadFile(filepath.Join(g.Dir(), "entities.vec"))
}

// entityVectorAt — вектор понятия id из сырых байт entities.vec; ok=false,
// если понятия с таким id нет или векторы ещё не досчитаны до него.
func entityVectorAt(raw []byte, id uint32) ([]int8, bool) {
	if id == 0 {
		return nil, false
	}
	from := (int(id) - 1) * entVecDim
	if from < 0 || from+entVecDim > len(raw) {
		return nil, false
	}
	v := make([]int8, entVecDim)
	for i := range v {
		v[i] = int8(raw[from+i])
	}
	return v, true
}

// cosine — косинус между двумя векторами одной модели (кусок и понятие,
// оба int8). Нулевой вектор ни с чем не связан по построению: делить не на
// что, отдаём 0, а не NaN.
func cosine(a, b []int8) float64 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// evenStep — шаг равномерной выборки не больше limit элементов из n; когда
// урезать нечего, шаг 1 (перебор всех). limit=0 при n>0 — ошибка вызывающего
// (та же, что была в перенесённой программе: она тоже делила на -cap как
// есть, без своей проверки).
func evenStep(n, limit int) int {
	if n > limit {
		return n / limit
	}
	return 1
}

// ensureOutDir создаёт каталог для списков перед первой записью, если он
// назван (-out); без -out каталог не нужен вовсе.
func ensureOutDir(out string) error {
	if out == "" {
		return nil
	}
	return os.MkdirAll(out, 0o755)
}

// writeForgetList сохраняет список кусков в формате --graph-forget-chunks:
// строки отсортированы, ссылка на кусок «книга#кусок» — в начале строки.
// Каталог должен уже существовать (ensureOutDir); без -out ничего не пишет.
func writeForgetList(outDir, file string, lines []string) error {
	if outDir == "" {
		return nil
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(outDir, file), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// cut обрезает строку до n рун, добавляя многоточие — для табличного вывода.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// cutList обрезает список строк до n элементов, каждую — до 18 рун.
func cutList(a []string, n int) []string {
	if len(a) > n {
		a = a[:n]
	}
	out := make([]string, len(a))
	for i, s := range a {
		out[i] = cut(s, 18)
	}
	return out
}

// ── misattrib-acronym: ложные раскрытия аббревиатур (этап 104, Ж1.5) ───────
//
// Понятие «Relation extraction» получило синоним «RE», синоним стал ключом
// поиска, и дальше всякое «RE» из кусков про регулярные выражения приходило
// в это понятие вместе со своими связями. Сокращение честно раскрывается
// и так и так — по буквам омоним не отличить.
//
// Два независимых признака омонима, оба без видеокарты: смысл (упоминание
// «только сокращением» лежит заметно дальше от понятия, чем упоминания
// с полным именем) и текст (в куске есть ДРУГОЕ раскрытие тех же букв —
// «regular expressions (RE)» у понятия Relation Extraction).

func lettersOnly(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// initials — первые буквы слов имени; слова делятся пробелами и дефисами.
func initials(name string) string {
	var b strings.Builder
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == ' ' || r == '-' || r == '_' }) {
		r := []rune(w)
		if len(r) > 0 && (unicode.IsLetter(r[0]) || unicode.IsDigit(r[0])) {
			b.WriteRune(r[0])
		}
	}
	return b.String()
}

// otherExpansion ищет в тексте раскрытие сокращения short, не совпадающее
// с longs: подряд идущие слова, чьи первые буквы дают short, рядом со скобкой
// с самим сокращением — «regular expressions (RE)» или «RE (regular
// expressions)».
func otherExpansion(words []string, short string, longs map[string]bool) string {
	n := len([]rune(short))
	for i := 0; i+n <= len(words); i++ {
		ok := true
		for k, r := range []rune(short) {
			wr := []rune(words[i+k])
			if len(wr) < 3 || wr[0] != r {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		phrase := strings.Join(words[i:i+n], " ")
		if longs[phrase] {
			continue
		}
		// рядом (до трёх слов) должно стоять само сокращение
		for j := max(0, i-3); j < min(len(words), i+n+3); j++ {
			if (j < i || j >= i+n) && words[j] == short {
				return phrase
			}
		}
	}
	return ""
}

// misattribAcronym — перепись ложных раскрытий аббревиатур
// (privatescripts/acronymcensus). Печатает таблицу понятий по числу
// подозрительных упоминаний и пишет список кусков в формате
// --graph-forget-chunks; -out пуст — список не пишется.
func misattribAcronym(stdout io.Writer, cfg *config.Config, collName string, out string, show uint, capPer int) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()
	g, err := graph.Open(c.Dir(), c.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	raw, err := readEntityVectors(g)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "перебираю понятия с сокращением-синонимом…")

	type only struct {
		k      graph.ChunkKey
		cos    float64
		other  string
		sample string
	}
	type suspect struct {
		ent                  graph.Entity
		short                string
		mentions, full, only int
		meanFull, sdFull     float64
		far, otherExp        int
		examples             []string
		chunks               []string
	}

	var all []*suspect
	candidates := 0
	for _, e := range g.Entities().Live() {
		if show != 0 && uint(e.ID) != show {
			continue
		}
		names := append([]string{e.Name}, g.Entities().DisplayAliases(e)...)
		// Короткая форма: 2–5 знаков, равная первым буквам какого-то длинного имени.
		short := ""
		longs := map[string]bool{}
		for _, n := range names {
			if len(strings.Fields(n)) >= 2 || strings.Contains(n, "-") {
				longs[strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(n)), " "))] = true
			}
		}
		for _, n := range names {
			s := lettersOnly(n)
			if l := len([]rune(s)); l < 2 || l > 5 || len(strings.Fields(n)) != 1 {
				continue
			}
			for long := range longs {
				if initials(long) == s {
					short = s
				}
			}
		}
		if short == "" {
			continue
		}
		candidates++
		ev, ok := entityVectorAt(raw, e.ID)
		if !ok {
			continue
		}
		ms := g.Mentions().Of(e.ID)
		step := evenStep(len(ms), capPer)
		sp := &suspect{ent: e, short: short, mentions: len(ms)}
		var fullCos []float64
		var onlys []only
		for i := 0; i < len(ms); i += step {
			k := ms[i]
			ci, ok := c.ChunkByRef(k.Doc, k.Ord)
			if !ok {
				continue
			}
			joined, hyph := graph.MatchText(ci.Text)
			hasFull := false
			for _, n := range names {
				if lettersOnly(n) != short && graph.SeenInText(joined, hyph, n) {
					hasFull = true
					break
				}
			}
			cv, okv := c.ChunkVectorByRef(k.Doc, k.Ord)
			if !okv {
				continue
			}
			cs := cosine(cv, ev)
			if hasFull {
				sp.full++
				fullCos = append(fullCos, cs)
				continue
			}
			words := strings.FieldsFunc(joined, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			hasShort := false
			for _, w := range words {
				if w == short {
					hasShort = true
					break
				}
			}
			if !hasShort {
				if show != 0 {
					fmt.Fprintf(stdout, "   ни имени, ни сокращения [%s, cos %.3f, %s]: %s\n", k, cs, cut(filepath.Base(ci.Book.Path), 30), cut(joined, 150))
				}
				continue
			}
			sp.only++
			o := only{k: k, cos: cs, other: otherExpansion(words, short, longs)}
			if idx := strings.Index(joined, short); idx >= 0 {
				r := []rune(joined)
				bi := len([]rune(joined[:idx]))
				o.sample = string(r[max(0, bi-50):min(len(r), bi+70)])
			}
			onlys = append(onlys, o)
		}
		if len(fullCos) >= 3 {
			var s float64
			for _, x := range fullCos {
				s += x
			}
			sp.meanFull = s / float64(len(fullCos))
			for _, x := range fullCos {
				sp.sdFull += (x - sp.meanFull) * (x - sp.meanFull)
			}
			sp.sdFull = math.Sqrt(sp.sdFull / float64(len(fullCos)))
		}
		for _, o := range onlys {
			far := len(fullCos) >= 3 && o.cos < sp.meanFull-2*sp.sdFull && o.cos < sp.meanFull-0.08
			if o.other != "" {
				sp.otherExp++
			}
			if far {
				sp.far++
			}
			if far || o.other != "" {
				why := fmt.Sprintf("cos %.3f при среднем %.3f", o.cos, sp.meanFull)
				if o.other != "" {
					why += "; в куске другое раскрытие: «" + o.other + "»"
				}
				sp.chunks = append(sp.chunks, fmt.Sprintf("%s\t%s ← %s: %s", o.k, e.Name, strings.ToUpper(short), why))
				if len(sp.examples) < 3 {
					sp.examples = append(sp.examples, fmt.Sprintf("[%s, cos %.3f%s] …%s…", o.k, o.cos,
						map[bool]string{true: ", др. раскрытие «" + o.other + "»", false: ""}[o.other != ""], o.sample))
				}
			}
		}
		if len(sp.chunks) > 0 || show != 0 {
			all = append(all, sp)
		}
	}

	// Порядок при равных числах задаётся вторым признаком: номер понятия. Без него
	// порядок брался из обхода карты и МЕНЯЛСЯ между прогонами — старый
	// `dotprefixcensus` дал два разных ответа на двух прогонах подряд (проверено
	// 30.09.2026). Прибор обязан быть воспроизводим, иначе его вывод
	// нельзя ни сверить, ни процитировать.
	sort.Slice(all, func(i, j int) bool {
		if len(all[i].chunks) != len(all[j].chunks) {
			return len(all[i].chunks) > len(all[j].chunks)
		}
		return all[i].ent.ID < all[j].ent.ID
	})
	fmt.Fprintf(stdout, "понятий с сокращением-синонимом (2–5 знаков = первые буквы длинного имени): %d\n", candidates)
	fmt.Fprintf(stdout, "из них с подозрительными упоминаниями: %d\n\n", len(all))
	total := 0
	var list []string
	for i, sp := range all {
		total += len(sp.chunks)
		list = append(list, sp.chunks...)
		if i < 40 {
			fmt.Fprintf(stdout, "%5d подозр. из %5d «только сокращением» (всего упом. %6d, с полным именем %5d, cos %.3f±%.3f; далёких %d, с др. раскрытием %d)  #%d %s ← %s\n",
				len(sp.chunks), sp.only, sp.mentions, sp.full, sp.meanFull, sp.sdFull, sp.far, sp.otherExp, sp.ent.ID, cut(sp.ent.Name, 40), strings.ToUpper(sp.short))
			for _, ex := range sp.examples {
				fmt.Fprintf(stdout, "        %s\n", cut(ex, 210))
			}
		}
	}
	fmt.Fprintf(stdout, "\nвсего подозрительных кусков: %d (выборка не больше %d упоминаний на понятие)\n", total, capPer)
	if err := ensureOutDir(out); err != nil {
		return err
	}
	return writeForgetList(out, "acronyms.txt", list)
}

// ── misattrib-alias: ложные синонимы, работающие ключами реестра
// (этап 104, Ж1.5 и Ж1.7) ───────────────────────────────────────────────────
//
// Синоним понятия — это ещё и ключ: имя из ответа модели, совпавшее
// с синонимом, приводит к этому понятию. Ложный синоним поэтому не просто
// справка с ошибкой, а воронка: у «credentials» в синонимах «указатель»,
// и каждая русская книга по Go, говоря об указателях, кормит «credentials».
//
// Признак: упоминания понятия делятся на те, где видно его собственное имя,
// и те, где видно ТОЛЬКО данный синоним. Если куски второй группы лежат
// заметно дальше от понятия по смыслу, синоним ведёт не туда. Законный
// синоним («K8s», перевод) такого разрыва не даёт: куски про то же самое.

// misattribAlias — печатает распределение разрыва и таблицу ложных
// синонимов; с -out пишет aliases-bad.tsv (понятие, синоним, числа) и список
// кусков «только по этому синониму» в формате --graph-forget-chunks.
func misattribAlias(stdout io.Writer, cfg *config.Config, collName string, out string, minOnly int, gap float64) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()
	g, err := graph.Open(c.Dir(), c.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	raw, err := readEntityVectors(g)
	if err != nil {
		return err
	}

	type form struct {
		text  string
		stems []string
	}
	type aliasStat struct {
		n      int
		cos    float64
		chunks []uint64
	}
	type entStat struct {
		own   aliasStat // куски, где видно собственное имя
		alias map[string]*aliasStat
		forms []form // [0] — собственное имя
	}

	stats := map[uint32]*entStat{}
	byChunk := map[uint64][]uint32{}
	withAliases := 0
	for _, e := range g.Entities().Live() {
		if len(e.Aliases) == 0 {
			continue
		}
		withAliases++
		st := &entStat{alias: map[string]*aliasStat{}}
		for _, n := range append([]string{e.Name}, e.Aliases...) {
			f := form{text: n}
			for _, t := range kb.Tokens(graph.MatchName(n), nil) {
				f.stems = append(f.stems, t.Term)
			}
			st.forms = append(st.forms, f)
		}
		stats[e.ID] = st
		for _, m := range g.Mentions().Of(e.ID) {
			byChunk[m.Pack()] = append(byChunk[m.Pack()], e.ID)
		}
	}
	fmt.Fprintf(stdout, "живых понятий с синонимами: %d\n", withAliases)

	fmt.Fprintln(os.Stderr, "разбираю куски коллекции…")
	if err := c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		key := graph.ChunkKey{Doc: ci.Doc, Ord: ci.Ord}
		ids := byChunk[key.Pack()]
		if len(ids) == 0 {
			return nil
		}
		cv, ok := c.ChunkVectorByRef(ci.Doc, ci.Ord)
		if !ok {
			return nil
		}
		joined, hyph := graph.MatchText(ci.Text)
		stems := map[string]bool{}
		for _, t := range kb.Tokens(joined, nil) {
			stems[t.Term] = true
		}
		visible := func(f form) bool {
			if graph.SeenInText(joined, hyph, f.text) {
				return true
			}
			if len(f.stems) == 0 {
				return false
			}
			for _, s := range f.stems {
				if !stems[s] {
					return false
				}
			}
			return true
		}
		for _, id := range ids {
			st := stats[id]
			ev, ok := entityVectorAt(raw, id)
			if !ok {
				continue
			}
			cs := cosine(cv, ev)
			if visible(st.forms[0]) {
				st.own.n++
				st.own.cos += cs
				continue
			}
			// Собственного имени нет: какие синонимы видны?
			var seen []string
			for _, f := range st.forms[1:] {
				if visible(f) {
					seen = append(seen, f.text)
				}
			}
			if len(seen) != 1 {
				continue // ни одного или несколько — вину одному синониму не приписать
			}
			a := st.alias[seen[0]]
			if a == nil {
				a = &aliasStat{}
				st.alias[seen[0]] = a
			}
			a.n++
			a.cos += cs
			a.chunks = append(a.chunks, key.Pack())
		}
		return nil
	}); err != nil {
		return err
	}

	type bad struct {
		id              uint32
		name, alias     string
		own, only       int
		cosOwn, cosOnly float64
		chunks          []uint64
	}
	var bads []bad
	var gaps []float64
	for id, st := range stats {
		if st.own.n < 5 {
			continue
		}
		cosOwn := st.own.cos / float64(st.own.n)
		ent, _ := g.Entities().Get(id)
		for a, as := range st.alias {
			if as.n < minOnly {
				continue
			}
			cosOnly := as.cos / float64(as.n)
			gaps = append(gaps, cosOwn-cosOnly)
			if cosOwn-cosOnly >= gap {
				bads = append(bads, bad{id, ent.Name, a, st.own.n, as.n, cosOwn, cosOnly, as.chunks})
			}
		}
	}
	// Второй признак — номер понятия и синоним: порядок не от обхода карты.
	sort.Slice(bads, func(i, j int) bool {
		if bads[i].only != bads[j].only {
			return bads[i].only > bads[j].only
		}
		if bads[i].id != bads[j].id {
			return bads[i].id < bads[j].id
		}
		return bads[i].alias < bads[j].alias
	})

	// Распределение разрыва по всем синонимам с достаточным числом упоминаний:
	// порог должен стоять в хвосте, а не в теле.
	hist := map[int]int{}
	for _, x := range gaps {
		hist[int(math.Floor(x*50))]++
	}
	var keys []int
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	fmt.Fprintf(stdout, "синонимов с %d+ упоминаниями «только по нему»: %d; распределение разрыва (cos с именем − cos только с синонимом):\n", minOnly, len(gaps))
	for _, k := range keys {
		fmt.Fprintf(stdout, "  %+.2f…%+.2f  %5d\n", float64(k)/50, float64(k+1)/50, hist[k])
	}

	total := 0
	var tsv, list []string
	fmt.Fprintf(stdout, "\nложных синонимов (разрыв ≥ %.2f): %d\n", gap, len(bads))
	for i, b := range bads {
		total += b.only
		tsv = append(tsv, fmt.Sprintf("%d\t%s\t%s\t%d\t%d\t%.3f\t%.3f", b.id, b.name, b.alias, b.own, b.only, b.cosOwn, b.cosOnly))
		for _, packed := range b.chunks {
			list = append(list, fmt.Sprintf("%s\t#%d %s ← ложный синоним «%s» (cos %.3f против %.3f с именем)", graph.UnpackChunk(packed), b.id, b.name, b.alias, b.cosOnly, b.cosOwn))
		}
		if i < 60 {
			fmt.Fprintf(stdout, "  %5d только по синониму (с именем %5d)  %.3f → %.3f  #%d %s ← «%s»\n", b.only, b.own, b.cosOwn, b.cosOnly, b.id, cut(b.name, 34), cut(b.alias, 34))
		}
	}
	fmt.Fprintf(stdout, "кусков, приписанных по ложным синонимам: %d\n", total)
	if err := ensureOutDir(out); err != nil {
		return err
	}
	if out != "" {
		header := "id\tимя\tсиноним\tс_именем\tтолько_синоним\tcos_имя\tcos_синоним\n"
		if err := os.WriteFile(filepath.Join(out, "aliases-bad.tsv"), []byte(header+strings.Join(tsv, "\n")+"\n"), 0o644); err != nil {
			return err
		}
	}
	return writeForgetList(out, "aliases-bad-chunks.txt", list)
}

// ── misattrib-mention: упоминания, приписанные не тем понятиям
// (этап 104, Ж1.5) ──────────────────────────────────────────────────────────
//
// До правки владения ключами (02.09.2026) ключ реестра зависел от порядка
// записей в файле, и синоним одного понятия мог отобрать ключ у собственного
// имени другого. Так «standard library» из книг по Go месяцами приходила
// в понятие «Relation extraction». Ключи с тех пор починены, но приписанное
// тогда осталось в журналах.
//
// Для каждого упоминания (понятие, кусок) — видно ли понятие в тексте куска:
// точно (фразой) или мягко (основы всех слов имени или синонима). Понятие-
// жертва — много невидимых упоминаний, лежащих заметно дальше видимых
// по смыслу. Отдельно ищется «настоящий хозяин»: чьё имя стоит в невидимых
// кусках понятия раз за разом.

// stopWord — служебные слова: хозяином упоминания (misattrib-mention) быть
// не могут.
var stopWord = map[string]bool{"that": true, "this": true, "with": true, "from": true, "have": true, "they": true,
	"will": true, "when": true, "which": true, "their": true, "what": true, "your": true, "about": true, "there": true,
	"then": true, "than": true, "them": true, "these": true, "those": true, "into": true, "also": true, "more": true,
	"some": true, "such": true, "only": true, "other": true, "each": true, "used": true, "using": true, "data": true,
	"этот": true, "того": true, "этом": true, "может": true, "быть": true, "если": true, "когда": true, "также": true}

// misattribMention — печатает распределение видно/не видно по понятиям
// и найденные подмены хозяина; с -out пишет hijacked.txt (все невидимые
// куски понятий-жертв) и hijacked-owner.txt (куски с найденным настоящим
// хозяином), оба в формате --graph-forget-chunks.
func misattribMention(stdout io.Writer, cfg *config.Config, collName string, out string, minNone int, gap float64) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()
	g, err := graph.Open(c.Dir(), c.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	raw, err := readEntityVectors(g)
	if err != nil {
		return err
	}

	// Упоминания по кускам.
	byChunk := map[uint64][]uint32{}
	total := 0
	live := g.Entities().Live()
	for _, e := range live {
		for _, m := range g.Mentions().Of(e.ID) {
			byChunk[m.Pack()] = append(byChunk[m.Pack()], e.ID)
			total++
		}
	}
	fmt.Fprintf(stdout, "живых понятий %d, упоминаний %d в %d кусках\n", len(live), total, len(byChunk))

	type nm struct {
		exact []string   // имена в виде для сверки фразой
		stems [][]string // основы слов каждого имени
	}
	names := map[uint32]*nm{}
	namesOf := func(id uint32) *nm {
		if v, ok := names[id]; ok {
			return v
		}
		v := &nm{}
		if ent, ok := g.Entities().Get(id); ok {
			for _, n := range append([]string{ent.Name}, g.Entities().DisplayAliases(ent)...) {
				v.exact = append(v.exact, n)
				var st []string
				for _, t := range kb.Tokens(graph.MatchName(n), nil) {
					st = append(st, t.Term)
				}
				v.stems = append(v.stems, st)
			}
		}
		names[id] = v
		return v
	}

	type entStat struct {
		seen, soft, none int
		cosSeen, cosNone float64
		nSeen, nNone     int
		noneChunks       []uint64
	}

	stats := map[uint32]*entStat{}
	var tSeen, tSoft, tNone int
	fmt.Fprintln(os.Stderr, "разбираю куски коллекции…")
	if err := c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		key := graph.ChunkKey{Doc: ci.Doc, Ord: ci.Ord}
		ids := byChunk[key.Pack()]
		if len(ids) == 0 {
			return nil
		}
		joined, hyph := graph.MatchText(ci.Text)
		stems := map[string]bool{}
		for _, t := range kb.Tokens(joined, nil) {
			stems[t.Term] = true
		}
		cv, hasVec := c.ChunkVectorByRef(ci.Doc, ci.Ord)
		for _, id := range ids {
			st := stats[id]
			if st == nil {
				st = &entStat{}
				stats[id] = st
			}
			n := namesOf(id)
			kind := 2 // не видно
			for i, name := range n.exact {
				if graph.SeenInText(joined, hyph, name) {
					kind = 0
					break
				}
				// Короткое имя («Go», «C») — по границе слова в основах.
				all := len(n.stems[i]) > 0
				for _, s := range n.stems[i] {
					if !stems[s] {
						all = false
						break
					}
				}
				if all {
					kind = 1
				}
			}
			var cs float64
			if ev, okv := entityVectorAt(raw, id); okv && hasVec {
				cs = cosine(cv, ev)
			}
			switch kind {
			case 0:
				st.seen++
				tSeen++
			case 1:
				st.soft++
				tSoft++
			default:
				st.none++
				tNone++
				st.noneChunks = append(st.noneChunks, key.Pack())
			}
			if cs != 0 {
				if kind == 2 {
					st.cosNone += cs
					st.nNone++
				} else {
					st.cosSeen += cs
					st.nSeen++
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	pct := func(n int) float64 { return 100 * float64(n) / float64(total) }
	fmt.Fprintf(stdout, "\nпонятие видно в своём куске: фразой %d (%.1f%%), по основам слов %d (%.1f%%), не видно %d (%.1f%%)\n\n",
		tSeen, pct(tSeen), tSoft, pct(tSoft), tNone, pct(tNone))

	type row struct {
		id   uint32
		st   *entStat
		gapV float64
	}
	var rows []row
	victims, victimMentions := 0, 0
	var list []string
	for id, st := range stats {
		if st.none < minNone || st.nSeen < 3 || st.nNone < 3 {
			continue
		}
		gv := st.cosSeen/float64(st.nSeen) - st.cosNone/float64(st.nNone)
		rows = append(rows, row{id, st, gv})
		if gv >= gap {
			victims++
			victimMentions += st.none
			ent, _ := g.Entities().Get(id)
			for _, packed := range st.noneChunks {
				list = append(list, fmt.Sprintf("%s\t#%d %s: понятия в куске не видно, разрыв по смыслу %.3f", graph.UnpackChunk(packed), id, ent.Name, gv))
			}
		}
	}
	// Второй признак — номер понятия: порядок не от обхода карты.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].st.none != rows[j].st.none {
			return rows[i].st.none > rows[j].st.none
		}
		return rows[i].id < rows[j].id
	})
	fmt.Fprintf(stdout, "понятий не меньше чем с %d невидимыми упоминаниями: %d; из них с разрывом по смыслу ≥ %.2f (жертвы): %d, невидимых упоминаний у них %d\n\n",
		minNone, len(rows), gap, victims, victimMentions)
	fmt.Fprintln(stdout, "первые 45 по числу невидимых упоминаний (видно / мягко / не видно; cos видимых − cos невидимых):")
	for i, r := range rows {
		if i >= 45 {
			break
		}
		ent, _ := g.Entities().Get(r.id)
		mark := " "
		if r.gapV >= gap {
			mark = "!"
		}
		fmt.Fprintf(stdout, " %s %6d / %5d / %6d   %.3f − %.3f = %+.3f   #%d %s  %v\n", mark, r.st.seen, r.st.soft, r.st.none,
			r.st.cosSeen/float64(r.st.nSeen), r.st.cosNone/float64(r.st.nNone), r.gapV, r.id, cut(ent.Name, 34), cutList(ent.Aliases, 4))
	}
	// ── Точный признак подмены ────────────────────────────────────────────────
	// У понятия-жертвы в «невидимых» кусках раз за разом стоит имя ДРУГОГО
	// понятия, которого среди упоминаний куска нет: модель назвала его, а ключ
	// привёл не туда. Ищем такого «настоящего хозяина» перебором словосочетаний
	// куска (1–4 слова) по нынешним ключам реестра.
	fmt.Fprintln(stdout, "\n== подмена: чьё имя стоит в невидимых кусках понятия (доля кусков ≥ 50%)")
	mentionedIn := map[uint64]map[uint32]bool{}
	for packed, ids := range byChunk {
		m := make(map[uint32]bool, len(ids))
		for _, id := range ids {
			m[id] = true
		}
		mentionedIn[packed] = m
	}
	type hj struct {
		victim, owner uint32
		chunks, of    int
	}
	var found []hj
	var hjList []string
	for _, r := range rows {
		st := r.st
		if float64(st.none) < 0.4*float64(st.seen+st.soft+st.none) {
			continue
		}
		owners := map[uint32]int{}
		ownChunks := map[uint32][]uint64{}
		for _, packed := range st.noneChunks {
			k := graph.UnpackChunk(packed)
			ci, ok := c.ChunkByRef(k.Doc, k.Ord)
			if !ok {
				continue
			}
			joined, _ := graph.MatchText(ci.Text)
			words := strings.FieldsFunc(joined, func(r rune) bool {
				return !(r == '.' || r == '_' || r == '+' || r == '#' || r == '/' || r == '-' || (r >= '0' && r <= '9') || r > 127 || (r >= 'a' && r <= 'z'))
			})
			seenOwner := map[uint32]bool{}
			// Жадно: самое длинное имя на каждом месте, иначе «standard library»
			// засчитывалась бы слову «library». Служебные слова хозяевами
			// не считаются — их как понятия вообще быть не должно (Ж1.10).
			for i := 0; i < len(words); i++ {
				for n := min(4, len(words)-i); n >= 1; n-- {
					phrase := strings.Trim(strings.Join(words[i:i+n], " "), ".-/ ")
					if len([]rune(phrase)) < 4 || (n == 1 && stopWord[phrase]) {
						continue
					}
					x, ok := g.Entities().Lookup(phrase)
					if !ok {
						continue
					}
					if x.ID != r.id && !mentionedIn[packed][x.ID] && !seenOwner[x.ID] {
						seenOwner[x.ID] = true
						owners[x.ID]++
						ownChunks[x.ID] = append(ownChunks[x.ID], packed)
					}
					i += n - 1
					break
				}
			}
		}
		// Хозяин выбирается обходом карты, и при РАВНОМ числе кусков
		// победитель прежде был случайным: два прогона одного кода называли
		// у куска 105#595 то «apiVersion», то «KinD» (проверено 30.09.2026,
		// 666 строк расхождения из 4 560). Ничья решается меньшим номером
		// понятия — иначе список «кусков к перечитыванию» нельзя ни сверить,
		// ни подать на правку дважды с тем же итогом.
		best, bestN := uint32(0), 0
		for id, n := range owners {
			if n > bestN || (n == bestN && best != 0 && id < best) {
				best, bestN = id, n
			}
		}
		if best != 0 && bestN*2 >= st.none {
			found = append(found, hj{r.id, best, bestN, st.none})
			v, _ := g.Entities().Get(r.id)
			o, _ := g.Entities().Get(best)
			for _, packed := range ownChunks[best] {
				hjList = append(hjList, fmt.Sprintf("%s\t#%d %s ← на деле #%d %s", graph.UnpackChunk(packed), r.id, v.Name, best, o.Name))
			}
		}
	}
	// Второй признак — номер понятия: порядок не от обхода карты.
	sort.Slice(found, func(i, j int) bool {
		if found[i].chunks != found[j].chunks {
			return found[i].chunks > found[j].chunks
		}
		if found[i].victim != found[j].victim {
			return found[i].victim < found[j].victim
		}
		return found[i].owner < found[j].owner
	})
	sum := 0
	for i, f := range found {
		sum += f.chunks
		if i < 40 {
			v, _ := g.Entities().Get(f.victim)
			o, _ := g.Entities().Get(f.owner)
			fmt.Fprintf(stdout, "  %5d из %5d невидимых  #%d %s  ← на деле #%d %s\n", f.chunks, f.of, f.victim, cut(v.Name, 36), f.owner, cut(o.Name, 36))
		}
	}
	fmt.Fprintf(stdout, "понятий с подменой: %d, кусков к перечитыванию: %d\n", len(found), sum)

	// Распределение разрыва — чтобы порог выбирать по данным.
	hist := map[int]int{}
	for _, r := range rows {
		hist[int(math.Floor(r.gapV*50))]++
	}
	var keys []int
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	fmt.Fprintln(stdout, "\nраспределение разрыва по смыслу среди этих понятий:")
	for _, k := range keys {
		fmt.Fprintf(stdout, "  %+.2f…%+.2f  %5d\n", float64(k)/50, float64(k+1)/50, hist[k])
	}

	// Обе записи — после ensureOutDir: в перенесённой программе
	// hijacked-owner.txt писался до своего единственного MkdirAll, случившегося
	// позже вместе с hijacked.txt (сработало бы только если -out уже
	// существовал). Здесь каталог гарантированно готов перед первой записью.
	if err := ensureOutDir(out); err != nil {
		return err
	}
	if err := writeForgetList(out, "hijacked-owner.txt", hjList); err != nil {
		return err
	}
	return writeForgetList(out, "hijacked.txt", list)
}
