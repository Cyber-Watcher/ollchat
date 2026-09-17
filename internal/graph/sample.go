package graph

import (
	"math/rand"
	"sort"
)

// SampleEdges возвращает n связей, выбранных равномерно по записям связей
// живых понятий, без повторов и воспроизводимо: одно зерно — одна выборка.
//
// Нужна замерам вида «у какой доли связей…». Выбирать «понятие, затем его
// связь» для этого нельзя: связь хаба попадает в выборку в сотни раз реже
// связи одиночки, и доля получается по понятиям, а не по связям.
func (g *Graph) SampleEdges(n int, seed int64) []Edge {
	if g == nil || n <= 0 {
		return nil
	}
	var all []Edge
	for _, e := range g.ents.Live() { // по возрастанию номера
		all = append(all, g.edge.Of(e.ID)...)
	}
	// Порядок внутри понятия зависит от обхода поглощённых — закрепляем.
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Src != b.Src {
			return a.Src < b.Src
		}
		if a.Dst != b.Dst {
			return a.Dst < b.Dst
		}
		if a.Evidence != b.Evidence {
			return a.Evidence.Pack() < b.Evidence.Pack()
		}
		return a.Type < b.Type
	})
	if n >= len(all) {
		return all
	}
	rnd := rand.New(rand.NewSource(seed))
	// Частичное перемешивание Фишера — Йетса: первые n мест.
	for i := 0; i < n; i++ {
		j := i + rnd.Intn(len(all)-i)
		all[i], all[j] = all[j], all[i]
	}
	return all[:n]
}

// SamplePairs возвращает n различных пар связанных понятий, выбранных
// равномерно по ПАРАМ (направление и число подтверждений не в счёт),
// воспроизводимо. В паре меньший номер стоит первым.
//
// Для замеров вида «у какой доли связей…», где связь — это пара понятий
// со всеми её подтверждениями: возраст связи, одно подтверждение или много.
func (g *Graph) SamplePairs(n int, seed int64) [][2]uint32 {
	if g == nil || n <= 0 {
		return nil
	}
	seen := map[uint64]bool{}
	var all [][2]uint32
	for _, e := range g.ents.Live() {
		for _, ed := range g.edge.Of(e.ID) {
			a, b := ed.Src, ed.Dst
			if a > b {
				a, b = b, a
			}
			k := uint64(a)<<32 | uint64(b)
			if !seen[k] {
				seen[k] = true
				all = append(all, [2]uint32{a, b})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i][0] != all[j][0] {
			return all[i][0] < all[j][0]
		}
		return all[i][1] < all[j][1]
	})
	if n >= len(all) {
		return all
	}
	rnd := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		j := i + rnd.Intn(len(all)-i)
		all[i], all[j] = all[j], all[i]
	}
	return all[:n]
}
