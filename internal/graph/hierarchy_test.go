package graph

import (
	"fmt"
	"sync"
	"testing"
)

// Тот же искусственный граф, что у `graphloops.py --selftest` (второй прибор),
// и ещё встречная пара разных типов: она видна только в «оба вместе».
func TestHierarchyCheck(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	id := map[int]uint32{}
	for i := 1; i <= 12; i++ {
		e, _, err := g.Entities().Add(fmt.Sprintf("понятие %d", i), TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		id[i] = e
	}
	ord := uint32(0)
	for _, r := range []struct {
		a, b int
		typ  uint8
	}{
		{1, 2, RelIs}, {2, 1, RelIs}, // встречная «является»
		{3, 4, RelPart}, {4, 5, RelPart}, {5, 3, RelPart}, // круг «часть» из трёх
		{6, 7, RelIs}, {8, 6, RelIs}, // 8 склеен в 7 — встречная 6↔7
		{9, 10, RelIs}, {10, 9, RelUses}, // обратно — другой тип связи, не в счёт
		{11, 12, RelIs}, {12, 11, RelPart}, // встречная только у «оба вместе»
	} {
		ord++
		if err := g.Edges().Add(Edge{Src: id[r.a], Dst: id[r.b], Type: r.typ, Weight: 1,
			Evidence: ChunkKey{Doc: 1, Ord: ord}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Merges().Add([]MergeRec{{To: id[7], From: id[8], Level: "mutual"}}); err != nil {
		t.Fatal(err)
	}

	got := g.HierarchyCheck()
	want := []Hierarchy{
		{Name: "является", Records: 6, Pairs: 6, Mutual: 2, Cycles: 2, InCycles: 4, Largest: 2},
		{Name: "часть", Records: 4, Pairs: 4, Mutual: 0, Cycles: 1, InCycles: 3, Largest: 3},
		{Name: "оба вместе", Records: 10, Pairs: 10, Mutual: 3, Cycles: 4, InCycles: 9, Largest: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("строк %d, ожидалось %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s:\n получено %+v\n ожидалось %+v", want[i].Name, got[i], want[i])
		}
	}
	if s := got[0].MutualShare(); s < 33.3 || s > 33.4 {
		t.Errorf("доля встречных «является» %.2f%%, ожидалось 2 из 6", s)
	}
}

// Круг-гигант длиннее любого разумного стека: обход без рекурсии обязан
// пройти его и найти одну компоненту.
func TestSCCSizesLongCycle(t *testing.T) {
	const n = 200000
	adj := map[uint32][]uint32{}
	for i := uint32(1); i <= n; i++ {
		adj[i] = []uint32{i%n + 1}
	}
	adj[5] = append(adj[5], n+1) // хвост наружу — отдельная компонента из одного
	sizes := sccSizes(adj)
	big, one := 0, 0
	for _, s := range sizes {
		switch s {
		case n:
			big++
		case 1:
			one++
		}
	}
	if big != 1 || one != 1 || len(sizes) != 2 {
		t.Errorf("компоненты %v… (всего %d), ожидались одна на %d и одна из одного", sizes[:min(len(sizes), 5)], len(sizes), n)
	}
}

// ── Правило перевеса (этап 126, Я1, вариант б) ──────────────────────────────
//
// Проверяется тем же путём, каким читает работа: Of, incoming, Neighbors,
// Between, — а не внутренним счётом.

// majorityGraph — граф с правилом перевеса k и понятиями n1…nN.
func majorityGraph(t *testing.T, k, n int) (*Graph, map[int]uint32) {
	t.Helper()
	g, err := Create(t.TempDir(), "проба", 100, Rules{HierarchyMajority: k})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	id := map[int]uint32{}
	for i := 1; i <= n; i++ {
		e, _, err := g.Entities().Add(fmt.Sprintf("понятие %d", i), TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		id[i] = e
	}
	return g, id
}

// addEdges пишет n записей src → dst этого типа, каждую из своего куска.
func addEdges(t *testing.T, g *Graph, src, dst uint32, typ uint8, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ord := uint32(g.Edges().Count() + 1)
		if err := g.Edges().Add(Edge{Src: src, Dst: dst, Type: typ, Weight: 1,
			Evidence: ChunkKey{Doc: 1, Ord: ord}}); err != nil {
			t.Fatal(err)
		}
	}
}

// countTo — сколько связей этого типа из списка ведут src → dst.
func countTo(list []Edge, src, dst uint32, typ uint8) int {
	n := 0
	for _, ed := range list {
		if ed.Src == src && ed.Dst == dst && ed.Type == typ {
			n++
		}
	}
	return n
}

// sideSeen — сколько записей src → dst типа typ видит каждое чтение: Of(src),
// incoming(dst), Between и Neighbors с обеих сторон. Все обязаны сходиться.
func sideSeen(t *testing.T, g *Graph, src, dst uint32, typ uint8) int {
	t.Helper()
	of := countTo(g.Edges().Of(src), src, dst, typ)
	in := countTo(g.Edges().incoming(dst), src, dst, typ)
	between := countTo(g.Edges().Between(src, dst), src, dst, typ)
	if of != in || of != between {
		t.Errorf("%d → %d тип %d: Of %d, incoming %d, Between %d — чтения разошлись", src, dst, typ, of, in, between)
	}
	nb := func(from, other uint32, in bool) int {
		for _, x := range g.Edges().Neighbors(from) {
			if x.ID == other && x.In == in && hasType(x.Types, typ) {
				return 1
			}
		}
		return 0
	}
	if want := min(of, 1); nb(src, dst, false) != want || nb(dst, src, true) != want {
		t.Errorf("%d → %d тип %d: Of %d, а соседи видят исходящую %d, входящую %d",
			src, dst, typ, of, nb(src, dst, false), nb(dst, src, true))
	}
	return of
}

// Слабая сторона встречной пары «часть» и «является» прячется во всех чтениях,
// и исходящих, и входящих; у пары ниже порога, у других типов и у пары разных
// типов видно всё. K = 0 в правилах — умолчание 3.
func TestMajorityHidesWeakSide(t *testing.T) {
	g, id := majorityGraph(t, 0, 10)
	if k := g.Rules().HierarchyMajority; k != 3 {
		t.Fatalf("ноль в правилах дал K=%d, ожидалось умолчание 3", k)
	}
	addEdges(t, g, id[1], id[2], RelPart, 3) // ab = 3·ba — ровно на пороге
	addEdges(t, g, id[2], id[1], RelPart, 1)
	addEdges(t, g, id[3], id[4], RelIs, 1) // слабая сторона — с меньшего номера
	addEdges(t, g, id[4], id[3], RelIs, 3)
	addEdges(t, g, id[5], id[6], RelPart, 2) // 2 < 3·1 — обе видны
	addEdges(t, g, id[6], id[5], RelPart, 1)
	addEdges(t, g, id[7], id[8], RelUses, 5) // другой тип — не трогается
	addEdges(t, g, id[8], id[7], RelUses, 1)
	addEdges(t, g, id[9], id[10], RelIs, 3) // «является» туда, «часть» обратно — не пара
	addEdges(t, g, id[10], id[9], RelPart, 1)

	for _, c := range []struct {
		a, b int
		typ  uint8
		want int
	}{
		{1, 2, RelPart, 3}, {2, 1, RelPart, 0},
		{3, 4, RelIs, 0}, {4, 3, RelIs, 3},
		{5, 6, RelPart, 2}, {6, 5, RelPart, 1},
		{7, 8, RelUses, 5}, {8, 7, RelUses, 1},
		{9, 10, RelIs, 3}, {10, 9, RelPart, 1},
	} {
		if got := sideSeen(t, g, id[c.a], id[c.b], c.typ); got != c.want {
			t.Errorf("понятие %d → %d (тип %d): видно %d записей, ожидалось %d", c.a, c.b, c.typ, got, c.want)
		}
	}
	if sides, recs := g.MajorityHidden(); sides != 2 || recs != 2 {
		t.Errorf("скрыто сторон %d, записей %d; ожидалось 2 и 2 (2 → 1 «часть», 3 → 4 «является»)", sides, recs)
	}
	// Как записано — всё на месте: правило не трогает ни файл, ни сырое чтение.
	if n := countTo(g.Edges().OfAsWritten(id[2]), id[2], id[1], RelPart); n != 1 {
		t.Errorf("OfAsWritten: 2 → 1 «часть» %d записей, ожидалась 1", n)
	}
	if n := g.Edges().Count(); n != 21 {
		t.Errorf("записей в журнале %d, ожидалось 21: правило не должно ничего удалять", n)
	}
}

// Отрицательное K выключает правило: видно всё, как записано.
func TestMajorityNegativeOff(t *testing.T) {
	g, id := majorityGraph(t, -1, 2)
	addEdges(t, g, id[1], id[2], RelPart, 5)
	addEdges(t, g, id[2], id[1], RelPart, 1)
	if got := sideSeen(t, g, id[2], id[1], RelPart); got != 1 {
		t.Errorf("при K=-1 слабая сторона видна %d раз, ожидалась 1", got)
	}
	if sides, recs := g.MajorityHidden(); sides != 0 || recs != 0 {
		t.Errorf("при K=-1 скрыто сторон %d, записей %d", sides, recs)
	}
	if k := g.Rules().Normalized().HierarchyMajority; k != -1 {
		t.Errorf("повторная нормализация дала K=%d: выключенное правило включилось бы при переоткрытии", k)
	}
}

// Заданное K действует: при K=2 пара 2 против 1 уже прячет слабую сторону.
func TestMajorityCustomK(t *testing.T) {
	g, id := majorityGraph(t, 2, 2)
	addEdges(t, g, id[1], id[2], RelPart, 2)
	addEdges(t, g, id[2], id[1], RelPart, 1)
	if got := sideSeen(t, g, id[2], id[1], RelPart); got != 0 {
		t.Errorf("при K=2 сторона 1 против 2 видна %d раз", got)
	}
	if got := sideSeen(t, g, id[1], id[2], RelPart); got != 2 {
		t.Errorf("при K=2 сильная сторона видна %d раз, ожидалось 2", got)
	}
}

// Стороны считаются после склеек: записи поглощённого достаются выжившему.
// До склейки 1 → 2 и 2 → 1 по одной записи, и ещё две 3 → 1; после склейки
// 3 в 2 сторона 2 → 1 набирает три записи против одной — 1 → 2 прячется.
// Склейка, добавленная после первого чтения, обязана пересчитать набор.
func TestMajorityAfterMerge(t *testing.T) {
	g, id := majorityGraph(t, 0, 3)
	addEdges(t, g, id[1], id[2], RelPart, 1)
	addEdges(t, g, id[2], id[1], RelPart, 1)
	addEdges(t, g, id[3], id[1], RelPart, 2)
	if got := sideSeen(t, g, id[1], id[2], RelPart); got != 1 {
		t.Fatalf("до склейки 1 → 2 видна %d раз, ожидалась 1", got)
	}
	if _, err := g.Merges().Add([]MergeRec{{From: id[3], To: id[2], Level: "mutual"}}); err != nil {
		t.Fatal(err)
	}
	if got := sideSeen(t, g, id[1], id[2], RelPart); got != 0 {
		t.Errorf("после склейки слабая 1 → 2 видна %d раз", got)
	}
	if got := sideSeen(t, g, id[2], id[1], RelPart); got != 3 {
		t.Errorf("после склейки сильная 2 → 1 видна %d раз, ожидалось 3", got)
	}
	// Поглощённое понятие читается выжившим — и тоже без слабой стороны.
	if n := countTo(g.Edges().incoming(id[3]), id[1], id[2], RelPart); n != 0 {
		t.Errorf("входящие поглощённого: слабая 1 → 2 видна %d раз", n)
	}
}

// Записи, добавленные после чтения (сборка дописывает граф, пока его читают),
// пересчитывают набор: сторона, ставшая сильной, видна, ставшая слабой — нет.
func TestMajorityRecountsAfterAdd(t *testing.T) {
	g, id := majorityGraph(t, 0, 2)
	addEdges(t, g, id[1], id[2], RelIs, 3)
	addEdges(t, g, id[2], id[1], RelIs, 1)
	if got := sideSeen(t, g, id[2], id[1], RelIs); got != 0 {
		t.Fatalf("2 → 1 при 1 против 3 видна %d раз", got)
	}
	addEdges(t, g, id[2], id[1], RelIs, 8) // 9 против 3
	if got := sideSeen(t, g, id[2], id[1], RelIs); got != 9 {
		t.Errorf("2 → 1, ставшая сильной (9 против 3), видна %d раз, ожидалось 9", got)
	}
	if got := sideSeen(t, g, id[1], id[2], RelIs); got != 0 {
		t.Errorf("1 → 2, ставшая слабой, видна %d раз", got)
	}
	addEdges(t, g, id[1], id[2], RelIs, 1) // 9 против 4 — ниже порога, видны обе
	if a, b := sideSeen(t, g, id[1], id[2], RelIs), sideSeen(t, g, id[2], id[1], RelIs); a != 4 || b != 9 {
		t.Errorf("при 4 против 9 видно %d и %d, ожидалось 4 и 9", a, b)
	}
}

// Доктор: счёт как записано и в выдаче расходятся ровно на спрятанное.
func TestHierarchyCheckAsWrittenAndShown(t *testing.T) {
	g, id := majorityGraph(t, 0, 2)
	addEdges(t, g, id[1], id[2], RelPart, 3)
	addEdges(t, g, id[2], id[1], RelPart, 1)
	raw, shown := g.HierarchyCheckAsWritten(), g.HierarchyCheck()
	if raw[1].Records != 4 || raw[1].Mutual != 1 || raw[1].Cycles != 1 {
		t.Errorf("как записано «часть»: %+v; ожидалось 4 записи, 1 встречная, 1 круг", raw[1])
	}
	if shown[1].Records != 3 || shown[1].Mutual != 0 || shown[1].Cycles != 0 {
		t.Errorf("в выдаче «часть»: %+v; ожидалось 3 записи, без встречных и кругов", shown[1])
	}
}

// Чтение во время дозаписи: сборка пишет, поиск читает — под -race без гонок,
// и после остановки записи выдача сходится со счётом по окончательным данным.
func TestMajorityConcurrentReadAdd(t *testing.T) {
	g, id := majorityGraph(t, 0, 4)
	addEdges(t, g, id[1], id[2], RelPart, 1)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				g.Edges().Of(id[1])
				g.Edges().Neighbors(id[2])
				g.Edges().Between(id[3], id[4])
			}
		}()
	}
	for i := 0; i < 50; i++ {
		ord := uint32(1000 + i)
		for _, ed := range []Edge{
			{Src: id[2], Dst: id[1], Type: RelPart},
			{Src: id[3], Dst: id[4], Type: RelIs},
		} {
			ed.Weight, ed.Evidence = 1, ChunkKey{Doc: 2, Ord: ord}
			if err := g.Edges().Add(ed); err != nil {
				t.Fatal(err)
			}
		}
	}
	close(stop)
	wg.Wait()
	if got := sideSeen(t, g, id[1], id[2], RelPart); got != 0 {
		t.Errorf("1 → 2 при 1 против 50 видна %d раз", got)
	}
	if got := sideSeen(t, g, id[2], id[1], RelPart); got != 50 {
		t.Errorf("2 → 1 видна %d раз, ожидалось 50", got)
	}
}
