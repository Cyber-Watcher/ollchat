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

	// stallTimeout — сколько поток ответа может молчать, прежде чем запрос
	// будет оборван. Ноль выключает сторожа. См. stall.go.
	stallTimeout time.Duration
}

// New создаёт клиент. baseURL — например "http://ollama.example:11434" (без /api).
// timeout — ожидание заголовков у быстрых вызовов (Version/Tags/PS/Show).
// chatTimeout — то же самое, но для потокового /api/chat: Ollama не шлёт ни
// байта, пока не обработает весь промпт, поэтому этот таймаут должен быть
// намного щедрее, а не разделять транспорт — значило бы обрывать честно
// работающий сервер. От мёртвого соединения при чате защищает не этот таймаут,
// а отмена пользователем (Esc/Ctrl+C).
// New собирает клиент с общим и потоковым таймаутами.
//
// Сторож молчания при этом выключен: его включает NewWithStall. Так сделано
// нарочно — вызовов New много, и молча менять их поведение опаснее, чем
// потребовать явного включения там, где это нужно.
func New(baseURL string, timeout, chatTimeout time.Duration, headers map[string]string) *Client {
	return NewWithStall(baseURL, timeout, chatTimeout, 0, headers)
}

// NewWithStall — то же, но с пределом молчания потока.
func NewWithStall(baseURL string, timeout, chatTimeout, stall time.Duration,
	headers map[string]string) *Client {
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	if chatTimeout <= 0 {
		chatTimeout = defaultChatTimeout
	}
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		headers:      headers,
		stallTimeout: stall,
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
		srvErr, _ := serverError(resp)
		return fmt.Errorf("%s %s: %w", method, path, srvErr)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// serverError читает тело ответа не-200 и собирает ошибку «сервер вернул …».
// Одна на do, поток чата и эмбеддинги (этап 91, R8.5): три копии разбора
// давали три разных текста об одном. Тело возвращается отдельно — по нему
// эмбеддинги узнают смерть раннера.
func serverError(resp *http.Response) (error, string) {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(msg))
	return fmt.Errorf("сервер вернул %s: %s", resp.Status, body), body
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

// Residency — где сейчас находится модель на сервере.
//
// **Зачем это нужно интерфейсу.** Ollama загружает модель в память карты при
// первом запросе, и загрузка идёт десятки секунд: чтение весов с диска плюс
// перенос в видеопамять. Всё это время `/api/chat` молчит — не приходит ни
// куска ответа. Со стороны программа выглядит зависшей ровно так же, как если
// бы она и вправду зависла, и человек первым делом жмёт Ctrl+C — то есть
// отменяет загрузку и начинает её заново следующим вопросом.
//
// Единственный способ отличить одно от другого — спросить сервер, что у него
// загружено, до того как ждать ответа.
type Residency struct {
	// Loaded — модель уже в памяти сервера и отвечать начнёт сразу.
	Loaded bool
	// VRAMPct — сколько процентов веса лежит в видеопамяти. Меньше 90 означает,
	// что часть считается на процессоре, и ответ пойдёт в разы медленнее.
	VRAMPct int
	// Known — сервер ответил. Ложь означает «спросить не удалось»: сеть,
	// таймаут, старая версия. Тогда молчим, а не выдумываем.
	Known bool
}

// Resident спрашивает сервер, загружена ли модель.
//
// Ошибку не возвращает намеренно: это подсказка, а не работа. Не ответил
// сервер — Known остаётся ложью, и вызывающий просто ничего не показывает.
func (c *Client) Resident(ctx context.Context, model string) Residency {
	running, err := c.PS(ctx)
	if err != nil {
		return Residency{}
	}
	for _, m := range running {
		if !sameModel(m, model) {
			continue
		}
		if m.Size <= 0 {
			return Residency{Loaded: true, VRAMPct: 100, Known: true}
		}
		return Residency{Loaded: true, VRAMPct: int(m.SizeVRAM * 100 / m.Size), Known: true}
	}
	return Residency{Known: true}
}

// sameModel сравнивает имя из /api/ps с тем, что задано в настройках.
//
// Имя в конфиге пишут и с тегом (`qwen3.5:122b`), и без него (`qwen3.5`),
// а сервер всегда отвечает с тегом. Без учёта этого проверка «загружена ли»
// всегда отвечала бы «нет» для половины конфигов.
func sameModel(m RunningModel, want string) bool {
	if want == "" {
		return false
	}
	for _, have := range []string{m.Name, m.Model} {
		if have == want || strings.HasPrefix(have, want+":") {
			return true
		}
	}
	return false
}
