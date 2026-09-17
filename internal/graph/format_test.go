package graph

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Строка подтверждения у связи берётся не из оглавления, пока есть другой кусок.
func TestEvidenceLineSkipsTableOfContents(t *testing.T) {
	src := memChunks{
		{104, 21}:  "DNS in Kubernetes 192\n10.1 A brief intro to DNS (and CoreDNS) 192\nPods need internal DNS 195\nConfiguring CoreDNS 203\n",
		{104, 682}: "In Kubernetes, CoreDNS is the Pod that answers cluster DNS.",
	}
	r := FoundRelation{Src: "CoreDNS", Dst: "Kubernetes", Evidences: []ChunkKey{{104, 21}, {104, 682}}}
	line := evidenceLine(src, r, 120)
	if !strings.Contains(line, "answers cluster DNS") {
		t.Fatalf("подтверждение взято из оглавления: %q", line)
	}
	r.Evidences = []ChunkKey{{104, 21}}
	if evidenceLine(src, r, 120) == "" {
		t.Fatal("единственное оглавление выброшено — строка пуста")
	}
}

// Выдача не должна выглядеть полнее, чем она есть (этап 104, П3.2 и П5.1),
// и должна показывать возраст подтверждения (П10.1).
//
// До 16.09.2026 карточка печатала «Связано с:» и четыре имени, хотя у хаба
// связей тысячи, а «подтверждений 186» не говорило ни сколько выдержек
// показано, ни какого они года. Замер 16.09: 16,3% связей держатся только
// на книгах старше 2023 года, и по выдаче это было невидимо.
func TestRenderEntityShowsHiddenAndYear(t *testing.T) {
	src := yearChunks{
		ChunkKey{Doc: 1, Ord: 1}.Pack(): 2019,
		ChunkKey{Doc: 1, Ord: 2}.Pack(): 2026,
	}
	e := FoundEntity{
		Entity:   Entity{ID: 1, Name: "Go", Type: TypeTech},
		Mentions: 100, Books: 40,
		NeighborsTotal: 11466,
		Neighbors: []NeighborInfo{{
			ID: 2, Name: "goroutine", Rel: "использует", Count: 186,
			Evidence: []ChunkKey{{Doc: 1, Ord: 1}, {Doc: 1, Ord: 2}},
		}},
	}
	out := RenderEntity(src, e, nil, RenderOpts{})
	if !strings.Contains(out, "показано 1 из 11466") {
		t.Fatalf("не сказано, сколько связей скрыто:\n%s", out)
	}
	if !strings.Contains(out, "свежайшее 2026 г.") {
		t.Fatalf("не показан год самого свежего подтверждения:\n%s", out)
	}

	// Когда показаны все связи, лишней строки быть не должно.
	e.NeighborsTotal = 1
	out = RenderEntity(src, e, nil, RenderOpts{})
	if strings.Contains(out, "показано") {
		t.Fatalf("строка о скрытом появилась, хотя скрывать нечего:\n%s", out)
	}
}

// Год не известен — печатаем как раньше, без пустых скобок и «0 г.».
// Таких связей у нас 12,2%: книги без года в имени, копирайте и метаданных.
func TestRenderEntityWithoutYear(t *testing.T) {
	src := yearChunks{ChunkKey{Doc: 1, Ord: 1}.Pack(): 0}
	e := FoundEntity{
		Entity: Entity{ID: 1, Name: "Go", Type: TypeTech}, Mentions: 10, Books: 2,
		NeighborsTotal: 1,
		Neighbors: []NeighborInfo{{
			ID: 2, Name: "goroutine", Count: 5,
			Evidence: []ChunkKey{{Doc: 1, Ord: 1}},
		}},
	}
	out := RenderEntity(src, e, nil, RenderOpts{})
	if strings.Contains(out, "свежайшее") || strings.Contains(out, "0 г.") {
		t.Fatalf("год напечатан, хотя он не известен:\n%s", out)
	}
	if !strings.Contains(out, "(подтверждений 5)") {
		t.Fatalf("обычная строка связи сломана:\n%s", out)
	}
}

// yearChunks — куски, у которых важен только год книги.
type yearChunks map[uint64]int

func (y yearChunks) ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool) {
	year, ok := y[ChunkKey{Doc: doc, Ord: ord}.Pack()]
	if !ok {
		return kb.ChunkInfo{}, false
	}
	return kb.ChunkInfo{Doc: doc, Ord: ord, Text: "текст", Book: kb.BookRec{Title: "Книга", Year: year}}, true
}
