package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// newOpenAIModel — модель интерфейса, чей сервер чата говорит на API OpenAI (этап 127).
func newOpenAIModel(t *testing.T) *Model {
	t.Helper()
	return newTestModelWith(t, func(c *config.Config) { c.Servers[0].API = config.APIOpenAI })
}

// Окно OpenAI-сервера задаёт запуск движка: /context set и /context max
// отказывают с объяснением и окна не меняют, у сервера Ollama — как прежде.
func TestContextSetRefusedOnOpenAIServer(t *testing.T) {
	m := newOpenAIModel(t)
	before := m.meter.Capacity
	for _, arg := range []string{"set 8192", "max"} {
		if cmd := m.contextCmd(arg); cmd != nil {
			t.Errorf("/context %s: ждали отказ без команды", arg)
		}
		last := m.blocks[len(m.blocks)-1]
		if last.kind != blockError || !strings.Contains(last.text, "API OpenAI") {
			t.Errorf("/context %s: ждали отказ про API OpenAI, получили %q", arg, last.text)
		}
	}
	if m.meter.Capacity != before {
		t.Errorf("окно изменилось: %d → %d", before, m.meter.Capacity)
	}
	if _, ok := m.server.Options["num_ctx"]; ok {
		t.Error("num_ctx записан в options сервера OpenAI")
	}

	o := newTestModel(t)
	o.contextCmd("set 8192")
	if last := o.blocks[len(o.blocks)-1]; strings.Contains(last.text, "API OpenAI") {
		t.Errorf("сервер Ollama получил отказ для OpenAI: %q", last.text)
	}
}

// /ps у OpenAI-сервера не говорит «нет загруженных моделей» — такого пути у API
// нет, и это была бы неправда; запроса к серверу при этом нет.
func TestPSOnOpenAIServer(t *testing.T) {
	m := newOpenAIModel(t)
	cmd := m.psCmd()
	if cmd == nil {
		t.Fatal("/ps: ждали команду с объяснением")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok || !strings.Contains(msg.text, "API OpenAI") || strings.Contains(msg.text, "нет загруженных") {
		t.Errorf("/ps у сервера OpenAI: %#v", cmd())
	}
}

// /calc считает по памяти и окну Ollama — у OpenAI-сервера отказ с объяснением.
func TestCalcRefusedOnOpenAIServer(t *testing.T) {
	m := newOpenAIModel(t)
	if cmd := m.calcArgCmd("4"); cmd != nil {
		t.Error("/calc 4: ждали отказ без команды")
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError || !strings.Contains(last.text, "API OpenAI") {
		t.Errorf("/calc: ждали отказ про API OpenAI, получили %q", last.text)
	}
}
