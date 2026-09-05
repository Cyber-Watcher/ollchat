package graph

// Группы понятий: «эти узлы про одно», но без слияния.
//
// **Зачем, и чем отличается от склеек.** Склейка (merges.jsonl) поглощает один
// узел другим: поиск ведёт к выжившему, а поглощённый исчезает как отдельное
// понятие вместе со своим счётом упоминаний и своими книгами. Группа мягче:
// узлы остаются каждый со своими книгами и связями, а выдача при поиске
// объединяется. Книга описывает это прямо: «resolved entities must not be
// merged, as their sources must be maintained distinctly» — «разрешённые
// сущности не сливают, их источники надо хранить раздельно» («Neo4j: The
// Definitive Guide», Misquitta, Willemsen, 2025, стр. 265).
//
// **Что группа несёт, а склейка теряла.** У каждой группы — уверенность и
// причина: «на связи узла с группой хранятся confidence и reason» (там же).
// Это ровно то, чего не хватало двойникам: почему сгруппировано и насколько
// уверенно.
//
// Решения лежат в journal `groups.jsonl` (дозапись, как склейки) и надеваются
// на граф при чтении. Реестр понятий цел, откат — обратной записью.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const groupsFile = "groups.jsonl"

// GroupMode — режим применения групп: как opt.Groups, но по умолчанию для всей
// программы. Задаётся настройкой graph.groups; ключ --graph-groups и поле
// SearchOpts.Groups перекрывают его на один запрос.
const (
	GroupOff    = "off"    // группы не применять
	GroupUnion  = "union"  // объединять выдачу: спросил одного — показать группу
	GroupExpand = "expand" // расширять запрос синонимами группы (пока не реализован)
)

// GroupRec — одно решение: узлы members образуют одну группу.
//
// Группа задаётся полным составом, а не парами: связная компонента уже
// вычислена тем, кто группу создаёт (из склеек или из рёбер «похоже»).
// Так чтение не пересобирает компоненты на каждом открытии графа.
type GroupRec struct {
	ID      uint32   `json:"id"`      // стабильный номер группы
	Members []uint32 `json:"members"` // номера понятий в группе
	Conf    float64  `json:"conf,omitempty"`
	Why     string   `json:"why,omitempty"`
	From    string   `json:"from,omitempty"` // откуда: "merges", "resolve", "manual"
	At      int64    `json:"at,omitempty"`
	// Undo снимает прежнюю группу с этим ID: журнал только на дозапись, поэтому
	// отмена — тоже запись.
	Undo bool `json:"undo,omitempty"`
}

// Groups — группы понятий, собранные по журналу.
type Groups struct {
	mu   sync.RWMutex
	path string

	recs   []GroupRec
	byID   map[uint32]GroupRec // номер группы → её запись
	member map[uint32]uint32   // понятие → номер группы (последняя побеждает)
	maxID  uint32
}

func openGroups(dir string) (*Groups, error) {
	g := &Groups{
		path:   filepath.Join(dir, groupsFile),
		byID:   map[uint32]GroupRec{},
		member: map[uint32]uint32{},
	}
	f, err := os.Open(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return g, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r GroupRec
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == 0 {
			continue // оборванная последняя строка — не беда
		}
		g.recs = append(g.recs, r)
	}
	g.rebuild()
	return g, nil
}

// rebuild собирает состав групп по журналу. Последняя запись по номеру группы
// побеждает: Undo снимает группу, повторное определение переопределяет состав.
func (g *Groups) rebuild() {
	g.byID = make(map[uint32]GroupRec, len(g.recs))
	g.member = map[uint32]uint32{}
	g.maxID = 0
	for _, r := range g.recs {
		if r.ID > g.maxID {
			g.maxID = r.ID
		}
		if r.Undo {
			delete(g.byID, r.ID)
			continue
		}
		g.byID[r.ID] = r
	}
	// Понятие → группа строим отдельным проходом по актуальным группам:
	// понятие могло состоять в снятой группе, и его нельзя оставлять в member.
	for id, r := range g.byID {
		for _, m := range r.Members {
			g.member[m] = id
		}
	}
}

