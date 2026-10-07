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
	out := captureStdout(t, func() { chainStats(g, set, 3, true, nil) })
	want := fmt.Sprintf("горутина | канал\tпрямая\t%v", map[string]int{graph.RelName(graph.RelRelated): 2})
	if !strings.Contains(out, want) {
		t.Errorf("строка пары:\n%s\nожидалась %q — две связи, по одной в каждую сторону", out, want)
	}
}
