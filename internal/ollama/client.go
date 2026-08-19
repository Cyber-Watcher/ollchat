package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultChatTimeout — сколько ждать первого байта ответа /api/chat, если
// chatTimeout не задан. Ollama не шлёт ни байта, пока не закончит обработку
// промпта (prefill) — на большом контексте, пересчитываемом с нуля, это может
// занять много минут при живом и работающем сервере. 30 минут — с запасом
// против худшего измеренного на стенде: 262144 токена при просевшей до
// 460-700 ток/с скорости обработки (после расширения окна до 262144 сама
// Ollama урезает размер пачки) — это около 10 минут.
const defaultChatTimeout = 30 * time.Minute

// Client — клиент одного сервера Ollama.
type Client struct {
	baseURL  string
	headers  map[string]string
	http     *http.Client // Version/Tags/PS/Show: короткое ожидание заголовков
	chatHTTP *http.Client // Chat: заголовки могут прийти только после prefill
}

// New создаёт клиент. baseURL — например "http://192.168.77.77:11434" (без /api).
// timeout — ожидание заголовков у быстрых вызовов (Version/Tags/PS/Show).
// chatTimeout — то же самое, но для потокового /api/chat: Ollama не шлёт ни
// байта, пока не обработает весь промпт, поэтому этот таймаут должен быть
// намного щедрее, а не разделять транспорт — значило бы обрывать честно
// работающий сервер. От мёртвого соединения при чате защищает не этот таймаут,
// а отмена пользователем (Esc/Ctrl+C).
func New(baseURL string, timeout, chatTimeout time.Duration, headers map[string]string) *Client {
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	if chatTimeout <= 0 {
		chatTimeout = defaultChatTimeout
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		headers: headers,
		http: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: timeout,
				IdleConnTimeout:       90 * time.Second,
			},
		},
		chatHTTP: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: chatTimeout,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

// BaseURL возвращает адрес сервера.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// do выполняет запрос и разбирает JSON-ответ в out.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: сервер вернул %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Version возвращает версию сервера Ollama.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v VersionResponse
	if err := c.do(ctx, http.MethodGet, "/api/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// Tags возвращает список моделей, доступных на сервере.
func (c *Client) Tags(ctx context.Context) ([]ModelInfo, error) {
	var t TagsResponse
	if err := c.do(ctx, http.MethodGet, "/api/tags", nil, &t); err != nil {
		return nil, err
	}
	return t.Models, nil
}

// PS возвращает список моделей, загруженных в память сервера.
func (c *Client) PS(ctx context.Context) ([]RunningModel, error) {
	var p PSResponse
	if err := c.do(ctx, http.MethodGet, "/api/ps", nil, &p); err != nil {
		return nil, err
	}
	return p.Models, nil
}

// Show возвращает подробную информацию о модели.
func (c *Client) Show(ctx context.Context, model string) (*ShowResponse, error) {
	var s ShowResponse
	body := map[string]string{"model": model}
	if err := c.do(ctx, http.MethodPost, "/api/show", body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ContextLengthFromShow достаёт ёмкость контекста из model_info. Ключ зависит от
// семейства модели ("gemma3.context_length", "qwen3moe.context_length" и т.п.),
// поэтому ищем любой ключ с суффиксом ".context_length".
func ContextLengthFromShow(s *ShowResponse) (int, bool) {
	if s == nil {
		return 0, false
	}
	for k, v := range s.ModelInfo {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		if f, ok := v.(float64); ok && f > 0 {
			return int(f), true
		}
	}
	return 0, false
}
