package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/redact"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Ход прогона с моделью: на экран — вызов и его сводка, но не текст
// документа; утечка в последнем вызове узнаётся по метке проверки.
func TestScanRedactEvents(t *testing.T) {
	out := "Скан a.pdf обработан: страниц 1.\n" + redact.LeakMark + ": в PDF — ничего; в .md — адрес.\n" +
		"\n" + tools.ScanRedactTextMark + " …\n\n# a\n\nтекст документа CLIENT\n"
	ch := make(chan agent.Event, 4)
	ch <- agent.Event{Kind: agent.EventToolPlan, Tool: &agent.ToolEvent{Name: tools.NameScanRedact, Args: `{"path":"a.pdf"}`}}
	ch <- agent.Event{Kind: agent.EventToolResult, Tool: &agent.ToolEvent{Name: tools.NameScanRedact, Output: out, OK: true}}
	ch <- agent.Event{Kind: agent.EventContent, Text: "Готово."}
	close(ch)
	var stdout, stderr bytes.Buffer
	sum, err := scanRedactEvents(&stdout, &stderr, ch)
	if err != nil {
		t.Fatal(err)
	}
	if sum.calls != 1 || !sum.leak {
		t.Errorf("вызовов %d, утечка %v — а должно быть 1 и true", sum.calls, sum.leak)
	}
	if s := stdout.String(); strings.Contains(s, "текст документа") || !strings.Contains(s, "обработан") ||
		!strings.Contains(s, "Готово.") {
		t.Errorf("вывод:\n%s", s)
	}
	if !strings.Contains(stderr.String(), `{"path":"a.pdf"}`) {
		t.Errorf("вызов не показан: %s", stderr.String())
	}
}
