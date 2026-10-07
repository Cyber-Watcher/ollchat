package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// panicTool падает там, где велено: при подготовке вызова или при запуске.
type panicTool struct{ inRun bool }

func (panicTool) Name() string { return "boom" }

func (panicTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{Name: "boom"}}
}

func (p panicTool) Plan(map[string]any) (*tools.Plan, error) {
	if !p.inRun {
		// Паника посреди Plan, как от записи в пустую карту в настоящем
		// инструменте. Явный panic, а не сама запись: такую запись линтер
		// справедливо считает ошибкой, а здесь паника и нужна.
		panic("assignment to entry in nil map")
	}
	return &tools.Plan{
		Tool:  "boom",
		Req:   permissions.Request{Kind: permissions.KindRead, Target: "/", Tool: "boom", Fixed: true},
		Title: "boom()",
		Run: func(context.Context) (string, error) {
			var p *tools.Plan
			return p.Title, nil // разыменование nil
		},
	}, nil
}

func toolCall(name string) []ollama.Event {
	return []ollama.Event{
		{Kind: ollama.EventToolCalls, ToolCalls: []ollama.ToolCall{{
			ID: "call_" + name, Function: ollama.ToolCallFunc{Name: name, Arguments: map[string]any{}},
		}}},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "stop"}},
	}
}

// Паника в инструменте — ошибка вызова, а не падение программы: раньше она
// уходила в горутину агента без recover, процесс падал, а терминал оставался
// в alt-screen. Модель получает объяснение, ход продолжается.
func TestToolPanicBecomesToolError(t *testing.T) {
	prev := planTool
	t.Cleanup(func() { planTool = prev })
	planTool = func(_ *tools.Registry, name string, args map[string]any) (*tools.Plan, error) {
		return panicTool{inRun: name == "boom_run"}.Plan(args)
	}

	f := &fakeChat{turns: [][]ollama.Event{toolCall("boom_plan"), toolCall("boom_run"), answer("справился")}}
	r := fakeRunner(t, f)
	text, results, stats, err := runAll(t, r, AnswerYes)
	if err != nil {
		t.Fatalf("ход оборвался: %v", err)
	}
	if text != "справился" {
		t.Fatalf("ответ после паники: %q", text)
	}
	if len(results) != 2 {
		t.Fatalf("результатов %d, ожидалось 2: %+v", len(results), results)
	}
	for i, res := range results {
		if res.OK || !strings.Contains(res.Output, "паника") {
			t.Errorf("вызов %d: паника не превращена в ошибку: %+v", i, res)
		}
	}
	if stats.Rejected != 1 || stats.Failed != 1 {
		t.Errorf("счёт вызовов: %+v", *stats)
	}
	// Модель узнала о сбое из ответа инструмента.
	for i, turn := range f.seen[1:] {
		last := turn.Messages[len(turn.Messages)-1]
		if last.Role != ollama.RoleTool || !strings.Contains(last.Content, "паника") {
			t.Errorf("запрос %d: модели не сообщено о сбое: %+v", i+2, last)
		}
	}
}
