package stats

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// captureStdout — что напечатала fn: замеры пишут прямо в os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	return <-done
}

// -chains-list считает каждую прямую связь пары один раз (аудит 07.10.2026):
// Between уже отдаёт связи в обе стороны, а второй вызов навстречу удваивал
// каждую.
func TestChainsListCountsEachEdgeOnce(t *testing.T) {
	g, err := graph.OpenOrCreate(t.TempDir(), "проба", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	a, _, _ := g.Entities().Add("горутина", "понятие")
	b, _, _ := g.Entities().Add("канал", "понятие")
	for _, ed := range []graph.Edge{
		{Src: a, Dst: b, Weight: 1, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}},
		{Src: b, Dst: a, Weight: 1, Evidence: graph.ChunkKey{Doc: 1, Ord: 2}},
	} {
		if err := g.Edges().Add(ed); err != nil {
			t.Fatal(err)
		}
	}
	set := filepath.Join(t.TempDir(), "chains.toml")
	if err := os.WriteFile(set, []byte("[[case]]\nquery = \"как связаны\"\nconcept_a = \"горутина\"\nconcept_b = \"канал\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { chainStats(g, set, 3, true, nil, nil) })
	want := fmt.Sprintf("горутина | канал\tпрямая\t%v", map[string]int{graph.RelName(graph.RelRelated): 2})
	if !strings.Contains(out, want) {
		t.Errorf("строка пары:\n%s\nожидалась %q — две связи, по одной в каждую сторону", out, want)
	}
}

// О12: КПД цепочки — кратчайший путь без запрета хабов против найденного.
// «старт → хаб → финиш» короче, но хаб (600 соседей) запрещён, и рабочая
// цепочка идёт в три шага «старт → x → y → финиш»: КПД 2/3, длина не совпала.
func TestChainsEfficiency(t *testing.T) {
	g, err := graph.OpenOrCreate(t.TempDir(), "проба", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	id := func(name string) uint32 {
		v, _, err := g.Entities().Add(name, "понятие")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	link := func(a, b uint32) {
		if err := g.Edges().Add(graph.Edge{Src: a, Dst: b, Weight: 1, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	start, finish, hub, x, y := id("старт"), id("финиш"), id("хаб"), id("икс"), id("игрек")
	link(start, hub)
	link(hub, finish)
	for i := range 600 {
		link(hub, id(fmt.Sprintf("лист%d", i)))
	}
	link(start, x)
	link(x, y)
	link(y, finish)
	set := filepath.Join(t.TempDir(), "chains.toml")
	if err := os.WriteFile(set, []byte("[[case]]\nquery = \"как связаны\"\nconcept_a = \"старт\"\nconcept_b = \"финиш\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { chainStats(g, set, 3, false, nil, nil) })
	if !strings.Contains(out, "средняя длина 3.00") {
		t.Fatalf("рабочая цепочка должна обойти хаб в 3 шага:\n%s", out)
	}
	if want := "среднее 0.667 по 1 парам, длина совпала у 0"; !strings.Contains(out, want) {
		t.Errorf("КПД цепочки:\n%s\nожидалось %q", out, want)
	}
}

// векторы кусков для проб: ключ — {книга, кусок}.
type testVecs map[[2]uint32][]int8

func (v testVecs) ChunkVectorByRef(doc, ord uint32) ([]int8, bool) {
	x, ok := v[[2]uint32{doc, ord}]
	return x, ok
}

// Этап 112, О4: у середины цепочки печатается косинус двух подтверждений,
// которые в ней сходятся; у куска без вектора — ничего.
func TestChainsListEvidenceCos(t *testing.T) {
	g, err := graph.OpenOrCreate(t.TempDir(), "проба", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	a, _, _ := g.Entities().Add("BM25", "понятие")
	m, _, _ := g.Entities().Add("TF", "понятие")
	b, _, _ := g.Entities().Add("Terraform", "понятие")
	for _, ed := range []graph.Edge{
		{Src: a, Dst: m, Weight: 1, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}},
		{Src: m, Dst: b, Weight: 1, Evidence: graph.ChunkKey{Doc: 2, Ord: 1}},
	} {
		if err := g.Edges().Add(ed); err != nil {
			t.Fatal(err)
		}
	}
	set := filepath.Join(t.TempDir(), "chains.toml")
	if err := os.WriteFile(set, []byte("[[case]]\nconcept_a = \"BM25\"\nconcept_b = \"Terraform\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Векторы коллекции нормированы до длины 127 — kb.Cosine делит на 127×127.
	apart := testVecs{{1, 1}: {127, 0}, {2, 1}: {0, 127}}
	if out := captureStdout(t, func() { chainStats(g, set, 2, true, nil, apart) }); !strings.Contains(out, "кн., cos 0.00]") {
		t.Errorf("подтверждения о разном, ждали cos 0.00:\n%s", out)
	}
	same := testVecs{{1, 1}: {127, 0}, {2, 1}: {127, 0}}
	if out := captureStdout(t, func() { chainStats(g, set, 2, true, nil, same) }); !strings.Contains(out, "кн., cos 1.00]") {
		t.Errorf("подтверждения об одном, ждали cos 1.00:\n%s", out)
	}
	half := testVecs{{1, 1}: {127, 0}}
	if out := captureStdout(t, func() { chainStats(g, set, 2, true, nil, half) }); strings.Contains(out, "cos") {
		t.Errorf("у куска нет вектора, а косинус напечатан:\n%s", out)
	}
}
