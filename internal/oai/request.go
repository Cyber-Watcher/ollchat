package oai

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Перевод ollama.ChatRequest в тело POST /v1/chat/completions.
//
// Чем формы расходятся (опись этапа 127, docs/plan/stage127.md):
//   - аргументы вызова инструмента у OpenAI — строка JSON, а не объект;
//   - у вызова обязателен id, ответ инструмента ссылается на него
//     tool_call_id; Ollama id часто не даёт — он дописывается здесь;
//   - картинки — части content вида image_url, а не поле images;
//   - мышление выключается chat_template_kwargs.enable_thinking (шаблон Qwen),
//     а не полем think;
//   - окно (num_ctx) и keep_alive запросом не задаются: окно — свойство
//     запуска движка, выгрузки по сроку нет.

// chatRequest — тело запроса.
type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []wireMessage  `json:"messages"`
	Tools         []ollama.Tool  `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`

	// Параметры выборки — как пришли в Options; nil не уходит, ноль уходит.
	Temperature      any `json:"temperature,omitempty"`
	TopP             any `json:"top_p,omitempty"`
	Seed             any `json:"seed,omitempty"`
	Stop             any `json:"stop,omitempty"`
	PresencePenalty  any `json:"presence_penalty,omitempty"`
	FrequencyPenalty any `json:"frequency_penalty,omitempty"`
	MaxTokens        int `json:"max_tokens,omitempty"`

	Logprobs           bool           `json:"logprobs,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`

	// User — кто спрашивает (Client.WithUser); пусто — поля нет.
	User string `json:"user,omitempty"`
	// Priority — приоритет запроса в очереди движка (Client.WithPriority); nil —
	// поля нет: движок без политики приоритета запрос с ним отвергает.
	Priority *int `json:"priority,omitempty"`
}

// streamOptions — без include_usage сервер в потоке не сообщает числа
// токенов, и индикатор окна остался бы пустым.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireMessage — сообщение в форме OpenAI. Content — строка или []contentPart.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// contentPart — часть content: текст или картинка.
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// wireToolCall — вызов инструмента в истории; arguments — строка JSON.
type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// pendingCall — вызов последнего ответа модели, ещё не получивший ответа
// инструмента.
type pendingCall struct {
	id, name string
	answered bool
}

// buildRequest переводит запрос программы в тело запроса OpenAI; user — поле
// user (Client.WithUser), пусто — не уходит.
func buildRequest(req ollama.ChatRequest, user string) chatRequest {
	out := chatRequest{
		User:          user,
		Model:         req.Model,
		Messages:      convertMessages(req.Messages),
		Tools:         req.Tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Logprobs:      req.Logprobs,
	}
	if req.Think != nil {
		out.ChatTemplateKwargs = map[string]any{"enable_thinking": *req.Think}
	}
	o := req.Options
	out.Temperature = o["temperature"]
	out.TopP = o["top_p"]
	out.Seed = o["seed"]
	out.Stop = o["stop"]
	out.PresencePenalty = o["presence_penalty"]
	out.FrequencyPenalty = o["frequency_penalty"]
	if n, ok := toInt(o["num_predict"]); ok && n > 0 {
		out.MaxTokens = n
	}
	return out
}

// convertMessages переводит историю. Мышление (Thinking) в историю не уходит:
// у OpenAI для него нет места в запросе, а шаблоны моделей прошлые рассуждения
// всё равно отбрасывают.
//
// Вызовы без id (их даёт Ollama) получают устойчивый id
// "call_<номер сообщения>_<номер вызова>": тот же при каждом следующем запросе,
// пока история не переписана, — иначе префикс промпта менялся бы от запроса
// к запросу. Ответ инструмента без tool_call_id привязывается к ближайшему
// ещё не отвеченному вызову последнего ответа модели: по имени инструмента,
// иначе по порядку. Не к чему привязать — tool_call_id не шлётся вовсе.
func convertMessages(msgs []ollama.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	var pending []pendingCall
	for i, m := range msgs {
		wm := wireMessage{Role: m.Role, Content: content(m)}
		switch m.Role {
		case ollama.RoleAssistant:
			// Новый ответ модели: прежние неотвеченные вызовы больше не ждут —
			// инструменты отвечают только на вызовы предыдущего ответа.
			pending = pending[:0]
			for j, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", i, j)
				}
				pending = append(pending, pendingCall{id: id, name: tc.Function.Name})
				wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
					ID:   id,
					Type: "function",
					Function: wireFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.ArgumentsJSON(),
					},
				})
			}
		case ollama.RoleTool:
			wm.ToolCallID = answer(pending, m.ToolCallID, m.ToolName)
		}
		out = append(out, wm)
	}
	return out
}

// answer отмечает вызов, на который отвечает инструмент, и возвращает его id.
// Заданный id остаётся как есть; пустой ищется среди неотвеченных: сперва по
// имени инструмента, затем первый по порядку.
func answer(pending []pendingCall, id, name string) string {
	if id != "" {
		for k := range pending {
			if pending[k].id == id {
				pending[k].answered = true
				break
			}
		}
		return id
	}
	pick := -1
	if name != "" {
		for k, p := range pending {
			if !p.answered && p.name == name {
				pick = k
				break
			}
		}
	}
	if pick < 0 {
		for k, p := range pending {
			if !p.answered {
				pick = k
				break
			}
		}
	}
	if pick < 0 {
		return ""
	}
	pending[pick].answered = true
	return pending[pick].id
}

// content — текст сообщения строкой, а с картинками — частями: текст первым,
// затем по части image_url на картинку (data URL с base64 как есть).
func content(m ollama.Message) any {
	if len(m.Images) == 0 {
		return m.Content
	}
	parts := make([]contentPart, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, contentPart{Type: "text", Text: m.Content})
	}
	for _, img := range m.Images {
		parts = append(parts, contentPart{
			Type:     "image_url",
			ImageURL: &imageURL{URL: "data:" + imageMIME(img) + ";base64," + img},
		})
	}
	return parts
}

// imageMIME угадывает тип картинки по началу её base64: у Ollama в images
// лежит только base64 без типа, а data URL тип требует. Начала — base64
// сигнатур форматов: PNG \x89PNG, JPEG \xFF\xD8\xFF, GIF «GIF8», WebP «RIFF».
// Незнакомое — PNG.
func imageMIME(b64 string) string {
	switch {
	case strings.HasPrefix(b64, "iVBOR"):
		return "image/png"
	case strings.HasPrefix(b64, "/9j/"):
		return "image/jpeg"
	case strings.HasPrefix(b64, "R0lG"):
		return "image/gif"
	case strings.HasPrefix(b64, "UklGR"):
		return "image/webp"
	}
	return "image/png"
}

// toInt читает целое из значения Options: из TOML приходит int64, из JSON —
// float64, из кода — int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(min(n, math.MaxInt32)), true
	case float32:
		return floatInt(float64(n)), true
	case float64:
		return floatInt(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return floatInt(f), true
		}
	}
	return 0, false
}

// floatInt — целая часть числа с потолком MaxInt32: перевод дробного вне
// диапазона int в Go не определён.
func floatInt(f float64) int {
	if f > math.MaxInt32 {
		return math.MaxInt32
	}
	if f < math.MinInt32 {
		return math.MinInt32
	}
	return int(f)
}
