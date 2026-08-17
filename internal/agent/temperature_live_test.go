package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Замер: как температура влияет на вызовы инструментов.
//
// Считаем не «хорошо или плохо», а по категориям сбоев — иначе не видно,
// что именно ломается: модель зовёт несуществующий инструмент, путается
// в аргументах, не зовёт вовсе или ходит по кругу до конца лимита итераций.
//
// Запуск:
//
//	OLLCHAT_TEST_SERVER=http://10.2.7.51:11434 OLLCHAT_AGENT_MODELS=gemma4:12b \
//	  go test ./internal/agent/ -run TestLiveToolCallsByTemperature -v -timeout 180m

// Категории исхода одной задачи.
const (
	outcomeOK          = "решено"
	outcomeWrongAnswer = "неверный ответ"
	outcomeNoToolCall  = "не вызвала инструмент"
	outcomeUnknownTool = "несуществующий инструмент"
	outcomeBadArgs     = "неверные аргументы"
	outcomeExecError   = "ошибка выполнения"
	outcomeIterLimit   = "упёрлась в лимит итераций"
	outcomeServerError = "сбой сервера"
)

// agentTask — задача для агента с проверкой результата.
//
// Проверка написана до замера и смотрит на факты: содержимое ответа или файла
// на диске. Так исключается соблазн засчитать «похоже на правду».
type agentTask struct {
	name   string
	prompt string
	// setup готовит песочницу, check проверяет итог по ответу и по каталогу.
	setup func(t *testing.T, root string)
	check func(answer, root string) bool
}

var agentTasks = []agentTask{
	{
		name:   "чтение файла",
		prompt: "Прочитай файл version.txt в рабочем каталоге и скажи, какая там версия.",
		setup: func(t *testing.T, root string) {
			write(t, root, "version.txt", "Версия приложения: 4.7.1\n")
		},
		check: func(a, _ string) bool { return strings.Contains(a, "4.7.1") },
	},
	{
		name:   "поиск по файлам",
		prompt: "Найди в файлах рабочего каталога строку TODO и скажи, в каком файле она встречается.",
		setup: func(t *testing.T, root string) {
			write(t, root, "notes.txt", "обычный текст\nещё строка\n")
			write(t, root, "tasks.txt", "первая строка\nTODO: починить разбор\n")
		},
		check: func(a, _ string) bool { return strings.Contains(strings.ToLower(a), "tasks.txt") },
	},
	{
		name:   "список каталога",
		prompt: "Сколько файлов лежит в рабочем каталоге? Ответь числом в конце ответа.",
		setup: func(t *testing.T, root string) {
			for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
				write(t, root, n, "содержимое\n")
			}
		},
		check: func(a, _ string) bool { return strings.Contains(a, "4") || strings.Contains(a, "четыре") },
	},
	{
		name:   "запись файла",
		prompt: "Создай в рабочем каталоге файл result.txt, в котором будет одно слово: ГОТОВО",
		setup:  func(t *testing.T, root string) {},
		check: func(_, root string) bool {
			b, err := os.ReadFile(filepath.Join(root, "result.txt"))
			return err == nil && strings.Contains(string(b), "ГОТОВО")
		},
	},
	{
		name:   "правка файла",
		prompt: "В файле config.ini замени порт 8080 на 9090. Ничего больше не меняй.",
		setup: func(t *testing.T, root string) {
			write(t, root, "config.ini", "[server]\nhost = 0.0.0.0\nport = 8080\n")
		},
		check: func(_, root string) bool {
			b, err := os.ReadFile(filepath.Join(root, "config.ini"))
			return err == nil && strings.Contains(string(b), "9090") && !strings.Contains(string(b), "8080")
		},
	},
	{
		name:   "команда оболочки",
		prompt: "Посчитай командой wc -l, сколько строк в файле data.txt, и назови число.",
		setup: func(t *testing.T, root string) {
			write(t, root, "data.txt", "1\n2\n3\n4\n5\n6\n7\n")
		},
		check: func(a, _ string) bool { return strings.Contains(a, "7") || strings.Contains(a, "семь") },
	},
	{
		name: "два шага",
		prompt: "Прочитай файл version.txt, увеличь в версии средний номер на единицу " +
			"и запиши получившуюся версию в файл next.txt (только версию, без лишних слов).",
		setup: func(t *testing.T, root string) {
			write(t, root, "version.txt", "4.7.1\n")
		},
		check: func(_, root string) bool {
			b, err := os.ReadFile(filepath.Join(root, "next.txt"))
			return err == nil && strings.Contains(string(b), "4.8.1")
		},
	},
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("подготовка %s: %v", name, err)
	}
}

