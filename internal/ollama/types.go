// Package ollama — HTTP-клиент к API Ollama.
//
// Структуры соответствуют реальным ответам Ollama 0.32.7 (проверено на стенде
// http://ollama.example:11434). Важное отличие от OpenAI: в tool_calls поле
// function.arguments — это JSON-объект, а не строка с JSON внутри.
package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Role — роль сообщения в диалоге.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message — одно сообщение диалога.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"`
	Images    []string   `json:"images,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// Заполняются только для Role == RoleTool.
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall — запрос модели на вызов инструмента.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc — тело вызова инструмента.
type ToolCallFunc struct {
	Index     int            `json:"index,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ArgumentsJSON возвращает аргументы вызова в виде компактного JSON — для показа
// в интерфейсе и записи в лог.
func (f ToolCallFunc) ArgumentsJSON() string {
	if len(f.Arguments) == 0 {
		return "{}"
	}
	b, err := json.Marshal(f.Arguments)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Tool — описание инструмента, передаваемое модели.
type Tool struct {
	Type     string   `json:"type"` // всегда "function"
	Function ToolSpec `json:"function"`
}

// ToolSpec — имя, описание и JSON Schema параметров инструмента.
type ToolSpec struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  ToolParams `json:"parameters"`
}

// ToolParams — JSON Schema объекта параметров.
type ToolParams struct {
	Type       string              `json:"type"`
	Properties map[string]ToolProp `json:"properties"`
	Required   []string            `json:"required,omitempty"`

	// Raw — схема стороннего сервера MCP как он её прислал (этап 121, Б3).
	// Задана — модели уходит она, слово в слово: items, вложенные объекты,
	// anyOf и type списком в ToolProp не ложатся, а выбросить их значило бы
	// звать чужой инструмент не теми доводами. Поля выше при этом — разбор
	// «на глаз» (RawParams): по ним пишется подсказка о параметрах в ошибке.
	// Пусто — схема собирается из полей, как у встроенных инструментов.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON отдаёт Raw как есть, если он задан, иначе — поля.
func (p ToolParams) MarshalJSON() ([]byte, error) {
	if len(p.Raw) > 0 {
		return append([]byte(nil), p.Raw...), nil
	}
	type plain ToolParams // без метода — иначе рекурсия
	return json.Marshal(plain(p))
}

// RawParams — схема параметров стороннего сервера: Raw как есть и разбор
// для подсказок. Ошибка — схема не JSON, не объект или не ложится в строгий
// разбор Ollama (ollamaAccepts): такую модели отдавать нельзя, Ollama
// отвергла бы весь запрос, а не один инструмент.
//
// Разбор терпим к тому, чего ToolProp не знает: type списком склеивается
// через «|», вложенное и items пропускаются — Raw от этого не меняется.
func RawParams(raw json.RawMessage) (ToolParams, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return ToolParams{}, errNotObject
	}
	// Сжатие без перестановки ключей: лишние пробелы модели не нужны, а
	// порядок полей — часть того, что прислал сервер.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return ToolParams{}, errNotObject
	}
	if err := ollamaAccepts(compact.Bytes()); err != nil {
		return ToolParams{}, err
	}
	p := ToolParams{Type: "object", Raw: json.RawMessage(compact.Bytes())}
	_ = json.Unmarshal(top["type"], &p.Type)
	var props map[string]json.RawMessage
	_ = json.Unmarshal(top["properties"], &props)
	if len(props) > 0 {
		p.Properties = make(map[string]ToolProp, len(props))
	}
	for name, v := range props {
		var one struct {
			Type        json.RawMessage `json:"type"`
			Description any             `json:"description"`
		}
		_ = json.Unmarshal(v, &one)
		prop := ToolProp{Type: schemaType(one.Type)}
		if s, ok := one.Description.(string); ok {
			prop.Description = s
		}
		p.Properties[name] = prop
	}
	_ = json.Unmarshal(top["required"], &p.Required)
	return p, nil
}

// errNotObject — схема параметров не объект JSON.
var errNotObject = errors.New("схема параметров должна быть объектом JSON")

// ollamaAccepts — ляжет ли схема в разбор самой Ollama.
//
// Ollama читает параметры инструмента в строгие типы (api/types.go:
// ToolFunctionParameters и ToolProperty, ветка main, прочитано 22.10.2026):
// type — строка или список строк, description — строка, required — список
// строк, properties и anyOf — снова такие же схемы. Несовпадение типа там —
// отказ ВСЕГО /api/chat: один кривой инструмент стороннего сервера ломал бы
// каждый обмен. Сервер MCP может прислать и такое (`"required": true` у
// свойства, description объектом), поэтому проверка своя — разбором в
// зеркало тех же типов тем же encoding/json. items, $defs и прочее Ollama
// берёт как any и не проверяет — не проверяем и мы.
func ollamaAccepts(raw []byte) error {
	var p ollamaParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("схема параметров не ложится в разбор Ollama, весь запрос был бы отвергнут: %v", err)
	}
	return nil
}

// ollamaParams — зеркало ToolFunctionParameters из api/types.go Ollama.
type ollamaParams struct {
	Type       string                `json:"type"`
	Required   []string              `json:"required"`
	Properties map[string]ollamaProp `json:"properties"`
}

// ollamaProp — зеркало ToolProperty из api/types.go Ollama.
type ollamaProp struct {
	AnyOf       []ollamaProp          `json:"anyOf"`
	Type        ollamaType            `json:"type"`
	Description string                `json:"description"`
	Enum        []any                 `json:"enum"`
	Properties  map[string]ollamaProp `json:"properties"`
	Required    []string              `json:"required"`
}

