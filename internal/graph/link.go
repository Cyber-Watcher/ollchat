package graph

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Связывание новой сущности при сборке — приём iText2KG (этап 90, пункт 5).
//
// **Зачем.** Двойники в графе рождаются в момент извлечения: модель назвала
// «сборщик мусора», а в графе уже есть `Garbage collection`, и реестр,
// склеивающий только по точному написанию, заводит второй узел. Потом его
// ищут склейкой — после факта, с порогами и арбитром (этап 83, 94). Здесь
// то же решается ДО того, как узел появился: вектор нового имени сравнивается
// с векторами существующих понятий, близкое уходит арбитру, и при «ДА»
// новое имя становится упоминанием существующего узла. Второго узла нет —
// склеивать нечего.
//
// **Что обратимо и что нет.** Решения пишутся в `links.jsonl` (дозапись,
// как склейки): что с чем связано, близость, вердикт, кусок. Упоминания
// и связи, отданные выжившему узлу, обратно не переносятся — они записаны
// на него сразу. Поэтому связывание идёт **ключом сборки, по умолчанию
// выключенным**, и только в опытном графе, пока не измерено.
//
// **Спорное — человеку, а не в корзину.** Вердикт «?» (или отсутствие
// арбитра) кладёт пару в тот же журнал с вердиктом «?», узел заводится как
// обычно; очередь видна доктору. Это шаг В этапа 89, отданный сюда записью.

//go:embed prompts/linkjudge.txt
var linkJudgePrompt string

const linksFile = "links.jsonl"

// LinkRec — одно решение о связывании: арбитра при сборке, ночного разбора
// двойников или человека в окне /graph review.
type LinkRec struct {
	Norm string `json:"norm"` // нормализованное новое имя
	Name string `json:"name"` // как написала модель
	// From — узел нового имени, если он заведён (у пар из разбора двойников
	// и у решений человека); 0 — узла ещё не было в момент решения.
	From uint32 `json:"from,omitempty"`
	// To — с каким понятием связано (вердикт ДА).
	To     uint32 `json:"to,omitempty"`
	ToName string `json:"to_name,omitempty"`
	// Cand — кандидат, о котором спрашивали, когда вердикт не ДА: по нему
	// человек видит, с чем именно машина не смогла сравнить.
	Cand     uint32   `json:"cand,omitempty"`
	CandName string   `json:"cand_name,omitempty"`
	Cos      float64  `json:"cos"`
	Verdict  string   `json:"verdict"`          // ДА — связано; ? — в очередь человеку; НЕТ — разные
	By       string   `json:"by,omitempty"`     // арбитр | человек
	Source   string   `json:"source,omitempty"` // сборка | двойники
	Why      string   `json:"why,omitempty"`
	Chunk    ChunkKey `json:"chunk"`
	At       int64    `json:"at"`
}

// Решения и их источники — строками, чтобы журнал читался глазами.
const (
	LinkYes   = "ДА"
	LinkNo    = "НЕТ"
	LinkDoubt = "?"

	LinkByJudge = "арбитр"
	LinkByHuman = "человек"

	LinkFromBuild   = "сборка"
	LinkFromDoubles = "двойники"
)

// pairKey — пара, о которой решение: имя и второй узел. Позднее решение
// перекрывает раннее: человек после арбитра, повторный разбор после первого.
func (r LinkRec) pairKey() string {
	other := r.To
	if other == 0 {
		other = r.Cand
	}
	return fmt.Sprintf("%s|%d", r.Norm, other)
}

// Links — журнал связываний, надеваемый при сборке: связанное имя ведёт
// к своему узлу, не заводя нового.
type Links struct {
	mu   sync.RWMutex
	path string
	recs []LinkRec
	to   map[string]uint32 // norm → узел (только вердикты ДА)
	seen map[string]bool   // norm с любым решением: спрашивать арбитра дважды незачем
	last map[string]int    // пара → номер последнего решения о ней
	n    int
	q    int // сколько в очереди человеку
}

func openLinks(dir string) (*Links, error) {
	l := &Links{path: filepath.Join(dir, linksFile), to: map[string]uint32{}, seen: map[string]bool{}, last: map[string]int{}}
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var r LinkRec
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // оборванная строка — как у других журналов
		}
		l.apply(r)
	}
	return l, nil
}

func (l *Links) apply(r LinkRec) {
	l.recs = append(l.recs, r)
	l.n++
	l.seen[r.Norm] = true
	key := r.pairKey()
	// Прежнее «?» по той же паре снято новым решением.
	if i, ok := l.last[key]; ok && l.recs[i].Verdict == LinkDoubt && r.Verdict != LinkDoubt {
		l.q--
	}
	l.last[key] = len(l.recs) - 1
	switch r.Verdict {
	case LinkYes:
		l.to[r.Norm] = r.To
	case LinkNo:
		if r.By == LinkByHuman {
			delete(l.to, r.Norm) // человек отменил связывание: дальше — свой узел
		}
	case LinkDoubt:
		l.q++
	}
}

