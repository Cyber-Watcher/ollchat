package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Смена потолка шагов агента командой `/tools iterations` пишется в журнал
// шагов (14.10.2026): по журналам рабочей машины ход на 279 обращений к модели
// при конфиге 60 нечем было объяснить — `off` менял потолок только в памяти.
func TestToolsIterationsLogged(t *testing.T) {
	m := newTestModel(t)
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	m.steps = steplog.New(t.TempDir(), pat, time.Now(), "test", true)
	start := m.cfg.Agent.MaxIterations

	m.toolsCmd("iterations off")
	m.toolsCmd("iterations 50")
	m.toolsCmd("iterations ноль") // ошибка ввода — не смена, записи нет

	if err := m.steps.LastError(); err != nil {
		t.Fatalf("журнал шагов: %v", err)
	}
	data, err := os.ReadFile(m.steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]float64
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		s, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка журнала %q: %v", line, err)
		}
		if s.Kind != steplog.KindSetting {
			continue
		}
		if s.Extra["key"] != "agent.max_iterations" {
			t.Fatalf("ключ настройки: %+v", s.Extra)
		}
		got = append(got, [2]float64{s.Extra["from"].(float64), s.Extra["to"].(float64)})
	}
	want := [][2]float64{{float64(start), -1}, {-1, 50}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("записи смены потолка: %v, ждали %v", got, want)
	}
	if m.cfg.Agent.MaxIterations != 50 {
		t.Fatalf("потолок после команд: %d", m.cfg.Agent.MaxIterations)
	}
}
