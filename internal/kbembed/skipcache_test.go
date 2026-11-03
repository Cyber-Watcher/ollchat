package kbembed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// SkipDiskCache (--kb-eval-fresh): кэш на диске не читается и не пишется —
// вектор вопроса приходит с сервера, который сейчас проверяется, а не из
// queries.cache, куда его положил прежний сервер.
func TestSkipDiskCacheLeavesNoFile(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0, 0}}})
	}))
	defer srv.Close()

	dir := t.TempDir()
	SkipDiskCache = true
	defer func() { SkipDiskCache = false }()
	e := New(Options{URL: srv.URL, Model: "bge-m3", CacheDir: dir}, "", 5*time.Second, nil)
	if _, err := e.Embed(context.Background(), []string{"вопрос только для этого теста 7f3a"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("запросов к серверу %d, ждали 1", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "queries.cache")); !os.IsNotExist(err) {
		t.Errorf("при SkipDiskCache появился файл кэша: %v", err)
	}
}