// Queue — пары, ждущие человека: последнее решение по паре — «?».
// Сперва самые близкие, при равной близости — более ранние.
func (l *Links) Queue() []LinkRec {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []LinkRec
	for _, i := range l.last {
		if r := l.recs[i]; r.Verdict == LinkDoubt {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Cos != out[b].Cos {
			return out[a].Cos > out[b].Cos
		}
		return out[a].At < out[b].At
	})
	return out
}

// Judged — решения арбитра «ДА» при сборке, не пересмотренные человеком:
// для выборочной проверки глазами (судейский режим окна разбора).
func (l *Links) Judged() []LinkRec {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []LinkRec
	for _, i := range l.last {
		if r := l.recs[i]; r.Verdict == LinkYes && r.By != LinkByHuman {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At > out[b].At })
	return out
}

// Add дописывает решение — из окна разбора или из ночного разбора двойников.
func (l *Links) Add(r LinkRec) error { return l.add(r) }

// LinkQueueSize — сколько пар ждёт человека, по файлу журнала, без открытия
// графа: для подсказки при запуске.
func LinkQueueSize(graphDir string) int {
	l, err := openLinks(graphDir)
	if err != nil {
		return 0
	}
	return l.Queued()
}

// Linked — с каким узлом связано имя; ok=false — решения нет.
func (l *Links) Linked(norm string) (uint32, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	id, ok := l.to[norm]
	return id, ok
}

// Count — сколько решений; Queued — сколько из них ждут человека.
func (l *Links) Count() int  { l.mu.RLock(); defer l.mu.RUnlock(); return l.n }
func (l *Links) Queued() int { l.mu.RLock(); defer l.mu.RUnlock(); return l.q }

func (l *Links) add(r LinkRec) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.At == 0 {
		r.At = time.Now().Unix()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	l.apply(r)
	return nil
}

// Links — журнал связываний графа.
func (g *Graph) Links() *Links { return g.links }

// LinkOpts — как связывать новые сущности при сборке.
type LinkOpts struct {
	// Embedder — чем считать вектор нового имени; той же моделью, что
	// векторы понятий графа, иначе расстояния несравнимы.
	Embedder kb.Embedder
	// Judge — арбитр для близких пар. nil — близкое не связывается,
	// а кладётся в очередь человеку.
	Judge Extractor
	// MinCos — ниже этой близости кандидат не рассматривается. 0 — 0.85:
	// уровень alias склеек (0.92) слишком строг для переводов, уровень
	// mutual (0.70) без взаимного синонима слишком вольный.
	MinCos float64
	// Candidates — сколько ближайших показывать арбитру. 0 — три.
	Candidates int
}

func (o LinkOpts) norm() LinkOpts {
	if o.MinCos <= 0 {
		o.MinCos = 0.85
	}
	if o.Candidates <= 0 {
		o.Candidates = 3
	}
	return o
}

// nearest — ближайшие по вектору понятия к запросу, лучшие первыми.
func (v *EntityVectors) nearest(query []int8, k int) []senseHit {
	if v == nil || !v.Ready() || len(query) != v.Dim() || k <= 0 {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	all := make([]senseHit, 0, v.meta.Count)
	for id := 1; id <= v.meta.Count; id++ {
		vec := v.at(uint32(id))
		if len(vec) == 0 {
			continue
		}
		all = append(all, senseHit{ID: uint32(id), Score: kb.Cosine(vec, query)})
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].Score != all[b].Score {
			return all[a].Score > all[b].Score
		}
		return all[a].ID < all[b].ID
	})
	if len(all) > k {
		all = all[:k]
	}
	return all
}

