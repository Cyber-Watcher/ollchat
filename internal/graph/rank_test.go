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
