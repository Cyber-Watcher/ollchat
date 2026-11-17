package chatclient

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
	"github.com/Cyber-Watcher/ollchat/internal/oai"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// openAICaps — что объявлять о модели OpenAI-совместимого сервера. /v1/models
// возможностей не сообщает; инструменты и мышление у тех, кого мы подключаем
// (vLLM и SGLang с разборщиками qwen3_coder и qwen3, llama-server с шаблоном
// jinja, LiteLLM поверх них), проверены записями Ш1 этапа 127. Картинки
// объявлены тоже: не умеющий их сервер отвечает понятной ошибкой 400 или 500
// (записи Ш1: vLLM с --language-model-only, llama-server без mmproj), а не
// молчанием, — это лучше, чем запрещать картинки тому, кто их понимает (SGLang).
var openAICaps = []string{"completion", "tools", "thinking", "vision"}

// openAI — сервер с OpenAI-совместимым API в форме Client.
type openAI struct {
	c      *oai.Client
	ctxLen int // [[servers]].context_length: перекрывает окно из /v1/models
}

func newOpenAI(srv *config.Server) Client {
	ctx, cancel := context.WithTimeout(context.Background(), keyTimeout)
	defer cancel()
	key, err := mcpclient.TokenAs(ctx, srv.APIKeyFile, srv.APIKeyEnv, "",
		"api_key_file", "api_key_env", "api_key_cmd")
	if err != nil {
		return broken{err: fmt.Errorf("сервер %q: %w", srv.Name, err)}
	}
	c := oai.New(srv.URL, key, srv.TimeoutDuration(), srv.ChatTimeoutDuration(),
		srv.StallTimeoutDuration(), srv.Headers)
	if srv.SendsUser() {
		c.WithUser(osUser())
	}
	if srv.PriorityLongTokens > 0 {
		c.WithPriority(srv.PriorityLongTokens, srv.PriorityLong)
	}
	return &openAI{c: c, ctxLen: srv.ContextLength}
}

// osUser — имя учётной записи ОС, под которой запущен ollchat: на рабочей
// машине каждый работает под своей, и шлюз по нему ведёт учёт по людям.
// Не определилось — пусто, поле user не уходит.
func osUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, k := range []string{"USER", "LOGNAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (o *openAI) Chat(ctx context.Context, req ollama.ChatRequest) <-chan ollama.Event {
	return o.c.Chat(ctx, req)
}

func (o *openAI) BaseURL() string { return o.c.BaseURL() }

// Version — у OpenAI-совместимого API версии сервера нет; строка идёт в
// заголовок вместо «ollama <номер>» (ui/view.go). Доступность сервера проверяет
// Tags, которую интерфейс зовёт следом.
func (o *openAI) Version(context.Context) (string, error) {
	return "API OpenAI", nil
}

// Tags — список моделей из /v1/models в форме /api/tags: имя, окно, возможности.
func (o *openAI) Tags(ctx context.Context) ([]ollama.ModelInfo, error) {
	ms, err := o.c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ollama.ModelInfo, 0, len(ms))
	for _, m := range ms {
		out = append(out, ollama.ModelInfo{
			Name:         m.ID,
			Model:        m.ID,
			Details:      ollama.ModelDetails{ContextLength: o.window(m)},
			Capabilities: openAICaps,
		})
	}
	return out, nil
}

// PS — загруженных моделей OpenAI-совместимый API не показывает: модель
// движка загружена всё время его работы. Пустой список без ошибки — окно
// интерфейс возьмёт у Show или Tags.
func (o *openAI) PS(context.Context) ([]ollama.RunningModel, error) { return nil, nil }

// Show — описание модели в форме /api/show: возможности и окно. Шаблона и
// Modelfile нет, поэтому ShowResponse.RealTools верит объявленному «tools».
func (o *openAI) Show(ctx context.Context, model string) (*ollama.ShowResponse, error) {
	ms, err := o.c.Models(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		if m.ID != model {
			ids = append(ids, m.ID)
			continue
		}
		info := map[string]any{}
		if n := o.window(m); n > 0 {
			// Ключ с суффиксом .context_length — его ищет ollama.ContextLengthFromShow.
			info["openai.context_length"] = float64(n)
		}
		return &ollama.ShowResponse{Capabilities: openAICaps, ModelInfo: info}, nil
	}
	return nil, fmt.Errorf("модели %q нет на сервере %s (есть: %s)", model, o.c.BaseURL(), strings.Join(ids, ", "))
}

// Resident — «не знаю»: у API нет пути, отвечающего, где лежит модель, и
// интерфейс в этом случае молчит, а не выдумывает загрузку.
func (o *openAI) Resident(context.Context, string) ollama.Residency { return ollama.Residency{} }

// window — окно модели: context_length из конфига, иначе то, что сообщил сервер.
func (o *openAI) window(m oai.Model) int {
	if o.ctxLen > 0 {
		return o.ctxLen
	}
	return m.ContextLength
}
