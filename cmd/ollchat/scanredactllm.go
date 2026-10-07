package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/redact"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Ключ --scan-redact-llm: обезличивание скана С МОДЕЛЬЮ, без интерфейса.
//
// Пользователи делают это в диалоге: просят модель, она зовёт scan_redact,
// читает обезличенный текст и, если видит оставшиеся имена, зовёт инструмент
// снова с clients, doctors, hide. Ключ --scan-redact проходит тот же путь
// одними правилами, без модели. Этот ключ — тот же диалог одной командой:
// та же модель и её настройки, тот же агент, тот же инструмент, те же
// песочница и правила deny. Слово владельца 06.10.2026: два ключа, «чтобы ты
// сам тоже мог тестировать этот функционал, как с моделью так и без».
//
// Почему не --ask --tools: там отклоняется всякое действие, требующее
// подтверждения, и для записи итогов приходилось держать отдельный конфиг
// (прогоны 03.10.2026, tmp/redact-test/config.toml); модель получала все
// инструменты из конфига; не было ни следа вызовов, ни кода выхода при
// утечке. Здесь подтверждать тоже некому, поэтому scan_redact разрешается
// на этот запуск целиком: файл назвал сам человек, запуская ключ. Правила
// deny остаются сильнее (Guard.Check), а других инструментов у модели нет.
//
// ЗАНИМАЕТ КАРТУ сервера на время ответа модели.

// scanRedactAsk — просьба к модели, как её пишут пользователи. Распознанные
// копии со всеми данными в ней не просятся: они делаются только по явной
// просьбе (слово владельца 07.10.2026) — ключом -formats или своей -ask.
const scanRedactAsk = "Обработай скан %s: сделай PDF с замазанными персональными данными " +
	"и .md без персональных данных."

func runScanRedactLLM(cfg *config.Config, srv *config.Server, model string, sandbox *permissions.Sandbox,
	guard *permissions.Guard, path string, args []string) error {
	fs := flag.NewFlagSet("scan-redact-llm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	formats := fs.String("formats", "", "какие файлы просить — "+redact.FormatsHelp+
		"; пусто — модель берёт умолчание инструмента ("+redact.DefaultFormats+")")
	ask := fs.String("ask", "", "своя просьба к модели вместо стандартной; %s в ней заменяется путём к скану")
	if err := fs.Parse(args); err != nil {
		return err
	}
	abs, err := sandbox.Resolve(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	rel := sandbox.Rel(abs)
	question := fmt.Sprintf(scanRedactAsk, rel)
	if *ask != "" {
		question = strings.ReplaceAll(*ask, "%s", rel)
	}
	if *formats != "" {
		if _, err := redact.ParseFormats(*formats); err != nil {
			return fmt.Errorf("-formats: %w", err)
		}
		question += fmt.Sprintf(" Передай инструменту formats=%q.", *formats)
	}

	registry, err := tools.NewRegistry([]string{tools.NameScanRedact}, tools.Options{
		Sandbox: sandbox, MaxOutputKB: cfg.Agent.MaxOutputKB,
	})
	if err != nil {
		return err
	}
	if err := guard.GrantSessionTool(tools.NameScanRedact); err != nil {
		return err
	}

	stepsPattern, err := cfg.Log.StepsPattern()
	if err != nil {
		return fmt.Errorf("log.steps_file_pattern: %w", err)
	}
	steps := steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollchat-scan-redact-llm", cfg.Log.Enabled)
	defer steps.Close()

	conv := session.New(srv.SystemPrompt)
	conv.SetToday(cfg.General.Today)
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: question})
	runner := &agent.Runner{
		Client: ollama.NewWithStall(srv.URL, srv.TimeoutDuration(), srv.ChatTimeoutDuration(),
			srv.StallTimeoutDuration(), srv.Headers),
		Model:          model,
		KeepAlive:      srv.KeepAlive,
		Options:        srv.Options,
		Think:          srv.Think,
		MaxIterations:  cfg.Agent.MaxIterations,
		MaxRetries:     cfg.Agent.MaxRetries,
		Steps:          steps,
		Turn:           "scan-redact-llm",
		Tools:          registry,
		Guard:          guard,
		ToolsSupported: true,
	}

	fmt.Fprintf(os.Stderr, "модель %s на сервере %s — занимает карту на время ответа\n", model, srv.Name)
	fmt.Fprintf(os.Stderr, "просьба: %s\n", question)
	sum, err := scanRedactEvents(os.Stdout, os.Stderr, runner.Run(context.Background(), conv))
	if err != nil {
		return err
	}
	return scanRedactVerdict(sum)
}