// ollamaType — зеркало PropertyType: строка или список строк.
type ollamaType []string

func (t *ollamaType) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*t = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*t = many
	return nil
}

// schemaType — type из схемы строкой: "string", ["string","null"] → "string|null".
func schemaType(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return strings.Join(many, "|")
	}
	return ""
}

// ToolProp — описание одного параметра инструмента.
type ToolProp struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

// ChatRequest — тело запроса POST /api/chat.
type ChatRequest struct {
	Model     string         `json:"model"`
	Messages  []Message      `json:"messages"`
	Tools     []Tool         `json:"tools,omitempty"`
	Stream    bool           `json:"stream"`
	Think     *bool          `json:"think,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	// Logprobs — попросить у сервера логарифм вероятности каждого выданного
	// токена (Ollama 0.32 отдаёт их в чанках потока). Нужен замеру «насколько
	// модель уверена в ответе» (этап 112, Ч15); в работе выключен.
	Logprobs bool `json:"logprobs,omitempty"`
}

// TokenLogprob — один токен ответа и логарифм его вероятности.
type TokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

// ChatResponse — один чанк NDJSON-потока (или полный ответ при stream=false).
type ChatResponse struct {
	Model      string  `json:"model"`
	CreatedAt  string  `json:"created_at"`
	Message    Message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason,omitempty"`
	Error      string  `json:"error,omitempty"`
	// Logprobs — токены этого чанка, если в запросе просили.
	Logprobs []TokenLogprob `json:"logprobs,omitempty"`

	TotalDuration      int64 `json:"total_duration,omitempty"`
	LoadDuration       int64 `json:"load_duration,omitempty"`
	PromptEvalCount    int   `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitempty"`
	EvalCount          int   `json:"eval_count,omitempty"`
	EvalDuration       int64 `json:"eval_duration,omitempty"`
}

// ModelDetails — блок details из /api/tags, /api/ps и /api/show.
type ModelDetails struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
	ContextLength     int      `json:"context_length"`
	EmbeddingLength   int      `json:"embedding_length"`
}

// ModelInfo — элемент списка моделей из /api/tags.
type ModelInfo struct {
	Name         string       `json:"name"`
	Model        string       `json:"model"`
	ModifiedAt   string       `json:"modified_at"`
	Size         int64        `json:"size"`
	Digest       string       `json:"digest"`
	Details      ModelDetails `json:"details"`
	Capabilities []string     `json:"capabilities"`
}

// HasCapability сообщает, поддерживает ли модель указанную возможность
// ("tools", "thinking", "vision", "completion").
func (m ModelInfo) HasCapability(c string) bool {
	for _, x := range m.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// TagsResponse — ответ GET /api/tags.
type TagsResponse struct {
	Models []ModelInfo `json:"models"`
}

// RunningModel — элемент списка загруженных моделей из /api/ps.
type RunningModel struct {
	Name    string       `json:"name"`
	Model   string       `json:"model"`
	Size    int64        `json:"size"`
	Digest  string       `json:"digest"`
	Details ModelDetails `json:"details"`

	ExpiresAt string `json:"expires_at"`
	SizeVRAM  int64  `json:"size_vram"`
	// ContextLength — фактический размер окна, с которым модель загружена в память.
	// Это самый достоверный источник ёмкости контекста.
	ContextLength int `json:"context_length"`
}

// PSResponse — ответ GET /api/ps.
type PSResponse struct {
	Models []RunningModel `json:"models"`
}

// ShowResponse — ответ POST /api/show.
type ShowResponse struct {
	License      string         `json:"license"`
	Modelfile    string         `json:"modelfile"`
	Parameters   string         `json:"parameters"`
	Template     string         `json:"template"`
	Details      ModelDetails   `json:"details"`
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
	ModifiedAt   string         `json:"modified_at"`
}

// HasCapability сообщает, поддерживает ли модель указанную возможность.
func (s ShowResponse) HasCapability(c string) bool {
	for _, x := range s.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// VersionResponse — ответ GET /api/version.
type VersionResponse struct {
	Version string `json:"version"`
}

// Ложная строка «tools» в описании модели.
//
// Ollama выводит `tools` в capabilities по метаданным сборки, а вызывать
// инструменты модель умеет только тогда, когда у сборки есть RENDERER (чем
// вызов рисуется в промпте) и PARSER (чем разбирается ответ). У `deepseek-r1`
// их нет — разбор этого случая в DeepSeekFakeTools.md. Замер 24.08.2026
// на стенде:
//
//	deepseek-r1:70b  capabilities [tools thinking completion], RENDERER нет, PARSER нет
//	qwen3.8:latest   capabilities [completion vision tools thinking], RENDERER и PARSER есть
//
// Разница видна и без запроса к модели, поэтому проверяется здесь: слать
// инструменты той, что их не разберёт, значит тратить контекст впустую
// и получить ответ по памяти вместо ответа по книгам.
func (s ShowResponse) RealTools() bool {
	if !s.HasCapability("tools") {
		return false
	}
	// Описания сборки нет — верим объявленному: ложный отказ хуже лишнего.
	if strings.TrimSpace(s.Modelfile) == "" && strings.TrimSpace(s.Template) == "" {
		return true
	}
	if strings.Contains(s.Modelfile, "RENDERER") && strings.Contains(s.Modelfile, "PARSER") {
		return true
	}
	// Старые сборки рисуют вызовы прямо в шаблоне.
	return strings.Contains(s.Template, ".Tools") || strings.Contains(s.Template, "ToolCalls")
}
