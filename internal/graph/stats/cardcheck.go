package stats

// Замер карточки понятия (этап 112, О5): видно ли понятие в выдержках «Где об
// этом написано». Куски отбираются ровно как в инструменте `graph_entity`
// (`internal/tools/graph.go`: g.Entity, затем g.Search по имени с TopChunks 4
// и отбором по словам коллекции), иначе замер мерил бы не то, что видит модель.
// По каждому куску — три признака: имя или синоним отдельным словом стоит в
// первых 400 знаках (так карточка печатала до 16.10.2026), в окне EntitySnippet,
// и где-нибудь в куске вообще. Карта не нужна.

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// cardRunes — длина выдержки карточки: умолчание RenderOpts.SnippetRunes.
const cardRunes = 400

func cardCheck(g *graph.Graph, c *kb.Collection, path string) {
	f, err := os.Open(path)
	die(err)
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" && !strings.HasPrefix(s, "#") {
			names = append(names, s)
		}
	}
	die(sc.Err())

	var shown, inStart, inWindow, inChunk, missing int
	fmt.Printf("%-24s %6s %8s %8s %8s\n", "понятие", "кусков", "в начале", "в окне", "в куске")
	for _, name := range names {
		ent, ok := g.Entity(name, graph.SearchOpts{})
		if !ok {
			missing++
			fmt.Printf("%-24s нет в графе\n", cut(name, 24))
			continue
		}
		words := append([]string{ent.Name}, ent.Aliases...)
		chunks := g.Search(ent.Name, graph.SearchOpts{TopEntities: 1, TopChunks: 4, Rank: graph.RankWith(c)}).Chunks
		var n, s, w, k int
		for _, ck := range chunks {
			info, ok := c.ChunkByRef(ck.Doc, ck.Ord)
			if !ok {
				continue
			}
			n++
			if graph.NameAt(cutRunes(strings.TrimSpace(info.Text), cardRunes), words) >= 0 {
				s++
			}
			if graph.NameAt(graph.EntitySnippet(info.Text, words, cardRunes), words) >= 0 {
				w++
			}
			if graph.NameAt(info.Text, words) >= 0 {
				k++
			}
		}
		shown, inStart, inWindow, inChunk = shown+n, inStart+s, inWindow+w, inChunk+k
		fmt.Printf("%-24s %6d %8d %8d %8d\n", cut(name, 24), n, s, w, k)
	}
	share := func(a int) float64 { return float64(a) / float64(max(shown, 1)) }
	fmt.Printf("\nпонятий %d (нет в графе %d), показанных кусков %d\n", len(names), missing, shown)
	fmt.Printf("имя или синоним отдельным словом: в первых %d знаках %d (%.3f), в окне карточки %d (%.3f), "+
		"где-нибудь в куске %d (%.3f)\n", cardRunes, inStart, share(inStart), inWindow, share(inWindow), inChunk, share(inChunk))
}

// cutRunes — первые n знаков строки.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
