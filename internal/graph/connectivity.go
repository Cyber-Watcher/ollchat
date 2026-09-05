package graph

// Связность тем.
//
// Книги называют для GraphRAG «typically Leiden» («Agentic RAG Systems», 2026,
// стр. 127): его отличие от Louvain — гарантия, что каждое сообщество связно.
// Louvain такой гарантии не даёт: тема может состоять из двух половин без
// единой связи между ними, и описание такой темы моделью — описание двух
// разных вещей под одним названием. Прежде чем переписывать алгоритм, доля
// таких тем меряется (этап 91, R9.2): обход занимает секунды и карту не трогает.
//
// Замер 04.09.2026 на графе books (33 429 тем нижнего уровня, 894 790 связей):
// несвязных тем 30 (0.09%), частей в них 76, самая рваная — на 10. Доля
// незаметная, поэтому Louvain остаётся, а разрез тем по связности не заведён;
// если следующий замер покажет иное, части даёт components().

// Connectivity — сколько тем нижнего уровня распадаются на несвязные части.
type Connectivity struct {
	Communities  int // тем нижнего уровня
	Disconnected int // из них распадающихся на несвязные части
	Parts        int // на сколько частей распадаются несвязные темы в сумме
	Largest      int // наибольшее число частей у одной темы
}

// Share — доля несвязных тем в процентах.
func (c Connectivity) Share() int {
	if c.Communities == 0 {
		return 0
	}
	return 100 * c.Disconnected / c.Communities
}

// CommunityConnectivity меряет связность тем нижнего уровня по связям графа.
func (g *Graph) CommunityConnectivity(c *Communities) Connectivity {
	if c == nil {
		return Connectivity{}
	}
	adj, _ := g.undirected()
	return connectivity(adj, c)
}

func connectivity(adj map[uint32]map[uint32]float64, c *Communities) Connectivity {
	var out Connectivity
	for _, com := range c.List {
		if com.Level != 0 {
			continue
		}
		out.Communities++
		parts := len(components(adj, com.Members))
		if parts > 1 {
			out.Disconnected++
			out.Parts += parts
			if parts > out.Largest {
				out.Largest = parts
			}
		}
	}
	return out
}

// components делит список понятий на связные части по связям между ними.
// Части идут в порядке первого члена; внутри — в порядке списка.
func components(adj map[uint32]map[uint32]float64, members []uint32) [][]uint32 {
	inSet := make(map[uint32]bool, len(members))
	for _, id := range members {
		inSet[id] = true
	}
	seen := make(map[uint32]bool, len(members))
	var out [][]uint32
	for _, start := range members {
		if seen[start] {
			continue
		}
		var part []uint32
		queue := []uint32{start}
		seen[start] = true
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			part = append(part, id)
			for nb := range adj[id] {
				if inSet[nb] && !seen[nb] {
					seen[nb] = true
					queue = append(queue, nb)
				}
			}
		}
		out = append(out, part)
	}
	return out
}
