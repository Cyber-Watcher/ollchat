package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// slowEmbedder — сервер эмбеддингов, который не отвечает.
//
// Так он и ведёт себя, пока видеокарта занята сборкой графа: запрос принят,
// ответа нет.
type slowEmbedder struct{ model string }

func (s slowEmbedder) Model() string { return s.model }
func (s slowEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Вектор вопроса к графу ждёт свой короткий срок, а не общий на пакет.
//
// **Замер 30.08.2026:** вопрос к графу висел минутами, потому что наследовался
// `kb.embed_timeout` — пятнадцать минут, отведённые на векторизацию тысяч
// кусков. Для одной строки это бессмысленно: не дождались — вход в граф
// остаётся словесным, и ответ приходит сразу.
func TestGraphQueryVectorHasOwnDeadline(t *testing.T) {
	opts := Options{
		Embedder:     slowEmbedder{model: "bge-m3"},
		QueryTimeout: 150 * time.Millisecond,
	}

	done := make(chan []int8, 1)
	g := graphWithVectors(t, "bge-m3", 1024)
	go func() { done <- queryVector(context.Background(), opts, g, "вопрос") }()

	select {
	case v := <-done:
		if v != nil {
			t.Errorf("при недоступном эмбеддере вектор должен быть пустым, длина %d", len(v))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("вопрос к графу завис: срок ожидания вектора не свой, а унаследованный")
	}
}

// Умолчание короткое, а не пятнадцатиминутное.
func TestGraphQueryVectorDefaultDeadlineIsShort(t *testing.T) {
	if kb.DefaultQueryTimeout > time.Minute {
		t.Errorf("умолчание ожидания вектора вопроса = %v — слишком долго для одной строки",
			kb.DefaultQueryTimeout)
	}
	if !errors.Is(context.DeadlineExceeded, context.DeadlineExceeded) {
		t.Fatal("проверка сломана")
	}
}
