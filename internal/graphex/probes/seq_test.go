package probes

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// TestParseChunkKeys — разбор списка кусков «13#7,4#109»: обычный список,
// лишние запятые и пробелы, пустая строка, неверная запись.
func TestParseChunkKeys(t *testing.T) {
	keys, err := parseChunkKeys("13#7,4#109")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	want := []graph.ChunkKey{{Doc: 13, Ord: 7}, {Doc: 4, Ord: 109}}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("keys = %v, хочу %v", keys, want)
	}
}

func TestParseChunkKeysTrimsAndSkipsEmpty(t *testing.T) {
	keys, err := parseChunkKeys(" 13#7 , ,4#109 ,")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	want := []graph.ChunkKey{{Doc: 13, Ord: 7}, {Doc: 4, Ord: 109}}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("keys = %v, хочу %v", keys, want)
	}
}

func TestParseChunkKeysEmpty(t *testing.T) {
	keys, err := parseChunkKeys("")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %v, хочу пусто", keys)
	}
}

func TestParseChunkKeysInvalid(t *testing.T) {
	if _, err := parseChunkKeys("не-кусок"); err == nil {
		t.Fatal("ожидал ошибку на неверной записи куска")
	}
}

// TestVerdict — подпись равенства для сообщений о расхождении.
func TestVerdict(t *testing.T) {
	if got := verdict(true); got != "равны" {
		t.Fatalf("verdict(true) = %q, хочу \"равны\"", got)
	}
	if got := verdict(false); got != "разные" {
		t.Fatalf("verdict(false) = %q, хочу \"разные\"", got)
	}
}