// scanRedactVerdict — чем кончается прогон с моделью.
//
// Код 3, как у --scan-redact: последний вызов нашёл скрытое в итоге. Код
// уходит наверх, к единственному os.Exit в main, а не вызывается здесь:
// os.Exit не ждёт defer, и журнал шагов прогона с базой знаний оставались
// незакрытыми ровно тогда, когда прогон нашёл утечку и разбирать его нужнее
// всего.
func scanRedactVerdict(sum scanRedactSummary) error {
	if sum.calls == 0 {
		return fmt.Errorf("модель не вызвала %s ни разу — файлов нет", tools.NameScanRedact)
	}
	if sum.leak {
		// Как у --scan-redact: код 3 — последний прогон нашёл скрытое в итоге.
		// Сообщение объясняет, куда делись файлы: под обычными именами их нет.
		return &exitCode{code: 3, msg: "проверка повторным распознаванием нашла скрытое в итоге: " +
			"обезличенные файлы записаны с пометкой UNVERIFIED в имени"}
	}
	return nil
}

// scanRedactSummary — что вышло из прогона с моделью.
type scanRedactSummary struct {
	calls int  // сколько раз scan_redact отработал
	leak  bool // последний отработавший прогон нашёл скрытое в итоге
}

// scanRedactEvents печатает ход прогона: вызовы инструмента (аргументы — без
// подсказок с именами, их прячет сам инструмент), сводку каждого вызова без
// текста документа и ответ модели.
func scanRedactEvents(stdout, stderr io.Writer, events <-chan agent.Event) (scanRedactSummary, error) {
	var sum scanRedactSummary
	var answer strings.Builder
	var runErr error
	started := time.Now()
	// Распознавание сорока листов идёт минутами, а сам инструмент о ходе
	// не рассказывает: без отметок ключ молчал бы от вызова до ответа.
	tick := time.NewTicker(scanRedactTick)
	defer tick.Stop()
	var toolSince time.Time
	for {
		var ev agent.Event
		select {
		case <-tick.C:
			if !toolSince.IsZero() {
				fmt.Fprintf(stderr, "[%3.0f с] инструмент работает %.0f с\n",
					time.Since(started).Seconds(), time.Since(toolSince).Seconds())
			}
			continue
		case e, ok := <-events:
			if !ok {
				fmt.Fprintf(stdout, "── ответ модели (%.0f с, вызовов %s: %d) ──\n%s\n", time.Since(started).Seconds(),
					tools.NameScanRedact, sum.calls, strings.TrimSpace(answer.String()))
				return sum, runErr
			}
			ev = e
		}
		switch ev.Kind {
		case agent.EventContent:
			answer.WriteString(ev.Text)
		case agent.EventToolPlan:
			if ev.Tool != nil {
				fmt.Fprintf(stderr, "[%3.0f с] модель зовёт %s %s\n", time.Since(started).Seconds(), ev.Tool.Name, ev.Tool.Args)
				toolSince = time.Now()
			}
		case agent.EventToolConfirm:
			// Инструмент разрешён на запуск целиком; сюда попадает только то,
			// что правила велят спросить, — а спросить некого.
			if ev.Confirm != nil {
				ev.Confirm.Reply <- agent.AnswerNo
			}
		case agent.EventToolResult:
			if ev.Tool == nil {
				continue
			}
			toolSince = time.Time{}
			fmt.Fprintf(stdout, "── %s, вызов на %.0f с ──\n", ev.Tool.Name, time.Since(started).Seconds())
			if ev.Tool.Skipped || ev.Tool.Reason != "" && !ev.Tool.OK {
				fmt.Fprintf(stdout, "не выполнен: %s\n", ev.Tool.Reason)
			}
			if ev.Tool.Name == tools.NameScanRedact && ev.Tool.OK {
				sum.calls++
				sum.leak = strings.Contains(ev.Tool.Output, redact.LeakMark)
			}
			fmt.Fprint(stdout, scanRedactHead(ev.Tool.Output))
		case agent.EventError:
			if ev.Err != nil {
				runErr = ev.Err
			}
		}
	}
}

// scanRedactTick — как часто ключ отмечает, что инструмент ещё работает.
const scanRedactTick = 15 * time.Second

// scanRedactHead — сводка вызова без текста документа: он уже в файле .md,
// а на экране он лишний.
func scanRedactHead(out string) string {
	if i := strings.Index(out, tools.ScanRedactTextMark); i >= 0 {
		out = out[:i]
	}
	return strings.TrimRight(out, "\n") + "\n"
}
