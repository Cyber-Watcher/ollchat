package graph

import "testing"

// Две половины без единой связи между ними, записанные одной темой, — тема
// несвязная; те же половины отдельными темами — связные.
func TestConnectivity(t *testing.T) {
	adj := map[uint32]map[uint32]float64{}
	link := func(a, b uint32) {
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
		}
		if adj[b] == nil {
			adj[b] = map[uint32]float64{}
		}
		adj[a][b], adj[b][a] = 1, 1
	}
	link(1, 2)
	link(2, 3)
	link(10, 11) // вторая половина, не связанная с первой
	link(20, 21) // отдельная связная тема

	comms := &Communities{List: []Community{
		{ID: 0, Level: 0, Members: []uint32{1, 2, 3, 10, 11}},
		{ID: 1, Level: 0, Members: []uint32{20, 21}},
		{ID: 2, Level: 1, Members: []uint32{1, 2, 3, 10, 11, 20, 21}},
	}}
	c := connectivity(adj, comms)
	if c.Communities != 2 || c.Disconnected != 1 || c.Parts != 2 || c.Largest != 2 || c.Share() != 50 {
		t.Fatalf("связность: %+v (доля %d%%)", c, c.Share())
	}

	after := &Communities{List: []Community{
		{Level: 0, Members: []uint32{1, 2, 3}}, {Level: 0, Members: []uint32{10, 11}}, {Level: 0, Members: []uint32{20, 21}},
	}}
	if got := connectivity(adj, after); got.Disconnected != 0 {
		t.Fatalf("раздельные темы должны быть связными: %+v", got)
	}
}
