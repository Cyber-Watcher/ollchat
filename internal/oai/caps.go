package oai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Пробы возможностей модели у OpenAI-совместимого сервера (этап 129).
//
// /v1/models возможностей не сообщает, а ключей запуска vLLM не отдаёт вовсе:
// /server_info у vLLM 0.32.0 — 404, в его openapi такого пути нет (снято
// 17.11.2026 на A100). Шлюз LiteLLM на /model/info отдаёт supports_vision и
// соседей пустыми — он знает только свой конфиг. Поэтому возможности узнаются
// так же, как их узнал бы пользователь: тремя крошечными запросами тем же путём,
// каким пойдёт работа, — через шлюз, если он есть. Отказы движка шлюз передаёт
// внутри своей ошибки, и признаки ниже ищутся подстрокой по всему телу.
//
// Записи, на которых признаки сняты, — testdata/caps/<вариант>/caps-*.raw
// (privatescripts/tuf/probe_ctx.py caps; vLLM 0.32.0 и SGLang 0.5.22, напрямую
// и через LiteLLM v1.103.2, всё включено и всё выключено).

// CapState — что проба узнала о возможности.
type CapState int

const (
	// CapUnknown — проба не дала внятного ответа: сеть, 5xx, незнакомый отказ,
	// модель не вызвала инструмент. Интерфейс показывает «?» и не запрещает.
	CapUnknown CapState = iota
	// CapOn — движок принимает и разбирает.
	CapOn
	// CapOff — у модели есть, движок выключил: vLLM с --language-model-only,
	// без --enable-auto-tool-choice, без разборщика мышления.
	CapOff
	// CapAbsent — у модели нет: ответила сразу, без мышления.
	CapAbsent
)

// Caps — возможности модели, как их видит движок.
type Caps struct {
	Vision, Tools, Thinking CapState
}

// Признаки «у модели есть, движок выключил» в теле отказа 400.
var (
	// vLLM с --language-model-only: «At most 0 image(s) may be provided in one prompt»;
	// SGLang с --limit-mm-data-per-request '{"image": 0}': «Image count 1 exceeds limit 0
	// per request» (поднимается только с --skip-server-warmup: прогрев шлёт картинку).
	visionOffMarks = []string{"At most 0 image(s)", "exceeds limit 0 per request"}
	// vLLM без разборщика: «"auto" tool choice requires --enable-auto-tool-choice
	// and --tool-call-parser to be set».
	toolsOffMarks = []string{"--enable-auto-tool-choice"}
)

// Сырая разметка в тексте ответа: модель её пишет, движок не разобрал.
var (
	toolsRawMarks = []string{"<tool_call>", "<function="}
	thinkRawMarks = []string{"</think>", "<think>"}
)

// png1x1 — картинка 1×1 для пробы зрения (та же, что в probe_ctx.py).
const png1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// probeBodies — тела трёх проб. Зрение — один токен: ответ виден по коду.
// Инструменты и мышление — 64 токена: движок без разборщика (SGLang) запрос
// не отвергает, и разница видна только в ответе.
func probeBodies(model, user string) (vision, tools, think map[string]any) {
	noThink := map[string]any{"enable_thinking": false}
	base := func(maxTokens int) map[string]any {
		b := map[string]any{"model": model, "max_tokens": maxTokens, "stream": false}
		if user != "" {
			b["user"] = user
		}
		return b
	}
	vision = base(1)
	vision["chat_template_kwargs"] = noThink
	vision["messages"] = []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "."},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + png1x1}},
	}}}
	tools = base(64)
	tools["chat_template_kwargs"] = noThink
	tools["tool_choice"] = "auto"
	tools["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{
		"name": "get_weather", "description": "погода в городе сейчас",
		"parameters": map[string]any{"type": "object", "required": []string{"city"},
			"properties": map[string]any{"city": map[string]any{"type": "string", "description": "город"}}},
	}}}
	tools["messages"] = []any{map[string]any{"role": "user",
		"content": "Какая погода в Париже? Вызови инструмент get_weather."}}
	think = base(64)
	think["messages"] = []any{map[string]any{"role": "user", "content": "2+2?"}}
	return vision, tools, think
}

// maxProbeBody — сколько байт ответа пробы читается: ответ на 64 токена
// и текст отказа помещаются с большим запасом.
const maxProbeBody = 64 << 10

