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
	line := evidenceLine(src, r, 120, false)
	if !strings.Contains(line, "answers cluster DNS") {
		t.Fatalf("подтверждение взято из оглавления: %q", line)
	}
	r.Evidences = []ChunkKey{{104, 21}}
	if evidenceLine(src, r, 120, false) == "" {
		t.Fatal("единственное оглавление выброшено — строка пуста")
	}
}

// Под связью печатается кусок, где её имена стоят ближе всего, а не первый,
// где они оба есть (замер 17.09.2026, этап 104, П5.3): в первом имена могут
// стоять в разных концах страницы, и в окно выдержки попадёт только одно.
func TestEvidenceLinePrefersNearestPair(t *testing.T) {
	far := "Go is a compiled language. " + strings.Repeat("Лишний текст о сборке проекта. ", 12) +
		"Garbage collection упомянута в самом конце."
	src := memChunks{
		{7, 1}: far,
		{7, 2}: "Только про Go и ничего больше.",
		{7, 3}: "In Go, garbage collection runs concurrently with the program.",
	}
	r := FoundRelation{Src: "Go", Dst: "Garbage collection", Evidences: []ChunkKey{{7, 1}, {7, 2}, {7, 3}}}
	line := evidenceLine(src, r, 140, false)
	if !strings.Contains(line, "runs concurrently") {
		t.Fatalf("взят не кусок с ближайшей парой имён: %q", line)
	}
	// Прежнее правило — за выключателем, для сравнения одним бинарём.
	// Прежний выбор — первый кусок с обоими именами; в его окно помещается
	// только одно из них, остальное занято посторонним текстом.
	if old := evidenceLine(src, r, 140, true); !strings.Contains(old, "Лишний текст") || strings.Contains(old, "runs concurrently") {
		t.Fatalf("выключатель не вернул прежний выбор (первый кусок с обоими именами): %q", old)
	}
	// Ни в одном куске нет обоих имён — берётся первый, строка не пуста.
	r.Dst = "Borrow checker"
	if evidenceLine(src, r, 140, false) == "" {
		t.Fatal("без куска с обоими именами выдержка пропала вовсе")
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

// Год свежайшего подтверждения доходит до карточки ПО ПУТИ ДАННЫХ — от журнала
// связей через Graph.Entity до строки, а не только в отрисовке собранной руками
// структуры. До 17.09.2026 поле NeighborInfo.Evidence не заполнялось нигде,
// год в карточке не печатался никогда, а тест выше этого не видел (аудит, Б8).
// Заодно: год берётся по всем книгам связи, а не по первым записям журнала (Б9).
func TestEntityCardYearThroughDataPath(t *testing.T) {
	g, _ := graph(t)
	goID, _, _ := g.Entities().Add("Go", TypeTech)
	gr, _, _ := g.Entities().Add("goroutine", TypeConcept)
	src := yearChunks{}
	// Шесть подтверждений из старой книги идут в журнале первыми, свежая — последней.
	for i := 1; i <= 6; i++ {
		k := ChunkKey{Doc: 1, Ord: uint32(i * 10)}
		src[k.Pack()] = 2015
		must(t, g.Edges().Add(Edge{Src: goID, Dst: gr, Type: RelUses, Weight: 1, Evidence: k}))
	}
	fresh := ChunkKey{Doc: 2, Ord: 5}
	src[fresh.Pack()] = 2026
	must(t, g.Edges().Add(Edge{Src: goID, Dst: gr, Type: RelUses, Weight: 1, Evidence: fresh}))

	card, ok := g.Entity("Go", SearchOpts{})
	if !ok || len(card.Neighbors) != 1 {
		t.Fatalf("карточка: %+v %v", card, ok)
	}
	if out := RenderEntity(src, card, nil, RenderOpts{}); !strings.Contains(out, "свежайшее 2026 г.") {
		t.Fatalf("год свежайшего подтверждения не дошёл до карточки:\n%s", out)
	}
	res := g.Search("Go goroutine", SearchOpts{})
	if out := Render(src, res, RenderOpts{}); !strings.Contains(out, "свежайшее 2026 г.") || strings.Contains(out, "показано") {
		t.Fatalf("список связей: год по первым записям или осталась приписка «показано»:\n%s", out)
	}
}

// Карта понятий для модели (подмес, mix.relation_years): год свежайшей книги
// у связи виден, а цитат по-прежнему нет — ни выдержки под связью, ни раздела
// подтверждений. Шапка подмеса говорит модели «цитат здесь нет, зови kb_search»,
// и строка со страницей провоцировала бы ссылаться на непрочитанное (П10.3).
func TestRenderYearsOnlyShowsAgeWithoutQuotes(t *testing.T) {
	src := yearChunks{
		ChunkKey{Doc: 1, Ord: 1}.Pack(): 2019,
		ChunkKey{Doc: 2, Ord: 5}.Pack(): 2021,
	}
	res := SearchResult{
		Entities: []FoundEntity{{Entity: Entity{ID: 1, Name: "Docker", Type: TypeTech}, Mentions: 9, Books: 2}},
		Relations: []FoundRelation{{
			Src: "Docker", Dst: "Swarm", Type: "использует", Count: 12,
			Evidence:  ChunkKey{Doc: 1, Ord: 1},
			Evidences: []ChunkKey{{Doc: 1, Ord: 1}},
			Books:     []ChunkKey{{Doc: 1, Ord: 1}, {Doc: 2, Ord: 5}},
		}},
		Chunks: []ChunkKey{{Doc: 1, Ord: 1}},
	}
	out := Render(src, res, RenderOpts{Collection: "books", YearsOnly: true})
	if !strings.Contains(out, "свежайшее 2021 г.") {
		t.Fatalf("год свежайшей книги у связи не напечатан:\n%s", out)
	}
	if strings.Contains(out, "Подтверждения из книг") || strings.Contains(out, "текст") {
		t.Fatalf("в карту для модели попали цитаты:\n%s", out)
	}
	// Прежнее поведение подмеса — без источника кусков: ни года, ни цитат.
	if plain := Render(nil, res, RenderOpts{Collection: "books"}); strings.Contains(plain, "свежайшее") {
		t.Fatalf("без источника кусков год взяться не мог:\n%s", plain)
	}
}
