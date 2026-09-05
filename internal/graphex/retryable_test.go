package graphex

import (
	"errors"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Повторяется то, что клиент Ollama пометил повторяемым: сеть, 5xx, обрыв
// потока. Голый текст ошибки признаком больше не является.
func TestRetryableFollowsOllamaMark(t *testing.T) {
	if !retryable(ollama.MarkRetryable(errors.New("dial tcp: connection refused"))) {
		t.Fatal("помеченный сбой дороги должен повторяться")
	}
	for _, err := range []error{nil, errors.New("connection refused"), errors.New("500"), ollama.ErrCanceled} {
		if retryable(err) {
			t.Fatalf("непомеченная ошибка %v не должна повторяться", err)
		}
	}
}
