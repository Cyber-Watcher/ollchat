package graph

import "testing"

// testRank — настройки с включённым ранжированием: эти тесты проверяют саму
// пересортировку, а по умолчанию она выключена (см. DefaultNeighborRank).
var testRank = NeighborRank{SenseWeight: 1.5, Pool: 8}

// rankFixture: у понятия «cookie» шесть соседей. Тяжёлый по весу, но чужой
// вопросу, стоит первым; уместный — последним.
//
// Это ровно тот случай, который вылез при замере склейки 27.08.2026: на вопрос
// про cookies наверх выходил `cookie —часть→ FastAPI`, вытесняя `SameSite`
// и `same-origin policy`. У FastAPI подтверждений больше — но не потому, что
// он отвечает на вопрос, а потому, что про него в книгах пишут часто.
func rankFixture(t *testing.T) (*Graph, uint32) {
	t.Helper()
	g := newGraphWith(t)
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	root := add("cookie")     // 1
	fast := add("FastAPI")    // 2 — тяжёлый, но не про то
	http := add("HTTP")       // 3
	sess := add("session_id") // 4
	sec := add("secure")      // 5
	only := add("HttpOnly")   // 6
	same := add("SameSite")   // 7 — лёгкий, но ровно про вопрос

	// Вес убывает от FastAPI к SameSite: по весу SameSite последний.
	for i, dst := range []uint32{fast, http, sess, sec, only, same} {
		if err := g.Edges().Add(Edge{
			Src: root, Dst: dst, Type: RelRelated, Weight: float32(60 - i*10),
			Evidence: ChunkKey{Doc: 1, Ord: uint32(i + 1)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Близость к вопросу задаётся первой составляющей: kb.Cosine делит
	// скалярное произведение на 127×127, поэтому при вопросе {127,0,0,0}
	// близость соседа равна его первой составляющей, делённой на 127.
	//
	// Ступеньками, а не «единица или ноль»: при одинаковой близости порядок
	// решает номер понятия, и проверка выродилась бы в проверку нумерации.
	const dim = 4
	first := map[uint32]int8{
		root: 127,
		same: 127, // ровно про вопрос, но самый лёгкий по весу
		sec:  114, // 0.90
		only: 108, // 0.85
		http: 89,  // 0.70
		sess: 64,  // 0.50
		fast: 0,   // не про то вовсе, зато тяжелее всех
	}
	data := make([]int8, 0, 7*dim)
	for id := uint32(1); id <= 7; id++ {
		v := make([]int8, dim)
		v[0] = first[id]
		data = append(data, v...)
	}
	if err := g.SaveEntityVectors("проба", "", dim, data); err != nil {
		t.Fatal(err)
	}
	return g, root
}

func names(list []NeighborInfo) []string {
	out := make([]string, len(list))
	for i, n := range list {
		out[i] = n.Name
	}
	return out
}

func has(list []NeighborInfo, name string) bool {
	for _, n := range list {
		if n.Name == name {
			return true
		}
	}
	return false
}

// Без вектора вопроса выдача прежняя — по весу.
//
// Это главное требование к правке: обзор тем зовёт ту же функцию, не имея
// вопроса вовсе, и не должен сдвинуться.
func TestNeighborsWithoutQueryKeepsWeightOrder(t *testing.T) {
	g, root := rankFixture(t)
	defer g.Close()

	got := names(g.neighborsOf(root, 3, nil, NeighborRank{}, nil))
	want := []string{"FastAPI", "HTTP", "session_id"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("без вопроса порядок %v, ожидался прежний по весу %v", got, want)
		}
	}
}

// С вектором вопроса уместный сосед поднимается, а тяжёлый, но чужой — тонет.
func TestNeighborsRankedByQuery(t *testing.T) {
	g, root := rankFixture(t)
	defer g.Close()

	// Вопрос лежит на оси «безопасность cookie».
	qv := []int8{127, 0, 0, 0}
	got := g.neighborsOf(root, 3, qv, testRank, nil)
	if !has(got, "SameSite") {
		t.Fatalf("уместный сосед не попал в тройку: %v", names(got))
	}
	if has(got, "FastAPI") {
		t.Errorf("тяжёлый, но чужой вопросу сосед остался в тройке: %v", names(got))
	}
}

// Слияние не выбрасывает вес совсем: при равной уместности впереди тот,
// у кого связь подтверждена чаще.
//
// Иначе получилась бы обратная беда — наверх всплывало бы редкое частное имя.
// На этом уже обжигались при смысловом входе в граф: вопрос «чем переранжировать
// найденные куски» поднимал «reranked chunk» с одним упоминанием в одной книге
// выше «reranking» со 159 упоминаниями в двенадцати.
func TestNeighborsFusionKeepsWeight(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	root := add("корень")
	heavy := add("частый")  // 2 — подтверждений много
	light := add("редкий")  // 3 — подтверждений мало
	other := add("сторона") // 4 — не про вопрос
	for _, e := range []Edge{
		{Src: root, Dst: heavy, Weight: 50},
		{Src: root, Dst: light, Weight: 1},
		{Src: root, Dst: other, Weight: 30},
	} {
		e.Type, e.Evidence = RelRelated, ChunkKey{Doc: 1, Ord: 1}
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	// Уместность у «частого» и «редкого» одинаковая, у «стороны» нулевая.
	if err := g.SaveEntityVectors("проба", "", 2, []int8{
		127, 0, // корень
		127, 0, // частый
		127, 0, // редкий — та же близость
		0, 127, // сторона
	}); err != nil {
		t.Fatal(err)
	}

	got := g.neighborsOf(root, 2, []int8{127, 0}, testRank, nil)
	if len(got) != 2 || got[0].Name != "частый" {
		t.Fatalf("при равной уместности первым должен идти чаще подтверждённый, получено %v",
			names(got))
	}
	if has(got, "сторона") {
		t.Errorf("сосед не по вопросу вытеснил уместного: %v", names(got))
	}
}

// Вектор вопроса чужой размерности не портит выдачу, а просто не применяется.
func TestNeighborsIgnoresForeignVector(t *testing.T) {
	g, root := rankFixture(t)
	defer g.Close()

	got := names(g.neighborsOf(root, 3, []int8{1, 2, 3}, testRank, nil))
	if got[0] != "FastAPI" {
		t.Fatalf("при чужой размерности порядок должен остаться прежним, получено %v", got)
	}
}

// Понятие без вектора не исчезает из выдачи: у него нет места во втором
// списке, но место в первом — есть.
func TestNeighborsKeepVectorlessNeighbors(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	root, a, b := add("корень"), add("с вектором"), add("без вектора")
	for i, dst := range []uint32{a, b} {
		if err := g.Edges().Add(Edge{Src: root, Dst: dst, Type: RelRelated,
			Weight: float32(10 - i), Evidence: ChunkKey{Doc: 1, Ord: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	// Векторы посчитаны только первым двум понятиям из трёх.
	if err := g.SaveEntityVectors("проба", "", 2, []int8{127, 0, 0, 127}); err != nil {
		t.Fatal(err)
	}
	got := g.neighborsOf(root, 5, []int8{0, 127}, testRank, nil)
	if len(got) != 2 {
		t.Fatalf("соседей %d, ожидалось 2 — понятие без вектора не должно пропадать: %v",
			len(got), names(got))
	}
}

// Пул шире показанного, но не бесконечен: у понятия с сотнями связей
// пересортировываются не все.
func TestNeighborsPoolIsBounded(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	root, _, err := g.Entities().Add("корень", TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	const n = 100
	for i := 0; i < n; i++ {
		id, _, err := g.Entities().Add("сосед "+itoa(i), TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Edges().Add(Edge{Src: root, Dst: id, Type: RelRelated,
			Weight: float32(n - i), Evidence: ChunkKey{Doc: 1, Ord: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	// Последнему соседу — вектор ровно по вопросу; он за пределами пула
	// (5 × 8 = 40 мест) и подняться не должен.
	data := make([]int8, (n+1)*2)
	data[len(data)-2] = 127
	if err := g.SaveEntityVectors("проба", "", 2, data); err != nil {
		t.Fatal(err)
	}
	got := g.neighborsOf(root, 5, []int8{127, 0}, testRank, nil)
	if has(got, "сосед 99") {
		t.Errorf("сосед за пределами пула поднялся наверх: %v", names(got))
	}
	if len(got) != 5 {
		t.Errorf("соседей %d, ожидалось 5", len(got))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// Связь с другим понятием вопроса не должна теряться на ОТБОРЕ соседей.
//
// Замер 16.09.2026 (`graphstats -localityeval`): прямая связь между двумя
// названными понятиями есть в графе у 30 пар из 60, а в выдачу попадала у 11 —
// её вытесняли более подтверждённые соседи. Сортировка «связи между найденными
// понятиями вперёд» в Search существовала и раньше, но спасти связь не могла:
// до неё дело не доходило, потому что отбор соседей связь уже выбросил.
func TestNeighborsKeepSeedEvenIfLight(t *testing.T) {
	g, root := rankFixture(t)
	defer g.Close()

	// SameSite — самый лёгкий сосед, по весу шестой из шести: в тройку
	// не попадает никогда.
	if got := g.neighborsOf(root, 3, nil, NeighborRank{}, nil); has(got, "SameSite") {
		t.Fatalf("заготовка негодна: SameSite и без подъёма в тройке — %v", names(got))
	}

	same, ok := g.Entities().Lookup("SameSite")
	if !ok {
		t.Fatal("SameSite не нашёлся")
	}
	got := names(g.neighborsOf(root, 3, nil, NeighborRank{}, map[uint32]bool{same.ID: true}))
	if !hasName(got, "SameSite") {
		t.Fatalf("понятие вопроса не поднято в выдачу: %v", got)
	}
	if got[0] != "SameSite" {
		t.Fatalf("понятие вопроса не первое: %v", got)
	}
	if len(got) != 3 {
		t.Fatalf("предел выдачи нарушен: %d соседей, ожидалось 3", len(got))
	}
	// Остальные места достаются прежним лидерам по весу, по порядку.
	if got[1] != "FastAPI" || got[2] != "HTTP" {
		t.Fatalf("прежний порядок сломан: %v", got)
	}
}

// Понятий вопроса больше, чем мест в выдаче: они не должны вытеснять предел.
// hasName — есть ли имя в списке имён (has берёт соседей, а не имена).
func hasName(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestNeighborsSeedsDoNotExceedLimit(t *testing.T) {
	g, root := rankFixture(t)
	defer g.Close()

	seeds := map[uint32]bool{}
	for _, n := range []string{"FastAPI", "HTTP", "session_id", "secure", "HttpOnly"} {
		e, ok := g.Entities().Lookup(n)
		if !ok {
			t.Fatalf("%s не нашёлся", n)
		}
		seeds[e.ID] = true
	}
	got := g.neighborsOf(root, 2, nil, NeighborRank{}, seeds)
	if len(got) != 2 {
		t.Fatalf("выдано %d соседей, предел 2: %v", len(got), names(got))
	}
}