// agentRun — исход одной задачи при одной температуре.
type agentRun struct {
	Model       string   `json:"model"`
	Temperature float64  `json:"temperature"`
	Task        string   `json:"task"`
	Attempt     int      `json:"attempt"`
	Outcome     string   `json:"outcome"`
	ToolsOK     int      `json:"tools_ok"`
	ToolsFail   int      `json:"tools_fail"`
	ToolNames   []string `json:"tool_names"`
	Answer      string   `json:"answer"`
	Detail      string   `json:"detail,omitempty"`
	Seconds     float64  `json:"seconds"`
}

func TestLiveToolCallsByTemperature(t *testing.T) {
	client, defModel := testServer(t)

	modelList := []string{defModel}
	if s := os.Getenv("OLLCHAT_AGENT_MODELS"); s != "" {
		modelList = nil
		for _, m := range strings.Split(s, ",") {
			if m = strings.TrimSpace(m); m != "" {
				modelList = append(modelList, m)
			}
		}
	}

	temps := []float64{0, 0.3, 0.6, 1.0, 1.5}
	if s := os.Getenv("OLLCHAT_AGENT_TEMPS"); s != "" {
		temps = nil
		for _, v := range strings.Split(s, ",") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				temps = append(temps, f)
			}
		}
	}

	attempts := 3
	if s := os.Getenv("OLLCHAT_PARAM_REPEATS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			attempts = n
		}
	}

	var runs []agentRun
	for _, model := range modelList {
		t.Logf("═══ модель %s ═══", model)
		for _, temp := range temps {
			counts := map[string]int{}
			for _, task := range agentTasks {
				for attempt := 1; attempt <= attempts; attempt++ {
					r := runAgentTask(t, client, model, temp, task, attempt)
					runs = append(runs, r)
					counts[r.Outcome]++
				}
			}
			total := len(agentTasks) * attempts
			t.Logf("temperature %.1f · решено %d из %d (%.0f%%) · %s",
				temp, counts[outcomeOK], total,
				100*float64(counts[outcomeOK])/float64(total), formatCounts(counts))
		}
		unloadModel(t, client, model)
	}

	saveAgentRuns(t, runs)
}

// runAgentTask прогоняет одну задачу и классифицирует исход.
func runAgentTask(t *testing.T, client *ollama.Client, model string, temp float64, task agentTask, attempt int) agentRun {
	t.Helper()
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	task.setup(t, realRoot)

	r := buildRunner(t, client, model, realRoot, temp)
	conv := session.New("Ты помощник-программист. Пользуйся инструментами, чтобы отвечать точно.")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: task.prompt})

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	out := agentRun{Model: model, Temperature: temp, Task: task.name, Attempt: attempt}
	var answer strings.Builder
	var firstFail string
	started := time.Now()

	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventContent:
			answer.WriteString(ev.Text)
		case EventToolResult:
			out.ToolNames = append(out.ToolNames, ev.Tool.Name)
			if ev.Tool.OK {
				out.ToolsOK++
				continue
			}
			out.ToolsFail++
			if firstFail == "" {
				firstFail = classifyToolFailure(ev.Tool)
				out.Detail = shortenText(ev.Tool.Output, 160)
			}
		case EventToolConfirm:
			// Режим yolo, подтверждений быть не должно; но если правило deny
			// всё же спросит — отвечаем «нет», чтобы прогон не завис.
			ev.Confirm.Reply <- AnswerNo
		case EventError:
			if strings.Contains(ev.Err.Error(), "max_iterations") {
				out.Outcome = outcomeIterLimit
			} else {
				out.Outcome = outcomeServerError
				out.Detail = shortenText(ev.Err.Error(), 160)
			}
		}
	}

	out.Seconds = time.Since(started).Seconds()
	out.Answer = shortenText(answer.String(), 300)

	switch {
	case out.Outcome != "": // ошибка уже определена
	case len(out.ToolNames) == 0:
		out.Outcome = outcomeNoToolCall
	case task.check(answer.String(), realRoot):
		out.Outcome = outcomeOK
	case firstFail != "":
		out.Outcome = firstFail
	default:
		out.Outcome = outcomeWrongAnswer
	}
	return out
}

