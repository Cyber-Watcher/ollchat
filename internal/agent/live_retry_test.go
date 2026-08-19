package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Диалог, на котором сбой разбора у nemotron-3-super воспроизводится чаще всего:
// в истории уже есть выполненный вызов инструмента.
func reproConversation() []ollama.Message {
	long := "Вот Hello World на трёх языках:\n\n```c\n#include <stdio.h>\n\nint main(void) {\n" +
		"    printf(\"Hello, World!\\n\");\n    return 0;\n}\n```\n\n```rust\nfn main() {\n" +
		"    println!(\"Hello, World!\");\n}\n```\n\n```go\npackage main\n\nimport \"fmt\"\n\n" +
		"func main() {\n    fmt.Println(\"Hello, World!\")\n}\n```\n"

	return []ollama.Message{
		{Role: ollama.RoleUser, Content: "Напиши Hello World на C, Rust и Go."},
		{Role: ollama.RoleAssistant, Content: long},
		{Role: ollama.RoleUser, Content: "Спасибо, но я имел в виду создать три файла на этих языках в текущем каталоге."},
		{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{{
			ID: "c1", Function: ollama.ToolCallFunc{Name: "write_file", Arguments: map[string]any{
				"path": "hello.c", "content": "#include <stdio.h>\nint main(void){return 0;}\n"}}}}},
		{Role: ollama.RoleTool, ToolName: "write_file", ToolCallID: "c1",
			Content: "Файл hello.c создан (6 строк)."},
	}
}

func writeFileSpec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name:        "write_file",
		Description: "Создаёт файл или полностью заменяет его содержимое.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":    {Type: "string", Description: "Путь к файлу"},
				"content": {Type: "string", Description: "Содержимое файла"},
			},
			Required: []string{"path", "content"},
		},
	}}
}

// TestLiveServerParseFailureIsRetryable ловит настоящий сбой разбора на сервере
// и проверяет главное: наш клиент помечает такую ошибку как пригодную к повтору,
// и она приходит до появления текста ответа — только тогда повтор безопасен.
//
// Сбой перемежающийся, поэтому его отсутствие в прогоне не считается провалом
// теста: проверять нечего, но и ломать сборку не за что.
func TestLiveServerParseFailureIsRetryable(t *testing.T) {
	url := os.Getenv("OLLCHAT_TEST_SERVER")
	if url == "" {
		t.Skip("не задан OLLCHAT_TEST_SERVER")
	}
	model := os.Getenv("OLLCHAT_TEST_MODEL")
	if model == "" {
		model = "nemotron-3-super:latest"
	}

	client := ollama.New(url, 600*time.Second, 0, nil)
	// Режим рассуждений включён намеренно: все воспроизведения сбоя получены
	// именно с ним, а без него за 8 попыток он не выпал ни разу.
	think := true
	req := ollama.ChatRequest{
		Model: model, Messages: reproConversation(),
		Tools:   []ollama.Tool{writeFileSpec()},
		Think:   &think,
		Options: map[string]any{"num_ctx": 32768, "temperature": 0.6},
	}

	const attempts = 8
	caught := 0
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		var (
			gotErr      error
			contentSeen int
			toolsSeen   int
		)
		for ev := range client.Chat(ctx, req) {
			switch ev.Kind {
			case ollama.EventContent:
				contentSeen++
			case ollama.EventToolCalls:
				toolsSeen++
			case ollama.EventError:
				gotErr = ev.Err
			}
		}
		cancel()

		if gotErr == nil {
			continue
		}
		if strings.Contains(gotErr.Error(), "генерация прервана") {
			t.Logf("прогон %d: не уложились в отведённое время, пропускаем", i+1)
			continue
		}

		caught++
		t.Logf("прогон %d: сбой сервера: %v", i+1, gotErr)

		if !ollama.Retryable(gotErr) {
			t.Errorf("сбой генерации должен помечаться как пригодный к повтору: %v", gotErr)
		}
		if contentSeen != 0 || toolsSeen != 0 {
			t.Errorf("сбой пришёл после начала ответа (текста %d, вызовов %d) — "+
				"повтор в таком случае запрещён, проверьте условие в exchangeWithRetry",
				contentSeen, toolsSeen)
		}
	}

	if caught == 0 {
		t.Log("сбой сервера в этот раз не воспроизвёлся — проверять нечего")
		return
	}
	t.Logf("поймано сбоев: %d из %d прогонов, все помечены как пригодные к повтору", caught, attempts)
}