// probeResult — код и тело ответа пробы; err — сбой до ответа.
type probeResult struct {
	code int
	body []byte
	err  error
}

// probe шлёт одну пробу. Срок — у ctx вызывающего.
func (c *Client) probe(ctx context.Context, body map[string]any) probeResult {
	raw, err := json.Marshal(body)
	if err != nil {
		return probeResult{err: err}
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/chat/completions", raw)
	if err != nil {
		return probeResult{err: err}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return probeResult{err: err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	return probeResult{code: resp.StatusCode, body: b, err: err}
}

// ProbeCaps узнаёт у движка, что из возможностей модели он принимает. Три пробы
// идут разом; срок — у ctx. Ответ «не знаю» (CapUnknown) — не ошибка: шапка
// покажет «?», а запросы пойдут как прежде.
func (c *Client) ProbeCaps(ctx context.Context, model string) Caps {
	vb, tb, kb := probeBodies(model, c.user)
	var vr, tr, kr probeResult
	var wg sync.WaitGroup
	for _, p := range []struct {
		body map[string]any
		out  *probeResult
	}{{vb, &vr}, {tb, &tr}, {kb, &kr}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			*p.out = c.probe(ctx, p.body)
		}()
	}
	wg.Wait()
	return Caps{Vision: visionState(vr), Tools: toolsState(tr), Thinking: thinkState(kr)}
}

func containsAny(b []byte, marks []string) bool {
	for _, m := range marks {
		if bytes.Contains(b, []byte(m)) {
			return true
		}
	}
	return false
}

// visionState: ответ чата на картинку — зрение есть; отказ с признаком
// выключенного зрения — Off. 200 не ответом чата (страница прокси) ничего не
// доказывает.
func visionState(r probeResult) CapState {
	switch {
	case r.err != nil:
		return CapUnknown
	case r.code == http.StatusOK:
		if _, ok := parseProbe(r.body); ok {
			return CapOn
		}
		return CapUnknown
	case r.code == http.StatusBadRequest && containsAny(r.body, visionOffMarks):
		return CapOff
	}
	return CapUnknown
}

// probeMessage — первое сообщение ответа пробы. Мышление у движков в разных
// полях (этап 127, Ш1): reasoning — vLLM, reasoning_content — SGLang и LiteLLM.
type probeMessage struct {
	Content          string            `json:"content"`
	Reasoning        string            `json:"reasoning"`
	ReasoningContent string            `json:"reasoning_content"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
	Finish           string            `json:"-"`
}

func parseProbe(body []byte) (probeMessage, bool) {
	var resp struct {
		Choices []struct {
			Message      probeMessage `json:"message"`
			FinishReason string       `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		return probeMessage{}, false
	}
	m := resp.Choices[0].Message
	m.Finish = resp.Choices[0].FinishReason
	return m, true
}

// toolsState: вызов в tool_calls — On; отказ vLLM без разборщика или сырая
// разметка вызова в тексте — Off; модель ответила текстом — не знаем.
func toolsState(r probeResult) CapState {
	switch {
	case r.err != nil:
		return CapUnknown
	case r.code == http.StatusBadRequest && containsAny(r.body, toolsOffMarks):
		return CapOff
	case r.code != http.StatusOK:
		return CapUnknown
	}
	m, ok := parseProbe(r.body)
	switch {
	case !ok:
		return CapUnknown
	case len(m.ToolCalls) > 0:
		return CapOn
	case containsAny([]byte(m.Content), toolsRawMarks):
		return CapOff
	}
	return CapUnknown
}

// thinkState: мышление в отдельном поле — On; разметка <think> в тексте —
// модель думает, движок не разбирает (Off); ответила и остановилась без
// мышления — Absent; оборвана на пределе без разметки — не знаем.
func thinkState(r probeResult) CapState {
	if r.err != nil || r.code != http.StatusOK {
		return CapUnknown
	}
	m, ok := parseProbe(r.body)
	switch {
	case !ok:
		return CapUnknown
	case strings.TrimSpace(m.Reasoning) != "" || strings.TrimSpace(m.ReasoningContent) != "":
		return CapOn
	case containsAny([]byte(m.Content), thinkRawMarks):
		return CapOff
	case m.Finish == "stop":
		return CapAbsent
	}
	return CapUnknown
}