// classifyToolFailure различает виды неудачных вызовов по тексту, который
// получила модель. Тексты формирует tools.Registry.Plan.
func classifyToolFailure(te *ToolEvent) string {
	s := te.Output + " " + te.Reason
	switch {
	case strings.Contains(s, "инструмент недоступен"):
		return outcomeUnknownTool
	case strings.Contains(s, "принимает параметры"):
		return outcomeBadArgs
	default:
		return outcomeExecError
	}
}

// buildRunner собирает агента с заданной температурой.
//
// MaxRetries = 0 намеренно: повтор обмена сгладил бы серверные сбои, а в замере
// их надо видеть отдельной категорией.
func buildRunner(t *testing.T, client *ollama.Client, model, root string, temp float64) *Runner {
	t.Helper()
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	set, err := permissions.Compile(
		[]string{"Read(./**)", "Write(./**)", "Bash(*)"},
		nil,
		[]string{"Bash(rm:*)", "Bash(sudo:*)"},
		sb.Root())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	reg, err := tools.NewRegistry(
		[]string{tools.NameReadFile, tools.NameListDir, tools.NameGrep,
			tools.NameWriteFile, tools.NameEditFile, tools.NameBash},
		tools.Options{Sandbox: sb, BashTimeout: 30 * time.Second, MaxOutputKB: 64})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return &Runner{
		Client: client, Model: model, KeepAlive: "10m",
		Options:        map[string]any{"num_ctx": 16384, "temperature": temp},
		Tools:          reg,
		Guard:          permissions.NewGuard(set, sb, permissions.ModeYolo),
		MaxIterations:  10,
		MaxRetries:     0,
		ToolsSupported: true,
	}
}

func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k, v := range counts {
		if k != outcomeOK && v > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "сбоев нет"
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

func shortenText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// unloadModel освобождает видеопамять после прогона модели.
func unloadModel(t *testing.T, c *ollama.Client, model string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Пустой запрос с keep_alive=0 — единственный способ попросить выгрузку.
	req := ollama.ChatRequest{Model: model, KeepAlive: "0s",
		Messages: []ollama.Message{{Role: ollama.RoleUser, Content: "1"}},
		Options:  map[string]any{"num_predict": 1}}
	for ev := range c.Chat(ctx, req) {
		_ = ev
	}
}

func saveAgentRuns(t *testing.T, runs []agentRun) {
	t.Helper()
	dir := os.Getenv("OLLCHAT_PARAM_OUT")
	if dir == "" {
		t.Logf("OLLCHAT_PARAM_OUT не задан — результаты не сохранены (прогонов: %d)", len(runs))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("создание каталога: %v", err)
		return
	}
	data, err := json.MarshalIndent(map[string]any{
		"format":       1,
		"generated_at": time.Now().Format(time.RFC3339),
		"runs":         runs,
	}, "", "  ")
	if err != nil {
		t.Logf("сериализация: %v", err)
		return
	}
	path := filepath.Join(dir, "agent_temperature.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Logf("запись: %v", err)
		return
	}
	t.Logf("результаты записаны: %s (прогонов: %d)", path, len(runs))
}
