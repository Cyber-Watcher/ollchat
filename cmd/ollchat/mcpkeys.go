package main

// Ключи проверки работы с серверами MCP и общей библиотекой без TUI (этап 121,
// А5, В1–В5): `--mcp-servers`, `--mcp-ping`, `--mcp-tools`, `--mcp-call`,
// `--mcp-registry`, `--kb-remote-check`. Читают те же настройки, реестр
// и права, что TUI, и ничего не пишут; ключ доступа в вывод не попадает.
// Отчёты общие с командой /mcp (internal/mcphub), а не свои.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/buildinfo"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/mcphub"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// clientVersion — версия ollchat для clientInfo серверов MCP: первое слово
// строки сборки, как у --version.
func clientVersion() string {
	if f := strings.Fields(buildinfo.Describe(version)); len(f) > 0 {
		return f[0]
	}
	return buildinfo.Unknown
}

// mcpKeyRequested — запрошен ключ, которому нужны только серверы MCP и права
// (В1–В4): ему не нужны ни база, ни модель, ни реестр.
func mcpKeyRequested(f *cliFlags) bool {
	return *f.mcpServers || *f.mcpPing != "" || *f.mcpTools != "" || *f.mcpCall != ""
}

// mcpKeyTimeout — срок ключа доступа (token_cmd) и подключения у ключей
// --mcp-ping, --mcp-tools, --mcp-call и шагов MCP в --kb-remote-check (аудит
// 23.10.2026: с context.Background() зависшая команда ключа держала ключ
// бесконечно). Тот же, что у /mcp reconnect в TUI (ui.mcpReconnectSeconds):
// человек сам попросил и ждёт, а 3 с запуска (mcphub.ConnectTimeout) — для
// того, кто ничего не просил. Сам вызов инструмента ограничен сроком сервера
// (mcp.servers.*.timeout), а не этим. Переменная — чтобы тест не ждал 30 с.
var mcpKeyTimeout = 30 * time.Second

// hubCloseTimeout — сколько выход из программы ждёт конца сеансов MCP.
const hubCloseTimeout = 2 * time.Second

// closeHub заканчивает сеансы серверов MCP на выходе (DELETE, mcphub.Hub.Close):
// иначе сервер держит брошенный сеанс до своего срока (аудит 23.10.2026).
// Зовётся одним defer в run сразу за mcphub.New — так его получают и TUI,
// и ключи --mcp-*, --mcp-registry, --kb-remote-check. Не дольше
// hubCloseTimeout: выход не ждёт медленный сервер; ошибку показать некому
// (TUI уже закрыт, у ключей итог от неё не меняется).
func closeHub(hub *mcphub.Hub) {
	ctx, cancel := context.WithTimeout(context.Background(), hubCloseTimeout)
	defer cancel()
	_ = hub.Close(ctx)
}

// runMCPKey исполняет --mcp-servers, --mcp-ping, --mcp-tools или --mcp-call.
// egressCheck — agent.private_egress_check: для --mcp-call, как для модели.
//
// Всё, что ключ печатает, и текст его ошибки проходят через termSafe: имя
// и версия сервера, описания и ответы инструментов — чужой текст.
func runMCPKey(f *cliFlags, hub *mcphub.Hub, guard *permissions.Guard, egressCheck bool, out, errOut io.Writer) error {
	out, errOut = termWriter{out}, termWriter{errOut}
	connect := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), mcpKeyTimeout)
	}
	switch {
	case *f.mcpServers:
		fmt.Fprintln(out, hub.Report())
		return nil
	case *f.mcpPing != "":
		name := *f.mcpPing
		fmt.Fprintf(errOut, "рукопожатие с сервером MCP %s (не дольше %s)…\n", name, mcpKeyTimeout)
		ctx, cancel := connect()
		defer cancel()
		info, err := hub.Ping(ctx, name)
		if err != nil {
			return termErr(err)
		}
		fmt.Fprintln(out, mcphub.PingReport(name, info))
		return nil
	case *f.mcpTools != "":
		name := *f.mcpTools
		fmt.Fprintf(errOut, "подключаюсь к серверу MCP %s (не дольше %s)…\n", name, mcpKeyTimeout)
		ctx, cancel := connect()
		defer cancel()
		if err := hub.Reconnect(ctx, name); err != nil {
			return termErr(err)
		}
		rep, err := hub.ToolsReport(name)
		if err != nil {
			return termErr(err)
		}
		fmt.Fprintln(out, rep)
		return nil
	case *f.mcpCall != "":
		return runMCPCall(context.Background(), hub, guard, egressCheck, *f.mcpCall, f.args, *f.mcpYes, out, errOut)
	}
	return nil
}

