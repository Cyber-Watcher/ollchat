// Package oai — клиент OpenAI-совместимого API (vLLM, SGLang, LiteLLM,
// llama-server) для чата ollchat (этап 127, Ш3).
//
// Внутренний язык программы — типы internal/ollama: клиент принимает
// ollama.ChatRequest и отдаёт те же события ollama.Event, что и
// ollama.(*Client).Chat, поэтому агент, сводка истории и инструменты не знают,
// с каким API говорят. Перевод — только на границе: request.go (запрос),
// stream.go (поток SSE). Чего у OpenAI API нет (окно в запросе, keep_alive,
// загружена ли модель), клиент не изображает.
package oai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// defaultChatTimeout — ожидание заголовков ответа чата, если chatTimeout не
// задан. То же, что у клиента Ollama (internal/ollama/client.go,
// defaultChatTimeout): движок не шлёт ни байта, пока не прочитает промпт, а на
// большом окне это минуты. Там постоянная не экспортирована, поэтому здесь копия.
const defaultChatTimeout = 30 * time.Minute

// defaultTimeout — ожидание заголовков быстрых вызовов (Models), как
// у ollama.NewWithStall.
const defaultTimeout = 300 * time.Second

// maxResponseBody — предел тела ответа быстрых вызовов, как maxResponseBody
// клиента Ollama. Поток чата им не ограничен: у него своё чтение (stream.go).
const maxResponseBody = 64 << 20

// maxErrorBody — сколько байт тела ответа не-200 читается в текст ошибки,
// как у serverError клиента Ollama.
const maxErrorBody = 4096

// Client — клиент одного OpenAI-совместимого сервера.
type Client struct {
	baseURL  string // как задано в настройках, без завершающей «/»
	apiURL   string // корень API: baseURL с «/v1» на конце
	apiKey   string
	headers  map[string]string
	http     *http.Client // Models: короткое ожидание заголовков
	chatHTTP *http.Client // Chat: заголовки приходят только после prefill

	// stallTimeout — сколько поток ответа может молчать, прежде чем запрос
	// будет оборван. Ноль выключает сторожа (ollama.NewStallReader).
	stallTimeout time.Duration

	// maxBody — предел тела ответа Models. Поле, а не постоянная, ради тестов,
	// как у клиента Ollama.
	maxBody int64

	// user — кто спрашивает: поле user каждого запроса чата. По нему шлюз
	// LiteLLM ведёт учёт конечных пользователей («Flat end-user ID in user»,
	// docs.litellm.ai/docs/proxy/customers), иначе все запросы ollchat легли бы
	// на один ключ. Пусто — поле не уходит.
	user string

	// longTokens и longPriority — приоритет «большого нового входа» (WithPriority):
	// запрос, чей новый вход не меньше longTokens токенов, уходит с полем
	// priority = longPriority. Ноль longTokens — поле не уходит вовсе.
	longTokens, longPriority int
}

// WithPriority задаёт приоритет больших входов и возвращает тот же клиент.
//
// Зачем (серия Z, «утренний залп»): длинные документы движок разбирает по
// очереди, и всё это время короткие диалоги стоят — у vLLM без защиты 724.1 с
// медианой; с --scheduling-policy priority и меньшим числом у коротких —
// 28.3 с. Большим входам здесь даётся БОЛЬШЕЕ число: у vLLM «lower value means
// earlier handling», у SGLang так же только с --schedule-low-priority-values-first.
// Движок без политики приоритета запрос с полем priority отвергает, поэтому
// по умолчанию поле не шлётся. Зовётся до первого запроса: поля читаются без замка.
func (c *Client) WithPriority(longTokens, priority int) *Client {
	c.longTokens, c.longPriority = longTokens, priority
	return c
}

// requestPriority — поле priority запроса: nil — не слать (приоритет не задан
// или вход небольшой).
func (c *Client) requestPriority(msgs []ollama.Message) *int {
	if c.longTokens <= 0 || newInputTokens(msgs) < c.longTokens {
		return nil
	}
	p := c.longPriority
	return &p
}

// newInputTokens — оценка нового входа хода: сообщения пользователя и
// инструментов после последнего ответа модели. Историю движок обычно берёт из
// кеша префиксов, а разбирать заново ему этот хвост — вставленный документ,
// большой вывод инструмента; его длина и делает запрос «длинным». Оценка —
// ctxmeter.Estimate (байты UTF-8 / 3, с запасом в большую сторону).
func newInputTokens(msgs []ollama.Message) int {
	n := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == ollama.RoleAssistant {
			break
		}
		if m.Role == ollama.RoleUser || m.Role == ollama.RoleTool {
			n += ctxmeter.Estimate(m.Content)
		}
	}
	return n
}

// WithUser задаёт поле user запросов чата и возвращает тот же клиент. Зовётся
// при сборке клиента, до первого запроса: поле читается без замка.
func (c *Client) WithUser(user string) *Client {
	c.user = strings.TrimSpace(user)
	return c
}

