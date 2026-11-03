package census

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// Ни одной пачки из одного текста: одиночный текст эмбеддер отдаёт из кеша
// по паре «модель, текст» без адреса сервера, и второй сервер получил бы
// вектор первого — сверка «сервер против сервера» мерила бы кеш.
func TestEmbedBatchesNeverSendsSingleText(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		sizes = append(sizes, len(req.Input))
		mu.Unlock()
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{1, 0, 0, float32(i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	defer srv.Close()

	e := kbembed.New(kbembed.Options{URL: srv.URL, Model: "bge-m3"}, "", 5*time.Second, nil)
	for _, n := range []int{2, 3, 16, 17, 33, 40} {
		sizes = nil
		texts := make([]string, n)
		for i := range texts {
			texts[i] = "кусок " + strconv.Itoa(i)
		}
		got, err := embedBatches(context.Background(), e, texts)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(got) != n {
			t.Fatalf("n=%d: векторов %d", n, len(got))
		}
		total := 0
		for _, s := range sizes {
			if s < 2 {
				t.Errorf("n=%d: пачка из %d текста — ушла бы в кеш (пачки %v)", n, s, sizes)
			}
			total += s
		}
		if total != n {
			t.Errorf("n=%d: отправлено %d текстов (пачки %v)", n, total, sizes)
		}
	}
}