// linkNew решает, есть ли у нового имени узел в графе. Возвращает (узел, true),
// если имя связано с существующим понятием; (0, false) — заводить как обычно.
//
// Имя, которое уже есть в реестре точным написанием, сюда не попадает:
// его находит Entities.Add. Имя с прежним решением — по журналу, без арбитра.
func (g *Graph) linkNew(ctx context.Context, name, typ string, chunk ChunkKey, o LinkOpts) (uint32, bool, error) {
	norm := Normalize(name)
	if norm == "" || g.links == nil {
		return 0, false, nil
	}
	if _, ok := g.ents.Lookup(name); ok {
		return 0, false, nil
	}
	if id, ok := g.links.Linked(norm); ok {
		return id, true, nil
	}
	g.links.mu.RLock()
	seen := g.links.seen[norm]
	g.links.mu.RUnlock()
	if seen {
		return 0, false, nil
	}
	o = o.norm()
	if o.Embedder == nil || g.vecs == nil || !g.vecs.Ready() {
		return 0, false, nil
	}
	vecs, err := o.Embedder.Embed(ctx, []string{name})
	if err != nil || len(vecs) != 1 {
		return 0, false, err
	}
	query := kb.Quantize(vecs[0])
	var cands []senseHit
	for _, h := range g.vecs.nearest(query, o.Candidates) {
		if h.Score >= o.MinCos {
			cands = append(cands, h)
		}
	}
	if len(cands) == 0 {
		return 0, false, nil
	}
	for _, h := range cands {
		ent, ok := g.ents.Get(h.ID)
		if !ok {
			continue
		}
		rec := LinkRec{Norm: norm, Name: name, Cand: ent.ID, CandName: ent.Name, Cos: h.Score,
			Chunk: chunk, By: LinkByJudge, Source: LinkFromBuild}
		if o.Judge == nil {
			rec.Verdict, rec.Why = LinkDoubt, "арбитра нет"
			return 0, false, g.links.add(rec)
		}
		verdict, why, err := askLinkJudge(ctx, o.Judge, name, typ, ent, g.ents.DisplayAliases(ent))
		if err != nil {
			return 0, false, err
		}
		rec.Verdict, rec.Why = verdict, why
		switch verdict {
		case LinkYes:
			rec.To, rec.ToName = ent.ID, ent.Name
			return ent.ID, true, g.links.add(rec)
		case LinkNo:
			if err := g.links.add(rec); err != nil {
				return 0, false, err
			}
			continue
		default:
			return 0, false, g.links.add(rec)
		}
	}
	return 0, false, nil
}

// askLinkJudge спрашивает арбитра об одной паре.
func askLinkJudge(ctx context.Context, judge Extractor, name, typ string, ent Entity, aliases []string) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Новое название: %s (%s)\n", name, NormalizeType(typ))
	fmt.Fprintf(&b, "Есть в графе: %s (%s)", ent.Name, ent.Type)
	if len(aliases) > 0 {
		fmt.Fprintf(&b, ", он же: %s", strings.Join(aliases, ", "))
	}
	b.WriteString("\n")
	answer, err := judge.Extract(ctx, linkJudgePrompt, b.String())
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(answer), "\n")
	first := strings.ToUpper(strings.TrimSpace(lines[0]))
	first = strings.Trim(first, ".!,:;«»\"' ")
	why := ""
	if len(lines) > 1 {
		why = strings.TrimSpace(lines[1])
	}
	switch {
	case strings.HasPrefix(first, "ДА"):
		return LinkYes, why, nil
	case strings.HasPrefix(first, "НЕТ"):
		return LinkNo, why, nil
	}
	return LinkDoubt, why, nil
}

// DecideLink — решение человека по паре из очереди или из судейского режима.
//
// «ДА»: оба узла уже есть (узел нового имени заведён, пока пара ждала), поэтому
// это склейка — запись в merges.jsonl, обратимая как все склейки, плюс запись
// в журнал связываний, чтобы имя при следующих встречах шло к узлу без арбитра.
// «НЕТ»: только журнал — арбитра об этой паре больше не спрашивают, а если это
// отмена решения арбитра, дальнейшие встречи имени заведут свой узел; упоминания,
// уже отданные чужому узлу, назад не переносятся — об этом говорит вызывающий.
func (g *Graph) DecideLink(r LinkRec, verdict, why string) error {
	if g.links == nil {
		return fmt.Errorf("у графа нет журнала связываний")
	}
	other := r.To
	if other == 0 {
		other = r.Cand
	}
	dec := LinkRec{Norm: r.Norm, Name: r.Name, From: r.From, Cand: other, CandName: r.CandName,
		Cos: r.Cos, Verdict: verdict, By: LinkByHuman, Source: r.Source, Why: why, Chunk: r.Chunk}
	if dec.CandName == "" {
		dec.CandName = r.ToName
	}
	switch verdict {
	case LinkYes:
		from := r.From
		if from == 0 {
			if ent, ok := g.ents.Lookup(r.Name); ok {
				from = ent.ID
			}
		}
		if from != 0 && other != 0 && from != other {
			if _, err := g.merges.Add([]MergeRec{{From: from, To: other, Cos: r.Cos, Verdict: LinkYes,
				Why: "человек, /graph review: " + why, Level: "human"}}); err != nil {
				return err
			}
		}
		dec.From, dec.To, dec.ToName = from, other, dec.CandName
	case LinkNo:
	default:
		return fmt.Errorf("решение должно быть ДА или НЕТ, получено %q", verdict)
	}
	return g.links.add(dec)
}
