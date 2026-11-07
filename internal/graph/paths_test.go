package graph

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Несколько цепочек на пару (этап 126, Я2): graph.Paths.

// pathNodes — узлы цепочки по порядку: начало и конец каждого шага.
func pathNodes(steps []PathStep) []string {
	if len(steps) == 0 {
		return nil
	}
	out := []string{steps[0].From}
	for _, s := range steps {
		out = append(out, s.To)
	}
	return out
}

// edgeAdder — связь по номерам с уникальным подтверждением на каждую.
func edgeAdder(t *testing.T, g *Graph) func(src, dst uint32, typ uint8) {
	t.Helper()
	ord := uint32(0)
	return func(src, dst uint32, typ uint8) {
		ord++
		if err := g.Edges().Add(Edge{Src: src, Dst: dst, Type: typ, Weight: 1,
			Evidence: ChunkKey{Doc: 1, Ord: ord}}); err != nil {
			t.Fatal(err)
		}
	}
}

func addConcept(t *testing.T, g *Graph, name string) uint32 {
	t.Helper()
	id, _, err := g.Entities().Add(name, TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Первая цепочка — ровно ответ Path в обоих режимах и при любом пределе;
// k = 0 и k = 1 дают ровно Path и ничего больше.
func TestPathsFirstIsPathInBothModes(t *testing.T) {
	// Тот же граф, что в TestPathByFlowPrefersSpecificIntermediates: по шагам
	// цепочка идёт через «общее», по потоку — через «узкое1 → узкое2».
	g := newGraphWith(t, "A", "общее", "B", "узкое1", "узкое2")
	defer g.Close()
	const a, common, b, y, z = 1, 2, 3, 4, 5
	add := edgeAdder(t, g)
	add(a, common, RelRelated)
	add(common, b, RelRelated)
	for i := 0; i < 30; i++ {
		add(common, addConcept(t, g, fmt.Sprintf("прочее%d", i)), RelRelated)
	}
	add(a, y, RelRelated)
	add(y, z, RelUses)
	add(z, b, RelPart)

	firsts := map[bool][]PathStep{}
	for _, flow := range []bool{false, true} {
		g.rules.PathFlow = flow
		for _, hops := range []int{0, 2, 3, 4} {
			want, wantOK := g.Path("A", "B", hops)
			if hops == 4 {
				firsts[flow] = want
			}
			for _, k := range []int{0, 1, 2, 3, 5} {
				got, _, ok := g.Paths("A", "B", hops, k)
				if ok != wantOK {
					t.Fatalf("поток=%v предел=%d k=%d: ok=%v, у Path %v", flow, hops, k, ok, wantOK)
				}
				if len(got) == 0 || !reflect.DeepEqual(got[0], want) {
					t.Fatalf("поток=%v предел=%d k=%d: первая цепочка %+v, Path даёт %+v", flow, hops, k, got, want)
				}
				if k <= 1 && len(got) != 1 {
					t.Fatalf("поток=%v предел=%d k=%d: ожидалась одна цепочка, получено %d", flow, hops, k, len(got))
				}
				if len(got) > max(k, 1) {
					t.Fatalf("поток=%v предел=%d k=%d: цепочек %d больше k", flow, hops, k, len(got))
				}
			}
		}
	}
	// Иначе тест не различает режимы и проверяет одно и то же дважды.
	if reflect.DeepEqual(firsts[false], firsts[true]) {
		t.Fatalf("режимы дали одну и ту же цепочку %+v — граф теста их не различает", firsts[false])
	}
	// По шагам вторая цепочка — обход через узкие понятия, на шаг длиннее.
	g.rules.PathFlow = false
	got, _, _ := g.Paths("A", "B", 4, 3)
	if len(got) != 2 || strings.Join(pathNodes(got[1]), " ") != "A узкое1 узкое2 B" {
		t.Fatalf("ожидались две цепочки, вторая A → узкое1 → узкое2 → B; получено %+v", got)
	}
}

// Вторая цепочка той же длины находится; внутри длины первыми идут пути
// с более узкими серединами; путь через ту же середину с другими типами
// связей — один путь; путь на шаг длиннее — после всех коротких.
func TestPathsSameLengthNarrowMiddlesFirst(t *testing.T) {
	g := newGraphWith(t, "A", "B", "середина1", "середина2", "середина3", "икс", "игрек")
	defer g.Close()
	const a, b, m1, m2, m3, x, yy = 1, 2, 3, 4, 5, 6, 7
	add := edgeAdder(t, g)
	for _, m := range []uint32{m1, m2, m3} {
		add(a, m, RelRelated)
		add(m, b, RelRelated)
	}
	// Ширина середин: у первой ещё три соседа, у второй два, у третьей нет.
	for i := 0; i < 3; i++ {
		add(m1, addConcept(t, g, fmt.Sprintf("лист1-%d", i)), RelRelated)
	}
	for i := 0; i < 2; i++ {
		add(m2, addConcept(t, g, fmt.Sprintf("лист2-%d", i)), RelRelated)
	}
	// Через третью — ещё связи других типов: путь от этого не размножается.
	add(a, m3, RelPart)
	add(b, m3, RelUses)
	// Путь на шаг длиннее.
	add(a, x, RelRelated)
	add(x, yy, RelRelated)
	add(yy, b, RelRelated)

	deg := func(id uint32) int { return len(g.Edges().Neighbors(id)) }
	if !(deg(m3) < deg(m2) && deg(m2) < deg(m1)) {
		t.Fatalf("ширины середин не те, что задуманы: %d, %d, %d", deg(m1), deg(m2), deg(m3))
	}

	first, _ := g.Path("A", "B", 3)
	got, cut, ok := g.Paths("A", "B", 3, 10)
	if !ok || cut {
		t.Fatalf("ok=%v cut=%v", ok, cut)
	}
	var lines []string
	for _, p := range got {
		lines = append(lines, strings.Join(pathNodes(p), " "))
	}
	t.Logf("цепочки: %q", lines)
	if !reflect.DeepEqual(got[0], first) {
		t.Fatalf("первая цепочка %+v, Path даёт %+v", got[0], first)
	}
	// Ожидаемый порядок остальных — из ширины середин, а не из того, какую
	// выбрал Path: та, что уже первая, не повторяется.
	byWidth := []string{"A середина3 B", "A середина2 B", "A середина1 B"}
	want := []string{lines[0]}
	for _, l := range byWidth {
		if l != lines[0] {
			want = append(want, l)
		}
	}
	want = append(want, "A икс игрек B")
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("цепочки %q, ожидались %q", lines, want)
	}
	if len(got[1]) != len(got[0]) {
		t.Fatalf("вторая цепочка длиной %d, первая %d — ожидалась той же длины", len(got[1]), len(got[0]))
	}

	// k = 2 — первая и лучшая из остальных.
	got2, _, _ := g.Paths("A", "B", 3, 2)
	if len(got2) != 2 || strings.Join(pathNodes(got2[1]), " ") != want[1] {
		t.Fatalf("k=2: получено %+v, вторая ожидалась %q", got2, want[1])
	}
}

// Хаб в середине не проходит и во второй, третьей цепочке — тем же порогом
// и той же мерой, что у Path (пары {сосед, направление}), в обоих режимах.
func TestPathsNeverThroughHubMiddle(t *testing.T) {
	g := newGraphWith(t, "A", "серединка", "B", "икс", "игрек")
	defer g.Close()
	const a, mid, b, x, yy = 1, 2, 3, 4, 5
	add := edgeAdder(t, g)
	both := func(p, q uint32) { add(p, q, RelRelated); add(q, p, RelRelated) }
	both(a, mid)
	both(mid, b)
	for i := 0; i < 2; i++ {
		both(mid, addConcept(t, g, fmt.Sprintf("прочее%d", i)))
	}
	add(a, x, RelRelated)
	add(x, yy, RelRelated)
	add(yy, b, RelRelated)

	uniq := map[uint32]bool{}
	for _, n := range g.Edges().Neighbors(mid) {
		uniq[n.ID] = true
	}
	pairs := len(g.Edges().Neighbors(mid))
	limit := len(uniq) + 1 // хаб по парам, не хаб по уникальным соседям
	if pairs < limit {
		t.Fatalf("меры совпали (%d и %d) — тест не проверяет меру", len(uniq), pairs)
	}

	for _, flow := range []bool{false, true} {
		g.rules.PathFlow = flow
		g.rules.ChainHubLimit = limit
		got, _, ok := g.Paths("A", "B", 3, 5)
		if !ok || len(got) == 0 {
			t.Fatalf("поток=%v: обходная цепочка обязана найтись", flow)
		}
		for _, p := range got {
			for _, s := range p {
				if s.From == "серединка" || s.To == "серединка" {
					t.Fatalf("поток=%v: цепочка через хаб-середину: %+v", flow, p)
				}
			}
		}
		// Запрет снят — путь через серединку появляется: значит, выше его
		// не пустил именно запрет, а не отсутствие связей.
		g.rules.ChainHubLimit = -1
		got, _, _ = g.Paths("A", "B", 3, 5)
		through := false
		for _, p := range got {
			if strings.Join(pathNodes(p), " ") == "A серединка B" {
				through = true
			}
		}
		if !through {
			t.Fatalf("поток=%v: без запрета ожидался путь через серединку, получено %+v", flow, got)
		}
	}
}

// Цепочки простые (узел не повторяется), разные, по длине не убывают и не
// выходят из окна «длина первой … на шаг длиннее».
func TestPathsSimpleDistinctAndWindowed(t *testing.T) {
	// Полный граф на шести узлах без прямой связи A–B: кругов сколько угодно.
	g := newGraphWith(t, "A", "B", "c1", "c2", "c3", "c4")
	defer g.Close()
	add := edgeAdder(t, g)
	for i := uint32(1); i <= 6; i++ {
		for j := i + 1; j <= 6; j++ {
			if i == 1 && j == 2 {
				continue
			}
			add(i, j, RelRelated)
		}
	}
	// Путей длины 2 — по одному через каждую из 4 середин; длины 3 —
	// упорядоченные пары разных середин, 4×3. Длины 4 в окно не входят:
	// первая цепочка длиной 2, окно — до 3.
	const mids = 4
	wantCount := mids + mids*(mids-1)
	for _, hops := range []int{3, 4} {
		got, cut, ok := g.Paths("A", "B", hops, 100)
		if !ok || cut {
			t.Fatalf("предел %d: ok=%v cut=%v", hops, ok, cut)
		}
		if len(got) != wantCount {
			t.Fatalf("предел %d: цепочек %d, ожидалось %d", hops, len(got), wantCount)
		}
		seen := map[string]bool{}
		prev := 0
		for _, p := range got {
			nodes := pathNodes(p)
			key := strings.Join(nodes, " ")
			if seen[key] {
				t.Fatalf("предел %d: цепочка повторилась: %s", hops, key)
			}
			seen[key] = true
			inPath := map[string]bool{}
			for _, n := range nodes {
				if inPath[n] {
					t.Fatalf("предел %d: узел %q повторился в цепочке %s", hops, n, key)
				}
				inPath[n] = true
			}
			if nodes[0] != "A" || nodes[len(nodes)-1] != "B" {
				t.Fatalf("предел %d: цепочка не от A к B: %s", hops, key)
			}
			if len(p) < prev || len(p) < 2 || len(p) > 3 {
				t.Fatalf("предел %d: длина %d вне порядка или окна: %s", hops, len(p), key)
			}
			prev = len(p)
		}
	}
}

// Склейки учтены: связь, записанная под поглощённым именем, ведёт цепочку
// через выжившего, а поглощённое имя в цепочках не появляется.
func TestPathsSeeMerges(t *testing.T) {
	g := newGraphWith(t, "старт", "сборщик мусора", "garbage collection", "финиш", "обход")
	const start, ru, en, finish, detour = 1, 2, 3, 4, 5
	add := edgeAdder(t, g)
	add(start, ru, RelRelated)
	add(en, finish, RelRelated)
	add(start, detour, RelRelated)
	add(detour, finish, RelRelated)

	viaRU := "старт сборщик мусора финиш"
	has := func(got [][]PathStep, want string) bool {
		for _, p := range got {
			if strings.Join(pathNodes(p), " ") == want {
				return true
			}
		}
		return false
	}
	before, _, _ := g.Paths("старт", "финиш", 3, 5)
	if has(before, viaRU) {
		t.Fatalf("до склейки пути через «сборщик мусора» нет, а получено %+v", before)
	}

	if _, err := g.Merges().Add([]MergeRec{{From: en, To: ru, Cos: 0.94, Verdict: "ДА"}}); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	defer g.Close()

	after, _, ok := g.Paths("старт", "финиш", 3, 5)
	if !ok || !has(after, viaRU) || !has(after, "старт обход финиш") {
		t.Fatalf("после склейки ожидались пути через «сборщик мусора» и через «обход», получено %+v", after)
	}
	for _, p := range after {
		for _, s := range p {
			if s.From == "garbage collection" || s.To == "garbage collection" {
				t.Fatalf("поглощённое имя в цепочке: %+v", p)
			}
		}
	}
}
