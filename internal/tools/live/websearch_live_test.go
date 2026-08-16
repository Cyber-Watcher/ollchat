package live

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itpro/ollchat/internal/permissions"
	"github.com/itpro/ollchat/internal/tools"
)

// TestLiveWebSearch — настоящий поиск через свой SearXNG.
func TestLiveWebSearch(t *testing.T) {
	url := os.Getenv("OLLCHAT_SEARX_URL")
	if url == "" {
		t.Skip("нужен OLLCHAT_SEARX_URL")
	}
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameWebSearch}, tools.Options{
		Sandbox: sb, MaxOutputKB: 64, SearxURL: url, SearxTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ollama api embed", "последняя версия Go 2026"} {
		plan, err := reg.Plan(tools.NameWebSearch, map[string]any{"query": q, "limit": 3})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		out, err := plan.Run(context.Background())
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		t.Logf("── %q за %s", q, time.Since(start).Round(time.Millisecond))
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "http") {
				t.Logf("   %s", trim(line, 88))
			}
		}
		if !strings.Contains(out, "http") {
			t.Errorf("по запросу %q нет ни одной ссылки", q)
		}
	}
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