// New создаёт клиент.
//
// baseURL — корень сервера ("http://host:8000"); если путь уже кончается на
// "/v1", второй не добавляется. apiKey непустой — заголовок
// "Authorization: Bearer <ключ>" в каждом запросе (поверх такого же заголовка
// из headers: ключ задан явно). Сам ключ не попадает ни в одну ошибку клиента.
// timeout — ожидание заголовков у Models, chatTimeout — у чата, stall — предел
// молчания потока (ноль выключает); нули — умолчания клиента Ollama.
func New(baseURL, apiKey string, timeout, chatTimeout, stall time.Duration,
	headers map[string]string) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if chatTimeout <= 0 {
		chatTimeout = defaultChatTimeout
	}
	base := strings.TrimRight(baseURL, "/")
	api := base
	if !strings.HasSuffix(api, "/v1") {
		api += "/v1"
	}
	return &Client{
		baseURL: base,
		apiURL:  api,
		// Ключ из файла часто кончается переводом строки; в заголовке он
		// сделал бы запрос недопустимым.
		apiKey:       strings.TrimSpace(apiKey),
		headers:      headers,
		stallTimeout: stall,
		maxBody:      maxResponseBody,
		http:         &http.Client{Transport: newTransport(timeout)},
		chatHTTP:     &http.Client{Transport: newTransport(chatTimeout)},
	}
}

// newTransport — транспорт клиента Ollama (ollama.NewTransport): одно правило
// сроков соединения, TLS, прокси и ожидания заголовков на оба клиента.
func newTransport(header time.Duration) *http.Transport { return ollama.NewTransport(header) }

// BaseURL возвращает адрес сервера, как он задан (без «/» на конце).
func (c *Client) BaseURL() string { return c.baseURL }

// newRequest собирает запрос к path от корня API ("/chat/completions").
func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

// redact убирает ключ API из текста, пришедшего от сервера: шлюз в ответе 401
// бывает вправе повторить присланный ключ, а текст ошибки идёт в ленту
// и журнал.
func (c *Client) redact(s string) string {
	if c.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "***")
}

// serverError читает тело ответа не-200 и собирает ошибку «сервер вернул …»
// с тем же текстом, что у клиента Ollama. 5xx и 429 помечены как повторяемые:
// это сбой или перегрузка сервера, а не неверный запрос.
func (c *Client) serverError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	err := fmt.Errorf("сервер вернул %s: %s", resp.Status, c.redact(errorText(msg)))
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return ollama.MarkRetryable(err)
	}
	return err
}

// errorText достаёт человеческий текст ошибки из тела ответа. Формы,
// снятые вживую 17.11.2026 (этап 127, Ш1):
//
//	{"error":{"message":…,"type":…,"code":…}}  vLLM, llama-server, LiteLLM
//	{"object":"error","message":…}              SGLang
//
// плюс {"error":"…"} строкой. Иначе — тело как есть.
func errorText(body []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if m := nestedMessage(e.Error); m != "" {
			return m
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return strings.TrimSpace(string(body))
}

// nestedMessage — текст из поля error: объекта с message или строки.
func nestedMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// Model — модель из GET /v1/models.
type Model struct {
	ID string
	// ContextLength — окно модели, если сервер его сообщил; 0 — не сообщил.
	ContextLength int
}

// modelsResponse — ответ GET /v1/models. Окно у каждого сервера своё
// (снято вживую 17.11.2026, этап 127, Ш1): max_model_len — vLLM и SGLang,
// meta.n_ctx — llama-server (рядом n_ctx_train — окно обучения, не запуска),
// max_input_tokens — псевдоним LiteLLM, context_length — прочие.
type modelsResponse struct {
	Data []struct {
		ID          string     `json:"id"`
		MaxModelLen lenientInt `json:"max_model_len"`
		Meta        struct {
			NCtx lenientInt `json:"n_ctx"`
		} `json:"meta"`
		MaxInputTokens lenientInt `json:"max_input_tokens"`
		ContextLength  lenientInt `json:"context_length"`
	} `json:"data"`
}

// lenientInt — целое, которое не роняет разбор всего списка, если сервер
// прислал его строкой, null или чем-то иным: тогда 0, «не сообщил».
type lenientInt int

func (n *lenientInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 && f <= math.MaxInt32 {
		*n = lenientInt(f)
	}
	return nil
}

// Models возвращает список моделей сервера: GET /v1/models.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	const what = "GET /v1/models"
	req, err := c.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %w", what, c.serverError(resp))
	}
	// Предел — maxBody байт; читается на байт больше, чтобы отличить ответ
	// ровно в предел от ответа длиннее него.
	limit := c.maxBody
	if limit <= 0 {
		limit = maxResponseBody
	}
	lr := &io.LimitedReader{R: resp.Body, N: limit + 1}
	var mr modelsResponse
	err = json.NewDecoder(lr).Decode(&mr)
	if lr.N <= 0 {
		return nil, fmt.Errorf("%s: ответ длиннее предела в %d байт — это не список моделей", what, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: разбор ответа: %w", what, err)
	}
	out := make([]Model, 0, len(mr.Data))
	for _, d := range mr.Data {
		m := Model{ID: d.ID}
		for _, n := range []lenientInt{d.MaxModelLen, d.Meta.NCtx, d.MaxInputTokens, d.ContextLength} {
			if n > 0 {
				m.ContextLength = int(n)
				break
			}
		}
		out = append(out, m)
	}
	return out, nil
}
