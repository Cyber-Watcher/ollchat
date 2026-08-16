package agent_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itpro/ollchat/internal/agent"
	"github.com/itpro/ollchat/internal/config"
	"github.com/itpro/ollchat/internal/kb"
	"github.com/itpro/ollchat/internal/kbembed"
	"github.com/itpro/ollchat/internal/ollama"
	"github.com/itpro/ollchat/internal/permissions"
	"github.com/itpro/ollchat/internal/session"
	"github.com/itpro/ollchat/internal/tools"
)

// TestLiveKBAnswerIsExplanation — ответ по книгам должен быть объяснением
// со ссылками, а не подборкой цитат.
//
// Живой случай: на вопрос «как работают горутины» модель выдала только выдержки
// из книг, ничего не объяснив. Виновата была формулировка «ОБЯЗАТЕЛЬНО
// ссылайся… не додумывай» — модель поняла её буквально.
func TestLiveKBAnswerIsExplanation(t *testing.T) {
	url := os.Getenv("OLLCHAT_TEST_SERVER")
	model := os.Getenv("OLLCHAT_TEST_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_TEST_SERVER и OLLCHAT_TEST_MODEL")
	}
	cfg, _, err := config.Load(os.Getenv("HOME") + "/.config/ollchat/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		t.Skip(err)
	}
	defer base.Close()

	sb, err := permissions.NewSandbox(".", false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameKBSearch, tools.NameKBRead}, tools.Options{
		Sandbox: sb, MaxOutputKB: 64, KB: base, KBDir: cfg.KB.Dir,
		KBTopK: cfg.KB.TopK, KBMaxPerBook: cfg.KB.MaxPerBook,
		Semantic: cfg.KB.Semantic, SemanticWeight: cfg.KB.SemanticWeight,
		Embedder: kbembed.New(cfg, url, 10*time.Minute, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile(nil, nil, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	guard := permissions.NewGuard(set, sb, permissions.ModeYolo)

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "Скажи мне как работают горутины?"})

	r := &agent.Runner{
		Client: ollama.New(url, 10*time.Minute, nil), Model: model,
		Tools: reg, Guard: guard, ToolsSupported: true, MaxIterations: 6,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	start := time.Now()
	var answer strings.Builder
	var calls []string
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case agent.EventContent:
			answer.WriteString(ev.Text)
		case agent.EventToolResult:
			if ev.Tool != nil {
				calls = append(calls, ev.Tool.Title)
				t.Logf("  [%s] инструмент: %s", time.Since(start).Round(time.Second), ev.Tool.Title)
			}
		case agent.EventError:
			t.Fatalf("обмен не удался: %v", ev.Err)
		}
	}
	got := answer.String()
	t.Logf("инструментов вызвано: %d %v", len(calls), calls)
	t.Logf("длина ответа: %d символов", len([]rune(got)))
	t.Logf("─── ответ ───\n%s", got)

	if !strings.Contains(got, "[1]") && !strings.Contains(got, "стр.") {
		t.Error("в ответе нет ссылок на источники")
	}
}
