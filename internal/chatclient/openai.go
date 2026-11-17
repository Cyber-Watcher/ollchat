package chatclient

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
	"github.com/Cyber-Watcher/ollchat/internal/oai"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// openAICaps — что объявлять о модели, пока проба не ответила (список моделей)
// или когда пробы выключены (probe_caps = false). /v1/models возможностей не
// сообщает. Так было до этапа 129 всегда — и шапка показывала vision у vLLM с
// --language-model-only (замечание владельца 17.11.2026).
var openAICaps = []string{"completion", "tools", "thinking", "vision"}

// probeTimeout — срок трёх проб возможностей разом. Пробы по 0.2–0.44 с на
// свободном движке (записи этапа 129); под нагрузкой ждут очереди, и тогда
// лучше «?» в шапке, чем задержка сведений о модели.
const probeTimeout = 20 * time.Second

// openAI — сервер с OpenAI-совместимым API в форме Client.
type openAI struct {
	c      *oai.Client
	ctxLen int  // [[servers]].context_length: перекрывает окно из /v1/models
	probe  bool // [[servers]].probe_caps: узнавать возможности пробами

	mu   sync.Mutex
	caps map[string]oai.Caps // итоги проб по моделям: пробы — раз на модель за жизнь клиента
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
	return &openAI{c: c, ctxLen: srv.ContextLength, probe: srv.ProbesCaps()}
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
// Возможности — по пробам движка (этап 129): что он выключил, уходит в
// CapabilitiesOff, на что проба не ответила — в CapabilitiesUnknown.
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
		show := &ollama.ShowResponse{ModelInfo: info}
		show.Capabilities, show.CapabilitiesOff, show.CapabilitiesUnknown = o.capsOf(ctx, model)
		return show, nil
	}
	return nil, fmt.Errorf("модели %q нет на сервере %s (есть: %s)", model, o.c.BaseURL(), strings.Join(ids, ", "))
}

// capsOf — возможности модели по пробам: включённые (с ними и непроверенные —
// запрещать из-за незнания хуже), выключенные движком и непроверенные. Пробы —
// раз на модель; probe_caps = false — всё объявлено и всё непроверено.
func (o *openAI) capsOf(ctx context.Context, model string) (on, off, unknown []string) {
	caps := oai.Caps{}
	if o.probe {
		o.mu.Lock()
		got, ok := o.caps[model]
		o.mu.Unlock()
		if !ok {
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			got = o.c.ProbeCaps(pctx, model)
			cancel()
			o.mu.Lock()
			if o.caps == nil {
				o.caps = map[string]oai.Caps{}
			}
			o.caps[model] = got
			o.mu.Unlock()
		}
		caps = got
	}
	on = []string{"completion"}
	for _, c := range []struct {
		name  string
		state oai.CapState
	}{{"tools", caps.Tools}, {"thinking", caps.Thinking}, {"vision", caps.Vision}} {
		switch c.state {
		case oai.CapOn:
			on = append(on, c.name)
		case oai.CapOff:
			off = append(off, c.name)
		case oai.CapUnknown:
			on = append(on, c.name)
			unknown = append(unknown, c.name)
		}
	}
	return on, off, unknown
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
