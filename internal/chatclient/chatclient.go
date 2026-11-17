// Package chatclient — клиент сервера чата по полю [[servers]].api (этап 127):
// Ollama или OpenAI-совместимый API (vLLM, SGLang, шлюз LiteLLM, llama-server).
//
// Интерфейс Client — ровно то, что нужно чату и интерфейсу: поток ответа
// (агент, сводка истории, --ask), список моделей, описание модели, загруженные
// модели. Внутренний язык — типы internal/ollama: агент, инструменты и MCP о
// втором API не знают, перевод идёт на границе, в internal/oai.
//
// Сборка графа и векторы ходят в Ollama напрямую и сюда не входят: их паспорт
// сверяет веса по digest, которого у /v1/models нет (решение владельца
// 17.11.2026, конфиг отказывает серверу "openai" в этих ролях).
package chatclient

import (
	"context"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Client — сервер чата.
type Client interface {
	Chat(ctx context.Context, req ollama.ChatRequest) <-chan ollama.Event
	BaseURL() string
	Version(ctx context.Context) (string, error)
	Tags(ctx context.Context) ([]ollama.ModelInfo, error)
	PS(ctx context.Context) ([]ollama.RunningModel, error)
	Show(ctx context.Context, model string) (*ollama.ShowResponse, error)
	Resident(ctx context.Context, model string) ollama.Residency
}

// New собирает клиент сервера srv. Ошибка настройки (ключ API не читается) не
// роняет вызывающего: возвращается клиент, который на каждый вызов отвечает этой
// ошибкой, — интерфейс показывает её там же, где показал бы «сервер недоступен».
func New(srv *config.Server) Client {
	if srv == nil {
		return broken{err: errNoServer}
	}
	if srv.IsOpenAI() {
		return newOpenAI(srv)
	}
	return ollama.NewWithStall(srv.URL, srv.TimeoutDuration(), srv.ChatTimeoutDuration(),
		srv.StallTimeoutDuration(), srv.Headers)
}

// keyTimeout — срок чтения ключа API (файл или переменная; команды нет).
const keyTimeout = 10 * time.Second
