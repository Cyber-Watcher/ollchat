package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/redact"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Утечка в итоге — код выхода 3 через main, а не os.Exit посреди прогона:
// тот не ждал defer, и журнал шагов оставался незакрытым.
func TestScanRedactVerdictExitCode(t *testing.T) {
	// Сообщение — о том, куда делись файлы: под обычными именами их нет,
	// они записаны с пометкой UNVERIFIED.
	code, msg := exitStatus(scanRedactVerdict(scanRedactSummary{calls: 2, leak: true}))
	if code != 3 || !strings.Contains(msg, "UNVERIFIED") {
		t.Errorf("утечка: код %d, сообщение %q; ожидался код 3 и пометка UNVERIFIED", code, msg)
	}
	if code, _ := exitStatus(scanRedactVerdict(scanRedactSummary{calls: 1})); code != 0 {
		t.Errorf("чистый итог: код %d", code)
	}
	if code, msg := exitStatus(scanRedactVerdict(scanRedactSummary{})); code != 1 || msg == "" {
		t.Errorf("модель не вызвала инструмент: код %d, сообщение %q", code, msg)
	}
}

// Код выхода доезжает и сквозь обёртку ошибки, справка — не сбой.
func TestExitStatus(t *testing.T) {
	if code, _ := exitStatus(fmt.Errorf("прогон: %w", &exitCode{code: 3})); code != 3 {
		t.Errorf("обёрнутый код: %d", code)
	}
	if code, msg := exitStatus(flag.ErrHelp); code != 0 || msg != "" {
		t.Errorf("справка: код %d, %q", code, msg)
	}
	if code, msg := exitStatus(errors.New("сломалось")); code != 1 || msg != "сломалось" {
		t.Errorf("обычная ошибка: код %d, %q", code, msg)
	}
}

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
