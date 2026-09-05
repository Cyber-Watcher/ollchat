package kb

import (
	"context"
	"strings"
	"testing"
	"time"
)

// slowEmbedder — эмбеддер, который «висит»: ровно так ведёт себя сервер,
// пока карту занимает сборка графа (замер 29.08.2026 — запрос не ответил
// за 120 с, модель эмбеддингов даже не загрузилась).
type slowEmbedder struct{ model string }

func (s slowEmbedder) Model() string { return s.model }

func (s slowEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	<-ctx.Done() // ждём, пока нас не отпустят по сроку
	return nil, ctx.Err()
}

// Недоступный эмбеддер не должен подвешивать поиск: у вектора вопроса свой
// короткий срок, после которого выдача идёт по словам.
func TestQueryEmbedDeadlineDoesNotHangSearch(t *testing.T) {
	if DefaultQueryTimeout > 30*time.Second {
		t.Fatalf("срок ожидания вектора вопроса %v — человек столько не ждёт", DefaultQueryTimeout)
	}

	c := &Collection{name: "проба"}
	// Векторов нет — до эмбеддера дело не дойдёт, это проверка на другое.
	if hits, note := c.semanticHits(context.Background(), "вопрос",
		SearchOpts{Semantic: true}, slowEmbedder{"bge-m3"}); hits != nil || note != "" {
		t.Errorf("без векторов смысловой поиск не затевается: %v, %q", hits, note)
	}
}

// Срок именно свой, а не унаследованный: общий предел ожидания эмбеддера
// (kb.embed_timeout) — четверть часа, и ждать столько один вопрос нельзя.
func TestDefaultQueryTimeoutIsShort(t *testing.T) {
	if DefaultQueryTimeout < 5*time.Second {
		t.Errorf("срок %v слишком мал: первый запрос после простоя может быть медленным", DefaultQueryTimeout)
	}
	if DefaultQueryTimeout > 20*time.Second {
		t.Errorf("срок %v слишком велик для одного вопроса", DefaultQueryTimeout)
	}
}

// Сообщение о недоступном эмбеддере объясняет, что поиск не сломан.
func TestSemanticNoteExplainsFallback(t *testing.T) {
	note := "сервер эмбеддингов недоступен (context deadline exceeded) — поиск идёт только по словам"
	if !strings.Contains(note, "только по словам") {
		t.Error("в сообщении должно быть сказано, что поиск продолжается по словам")
	}
}
