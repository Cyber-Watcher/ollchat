package session

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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
//
// keepAlive и options — те же, с какими идёт сам диалог. Без num_ctx в options
// сервер поднимает модель со своим умолчанием: история длиннее этого окна
// в сводку целиком не попадёт, а модель будет перезагружена дважды — под
// сводку и обратно под диалог (этап 115).
func Summarize(ctx context.Context, cl Chatter, model string, msgs []ollama.Message,
	keepAlive string, options map[string]any) (string, ollama.Stats, error) {
	return SummarizeWith(ctx, cl, model, msgs, keepAlive, options, nil)
}

// SummarizeWith — Summarize, которая по ходу ответа сообщает written, сколько
// символов сводки уже написано: индикатор сжатия показывает, что модель пишет,
// а не висит (этап 134). written зовётся из той же горутины, что и Summarize.
func SummarizeWith(ctx context.Context, cl Chatter, model string, msgs []ollama.Message,
	keepAlive string, options map[string]any, written func(chars int)) (string, ollama.Stats, error) {
	if len(msgs) == 0 {
		return "", ollama.Stats{}, errors.New("сжимать нечего")
	}
	no := false
	body := transcript(msgs)
	req := ollama.ChatRequest{
		Model: model,
		Messages: []ollama.Message{
			{Role: ollama.RoleSystem, Content: CompactPrompt},
			{Role: ollama.RoleUser, Content: body},
		},
		// Рассуждения здесь не нужны: они удлиняют ответ и не попадают в сводку.
		Think:     &no,
		KeepAlive: keepAlive,
		Options:   options,
	}
	var b strings.Builder
	var stats ollama.Stats
	done := false
	for ev := range cl.Chat(ctx, req) {
		switch ev.Kind {
		case ollama.EventContent:
			b.WriteString(ev.Text)
			if written != nil {
				written(b.Len())
			}
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
	// Сводка, оборванная пределом вывода, выглядит полной, а хвоста истории
	// в ней нет: принятая, она молча заменила бы собой сжатые сообщения
	// (этап 112, Х2; «summarization … rejects … length-limited results»,
	// «Learning Agent Engineering from Pi», 2026, разд. 20).
	if stats.DoneReason == "length" {
		return "", stats, errors.New("сводка оборвана пределом длины вывода (num_predict) и не принята: " +
			"поднимите options.num_predict в настройках сервера (-1 — без предела)")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", stats, errors.New("модель вернула пустую сводку")
	}
	// Сводка не короче сжатого окна не освобождает, а точность теряет: такая
	// отвергается, и вызывающая сторона обрезает историю без сводки (этап 112,
	// Ш2; «a summary is not guaranteed to be smaller than what it replaced»,
	// «Multi-Agent AI Engineering», 2026, разд. 13).
	if n, was := utf8.RuneCountInString(out), utf8.RuneCountInString(body); n >= was {
		return "", stats, fmt.Errorf("сводка (%d знаков) не короче сжимаемой истории (%d знаков) и не принята", n, was)
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

// PrunedToolOutput — что остаётся на месте вывода инструмента, убранного
// PruneToolOutputs.
const PrunedToolOutput = "[вывод убран при сжатии истории, чтобы освободить окно; если он нужен — повтори вызов]"

// PruneToolOutputs заменяет пометкой выводы инструментов старше последних
// keepTurns ходов (ход начинается сообщением человека); вызовы с аргументами
// и ответы модели остаются, картинки из выводов уходят. Пересказа моделью нет,
// поэтому нет и выдуманных путей, из-за которых агентный режим не сжимали
// сводкой (решение 04.09.2026). Книга: «apply selective pruning first … then
// summarization» (этап 112, Ш1). Замер на рабочей машине: выводы инструментов
// — 52,4 % истории сеанса из 11 ходов, старше двух последних ходов — 45,1 %.
// Возвращает, сколько выводов убрано и сколько байт освобождено (мера та же,
// что у EstimatedChars).
func (c *Conversation) PruneToolOutputs(keepTurns int) (n, freed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if keepTurns < 1 {
		return 0, 0
	}
	cut, seen := 0, 0
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Role == ollama.RoleUser {
			if seen++; seen == keepTurns {
				cut = i
				break
			}
		}
	}
	for i := 0; i < cut; i++ {
		m := &c.messages[i]
		if m.Role != ollama.RoleTool || len(m.Content) <= len(PrunedToolOutput) && len(m.Images) == 0 {
			continue
		}
		if len(m.Content) > len(PrunedToolOutput) {
			freed += len(m.Content) - len(PrunedToolOutput)
			m.Content = PrunedToolOutput
		}
		m.Images = nil
		n++
	}
	return n, freed
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