// termSafe — чужой текст для терминала: управляющие знаки C0, DEL и C1
// печатаются видимо (`\x1b`, `\r`), как в показе конфига проекта (visible);
// перевод строки и табуляция остаются как есть. ESC-последовательность
// в ответе сервера иначе чистит экран, перекрашивает вывод или меняет
// заголовок окна (OSC), а `\r` затирает напечатанное (аудит 23.10.2026).
func termSafe(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		for j, part := range strings.Split(line, "\t") {
			if j > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(visible(part))
		}
	}
	return b.String()
}

// termWriter пропускает всё, что пишут ключи, через termSafe. fmt.Fprint*
// отдаёт строку одним Write, так что многобайтовый знак не рвётся.
type termWriter struct{ w io.Writer }

func (t termWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(t.w, termSafe(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// termError — ошибка ключа с текстом через termSafe: её печатает main
// в stderr, а в ней бывают строки сервера. Цепочка (errors.Is/As) цела.
type termError struct{ err error }

func (e termError) Error() string { return termSafe(e.err.Error()) }
func (e termError) Unwrap() error { return e.err }

// termErr оборачивает err в termError; nil — nil.
func termErr(err error) error {
	if err == nil {
		return nil
	}
	return termError{err}
}

// runMCPCall — один вызов инструмента через тот же охранник, что в TUI (В4):
// запрет — отказ всегда; «спросить» — отказ, пока не задан --mcp-yes
// (спросить некого, как у --ask --tools). При egressCheck
// (agent.private_egress_check) частный адрес или имя хоста в доводах — запрет,
// как у модели (agent.PrivateEgress): --mcp-yes его не снимает.
//
// Подключение (с ключом доступа) — не дольше mcpKeyTimeout; ответ и ошибка
// сервера печатаются через termSafe.
func runMCPCall(ctx context.Context, hub *mcphub.Hub, guard *permissions.Guard, egressCheck bool, server string, args []string,
	yes bool, out, errOut io.Writer) error {
	out, errOut = termWriter{out}, termWriter{errOut}
	return termErr(mcpCall(ctx, hub, guard, egressCheck, server, args, yes, out, errOut))
}

// mcpCall — тело runMCPCall: писатели уже чистят вывод, ошибку чистит runMCPCall.
func mcpCall(ctx context.Context, hub *mcphub.Hub, guard *permissions.Guard, egressCheck bool, server string, args []string,
	yes bool, out, errOut io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("использование: --mcp-call <сервер> <инструмент> ['<доводы JSON>']")
	}
	tool, raw := args[0], "{}"
	if len(args) == 2 {
		raw = args[1]
	}
	var callArgs map[string]any
	if err := json.Unmarshal([]byte(raw), &callArgs); err != nil || callArgs == nil {
		return fmt.Errorf("--mcp-call: доводы должны быть объектом JSON, например '{\"query\":\"…\"}'")
	}
	fmt.Fprintf(errOut, "подключаюсь к серверу MCP %s (не дольше %s)…\n", server, mcpKeyTimeout)
	cctx, cancel := context.WithTimeout(ctx, mcpKeyTimeout)
	err := hub.Reconnect(cctx, server)
	cancel()
	if err != nil {
		return err
	}
	plan, err := hub.Plan(server, tool, callArgs)
	if err != nil {
		return err
	}
	res := guard.Check(plan.Req)
	if res.Decision != permissions.DecisionDeny && egressCheck {
		// Доводы — те, что ушли бы серверу; имя — как его видит модель.
		if why := agent.PrivateEgress(tools.ExternalName(server, tool), raw); why != "" {
			res = permissions.Result{Decision: permissions.DecisionDeny, Reason: why}
		}
	}
	switch res.Decision {
	case permissions.DecisionDeny:
		return fmt.Errorf("%s: запрещено — %s", plan.Tool, res.Reason)
	case permissions.DecisionAsk:
		if !yes {
			return fmt.Errorf("%s: нужно подтверждение (%s, режим %s); подтвердить из командной строки — --mcp-yes",
				plan.Tool, res.Reason, guard.Mode())
		}
		fmt.Fprintf(errOut, "%s: подтверждено ключом --mcp-yes (%s)\n", plan.Tool, res.Reason)
	default:
		fmt.Fprintf(errOut, "%s: без вопроса — %s\n", plan.Tool, res.Reason)
	}
	start := time.Now()
	text, err := plan.Run(ctx)
	took := time.Since(start).Milliseconds()
	if text != "" {
		fmt.Fprintln(out, text)
	}
	if err != nil {
		return fmt.Errorf("%w (%d мс)", err, took)
	}
	n := 0
	if plan.Images != nil {
		n = len(plan.Images())
	}
	fmt.Fprintf(errOut, "%s: %d мс, ответ %d байт, картинок %d\n", plan.Tool, took, len(text), n)
	return nil
}

// runMCPRegistry печатает весь набор инструментов модели строкой JSON (В5) —
// тот же, что уходит в запрос: встроенные и внешние, описания с пометкой
// сервера, схемы как есть.
func runMCPRegistry(hub *mcphub.Hub, registry *tools.Registry, out, errOut io.Writer) error {
	if hub.Connectable() {
		fmt.Fprintf(errOut, "подключаюсь к серверам MCP (не дольше %s)…\n", mcphub.ConnectTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), mcphub.ConnectTimeout)
		hub.Connect(ctx)
		cancel()
	}
	b, err := json.Marshal(registry.Specs())
	if err != nil {
		return err
	}
	fmt.Fprintln(out, string(b))
	fmt.Fprintf(errOut, "инструментов: встроенных %d, сторонних серверов %d\n",
		len(registry.Names()), len(registry.ExternalNames()))
	return nil
}

// remoteCheckQuestion — вопрос проверки, когда он не задан.
const remoteCheckQuestion = "граф понятий"

// remoteLibrary — что проверяет --kb-remote-check у клиента общей библиотеки
// (*kbremote.Client): интерфейс, чтобы проверку можно было прогнать на
// подставной службе.
type remoteLibrary interface {
	Names() ([]string, error)
	Source(name string) (kb.Source, error)
	GraphSearch(ctx context.Context, collection, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error)
	GraphTool(ctx context.Context, collection, name string, args map[string]any) (string, error)
}

// runKBRemoteCheck — проверка общей библиотеки без TUI (А5): коллекции →
// книги → один поиск → карта для подмешивания → один инструмент графа →
// MCP: рукопожатие и список инструментов. Каждый шаг со временем; отказ
// любого — ошибка (код возврата не ноль), но шаги идут все: человеку нужна
// картина целиком, а не первая поломка.
func runKBRemoteCheck(cfg *config.Config, library remoteLibrary, hub *mcphub.Hub, args []string, out io.Writer) error {
	if library == nil || strings.TrimSpace(cfg.KB.ServerURL) == "" {
		return errors.New("--kb-remote-check: kb.server_url не задан — проверять нечего")
	}
	// Имена коллекций и книг, ответы службы — чужой текст для терминала.
	out = termWriter{out}
	question := strings.TrimSpace(strings.Join(args, " "))
	if question == "" {
		question = remoteCheckQuestion
	}
	ctx := context.Background()
	failed := 0
	step := func(name string, run func() (string, error)) {
		fmt.Fprintf(out, "%-26s ", name+"…")
		start := time.Now()
		res, err := run()
		ms := time.Since(start).Milliseconds()
		if err != nil {
			failed++
			fmt.Fprintf(out, "ОТКАЗ за %d мс: %v\n", ms, err)
			return
		}
		fmt.Fprintf(out, "%d мс — %s\n", ms, res)
	}
	fmt.Fprintf(out, "общая библиотека %s, вопрос «%s»\n", mcphub.CleanURL(cfg.KB.ServerURL), question)

	coll := cfg.KB.Default
	step("коллекции", func() (string, error) {
		names, err := library.Names()
		if err != nil {
			return "", err
		}
		if coll == "" && len(names) > 0 {
			coll = names[0]
		}
		return fmt.Sprintf("%d: %s", len(names), strings.Join(names, ", ")), nil
	})
	var src kb.Source
	step("книги", func() (string, error) {
		var err error
		if src, err = library.Source(coll); err != nil {
			return "", err
		}
		st := src.Stats()
		return fmt.Sprintf("коллекция %s: книг %d, кусков %d", src.Name(), len(src.Books()), st.Chunks), nil
	})
	step("поиск", func() (string, error) {
		if src == nil {
			return "", errors.New("коллекция не открылась — шаг пропущен")
		}
		res, err := src.SearchWith(ctx, question, kb.SearchOpts{TopK: 3}, nil)
		if err != nil {
			return "", err
		}
		if len(res) == 0 {
			return "выдержек 0", nil
		}
		return fmt.Sprintf("выдержек %d, первая — %s", len(res), res[0].Book), nil
	})
	entity := ""
	step("карта для подмешивания", func() (string, error) {
		res, chain, err := library.GraphSearch(ctx, coll, question, cfg.Mix.Entities, cfg.Mix.Neighbors)
		if err != nil {
			return "", err
		}
		if len(res.Entities) > 0 {
			entity = res.Entities[0].Name
		}
		return fmt.Sprintf("понятий %d, связей %d, цепочка %d шагов", len(res.Entities), len(res.Relations), len(chain)), nil
	})
	step("инструмент графа", func() (string, error) {
		name, args := tools.NameGraphOverview, map[string]any{}
		if entity != "" {
			name, args = tools.NameGraphEntity, map[string]any{"name": entity}
		}
		text, err := library.GraphTool(ctx, coll, name, args)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return fmt.Sprintf("%s — ответ %d байт", name, len(text)), nil
	})
	// Шаги MCP: служба — сервер хаба под именем LibraryName. Пустое имя —
	// служба не видна ключам (имя kb занято другим адресом; конфиг из файла
	// такого не пропускает, а собранный кодом — может): отказ шага, а не
	// Ping("") и Reconnect(""), который подключил бы ВСЕ сторонние серверы
	// с их token_cmd (аудит 23.10.2026, находка 9). Ключ доступа и подключение
	// — не дольше mcpKeyTimeout.
	lib := hub.LibraryName()
	mcpStep := func(name string, run func(ctx context.Context) (string, error)) {
		step(name, func() (string, error) {
			if lib == "" {
				return "", fmt.Errorf("служба не видна ключам MCP: имя %s занято разделом [mcp.servers.%s] с другим адресом — "+
					"назовите тот сервер иначе", mcphub.LibraryServer, mcphub.LibraryServer)
			}
			cctx, cancel := context.WithTimeout(ctx, mcpKeyTimeout)
			defer cancel()
			return run(cctx)
		})
	}
	mcpStep("MCP: рукопожатие", func(ctx context.Context) (string, error) {
		info, err := hub.Ping(ctx, lib)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ревизия %s, сервер %s %s", info.Protocol, info.ServerName, info.ServerVersion), nil
	})
	mcpStep("MCP: tools/list", func(ctx context.Context) (string, error) {
		if err := hub.Reconnect(ctx, lib); err != nil {
			return "", err
		}
		list, _, err := hub.Tools(lib)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(list))
		for _, t := range list {
			names = append(names, t.Remote)
		}
		return fmt.Sprintf("%d: %s", len(names), strings.Join(names, ", ")), nil
	})
	if failed > 0 {
		return fmt.Errorf("--kb-remote-check: отказов %d из 7", failed)
	}
	fmt.Fprintln(out, "все 7 шагов прошли")
	return nil
}
