package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	PromptEvalCount    int   // токенов в промпте — фактический размер занятого контекста
	PromptEvalDuration int64 // наносекунды на чтение промпта
	EvalCount          int   // токенов сгенерировано
	TotalDuration      int64 // наносекунды
	LoadDuration       int64
	EvalDuration       int64
	DoneReason         string
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

// PromptTokensPerSecond — скорость чтения промпта; 0, если данных недостаточно.
//
// Это другая величина, чем скорость генерации: промпт читается разом (сервер
// зовёт это prefill), и на неё влияет num_batch, а не скорость выдачи токенов.
func (s Stats) PromptTokensPerSecond() float64 {
	if s.PromptEvalDuration <= 0 || s.PromptEvalCount <= 0 {
		return 0
	}
	return float64(s.PromptEvalCount) / (float64(s.PromptEvalDuration) / 1e9)
}

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

// MarkRetryable помечает ошибку как повторяемую: сбой дороги или сервера,
// который имеет смысл повторить. Экспортирована, чтобы агент мог пометить тот же
// обрыв («поток кончился без завершающего чанка») тем же признаком.
func MarkRetryable(err error) error { return &RetryableError{Err: err} }

// Chat запускает генерацию и возвращает канал событий. Канал закрывается после
// EventDone или EventError. Прерывание — отменой ctx.
func (c *Client) Chat(ctx context.Context, req ChatRequest) <-chan Event {
	out := make(chan Event, 64)

	parent := ctx
	go func() {
		defer close(out)

		// Сторожу молчания нужен свой отменяемый контекст: он рвёт запрос,
		// когда поток замолкает дольше отведённого. См. stall.go — там
		// разобрано, чем обошлось отсутствие такого предела.
		ctx, cancelStall := context.WithCancel(ctx)
		defer cancelStall()

		req.Stream = true
		httpReq, err := c.newRequest(ctx, http.MethodPost, "/api/chat", req)
		if err != nil {
			out <- Event{Kind: EventError, Err: err}
			return
		}

		resp, err := c.chatHTTP.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				out <- Event{Kind: EventError, Err: ErrCanceled}
				return
			}
			// Сетевой сбой — соединение можно попробовать установить заново.
			// Сюда же попадает «connection refused»: сборка графа ходит на стенд
			// через ssh-туннель, и 01.09.2026 семь секунд его переподъёма стоили
			// целого захода — отказ в соединении сочли неустранимым и остановили
			// всё. Адрес на месте, слушателя нет **сейчас**: это и надо повторять.
			out <- Event{Kind: EventError,
				Err: MarkRetryable(fmt.Errorf("запрос к %s: %w", c.baseURL, err))}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			srvErr, _ := serverError(resp)
			// 5xx — сбой на стороне сервера, его есть смысл повторить.
			// 4xx означает неверный запрос: повтор ничего не изменит.
			if resp.StatusCode >= 500 {
				out <- Event{Kind: EventError, Err: MarkRetryable(srvErr)}
				return
			}
			out <- Event{Kind: EventError, Err: srvErr}
			return
		}

		// Ответ — поток JSON-объектов, разделённых переводами строк.
		// json.Decoder читает их подряд и не ограничивает длину объекта.
		body := newStallReader(resp.Body, c.stallTimeout, cancelStall)
		defer body.Close()
		dec := json.NewDecoder(body)
		for {
			var chunk ChatResponse
			if err := dec.Decode(&chunk); err != nil {
				if errors.Is(err, io.EOF) {
					// Поток закончился без финального чанка — считаем это обрывом.
					out <- Event{Kind: EventError,
						Err: MarkRetryable(errors.New("поток ответа оборван сервером"))}
					return
				}
				if ctx.Err() != nil {
					// Отличаем отмену человеком от обрыва по молчанию: первое
					// — обычное дело, второе — беда на сервере, и говорить
					// о ней надо прямо.
					if parent.Err() == nil && c.stallTimeout > 0 {
						out <- Event{Kind: EventError, Err: MarkRetryable(fmt.Errorf(
							"сервер молчит дольше %s — вероятно, запрос стоит в очереди "+
								"за другой моделью; предел меняется настройкой stall_timeout",
							c.stallTimeout))}
						return
					}
					out <- Event{Kind: EventError, Err: ErrCanceled}
					return
				}
				out <- Event{Kind: EventError, Err: MarkRetryable(fmt.Errorf("разбор ответа: %w", err))}
				return
			}

			if chunk.Error != "" {
				// Сервер сообщил о сбое прямо в потоке — генерация не удалась.
				// Такое бывает, например, когда сервер не может разобрать вывод
				// модели; повтор запроса обычно проходит успешно.
				out <- Event{Kind: EventError, Err: MarkRetryable(errors.New(chunk.Error))}
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
					PromptEvalCount:    chunk.PromptEvalCount,
					PromptEvalDuration: chunk.PromptEvalDuration,
					EvalCount:          chunk.EvalCount,
					TotalDuration:      chunk.TotalDuration,
					LoadDuration:       chunk.LoadDuration,
					EvalDuration:       chunk.EvalDuration,
					DoneReason:         chunk.DoneReason,
				}}
				return
			}
		}
	}()

	return out
}
