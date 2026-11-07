package probes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// dirTestGraph заводит настоящий граф во временном каталоге (тем же graph.Create,
// что и работа) и понятия по именам; номера идут с единицы в порядке имён.
func dirTestGraph(t *testing.T, rules graph.Rules, names ...string) (*graph.Graph, map[string]uint32) {
	t.Helper()
	g, err := graph.Create(t.TempDir(), "проба", 100, rules)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	id := map[string]uint32{}
	for _, n := range names {
		e, _, err := g.Entities().Add(n, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		id[n] = e
	}
	return g, id
}

// dirEdgeSpec — связь фикстуры: от кого, к кому, какого типа, в каком куске.
type dirEdgeSpec struct {
	src, dst string
	typ      uint8
	ord      uint32
}

// dirFixture — рабочий граф для проверки отбора и замера. Читать так:
//
//	каталог→реестр «часть» ×6 (куски 10–15), реестр→каталог «часть» ×1 (кусок 20),
//	«копия реестра» поглощена реестром и дала ещё реестр→каталог (кусок 21) и
//	петлю реестр→реестр (кусок 22) — петля после склейки отбрасывается;
//	очередь↔журнал «является» по разу в каждую сторону (куски 30 и 31);
//	индекс→страница «является» без встречной, страница→индекс «часть» — другой тип;
//	каталог↔очередь «использует» в обе стороны — не «является» и не «часть».
func dirFixture(t *testing.T) (*graph.Graph, map[string]uint32) {
	t.Helper()
	g, id := dirTestGraph(t, graph.Rules{}, "каталог", "реестр", "очередь", "журнал", "индекс", "страница", "копия реестра")
	specs := []dirEdgeSpec{
		{"каталог", "реестр", graph.RelPart, 10}, {"каталог", "реестр", graph.RelPart, 11},
		{"каталог", "реестр", graph.RelPart, 12}, {"каталог", "реестр", graph.RelPart, 13},
		{"каталог", "реестр", graph.RelPart, 14}, {"каталог", "реестр", graph.RelPart, 15},
		{"реестр", "каталог", graph.RelPart, 20},
		{"копия реестра", "каталог", graph.RelPart, 21},
		{"копия реестра", "реестр", graph.RelPart, 22},
		{"очередь", "журнал", graph.RelIs, 30}, {"журнал", "очередь", graph.RelIs, 31},
		{"индекс", "страница", graph.RelIs, 40}, {"страница", "индекс", graph.RelPart, 41},
		{"каталог", "очередь", graph.RelUses, 50}, {"очередь", "каталог", graph.RelUses, 51},
	}
	for _, s := range specs {
		if err := g.Edges().Add(graph.Edge{Src: id[s.src], Dst: id[s.dst], Type: s.typ, Weight: 1,
			Evidence: graph.ChunkKey{Doc: 1, Ord: s.ord}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Merges().Add([]graph.MergeRec{{To: id["реестр"], From: id["копия реестра"], Level: "mutual"}}); err != nil {
		t.Fatal(err)
	}
	return g, id
}

func keysOf(doc uint32, ords ...uint32) []graph.ChunkKey {
	out := make([]graph.ChunkKey, 0, len(ords))
	for _, o := range ords {
		out = append(out, graph.ChunkKey{Doc: doc, Ord: o})
	}
	return out
}

// TestCollectMutual — встречные пары берутся с живого графа: склейка применена,
// петля после неё отброшена, встречные другого типа и односторонние не в счёте,
// записи каждой стороны посчитаны, порядок — по (тип, A, B).
func TestCollectMutual(t *testing.T) {
	g, id := dirFixture(t)
	got := collectMutual(g)
	if len(got) != 2 {
		t.Fatalf("пар %d, ожидалось 2 (очередь↔журнал «является», каталог↔реестр «часть»): %+v", len(got), got)
	}
	// «является» (тип 1) идёт раньше «часть» (тип 2).
	first, second := got[0], got[1]
	if first.A != id["очередь"] || first.B != id["журнал"] || first.Type != graph.RelIs {
		t.Errorf("первая пара %+v, ожидалась очередь↔журнал «является»", first)
	}
	if !reflect.DeepEqual(first.AB, keysOf(1, 30)) || !reflect.DeepEqual(first.BA, keysOf(1, 31)) {
		t.Errorf("подтверждения первой пары AB=%v BA=%v, ожидались 1#30 и 1#31", first.AB, first.BA)
	}
	if second.A != id["каталог"] || second.B != id["реестр"] || second.Type != graph.RelPart {
		t.Errorf("вторая пара %+v, ожидалась каталог↔реестр «часть»", second)
	}
	// A→B — шесть записей в кусках 10–15; B→A — собственная (20) и унаследованная
	// от поглощённой копии (21); петля копия→реестр (22) не вошла.
	if len(second.AB) != 6 || len(second.BA) != 2 {
		t.Fatalf("записей AB=%d BA=%d, ожидалось 6 и 2", len(second.AB), len(second.BA))
	}
	haveBA := map[graph.ChunkKey]bool{}
	for _, k := range second.BA {
		haveBA[k] = true
	}
	if !haveBA[graph.ChunkKey{Doc: 1, Ord: 20}] || !haveBA[graph.ChunkKey{Doc: 1, Ord: 21}] || haveBA[graph.ChunkKey{Doc: 1, Ord: 22}] {
		t.Errorf("подтверждения B→A %v: ожидались 1#20 и 1#21 без петли 1#22", second.BA)
	}
	if first.tilt() != 0 {
		t.Errorf("перевес у пары 1:1 = %d, ожидался 0", first.tilt())
	}
	if second.tilt() != 1 {
		t.Errorf("перевес у пары 6:2 = %d, ожидалось +1 (6 ≥ 3×2)", second.tilt())
	}
}

// TestTilt — перевес: записей одной стороны не меньше чем в три раза больше.
func TestTilt(t *testing.T) {
	mk := func(ab, ba int) dirPair {
		return dirPair{AB: make([]graph.ChunkKey, ab), BA: make([]graph.ChunkKey, ba)}
	}
	cases := []struct {
		ab, ba, want int
	}{
		{6, 2, 1}, {3, 1, 1}, {1, 3, -1}, {1, 7, -1}, {2, 1, 0}, {2, 3, 0}, {1, 1, 0}, {5, 2, 0},
	}
	for _, c := range cases {
		if got := mk(c.ab, c.ba).tilt(); got != c.want {
			t.Errorf("tilt(%d:%d) = %d, ожидалось %d", c.ab, c.ba, got, c.want)
		}
	}
}

// TestSampleMutual — выборка детерминирована по зерну, не зависит от входного
// порядка, ноль даёт умолчание, отрицательное число — все пары.
func TestSampleMutual(t *testing.T) {
	var all []dirPair
	for i := 0; i < 100; i++ {
		typ := graph.RelIs
		if i%2 == 1 {
			typ = graph.RelPart
		}
		all = append(all, dirPair{A: uint32(i + 1), B: uint32(i + 1001), Type: typ})
	}
	reversed := make([]dirPair, len(all))
	for i, p := range all {
		reversed[len(all)-1-i] = p
	}

	const seed = 20261107
	def := sampleMutual(all, 0, seed)
	if len(def) != dirDefaultPairs {
		t.Fatalf("n=0 дал %d пар, ожидалось умолчание %d", len(def), dirDefaultPairs)
	}
	ten := sampleMutual(all, 10, seed)
	if !reflect.DeepEqual(ten, def[:10]) {
		t.Errorf("первые 10 при n=10 не совпали с началом выборки n=0: порядок перемешивания зависит от n")
	}
	if !reflect.DeepEqual(sampleMutual(all, 10, seed), ten) {
		t.Errorf("тот же вход и то же зерно дали другую выборку")
	}
	if !reflect.DeepEqual(sampleMutual(reversed, 10, seed), ten) {
		t.Errorf("порядок входа повлиял на выборку: список обязан сортироваться до перемешивания")
	}
	if reflect.DeepEqual(sampleMutual(all, 10, seed+1), ten) {
		t.Errorf("другое зерно дало ту же выборку из 10 пар по 100")
	}
	seen := map[[2]uint32]bool{}
	for _, p := range ten {
		k := [2]uint32{p.A, p.B}
		if seen[k] {
			t.Errorf("пара %v выбрана дважды", k)
		}
		seen[k] = true
	}
	every := sampleMutual(all, -1, seed)
	if len(every) != len(all) {
		t.Errorf("n=-1 дал %d пар, ожидались все %d", len(every), len(all))
	}
	if got := sampleMutual(all[:5], 60, seed); len(got) != 5 {
		t.Errorf("n больше списка дал %d пар, ожидалось 5", len(got))
	}
	// Входной срез не меняется выборкой.
	if all[0].A != 1 || all[99].A != 100 {
		t.Errorf("sampleMutual переставил входной список")
	}
}

// TestPairCases — до per разных кусков с каждой стороны по возрастанию ключа,
// повторы записей схлопываются, кусок первой стороны на второй не повторяется.
func TestPairCases(t *testing.T) {
	p := dirPair{
		AB: []graph.ChunkKey{{Doc: 2, Ord: 5}, {Doc: 1, Ord: 9}, {Doc: 1, Ord: 9}, {Doc: 1, Ord: 3}},
		BA: []graph.ChunkKey{{Doc: 1, Ord: 3}, {Doc: 3, Ord: 1}, {Doc: 1, Ord: 7}},
	}
	got := pairCases(4, p, 2)
	want := []dirCase{
		{Pair: 4, Key: graph.ChunkKey{Doc: 1, Ord: 3}, Side: sideAB},
		{Pair: 4, Key: graph.ChunkKey{Doc: 1, Ord: 9}, Side: sideAB},
		{Pair: 4, Key: graph.ChunkKey{Doc: 1, Ord: 7}, Side: sideBA}, // 1#3 уже взят с первой стороны
		{Pair: 4, Key: graph.ChunkKey{Doc: 3, Ord: 1}, Side: sideBA},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("куски пары:\n получено %+v\n ожидалось %+v", got, want)
	}
	// per < 0 — без предела: четыре разных на A→B ({1#3,1#9,2#5} — три) и два на B→A.
	if all := pairCases(0, p, -1); len(all) != 3+2 {
		t.Errorf("без предела вышло %d кусков, ожидалось 5 (три разных A→B и два B→A без 1#3)", len(all))
	}
}

// TestClassifyDirection — пять видов исхода.
func TestClassifyDirection(t *testing.T) {
	const a, b, c = 1, 2, 3
	is, part := graph.RelIs, graph.RelPart
	cases := []struct {
		name  string
		edges []dirEdge
		want  dirOutcome
	}{
		{"только A→B", []dirEdge{{a, b, part}}, outAB},
		{"только B→A", []dirEdge{{b, a, part}}, outBA},
		{"обе стороны", []dirEdge{{a, b, part}, {b, a, part}}, outBoth},
		{"другой тип туда", []dirEdge{{a, b, is}}, outOther},
		{"другой тип обратно", []dirEdge{{b, a, is}}, outOther},
		{"нужный тип важнее другого", []dirEdge{{a, b, is}, {b, a, part}}, outBA},
		{"связи с третьим не в счёт", []dirEdge{{a, c, part}, {c, b, part}}, outNone},
		{"пустой ответ", nil, outNone},
	}
	for _, tc := range cases {
		if got := classifyDirection(tc.edges, a, b, part); got != tc.want {
			t.Errorf("%s: %s, ожидалось %s", tc.name, got, tc.want)
		}
	}
}

// TestAnswerEdges — имена из ответа переводятся в номера путём сборки: поиск
// в реестре по написанию, перевод к выжившему склейки, конец связи берётся из
// понятий ответа; неизвестное имя и петля после склейки отпадают.
func TestAnswerEdges(t *testing.T) {
	g, id := dirFixture(t)
	f := graph.Facts{
		Entities: []graph.FactEntity{
			{Name: "Каталог", Type: graph.TypeConcept},
			{Name: "Копия реестра", Type: graph.TypeConcept}, // поглощена реестром
			{Name: "Реестр", Type: graph.TypeConcept},
			{Name: "Нигде не заведённое", Type: graph.TypeConcept},
		},
		Relations: []graph.FactRelation{
			{Src: "Каталог", Dst: "Копия реестра", Type: "часть"},
			{Src: "Копия реестра", Dst: "Каталог", Type: "part"},
			{Src: "Каталог", Dst: "Нигде не заведённое", Type: "является"},
			{Src: "Копия реестра", Dst: "Реестр", Type: "часть"}, // после склейки петля
			{Src: "Каталог", Dst: "Реестр", Type: "что-то своё"}, // тип не назван
			{Src: "Каталог", Dst: "Не перечисленное", Type: "часть"},
		},
	}
	got := answerEdges(g, f)
	want := []dirEdge{
		{id["каталог"], id["реестр"], graph.RelPart},
		{id["реестр"], id["каталог"], graph.RelPart},
		{id["каталог"], id["реестр"], graph.RelRelated}, // формат 1: незнакомый тип — «связано»
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("связи ответа:\n получено %+v\n ожидалось %+v", got, want)
	}
}

// TestAnswerEdgesStrict — у графа формата 2 связь без названного типа не
// пишется, как в сборке (RelTypeStrict).
func TestAnswerEdgesStrict(t *testing.T) {
	g, id := dirTestGraph(t, graph.Rules{Name: "lab", Format: graph.FormatV2}, "каталог", "реестр")
	f := graph.Facts{
		Entities: []graph.FactEntity{{Name: "каталог", Type: graph.TypeConcept}, {Name: "реестр", Type: graph.TypeConcept}},
		Relations: []graph.FactRelation{
			{Src: "каталог", Dst: "реестр", Type: "часть"},
			{Src: "реестр", Dst: "каталог", Type: "что-то своё"},
			{Src: "реестр", Dst: "каталог", Type: "связано"},
		},
	}
	got := answerEdges(g, f)
	want := []dirEdge{{id["каталог"], id["реестр"], graph.RelPart}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("связи ответа:\n получено %+v\n ожидалось %+v", got, want)
	}
}

// TestTallyRun — итог прогона по готовым исходам: сбой не исход, «обе» одним
// куском делает пару встречной, перевес считается только по A→B/B→A.
func TestTallyRun(t *testing.T) {
	pairs := []dirPair{
		{AB: make([]graph.ChunkKey, 6), BA: make([]graph.ChunkKey, 2)}, // перевес к A→B
		{AB: make([]graph.ChunkKey, 1), BA: make([]graph.ChunkKey, 4)}, // перевес к B→A
		{AB: make([]graph.ChunkKey, 2), BA: make([]graph.ChunkKey, 2)}, // без перевеса
		{AB: make([]graph.ChunkKey, 1), BA: make([]graph.ChunkKey, 1)}, // ни одного ответа
	}
	cases := []dirCase{
		{Pair: 0}, {Pair: 0}, {Pair: 0}, // пара 0: A→B, B→A, обе
		{Pair: 1}, {Pair: 1}, // пара 1: B→A, другой тип
		{Pair: 2}, // пара 2: обе
		{Pair: 3}, // пара 3: сбой
	}
	outs := [][dirRuns]dirOutcome{
		{outAB}, {outBA}, {outBoth},
		{outBA}, {outOther},
		{outBoth},
		{outFail},
	}
	got := tallyRun(pairs, cases, outs, 0)
	want := dirTally{
		Cases: 6, Failed: 1,
		Outcomes:      [outCounted]int{outAB: 1, outBA: 2, outBoth: 2, outOther: 1},
		PairsAnswered: 3,
		// Пара 0 увидела A→B, B→A и «обе»; пара 1 — только B→A; пара 2 — «обе».
		PairsMutual: 2,
		// Перевес у пар 0 и 1 (обе с ответами). В сторону перевеса: пара 0 — A→B (1),
		// пара 1 — B→A (1). Против: пара 0 — B→A (1). «Обе» и «другой тип» в долю не входят.
		TiltPairs: 2, TiltToward: 2, TiltAgainst: 1,
	}
	if got != want {
		t.Errorf("итог:\n получено %+v\n ожидалось %+v", got, want)
	}
}

// fakeExtractor отвечает по тексту куска и по тому, какой промпт пришёл: так
// видно, что старый уходит дважды, а кандидат — раз и в свой прогон.
//
// «Старым» считается graph.SystemPrompt, а при заданном base — он: подмена
// старого промпта (-base-file) не должна пропускать рабочий ни разу, поэтому
// запросы с graph.SystemPrompt при base считаются отдельно в defaultCalls.
type fakeExtractor struct {
	candidate, base string
	oldCalls        atomic.Int64 // запросов промптом «старого» прогона
	candCalls       atomic.Int64
	defaultCalls    atomic.Int64 // запросов именно graph.SystemPrompt
	// delay — сколько «думает» модель над промптом; nil — без задержки.
	delay  func(cand bool) time.Duration
	answer func(cand bool, user string) (string, error)
}

func (f *fakeExtractor) Model() string { return "фиктивная" }

func (f *fakeExtractor) Extract(_ context.Context, system, user string) (string, error) {
	cand := false
	switch {
	case system == f.candidate:
		cand = true
		f.candCalls.Add(1)
	case f.base != "" && system == f.base:
		f.oldCalls.Add(1)
	case system == graph.SystemPrompt:
		f.defaultCalls.Add(1)
		if f.base == "" {
			f.oldCalls.Add(1)
		}
	default:
		return "", fmt.Errorf("пришёл незнакомый системный промпт")
	}
	if f.delay != nil {
		time.Sleep(f.delay(cand))
	}
	return f.answer(cand, user)
}

func factsJSON(t *testing.T, ents []string, rels ...[3]string) string {
	t.Helper()
	var f graph.Facts
	for _, e := range ents {
		f.Entities = append(f.Entities, graph.FactEntity{Name: e, Type: graph.TypeConcept})
	}
	for _, r := range rels {
		f.Relations = append(f.Relations, graph.FactRelation{Src: r[0], Dst: r[1], Type: r[2]})
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// directionScenario — ответы фиктивной модели по номеру куска (ord) для
// фикстуры dirFixture: у старого промпта и у кандидата свои.
func directionScenario(t *testing.T) (old, cand map[uint32]string) {
	t.Helper()
	cr := []string{"каталог", "реестр"}
	qj := []string{"очередь", "журнал"}
	ab := [3]string{"каталог", "реестр", "часть"}
	ba := [3]string{"реестр", "каталог", "часть"}
	old = map[uint32]string{
		10: factsJSON(t, cr, ab),     // A→B
		11: factsJSON(t, cr, ab),     // A→B
		20: factsJSON(t, cr, ba),     // B→A
		21: factsJSON(t, cr, ab, ba), // обе
		30: factsJSON(t, qj, [3]string{"очередь", "журнал", "является"}),
		31: factsJSON(t, qj, [3]string{"журнал", "очередь", "использует"}), // другой тип
	}
	cand = map[uint32]string{
		10: factsJSON(t, cr, ab),
		// 11 — сбой запроса (см. тест)
		20: factsJSON(t, cr, ab),
		21: factsJSON(t, cr, ab),
		30: factsJSON(t, qj, [3]string{"очередь", "журнал", "является"}),
		31: factsJSON(t, []string{"журнал", "Неизвестное"}), // связи нет
	}
	return old, cand
}

func dirTestChunks(missing ...uint32) func(graph.ChunkKey) (poolChunk, bool) {
	skip := map[uint32]bool{}
	for _, o := range missing {
		skip[o] = true
	}
	return func(k graph.ChunkKey) (poolChunk, bool) {
		if k.Doc != 1 || skip[k.Ord] {
			return poolChunk{}, false
		}
		return poolChunk{Key: k, Title: "Книга", Unit: "глава", From: 1, To: 1,
			Text: fmt.Sprintf("фрагмент %d: каталог, реестр, очередь, журнал", k.Ord)}, true
	}
}

// TestMeasureDirection — сквозной прогон тем же measureDirection, что и работа,
// на настоящем графе-фикстуре и фиктивном извлекателе. Ожидания считаются от
// фикстуры и сценария ответов (см. комментарии у directionScenario и ниже).
func TestMeasureDirection(t *testing.T) {
	g, _ := dirFixture(t)
	old, cand := directionScenario(t)
	const candidate = "ПРОМПТ-КАНДИДАТ"
	ex := &fakeExtractor{candidate: candidate}
	ex.answer = func(isCand bool, user string) (string, error) {
		src := old
		if isCand {
			src = cand
		}
		for ord, ans := range src {
			if strings.Contains(user, fmt.Sprintf("фрагмент %d:", ord)) {
				return ans, nil
			}
		}
		return "", errors.New("сбой фиктивной модели")
	}

	var out, tsv bytes.Buffer
	err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex, directionOpts{
		Pairs: -1, Per: 2, Seed: 7, Workers: 3, Candidate: candidate, TSV: &tsv,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()

	// Кусков: пара каталог↔реестр — 10, 11 (A→B) и 20, 21 (B→A); пара очередь↔журнал —
	// 30 и 31; шесть разных, по три запроса: старый ×2 и кандидат ×1.
	if n := ex.oldCalls.Load(); n != 12 {
		t.Errorf("запросов старым промптом %d, ожидалось 12 (6 кусков × 2)", n)
	}
	if n := ex.candCalls.Load(); n != 6 {
		t.Errorf("запросов кандидатом %d, ожидалось 6 (6 кусков × 1)", n)
	}
	for _, line := range []string{
		"встречных пар в графе: является 1, часть 1",
		"выбрано пар: 2 (зерно 7): является 1, часть 1",
		"кусков-доказательств: 6 (разных кусков 6, нет в коллекции 0)",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("в выводе нет строки %q:\n%s", line, got)
		}
	}
	// Без -rel и -base-file в шапке нет ни типа, ни подмены, а рабочий промпт идёт.
	for _, line := range []string{"тип связи:", "старый промпт подменён"} {
		if strings.Contains(got, line) {
			t.Errorf("без -rel и -base-file в выводе есть %q:\n%s", line, got)
		}
	}
	if n := ex.defaultCalls.Load(); n != 12 {
		t.Errorf("запросов graph.SystemPrompt %d, ожидалось 12", n)
	}

	// Старый промпт, оба прогона одинаковы: исходы по кускам 10 A→B, 11 A→B, 20 B→A,
	// 21 обе, 30 A→B, 31 другой тип. Пара каталог↔реестр встречная (10 и 20); у пары
	// очередь↔журнал B→A нет. Перевес только у каталог↔реестр (6:2): к нему 10 и 11
	// против него 20 (кусок 21 «обе» в долю не входит).
	oldWant := dirTally{
		Cases: 6, Outcomes: [outCounted]int{outAB: 3, outBA: 1, outBoth: 1, outOther: 1},
		PairsAnswered: 2, PairsMutual: 1, TiltPairs: 1, TiltToward: 2, TiltAgainst: 1,
	}
	// Кандидат: кусок 11 — сбой; 10, 20, 21, 30 — A→B, 31 — нет. Встречных не осталось;
	// по перевесу — все три исхода пары каталог↔реестр к перевесу.
	candWant := dirTally{
		Cases: 5, Failed: 1, Outcomes: [outCounted]int{outAB: 4, outNone: 1},
		PairsAnswered: 2, PairsMutual: 0, TiltPairs: 1, TiltToward: 3, TiltAgainst: 0,
	}
	for r, want := range []dirTally{oldWant, oldWant, candWant} {
		var block bytes.Buffer
		printDirTally(&block, dirRunNames[r], want)
		if !strings.Contains(got, block.String()) {
			t.Errorf("в выводе нет блока прогона %s:\n%s\nвесь вывод:\n%s", dirRunNames[r], block.String(), got)
		}
	}

	// Жаккар: куски, где ответили все три прогона, — 10, 20, 21, 30, 31 (на 11 кандидат
	// упал). Старый-1 против старый-2 — одинаковые ответы, 1. Старый против кандидата: на
	// четырёх кусках имена те же (1), на 31 {очередь, журнал} против {журнал, неизвестное}
	// — 1/3; среднее (4 + 1/3) / 5 = 0.867.
	for _, line := range []string{
		"кусков с ответами всех трёх прогонов: 5",
		"Жаккар имён понятий, старый-1 против старый-2: 1.000",
		"Жаккар имён понятий, старый-1 против кандидат: 0.867",
		"первый сбой запроса или разбора: ",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("в выводе нет строки %q:\n%s", line, got)
		}
	}

	rows := strings.Split(strings.TrimRight(tsv.String(), "\n"), "\n")
	if len(rows) != 1+6*dirRuns {
		t.Errorf("строк TSV %d, ожидалось заголовок и 6 кусков × 3 прогона = %d", len(rows), 1+6*dirRuns)
	}
	if rows[0] != "кусок\tA\tB\tтип\tсторона\tпрогон\tисход" {
		t.Errorf("заголовок TSV %q", rows[0])
	}
	for _, row := range []string{
		"1#21\tкаталог\tреестр\tчасть\tB→A\tстарый-1\tобе",
		"1#21\tкаталог\tреестр\tчасть\tB→A\tкандидат\tA→B",
		"1#11\tкаталог\tреестр\tчасть\tA→B\tкандидат\tсбой",
		"1#31\tочередь\tжурнал\tявляется\tB→A\tстарый-2\tдругой тип",
	} {
		found := false
		for _, r := range rows {
			if r == row {
				found = true
			}
		}
		if !found {
			t.Errorf("в TSV нет строки %q:\n%s", row, tsv.String())
		}
	}
}

// TestMeasureDirectionMissingChunk — куска нет в коллекции: он не опрашивается и
// называется в итоге; остальные считаются.
func TestMeasureDirectionMissingChunk(t *testing.T) {
	g, _ := dirFixture(t)
	old, cand := directionScenario(t)
	const candidate = "ПРОМПТ-КАНДИДАТ"
	ex := &fakeExtractor{candidate: candidate}
	ex.answer = func(isCand bool, user string) (string, error) {
		src := old
		if isCand {
			src = cand
		}
		for ord, ans := range src {
			if strings.Contains(user, fmt.Sprintf("фрагмент %d:", ord)) {
				return ans, nil
			}
		}
		return "", errors.New("сбой фиктивной модели")
	}
	var out bytes.Buffer
	err := measureDirection(context.Background(), &out, g, dirTestChunks(20), ex, directionOpts{
		Pairs: -1, Seed: 7, Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "кусков-доказательств: 5 (разных кусков 5, нет в коллекции 1)"; !strings.Contains(out.String(), want) {
		t.Errorf("в выводе нет %q:\n%s", want, out.String())
	}
	if n := ex.oldCalls.Load(); n != 10 {
		t.Errorf("запросов старым промптом %d, ожидалось 10 (5 кусков × 2)", n)
	}
}

// TestMeasureDirectionRefuses — пустой кандидат и граф без встречных пар — ошибка
// до всяких запросов.
func TestMeasureDirectionRefuses(t *testing.T) {
	g, _ := dirFixture(t)
	ex := &fakeExtractor{candidate: "x", answer: func(bool, string) (string, error) { return "", errors.New("не должно звать") }}
	var out bytes.Buffer
	if err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex, directionOpts{Candidate: "  \n"}); err == nil {
		t.Error("пустой кандидат принят")
	}
	empty, _ := dirTestGraph(t, graph.Rules{}, "каталог", "реестр")
	err := measureDirection(context.Background(), &out, empty, dirTestChunks(), ex, directionOpts{Candidate: "x"})
	if err == nil || !strings.Contains(err.Error(), "нет встречных пар") {
		t.Errorf("граф без встречных пар: ошибка %v, ожидалась про отсутствие пар", err)
	}
	if n := ex.oldCalls.Load() + ex.candCalls.Load(); n != 0 {
		t.Errorf("до отказа ушло %d запросов", n)
	}
}

// TestRunDirectionKeys — ключи проверяются до открытия коллекции (cfg == nil
// упал бы паникой): -url обязателен без запасного, кандидат обязателен и не пуст.
func TestRunDirectionKeys(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("промпт"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"без -url", []string{"-only", "direction", "-prompt-file", empty}, "-url"},
		{"без -prompt-file", []string{"-only", "direction", "-url", "http://llama.example:8080"}, "-prompt-file"},
		{"нет файла", []string{"-only", "direction", "-url", "http://llama.example:8080", "-prompt-file", filepath.Join(dir, "нет.txt")}, "промпт-кандидат"},
		{"пустой файл", []string{"-only", "direction", "-url", "http://llama.example:8080", "-prompt-file", empty}, "пуст"},
		{"неизвестный -rel", []string{"-only", "direction", "-url", "http://llama.example:8080", "-prompt-file", good, "-rel", "использует"}, "-rel"},
		{"нет файла -base-file", []string{"-only", "direction", "-url", "http://llama.example:8080", "-prompt-file", good, "-base-file", filepath.Join(dir, "нет.txt")}, "базовый промпт"},
		{"пустой -base-file", []string{"-only", "direction", "-url", "http://llama.example:8080", "-prompt-file", good, "-base-file", empty}, "пуст"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		err := Run(&buf, nil, "books", c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ошибка %v, ожидалось упоминание %q", c.name, err, c.want)
		}
	}
}

// TestParseRel — значение -rel: пусто — оба типа, иначе «является» или «часть».
func TestParseRel(t *testing.T) {
	for in, want := range map[string]uint8{"": 0, "является": graph.RelIs, "часть": graph.RelPart, " часть ": graph.RelPart} {
		got, err := parseRel(in)
		if err != nil || got != want {
			t.Errorf("parseRel(%q) = %d, %v; ожидалось %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"использует", "is", "связано", "bogus"} {
		if _, err := parseRel(bad); err == nil {
			t.Errorf("parseRel(%q) принят, ожидалась ошибка", bad)
		}
	}
}

// TestMeanSeconds — среднее время считается по запросам с ответом, сбои не входят.
func TestMeanSeconds(t *testing.T) {
	answers := [][]poolAnswer{
		{{OK: true, Dur: 1 * time.Second}, {Dur: 50 * time.Second}},
		{{OK: true, Dur: 3 * time.Second}, {Dur: 70 * time.Second}},
		{{Dur: 100 * time.Second}, {Dur: 90 * time.Second}},
	}
	// Прогон 0: ответы 1 с и 3 с (третий сбой, его 100 с не в счёт) — среднее 2 с по двум.
	if got, n := meanSeconds(answers, 0); got != 2 || n != 2 {
		t.Errorf("прогон 0: %v с по %d запросам, ожидалось 2 с по 2", got, n)
	}
	// Прогон 1: ни одного ответа.
	if got, n := meanSeconds(answers, 1); got != 0 || n != 0 {
		t.Errorf("прогон 1: %v с по %d запросам, ожидалось 0 по 0", got, n)
	}
}

// scenarioAnswer — ответ фиктивной модели по сценарию directionScenario.
func scenarioAnswer(old, cand map[uint32]string) func(bool, string) (string, error) {
	return func(isCand bool, user string) (string, error) {
		src := old
		if isCand {
			src = cand
		}
		for ord, ans := range src {
			if strings.Contains(user, fmt.Sprintf("фрагмент %d:", ord)) {
				return ans, nil
			}
		}
		return "", errors.New("сбой фиктивной модели")
	}
}

// secondsLines — числа из строк «среднее время запроса: X с» по порядку вывода.
func secondsLines(t *testing.T, out string) []float64 {
	t.Helper()
	var vals []float64
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "среднее время запроса:") {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "среднее время запроса: %f с", &v); err != nil {
			t.Fatalf("строка времени %q не разобралась: %v", line, err)
		}
		vals = append(vals, v)
	}
	return vals
}

// TestMeasureDirectionRel — -rel оставляет в выборке пары одного типа; строки
// о числе пар в графе не меняются, в шапке названо, какой тип взят.
func TestMeasureDirectionRel(t *testing.T) {
	g, _ := dirFixture(t)
	old, cand := directionScenario(t)
	const candidate = "ПРОМПТ-КАНДИДАТ"
	cases := []struct {
		rel        uint8
		head       string
		selected   string
		oldN, cndN int64
		notInTSV   string
	}{
		// Пара каталог↔реестр «часть»: куски 10, 11, 20, 21 — четыре куска.
		{graph.RelPart, "тип связи: только «часть» (-rel)", "выбрано пар: 1 (зерно 7): является 0, часть 1", 8, 4, "\tявляется\t"},
		// Пара очередь↔журнал «является»: куски 30 и 31.
		{graph.RelIs, "тип связи: только «является» (-rel)", "выбрано пар: 1 (зерно 7): является 1, часть 0", 4, 2, "\tчасть\t"},
	}
	for _, c := range cases {
		ex := &fakeExtractor{candidate: candidate, answer: scenarioAnswer(old, cand)}
		var out, tsv bytes.Buffer
		err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex, directionOpts{
			Pairs: -1, Seed: 7, Workers: 2, Candidate: candidate, Rel: c.rel, TSV: &tsv,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{c.head, c.selected, "встречных пар в графе: является 1, часть 1"} {
			if !strings.Contains(out.String(), line) {
				t.Errorf("-rel %s: в выводе нет %q:\n%s", graph.RelName(c.rel), line, out.String())
			}
		}
		if n := ex.oldCalls.Load(); n != c.oldN {
			t.Errorf("-rel %s: запросов старым %d, ожидалось %d", graph.RelName(c.rel), n, c.oldN)
		}
		if n := ex.candCalls.Load(); n != c.cndN {
			t.Errorf("-rel %s: запросов кандидатом %d, ожидалось %d", graph.RelName(c.rel), n, c.cndN)
		}
		if strings.Contains(tsv.String(), c.notInTSV) {
			t.Errorf("-rel %s: в TSV строки другого типа:\n%s", graph.RelName(c.rel), tsv.String())
		}
	}
}

// TestMeasureDirectionRelNoPairs — в графе есть встречные пары, но не того типа,
// что просили: ошибка, а не пустой замер.
func TestMeasureDirectionRelNoPairs(t *testing.T) {
	g, id := dirTestGraph(t, graph.Rules{}, "каталог", "реестр")
	for i, e := range [][2]string{{"каталог", "реестр"}, {"реестр", "каталог"}} {
		if err := g.Edges().Add(graph.Edge{Src: id[e[0]], Dst: id[e[1]], Type: graph.RelIs, Weight: 1,
			Evidence: graph.ChunkKey{Doc: 1, Ord: uint32(i + 1)}}); err != nil {
			t.Fatal(err)
		}
	}
	ex := &fakeExtractor{candidate: "x", answer: func(bool, string) (string, error) { return "", errors.New("не должно звать") }}
	var out bytes.Buffer
	err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex, directionOpts{Candidate: "x", Rel: graph.RelPart})
	if err == nil || !strings.Contains(err.Error(), "нет встречных пар типа «часть»") {
		t.Errorf("ошибка %v, ожидалась про отсутствие пар типа «часть»", err)
	}
	if n := ex.oldCalls.Load() + ex.candCalls.Load(); n != 0 {
		t.Errorf("до отказа ушло %d запросов", n)
	}
	// Тот же граф, но -rel является — замер идёт.
	ex2 := &fakeExtractor{candidate: "x", answer: func(bool, string) (string, error) { return "{}", nil }}
	if err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex2, directionOpts{Candidate: "x", Rel: graph.RelIs}); err != nil {
		t.Errorf("-rel является на графе с такой парой: %v", err)
	}
}

// TestMeasureDirectionBaseFile — при подмене старого промпта рабочий
// graph.SystemPrompt не уходит ни разу: оба «старых» прогона идут базовым.
func TestMeasureDirectionBaseFile(t *testing.T) {
	g, _ := dirFixture(t)
	old, cand := directionScenario(t)
	const candidate, base = "ПРОМПТ-КАНДИДАТ", "БАЗОВЫЙ ПРОМПТ"
	ex := &fakeExtractor{candidate: candidate, base: base, answer: scenarioAnswer(old, cand)}
	var out bytes.Buffer
	err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex, directionOpts{
		Pairs: -1, Seed: 7, Workers: 3, Candidate: candidate, Base: base, BaseName: "base.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := ex.defaultCalls.Load(); n != 0 {
		t.Errorf("graph.SystemPrompt ушёл %d раз при -base-file, ожидалось ни разу", n)
	}
	if n := ex.oldCalls.Load(); n != 12 {
		t.Errorf("запросов базовым промптом %d, ожидалось 12 (6 кусков × 2)", n)
	}
	if n := ex.candCalls.Load(); n != 6 {
		t.Errorf("запросов кандидатом %d, ожидалось 6", n)
	}
	want := fmt.Sprintf("старый промпт подменён (-base-file): base.txt, %d знаков", len([]rune(base)))
	if !strings.Contains(out.String(), want) {
		t.Errorf("в шапке нет %q:\n%s", want, out.String())
	}
	// Сценарий старого прогона не менялся — исходы те же, что в TestMeasureDirection.
	if !strings.Contains(out.String(), "прогон старый-1\n  кусков-прогонов: 6\n") {
		t.Errorf("прогон «старый-1» не отработал:\n%s", out.String())
	}

	// Пустая подмена — отказ до запросов.
	ex2 := &fakeExtractor{candidate: candidate, answer: scenarioAnswer(old, cand)}
	err = measureDirection(context.Background(), &out, g, dirTestChunks(), ex2, directionOpts{Candidate: candidate, Base: " \n"})
	if err == nil || !strings.Contains(err.Error(), "базовый промпт") {
		t.Errorf("пустая подмена: ошибка %v, ожидалась про базовый промпт", err)
	}
}

// TestMeasureDirectionSeconds — по каждому прогону печатается среднее время
// запроса; медленный промпт-кандидат виден как большее число.
func TestMeasureDirectionSeconds(t *testing.T) {
	g, _ := dirFixture(t)
	old, cand := directionScenario(t)
	const candidate = "ПРОМПТ-КАНДИДАТ"
	const fast, slow = 10 * time.Millisecond, 80 * time.Millisecond
	ex := &fakeExtractor{candidate: candidate, answer: scenarioAnswer(old, cand),
		delay: func(isCand bool) time.Duration {
			if isCand {
				return slow
			}
			return fast
		}}
	var out bytes.Buffer
	err := measureDirection(context.Background(), &out, g, dirTestChunks(), ex, directionOpts{
		Pairs: -1, Seed: 7, Workers: 3, Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	v := secondsLines(t, out.String())
	if len(v) != dirRuns {
		t.Fatalf("строк со временем %d, ожидалось по одной на прогон (%d):\n%s", len(v), dirRuns, out.String())
	}
	// Запрос не может быть короче своей задержки; печатается с двумя знаками.
	if v[0] < fast.Seconds()-0.005 || v[1] < fast.Seconds()-0.005 {
		t.Errorf("старый: %v и %v с, не меньше %v ожидалось", v[0], v[1], fast)
	}
	if v[2] < slow.Seconds()-0.005 {
		t.Errorf("кандидат: %v с, не меньше %v ожидалось", v[2], slow)
	}
	if !(v[2] > v[0] && v[2] > v[1]) {
		t.Errorf("медленный кандидат не вышел медленнее старого: %v", v)
	}
	// Кусок 11 у кандидата — сбой: по времени он не считается, остальные пять — да.
	if want := "среднее время запроса: " + fmt.Sprintf("%.2f", v[2]) + " с (по 5 запросам с ответом)"; !strings.Contains(out.String(), want) {
		t.Errorf("в выводе нет %q:\n%s", want, out.String())
	}
}