// GroupOf возвращает номер группы понятия и есть ли она. Nil-приёмник — групп нет.
func (g *Groups) GroupOf(ent uint32) (uint32, bool) {
	if g == nil {
		return 0, false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	id, ok := g.member[ent]
	return id, ok
}

// Members возвращает состав группы (пусто, если такой группы нет).
func (g *Groups) Members(groupID uint32) []uint32 {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	r, ok := g.byID[groupID]
	if !ok {
		return nil
	}
	out := append([]uint32(nil), r.Members...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Siblings — остальные понятия группы данного понятия (без него самого).
// Это и есть то, что объединяется в выдаче: спросили одно — показать группу.
func (g *Groups) Siblings(ent uint32) []uint32 {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	id, ok := g.member[ent]
	if !ok {
		return nil
	}
	var out []uint32
	for _, m := range g.byID[id].Members {
		if m != ent {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Count — сколько групп сейчас.
func (g *Groups) Count() int {
	if g == nil {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.byID)
}

// Add дозаписывает группу. ID == 0 означает «новая»: номер выдаётся сам.
func (g *Groups) Add(r GroupRec) (uint32, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.ID == 0 {
		g.maxID++
		r.ID = g.maxID
	}
	r.At = time.Now().Unix()
	f, err := os.OpenFile(g.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	line, err := json.Marshal(r)
	if err != nil {
		f.Close()
		return 0, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	g.recs = append(g.recs, r)
	g.rebuild()
	return r.ID, nil
}

// Groups отдаёт группы понятий графа.
func (g *Graph) Groups() *Groups { return g.groups }

// unionFind — объединение непересекающихся множеств: пары «похоже» превращаются
// в связные компоненты, а компонента и есть группа. Ровно как в книге:
// «all nodes in a set form a connected component and are equivalent to a unique
// entity» («Building Knowledge Graphs», 2023, стр. 158).
type unionFind struct{ parent map[uint32]uint32 }

func newUnionFind() *unionFind { return &unionFind{parent: map[uint32]uint32{}} }

func (u *unionFind) find(x uint32) uint32 {
	p, ok := u.parent[x]
	if !ok {
		u.parent[x] = x
		return x
	}
	if p != x {
		u.parent[x] = u.find(p)
	}
	return u.parent[x]
}

func (u *unionFind) union(a, b uint32) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// components собирает компоненты: корень → все его члены. Одиночек не отдаёт.
func (u *unionFind) components() map[uint32][]uint32 {
	out := map[uint32][]uint32{}
	for x := range u.parent {
		r := u.find(x)
		out[r] = append(out[r], x)
	}
	for r, m := range out {
		if len(m) < 2 {
			delete(out, r)
		}
	}
	return out
}

// GroupsFromPairs собирает группы из пар «эти два про одно» и дозаписывает их
// в журнал. Возвращает, сколько групп заведено и сколько понятий охвачено.
//
// pairs — пары номеров; conf/why/from пишутся на каждую полученную группу.
// Дубли и цепочки объединяются: (A,B) и (B,C) дают одну группу {A,B,C}.
func (g *Graph) GroupsFromPairs(pairs [][2]uint32, conf float64, why, from string) (groups, members int, err error) {
	uf := newUnionFind()
	for _, p := range pairs {
		uf.union(p[0], p[1])
	}
	for _, m := range uf.components() {
		if _, e := g.groups.Add(GroupRec{Members: m, Conf: conf, Why: why, From: from}); e != nil {
			return groups, members, e
		}
		groups++
		members += len(m)
	}
	return groups, members, nil
}

// pairsFromMerges превращает накопленные склейки в пары: выживший связан
// с каждым поглощённым. Так жёсткая склейка становится мягкой группой,
// не теряя, что склеивалось.
func pairsFromMerges(m *Merges) [][2]uint32 {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out [][2]uint32
	for absorbed, survivor := range m.to {
		if absorbed != survivor {
			out = append(out, [2]uint32{absorbed, survivor})
		}
	}
	return out
}

// PairsFromMerges — пары из журнала склеек, снаружи.
func (g *Graph) PairsFromMerges() [][2]uint32 { return pairsFromMerges(g.merges) }

// addGroupSiblings дотягивает в основу ответа членов группы каждого найденного
// понятия — эффект "union". Узлы остаются раздельными: добавляется тот же вид
// записи, что у найденного по смыслу, с пометкой «по группе».
//
// Ограничено половиной TopEntities, как и смысловой вход: группа надёжнее
// вектора, но всё же не сам вопрос, и заливать ею весь ответ нельзя.
func (g *Graph) addGroupSiblings(seeds []FoundEntity, have map[uint32]bool, opt SearchOpts) []FoundEntity {
	if g.groups == nil || g.groups.Count() == 0 {
		return seeds
	}
	room := opt.TopEntities - len(seeds)
	if max := opt.TopEntities / 2; room > max {
		room = max
	}
	if room <= 0 {
		return seeds
	}

	// Собираем добавляемых заранее, чтобы не зависеть от порядка обхода seeds.
	add := make([]uint32, 0, room)
	for _, s := range seeds {
		for _, sib := range g.groups.Siblings(s.ID) {
			r := g.merges.Resolve(sib) // склеенный ведёт к выжившему
			if have[r] {
				continue
			}
			have[r] = true
			add = append(add, r)
		}
	}

	for _, id := range add {
		if room <= 0 || len(seeds) >= opt.TopEntities {
			break
		}
		ent, ok := g.ents.Get(id)
		if !ok {
			continue
		}
		seeds = append(seeds, FoundEntity{
			Entity:      ent,
			Mentions:    len(g.ment.Of(id)),
			Books:       booksOf(g, id),
			Matched:     "по группе",
			Aliases:     g.ents.DisplayAliases(ent),
			AliasesSafe: g.ents.SafeAliases(ent),
		})
		room--
	}
	return seeds
}
