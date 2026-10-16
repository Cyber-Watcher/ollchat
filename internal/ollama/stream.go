package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
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

	// Сводка по логарифмам вероятностей токенов ОТВЕТА (рассуждения не входят),
	// если запрос шёл с Logprobs; иначе LogprobN == 0 (этап 112, Ч15).
	LogprobN   int     // сколько токенов ответа с вероятностью
	LogprobSum float64 // сумма логарифмов: средняя = LogprobSum / LogprobN
	LogprobMin float64 // самый неуверенный токен ответа
	LogprobLow int     // токенов с вероятностью ниже 0,5
}

// MeanLogprob — средний логарифм вероятности токена ответа; 0, если данных нет.
func (s Stats) MeanLogprob() float64 {
	if s.LogprobN == 0 {
		return 0
	}
	return s.LogprobSum / float64(s.LogprobN)
}

// addLogprobs учитывает токены очередного чанка. Токены чанка с рассуждением
// не считаются: уверенность в ответе — это уверенность в его тексте.
func (s *Stats) addLogprobs(chunk ChatResponse) {
	if chunk.Message.Thinking != "" {
		return
	}
	for _, t := range chunk.Logprobs {
		if s.LogprobN == 0 || t.Logprob < s.LogprobMin {
			s.LogprobMin = t.Logprob
		}
		s.LogprobN++
		s.LogprobSum += t.Logprob
		if t.Logprob < logprobLow {
			s.LogprobLow++
		}
	}
}

// logprobLow — ln 0,5: ниже этого модель выбрала токен, которому давала меньше половины.
const logprobLow = -0.6931471805599453

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

// Backoff — пауза перед повтором номер attempt (0 — после первой неудачи):
// base·2^attempt плюс случайная добавка 0…0,5 с (этап 112, А2; «AI Agents With
// Python», 2026, разд. 16). Растущая пауза не добавляет нагрузки занятому
// серверу; случайная часть разводит во времени повторы многих клиентов разом:
// на рабочей машине ollchat запускают многие пользователи, и после перезапуска
// Ollama все они повторяли запрос ровно через 1 и 2 с. Потолок — 30 с.
func Backoff(attempt int, base time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := base << min(attempt, 5)
	d = min(d, 30*time.Second)
	return d + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
}

// sendGrace — сколько событие ждёт потребителя, отменившего запрос.
// Секунды хватает тому, кто дочитывает канал до конца, а ушедший держит
// горутину не дольше этого.
const sendGrace = time.Second

// Chat запускает генерацию и возвращает канал событий. Канал закрывается после
// EventDone или EventError. Прерывание — отменой ctx.
func (c *Client) Chat(ctx context.Context, req ChatRequest) <-chan Event {
	out := make(chan Event, 64)

	parent := ctx
	// send отдаёт событие потребителю, пока тот слушает. Раньше отправка
	// была голой: потребитель, отменивший запрос и бросивший канал, оставлял
	// горутину на полном канале навсегда — вместе с телом ответа и
	// соединением. После отмены событие ещё ждёт sendGrace: вызывающие
	// (agent.drain, graphex, сжатие истории) дочитывают канал до конца
	// и ждут последнего события — его они получают, как прежде.
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-parent.Done():
		}
		t := time.NewTimer(sendGrace)
		defer t.Stop()
		select {
		case out <- ev:
			return true
		case <-t.C:
			return false
		}
	}
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
			send(Event{Kind: EventError, Err: err})
			return
		}

		resp, err := c.chatHTTP.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				send(Event{Kind: EventError, Err: ErrCanceled})
				return
			}
			// Сетевой сбой — соединение можно попробовать установить заново.
			// Сюда же попадает «connection refused»: сборка графа ходит на стенд
			// через ssh-туннель, и 01.09.2026 семь секунд его переподъёма стоили
			// целого захода — отказ в соединении сочли неустранимым и остановили
			// всё. Адрес на месте, слушателя нет **сейчас**: это и надо повторять.
			send(Event{Kind: EventError,
				Err: MarkRetryable(fmt.Errorf("запрос к %s: %w", c.baseURL, err))})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			srvErr, _ := serverError(resp)
			// 5xx — сбой на стороне сервера, его есть смысл повторить.
			// 4xx означает неверный запрос: повтор ничего не изменит.
			if resp.StatusCode >= 500 {
				send(Event{Kind: EventError, Err: MarkRetryable(srvErr)})
				return
			}
			send(Event{Kind: EventError, Err: srvErr})
			return
		}

		// Ответ — поток JSON-объектов, разделённых переводами строк.
		// json.Decoder читает их подряд и не ограничивает длину объекта.
		body := newStallReader(resp.Body, c.stallTimeout, cancelStall)
		defer body.Close()
		dec := json.NewDecoder(body)
		var lp Stats // сводка по логарифмам вероятностей; остальное — из финального чанка
		for {
			var chunk ChatResponse
			if err := dec.Decode(&chunk); err != nil {
				if errors.Is(err, io.EOF) {
					// Поток закончился без финального чанка — считаем это обрывом.
					send(Event{Kind: EventError,
						Err: MarkRetryable(errors.New("поток ответа оборван сервером"))})
					return
				}
				if ctx.Err() != nil {
					// Отличаем отмену человеком от обрыва по молчанию: первое
					// — обычное дело, второе — беда на сервере, и говорить
					// о ней надо прямо.
					if parent.Err() == nil && c.stallTimeout > 0 {
						send(Event{Kind: EventError, Err: MarkRetryable(fmt.Errorf(
							"сервер молчит дольше %s — вероятно, запрос стоит в очереди "+
								"за другой моделью; предел меняется настройкой stall_timeout",
							c.stallTimeout))})
						return
					}
					send(Event{Kind: EventError, Err: ErrCanceled})
					return
				}
				send(Event{Kind: EventError, Err: MarkRetryable(fmt.Errorf("разбор ответа: %w", err))})
				return
			}

			if chunk.Error != "" {
				// Сервер сообщил о сбое прямо в потоке — генерация не удалась.
				// Такое бывает, например, когда сервер не может разобрать вывод
				// модели; повтор запроса обычно проходит успешно.
				send(Event{Kind: EventError, Err: MarkRetryable(errors.New(chunk.Error))})
				return
			}

			lp.addLogprobs(chunk)
			if len(chunk.Message.ToolCalls) > 0 {
				if !send(Event{Kind: EventToolCalls, ToolCalls: chunk.Message.ToolCalls}) {
					return
				}
			}
			if chunk.Message.Thinking != "" {
				if !send(Event{Kind: EventThinking, Text: chunk.Message.Thinking}) {
					return
				}
			}
			if chunk.Message.Content != "" {
				if !send(Event{Kind: EventContent, Text: chunk.Message.Content}) {
					return
				}
			}

			if chunk.Done {
				st := lp
				st.PromptEvalCount = chunk.PromptEvalCount
				st.PromptEvalDuration = chunk.PromptEvalDuration
				st.EvalCount = chunk.EvalCount
				st.TotalDuration = chunk.TotalDuration
				st.LoadDuration = chunk.LoadDuration
				st.EvalDuration = chunk.EvalDuration
				st.DoneReason = chunk.DoneReason
				send(Event{Kind: EventDone, Stats: st})
				return
			}
		}
	}()

	return out
}
