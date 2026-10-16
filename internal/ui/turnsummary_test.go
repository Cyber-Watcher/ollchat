package ui

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Ц8 (этап 112): после хода с вызовами инструментов в ленте строка итога;
// ход без вызовов и выключенная настройка строки не дают.
func TestTurnSummaryLine(t *testing.T) {
	stats := &agent.ToolStats{Calls: 5, Rejected: 2, Failed: 1}
	const want = "Итог хода: вызовов инструментов 5, отклонено 2, с ошибкой 1"
	cases := []struct {
		name  string
		on    bool
		tools *agent.ToolStats
		lines int
	}{
		{"включено, вызовы были", true, stats, 1},
		{"включено, вызовов нет", true, &agent.ToolStats{}, 0},
		{"включено, счёта нет", true, nil, 0},
		{"выключено", false, stats, 0},
	}
	for _, c := range cases {
		m := newTestModelWith(t, func(cfg *config.Config) { cfg.Agent.TurnSummary = c.on })
		m.streaming = true
		m.handleAgentEvent(agent.Event{Kind: agent.EventTurnDone, Tools: c.tools})
		if n := countBlocks(m, want); n != c.lines {
			t.Errorf("%s: строк итога %d, ждали %d", c.name, n, c.lines)
		}
		if n := countBlocks(m, "Итог хода:"); n != c.lines {
			t.Errorf("%s: строк «Итог хода:» %d, ждали %d", c.name, n, c.lines)
		}
	}
}
