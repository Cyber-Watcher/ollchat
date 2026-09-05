// Package ollama — HTTP-клиент к API Ollama.
//
// Структуры соответствуют реальным ответам Ollama 0.32.7 (проверено на стенде
// http://ollama.example:11434). Важное отличие от OpenAI: в tool_calls поле
// function.arguments — это JSON-объект, а не строка с JSON внутри.
package ollama

import (
	"encoding/json"
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
}

// ChatResponse — один чанк NDJSON-потока (или полный ответ при stream=false).
type ChatResponse struct {
	Model      string  `json:"model"`
	CreatedAt  string  `json:"created_at"`
	Message    Message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason,omitempty"`
	Error      string  `json:"error,omitempty"`

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
