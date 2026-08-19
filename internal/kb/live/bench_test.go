package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// TestEmbedThroughput меряет, во что упирается векторизация: в размер пачки
// или в то, что пачки идут по одной.
func TestEmbedThroughput(t *testing.T) {
	url := os.Getenv("OLLCHAT_EMBED_URL")
	if url == "" {
		t.Skip("нужен OLLCHAT_EMBED_URL")
	}
	c := ollama.New(url, 10*time.Minute, 0, nil)
	text := strings.Repeat("буферизированный канал в языке Go и его ёмкость. ", 30)

	for _, batch := range []int{32, 64, 128, 256} {
		for _, conc := range []int{1, 2, 4} {
			inputs := make([]string, batch)
			for i := range inputs {
				inputs[i] = fmt.Sprintf("%d %s", i, text)
			}
			start := time.Now()
			var wg sync.WaitGroup
			var mu sync.Mutex
			var failed error
			for k := 0; k < conc; k++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := c.Embed(context.Background(), ollama.EmbedRequest{
						Model: "bge-m3", Input: inputs, KeepAlive: "5m",
					})
					if err != nil {
						mu.Lock()
						failed = err
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if failed != nil {
				t.Logf("пачка %-3d × %d потоков: ОШИБКА %v", batch, conc, failed)
				continue
			}
			spent := time.Since(start)
			done := batch * conc
			t.Logf("пачка %-3d × %d потоков: %6.1f кусков/с  (%d кусков за %s) → 249к за %s",
				batch, conc, float64(done)/spent.Seconds(), done, spent.Round(time.Millisecond),
				(time.Duration(float64(spent) / float64(done) * 249056)).Round(time.Minute))
		}
	}
}
