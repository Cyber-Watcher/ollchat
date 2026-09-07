package graph

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// источник кусков для проверки: пара «книга+номер» → текст.
type memChunks map[[2]uint32]string

func (m memChunks) ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool) {
	t, ok := m[[2]uint32{doc, ord}]
	if !ok {
		return kb.ChunkInfo{}, false
	}
	return kb.ChunkInfo{Text: t}, true
}

// TestRankWithPrefersAnswerOverMentions — подтверждение должно отвечать
// на вопрос, а не просто содержать все понятия разом.
//
// Случай не выдуман: на живом графе вопрос про сообщества в GraphRAG получал
// в подтверждения отзывы с обложки, где перечислены все понятия книги.
func TestRankWithPrefersAnswerOverMentions(t *testing.T) {
	cover := "Praise for this book: GraphRAG, knowledge graphs, communities, agents, memory, " +
		"governance — the definitive guide. — Principal Engineer, Nike"
	onPoint := "The system partitions the knowledge graph into communities and generates " +
		"community summaries for these groups in a bottom-up recursive manner."

	src := memChunks{
		{1, 0}:  cover,
		{1, 74}: onPoint,
	}
	rank := RankWith(src)
	if rank == nil {
		t.Fatal("отбор не собрался")
	}
	// Порядок кандидатов от графа: обложка идёт первой, как и было на живом графе.
	got := rank("community summaries in GraphRAG", []ChunkKey{{Doc: 1, Ord: 0}, {Doc: 1, Ord: 74}}, 1)
	if len(got) != 1 || got[0].Ord != 74 {
		t.Fatalf("выбрана не та страница: %+v", got)
	}

	// Пустой источник и пустой вопрос не должны ронять отбор.
	if RankWith(nil) != nil {
		t.Fatal("без источника отбора быть не может")
	}
	if got := rank("", []ChunkKey{{Doc: 1, Ord: 0}}, 1); len(got) != 1 {
		t.Fatalf("пустой вопрос сломал отбор: %+v", got)
	}
	// Кусок, которого нет в источнике, просто пропускается.
	if got := rank("communities", []ChunkKey{{Doc: 9, Ord: 9}, {Doc: 1, Ord: 74}}, 1); len(got) != 1 || got[0].Doc != 1 {
		t.Fatalf("пропавший кусок сломал отбор: %+v", got)
	}
}

// источник кусков с векторами: тексты плюс вектор у части кусков.
type memChunksVec struct {
	memChunks
	vec map[[2]uint32][]int8
}

func (m memChunksVec) ChunkVectorByRef(doc, ord uint32) ([]int8, bool) {
	v, ok := m.vec[[2]uint32{doc, ord}]
	return v, ok
}

// Со смыслом кусок на другом языке обгоняет кусок, где слов вопроса больше,
// а смысла меньше (оглавление). Без вектора — прежний порядок по словам.
//
// Замер 07.09.2026: по английскому вопросу «configuring CoreDNS» перевод
// книги не попадал в подтверждения ни разу — русский кусок содержит одно
// слово вопроса из двух, любой английский оба.
func TestRankWithVectorSeesThroughLanguage(t *testing.T) {
	good := ChunkKey{Doc: 104, Ord: 682} // английский, по делу
	toc := ChunkKey{Doc: 104, Ord: 21}   // английское оглавление: все слова, смысла нет
	rus := ChunkKey{Doc: 105, Ord: 732}  // перевод: одно слово вопроса, смысл тот же
	src := memChunksVec{
		memChunks: memChunks{
			{104, 682}: "CoreDNS is configured through the Corefile and its plugins",
			{104, 21}:  "configuring CoreDNS 203, configuring kubelet 176, configuring CoreDNS plugins 203",
			{105, 732}: "CoreDNS настраивается через Corefile и его плагины",
		},
		vec: map[[2]uint32][]int8{
			{104, 682}: {90, 90, 0, 0},
			{104, 21}:  {0, 127, 0, 0},
			{105, 732}: {120, 10, 0, 0},
		},
	}
	q := []int8{127, 0, 0, 0}
	cands := []ChunkKey{good, toc, rus}

	words := RankWith(src)("configuring CoreDNS", cands, 3)
	if words[len(words)-1] != rus {
		t.Fatalf("по словам перевод должен быть последним: %v", words)
	}
	sense := RankWithVector(src, q, 0)("configuring CoreDNS", cands, 3)
	if len(sense) != 3 {
		t.Fatalf("потеряны кандидаты: %v", sense)
	}
	pos := map[ChunkKey]int{}
	for i, k := range sense {
		pos[k] = i
	}
	if pos[rus] > pos[toc] {
		t.Fatalf("со смыслом перевод обязан обогнать оглавление: %v", sense)
	}
	if pos[good] > pos[toc] {
		t.Fatalf("кусок по делу обязан обогнать оглавление: %v", sense)
	}
}

