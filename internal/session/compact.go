package session

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Сжатие истории сводкой (этап 91, R9.1; решение владельца 04.09.2026).
//
// Книги предлагают ровно это: «keep a summary of the conversation along with
// the latest K replies» — «держать сводку разговора вместе с последними K
// репликами» («LLM Engineer's Handbook», 2024, стр. 1133). Обрезка без сводки
// теряет нить; сводка без обрезки не освобождает окно.

// CompactPrompt — постановка задачи модели-сжимателю. Файл, а не константа:
// у него есть версия, как у промптов графа.
//
//go:embed prompts/compact.txt
var CompactPrompt string

// CompactPromptID — версия промпта сводки, пишется в журнал шагов.
var CompactPromptID = func() string {
	sum := sha256.Sum256([]byte(CompactPrompt))
	return hex.EncodeToString(sum[:])[:8]
}()

// Chatter — то, что умеет один обмен с моделью. Объявлен здесь, у потребителя.
type Chatter interface {
	Chat(ctx context.Context, req ollama.ChatRequest) <-chan ollama.Event
}

// Summarize просит модель сжать сообщения в сводку. Возвращает текст сводки
// и статистику обмена (токены — для журнала шагов).
func Summarize(ctx context.Context, cl Chatter, model string, msgs []ollama.Message) (string, ollama.Stats, error) {
	if len(msgs) == 0 {
		return "", ollama.Stats{}, errors.New("сжимать нечего")
	}
	no := false
	req := ollama.ChatRequest{
		Model: model,
		Messages: []ollama.Message{
			{Role: ollama.RoleSystem, Content: CompactPrompt},
			{Role: ollama.RoleUser, Content: transcript(msgs)},
		},
		// Рассуждения здесь не нужны: они удлиняют ответ и не попадают в сводку.
		Think: &no,
	}
	var b strings.Builder
	var stats ollama.Stats
	done := false
	for ev := range cl.Chat(ctx, req) {
		switch ev.Kind {
		case ollama.EventContent:
			b.WriteString(ev.Text)
		case ollama.EventDone:
			stats = ev.Stats
			done = true
		case ollama.EventError:
			return "", stats, ev.Err
		}
	}
	if !done {
		return "", stats, errors.New("модель не завершила сводку")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", stats, errors.New("модель вернула пустую сводку")
	}
	return out, stats, nil
}

// transcript записывает историю в текст для сжимателя: роль и содержимое,
// вызовы инструментов — одной строкой, картинки — пометкой.
func transcript(msgs []ollama.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case ollama.RoleUser:
			b.WriteString("Человек: ")
		case ollama.RoleAssistant:
			b.WriteString("Ассистент: ")
		case ollama.RoleTool:
			fmt.Fprintf(&b, "Результат инструмента %s: ", m.ToolName)
		default:
			b.WriteString(m.Role + ": ")
		}
		b.WriteString(strings.TrimSpace(m.Content))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "\n[вызов %s(%s)]", tc.Function.Name, tc.Function.ArgumentsJSON())
		}
		if len(m.Images) > 0 {
			fmt.Fprintf(&b, "\n[приложено картинок: %d]", len(m.Images))
		}
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// SummaryMessage — как сводка ложится в историю: сообщением человека, потому
// что системное сообщение одно и занято, а модели важно лишь содержимое.
func SummaryMessage(summary string, dropped int) ollama.Message {
	return ollama.Message{Role: ollama.RoleUser, Content: fmt.Sprintf(
		"Сводка предыдущей части этого разговора (сжато сообщений: %d). "+
			"Опирайся на неё как на то, что уже обсуждено:\n\n%s", dropped, summary)}
}

// Older отдаёт сообщения, которые уйдут в сводку при сжатии до keep
// последних: та же граница, что у Compact, — история не начинается
// с ответа инструмента без запроса.
func (c *Conversation) Older(keep int) []ollama.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	cut := c.cutIndex(keep)
	return append([]ollama.Message(nil), c.messages[:cut]...)
}

// CompactWith заменяет старые сообщения сводкой, оставляя keep последних.
// Возвращает, сколько сообщений ушло в сводку.
func (c *Conversation) CompactWith(keep int, summary string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	cut := c.cutIndex(keep)
	if cut == 0 {
		return 0
	}
	tail := append([]ollama.Message(nil), c.messages[cut:]...)
	c.messages = append([]ollama.Message{SummaryMessage(summary, cut)}, tail...)
	return cut
}

// cutIndex — с какого сообщения начинается сохраняемый хвост при keep
// последних. Хвост не начинается с ответа инструмента без его запроса.
func (c *Conversation) cutIndex(keep int) int {
	if keep < 0 {
		keep = 0
	}
	if len(c.messages) <= keep {
		return 0
	}
	for keep > 0 && keep < len(c.messages) && c.messages[len(c.messages)-keep].Role == ollama.RoleTool {
		keep++
	}
	return len(c.messages) - keep
}
