package ui

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// collWithGraph — коллекция из одной книги с крошечным графом рядом.
func collWithGraph(t *testing.T) (*Model, *kb.Collection) {
	t.Helper()
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	coll, err := m.kbCollection("go")
	if err != nil {
		t.Fatal(err)
	}
	buildTestGraph(t, coll)
	return m, coll
}

// openGraphFiles — сколько файлов каталога графа держит открытыми процесс.
// Считается по ссылкам /proc/self/fd, а не числом дескрипторов: сокеты
// и файлы чужих горутин в счёт не попадают.
func openGraphFiles(t *testing.T, graphDir string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("нет /proc/self/fd: открытые файлы считаются только на Linux")
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.HasPrefix(target, graphDir+string(filepath.Separator)) {
			n++
		}
	}
	return n
}

// /graph pack открывал граф ради проверки и не закрывал: журналы оставались
// открытыми, а граф — в памяти, до самого выхода (аудит 07.10.2026).
func TestGraphPackClosesGraph(t *testing.T) {
	m, coll := collWithGraph(t)
	gdir, err := filepath.EvalSymlinks(filepath.Join(coll.Dir(), graph.DirFor(m.cfg.Graph.Name)))
	if err != nil {
		t.Fatal(err)
	}
	// Без сборщика мусора: брошенные файлы закрыл бы финализатор, и утечку
	// было бы не разглядеть.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	before := openGraphFiles(t, gdir)
	cmd := m.runCommand("/graph pack go.tar go")
	if cmd == nil {
		t.Fatalf("упаковка не ушла в фон: %q", lastBlock(m).text)
	}
	if msg, ok := cmd().(noticeMsg); !ok {
		t.Fatalf("упаковка не удалась: %#v", msg)
	}
	if after := openGraphFiles(t, gdir); after != before {
		t.Fatalf("после /graph pack открыто файлов графа %d, было %d", after, before)
	}
}

// Вторая /graph review, пока открывался граф первой: оба ответа приходят,
// и окно первой подменялось второй, а её граф так и висел открытым.
func TestSecondReviewClosesFirstGraph(t *testing.T) {
	m, coll := collWithGraph(t)
	open := func() *graph.Graph {
		g, err := graph.Open(coll.Dir(), coll.ChunkCount(), m.cfg.Graph.Rules())
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	first, second := open(), open()

	m.Update(reviewReadyMsg{panel: &reviewPanel{coll: "go", g: first, kbc: coll}})
	m.Update(reviewReadyMsg{panel: &reviewPanel{coll: "go", g: second, kbc: coll}})
	if !graphClosed(t, first) {
		t.Fatal("граф прежнего окна разбора не закрыт")
	}
	if m.review == nil || m.review.g != second {
		t.Fatal("открыто не последнее окно разбора")
	}
	if graphClosed(t, second) {
		t.Fatal("закрыт граф открытого окна")
	}
	m.closeReviewPanel()
}
