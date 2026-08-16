package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// EventKind — вид события потока генерации.
type EventKind int

// Виды событий потока.
const (
	EventContent   EventKind = iota // очередной кусок текста ответа
	EventThinking                   // очередной кусок рассуждений
	EventToolCalls                  // модель запросила вызов инструментов
	EventDone                       // генерация завершена, доступна статистика
	EventError                      // ошибка сети или сервера
)

// Event — событие потока генерации.
type Event struct {
	Kind      EventKind
	Text      string     // для EventContent и EventThinking
	ToolCalls []ToolCall // для EventToolCalls
	Stats     Stats      // для EventDone
	Err       error      // для EventError
}

// Stats — статистика завершённой генерации из финального чанка.
type Stats struct {
	PromptEvalCount int   // токенов в промпте — фактический размер занятого контекста
	EvalCount       int   // токенов сгенерировано
	TotalDuration   int64 // наносекунды
	LoadDuration    int64
	EvalDuration    int64
	DoneReason      string
}

// TokensPerSecond возвращает скорость генерации; 0, если данных недостаточно.
func (s Stats) TokensPerSecond() float64 {
	if s.EvalDuration <= 0 || s.EvalCount <= 0 {
		return 0
	}
	return float64(s.EvalCount) / (float64(s.EvalDuration) / 1e9)
}

// TotalTokens — сколько токенов занято в контекстном окне после этого ответа.
func (s Stats) TotalTokens() int { return s.PromptEvalCount + s.EvalCount }

// ErrCanceled возвращается, когда генерация прервана пользователем.
var ErrCanceled = errors.New("генерация прервана")

// RetryableError помечает сбой, при котором повторный запрос имеет смысл:
// обрыв соединения, ошибка сервера 5xx или сбой генерации, о котором сервер
// сообщил прямо в потоке. Ошибки вида «модель не найдена» так не помечаются —
// повторять их бессмысленно.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Retryable сообщает, имеет ли смысл повторить запрос.
func Retryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}

func retryable(err error) error { return &RetryableError{Err: err} }

// Chat запускает генерацию и возвращает канал событий. Канал закрывается после
// EventDone или EventError. Прерывание — отменой ctx.
func (c *Client) Chat(ctx context.Context, req ChatRequest) <-chan Event {
	out := make(chan Event, 64)

	go func() {
		defer close(out)

		req.Stream = true
		httpReq, err := c.newRequest(ctx, http.MethodPost, "/api/chat", req)
		if err != nil {
			out <- Event{Kind: EventError, Err: err}
			return
		}

		resp, err := c.http.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				out <- Event{Kind: EventError, Err: ErrCanceled}
				return
			}
			// Сетевой сбой — соединение можно попробовать установить заново.
			out <- Event{Kind: EventError,
				Err: retryable(fmt.Errorf("запрос к %s: %w", c.baseURL, err))}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			srvErr := fmt.Errorf("сервер вернул %s: %s", resp.Status, strings.TrimSpace(string(msg)))
			// 5xx — сбой на стороне сервера, его есть смысл повторить.
			// 4xx означает неверный запрос: повтор ничего не изменит.
			if resp.StatusCode >= 500 {
				out <- Event{Kind: EventError, Err: retryable(srvErr)}
				return
			}
			out <- Event{Kind: EventError, Err: srvErr}
			return
		}

		// Ответ — поток JSON-объектов, разделённых переводами строк.
		// json.Decoder читает их подряд и не ограничивает длину объекта.
		dec := json.NewDecoder(resp.Body)
		for {
			var chunk ChatResponse
			if err := dec.Decode(&chunk); err != nil {
				if errors.Is(err, io.EOF) {
					// Поток закончился без финального чанка — считаем это обрывом.
					out <- Event{Kind: EventError,
						Err: retryable(errors.New("поток ответа оборван сервером"))}
					return
				}
				if ctx.Err() != nil {
					out <- Event{Kind: EventError, Err: ErrCanceled}
					return
				}
				out <- Event{Kind: EventError, Err: retryable(fmt.Errorf("разбор ответа: %w", err))}
				return
			}

			if chunk.Error != "" {
				// Сервер сообщил о сбое прямо в потоке — генерация не удалась.
				// Такое бывает, например, когда сервер не может разобрать вывод
				// модели; повтор запроса обычно проходит успешно.
				out <- Event{Kind: EventError, Err: retryable(errors.New(chunk.Error))}
				return
			}

			if len(chunk.Message.ToolCalls) > 0 {
				out <- Event{Kind: EventToolCalls, ToolCalls: chunk.Message.ToolCalls}
			}
			if chunk.Message.Thinking != "" {
				out <- Event{Kind: EventThinking, Text: chunk.Message.Thinking}
			}
			if chunk.Message.Content != "" {
				out <- Event{Kind: EventContent, Text: chunk.Message.Content}
			}

			if chunk.Done {
				out <- Event{Kind: EventDone, Stats: Stats{
					PromptEvalCount: chunk.PromptEvalCount,
					EvalCount:       chunk.EvalCount,
					TotalDuration:   chunk.TotalDuration,
					LoadDuration:    chunk.LoadDuration,
					EvalDuration:    chunk.EvalDuration,
					DoneReason:      chunk.DoneReason,
				}}
				return
			}
		}
	}()

	return out
}