// Кусок без вектора не выпадает из подтверждений: долитые книги ждут
// --kb-embed, и молча терять их нельзя.
func TestRankWithVectorKeepsVectorlessChunks(t *testing.T) {
	src := memChunksVec{
		memChunks: memChunks{
			{1, 1}: "configuring CoreDNS",
			{2, 1}: "configuring CoreDNS in detail",
		},
		vec: map[[2]uint32][]int8{{1, 1}: {127, 0}},
	}
	got := RankWithVector(src, []int8{127, 0}, 0)("configuring CoreDNS", []ChunkKey{{1, 1}, {2, 1}}, 2)
	if len(got) != 2 {
		t.Fatalf("кусок без вектора выпал: %v", got)
	}
}

// Без вектора вопроса RankWithVector — это RankWith: порядок по словам.
func TestRankWithVectorWithoutQueryIsWords(t *testing.T) {
	src := memChunksVec{memChunks: memChunks{{1, 1}: "a b", {1, 2}: "a b c"}}
	cands := []ChunkKey{{1, 1}, {1, 2}}
	a := RankWith(src)("a b c", cands, 2)
	b := RankWithVector(src, nil, 0)("a b c", cands, 2)
	if len(a) != len(b) || a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("порядки разошлись: %v против %v", a, b)
	}
}

// Оглавление в подтверждения не идёт, пока есть что-то ещё; когда кроме него
// ничего нет — остаётся (пустые подтверждения хуже плохих).
func TestRankDropsTableOfContents(t *testing.T) {
	toc := "DNS in Kubernetes 192\n10.1 A brief intro to DNS (and CoreDNS) 192\n" +
		"NXDOMAINs, A records, and CNAME records 193\nConfiguring CoreDNS 203\n"
	src := memChunks{
		{104, 21}:  toc,
		{104, 682}: "CoreDNS is powered by plugins; you read the Corefile from the top down",
	}
	got := RankWith(src)("configuring CoreDNS", []ChunkKey{{104, 21}, {104, 682}}, 2)
	if len(got) != 1 || got[0] != (ChunkKey{104, 682}) {
		t.Fatalf("оглавление осталось в подтверждениях: %v", got)
	}
	only := RankWith(src)("configuring CoreDNS", []ChunkKey{{104, 21}}, 2)
	if len(only) != 1 {
		t.Fatalf("единственный кусок выброшен: %v", only)
	}
	// Два оглавления и ничего больше — оба остаются.
	src[[2]uint32{105, 12}] = "Глава 10. DNS в Kubernetes 201\n10.1 Введение в DNS 201\nЗаписи A и CNAME 202\nНастройка CoreDNS 212\n"
	both := RankWith(src)("configuring CoreDNS", []ChunkKey{{104, 21}, {105, 12}}, 2)
	if len(both) != 2 {
		t.Fatalf("при одних оглавлениях выдача опустела: %v", both)
	}
}
