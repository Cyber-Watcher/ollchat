package oai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// sendGrace — сколько событие ждёт потребителя, отменившего запрос; то же, что
// у клиента Ollama (internal/ollama/stream.go, там не экспортировано).
const sendGrace = time.Second

// chunk — одно событие потока chat.completion.chunk. Поля, которых нет у
// одного из серверов, просто пусты: null в строку не пишется.
type chunk struct {
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage"`

	// Ошибка внутри потока: {"error":{…}} или {"object":"error","message":…}.
	Error   json.RawMessage `json:"error"`
	Object  string          `json:"object"`
	Message string          `json:"message"`
}

type choice struct {
	Index        int       `json:"index"`
	Delta        delta     `json:"delta"`
	FinishReason string    `json:"finish_reason"`
	Logprobs     *logprobs `json:"logprobs"`
}

// delta — приращение ответа. Мышление vLLM отдаёт полем reasoning, SGLang,
// llama-server и LiteLLM — reasoning_content (запись Ш1, 17.11.2026).
type delta struct {
	Content          string          `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []deltaToolCall `json:"tool_calls"`
}

// deltaToolCall — кусок вызова инструмента: id и имя приходят с первым
// куском, arguments — строкой по частям; куски одного вызова связывает index.
type deltaToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type logprobs struct {
	Content []ollama.TokenLogprob `json:"content"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// toolAcc — вызов инструмента, собираемый из кусков.
type toolAcc struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// streamState — всё, что копится за поток до EventDone.
type streamState struct {
	start   time.Time // запрос отправлен
	first   time.Time // пришёл первый кусок ответа: текст, мышление или вызов
	stats   ollama.Stats
	reason  string // finish_reason
	calls   []*toolAcc
	emitted bool // EventToolCalls уже отдан
}

// Chat запускает генерацию и возвращает канал событий — тех же, что отдаёт
// ollama.(*Client).Chat. Канал закрывается после EventDone или EventError.
// Прерывание — отменой ctx.
func (c *Client) Chat(ctx context.Context, req ollama.ChatRequest) <-chan ollama.Event {
	out := make(chan ollama.Event, 64)

	parent := ctx
	// send — как у клиента Ollama: отдаёт событие, пока потребитель слушает,
	// а после отмены ждёт его ещё sendGrace и не держит горутину вечно.
	send := func(ev ollama.Event) bool {
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
	fail := func(err error) { send(ollama.Event{Kind: ollama.EventError, Err: err}) }

	go func() {
		defer close(out)

		// Свой отменяемый контекст — для сторожа молчания (ollama.NewStallReader).
		ctx, cancelStall := context.WithCancel(ctx)
		defer cancelStall()

		wire := buildRequest(req, c.user)
		wire.Priority = c.requestPriority(req.Messages)
		body, err := json.Marshal(wire)
		if err != nil {
			fail(fmt.Errorf("сборка запроса: %w", err))
			return
		}
		httpReq, err := c.newRequest(ctx, http.MethodPost, "/chat/completions", body)
		if err != nil {
			fail(err)
			return
		}

		st := streamState{start: time.Now()}
		resp, err := c.chatHTTP.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				fail(ollama.ErrCanceled)
				return
			}
			// Сетевой сбой, в том числе отказ в соединении: слушателя нет
			// сейчас — это и надо повторять (как у клиента Ollama).
			fail(ollama.MarkRetryable(fmt.Errorf("запрос к %s: %w", c.baseURL, err)))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fail(c.serverError(resp))
			return
		}
		// Сервер, пропустивший мимо stream: true, отдаёт ответ целиком одним
		// JSON. Разбор SSE не нашёл бы в нём ни одного события и назвал бы это
		// обрывом, а агент повторял бы запрос впустую; говорится прямо.
		if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
			fail(fmt.Errorf("сервер %s ответил не потоком (Content-Type %s): клиент просит "+
				"stream: true, а ответ целиком не разбирает", c.baseURL, ct))
			return
		}

		rd := ollama.NewStallReader(resp.Body, c.stallTimeout, cancelStall)
		defer rd.Close()
		sse := newSSE(rd)
		for {
			data, err := sse.next()
			if err != nil {
				if ctx.Err() != nil {
					// Отмена человеком — обычное дело; обрыв по молчанию — беда
					// на сервере, о ней говорится прямо.
					if parent.Err() == nil && c.stallTimeout > 0 {
						fail(ollama.MarkRetryable(fmt.Errorf(
							"сервер молчит дольше %s — вероятно, запрос стоит в очереди "+
								"на сервере; предел меняется настройкой stall_timeout",
							c.stallTimeout)))
						return
					}
					fail(ollama.ErrCanceled)
					return
				}
				if errors.Is(err, io.EOF) {
					if st.reason != "" {
						// Конец ответа сервер назвал, а [DONE] не прислал —
						// ответ полный, обрыва нет.
						st.finish(send)
						return
					}
					fail(ollama.MarkRetryable(errors.New("поток ответа оборван сервером")))
					return
				}
				fail(ollama.MarkRetryable(fmt.Errorf("чтение ответа: %w", err)))
				return
			}

			if strings.TrimSpace(data) == "[DONE]" {
				st.finish(send)
				return
			}

			var ch chunk
			if err := json.Unmarshal([]byte(data), &ch); err != nil {
				fail(ollama.MarkRetryable(fmt.Errorf("разбор ответа: %w", err)))
				return
			}
			if msg := ch.streamError(); msg != "" {
				// Сервер сообщил о сбое прямо в потоке — генерация не удалась;
				// повтор, как и у клиента Ollama, имеет смысл.
				fail(ollama.MarkRetryable(errors.New(c.redact(msg))))
				return
			}
			if !st.apply(ch, send) {
				return
			}
		}
	}()

	return out
}

// streamError — текст ошибки, если событие потока — ошибка.
func (ch chunk) streamError() string {
	if m := nestedMessage(ch.Error); m != "" {
		return m
	}
	if ch.Object == "error" {
		if ch.Message != "" {
			return ch.Message
		}
		return "сервер сообщил об ошибке без текста"
	}
	return ""
}

// apply учитывает одно событие потока и отдаёт куски текста и мышления.
// Ложь — потребитель ушёл.
func (st *streamState) apply(ch chunk, send func(ollama.Event) bool) bool {
	if ch.Usage != nil {
		st.stats.PromptEvalCount = ch.Usage.PromptTokens
		st.stats.EvalCount = ch.Usage.CompletionTokens
	}
	for _, c := range ch.Choices {
		if c.Index != 0 {
			continue // n > 1 не запрашивается
		}
		d := c.Delta
		thinking := d.ReasoningContent
		if thinking == "" {
			thinking = d.Reasoning
		}
		if st.first.IsZero() && (d.Content != "" || thinking != "" || len(d.ToolCalls) > 0) {
			st.first = time.Now()
		}
		if c.Logprobs != nil && thinking == "" {
			st.addLogprobs(c.Logprobs.Content)
		}
		for _, tc := range d.ToolCalls {
			st.addToolCall(tc)
		}
		if thinking != "" {
			if !send(ollama.Event{Kind: ollama.EventThinking, Text: thinking}) {
				return false
			}
		}
		if d.Content != "" {
			if !send(ollama.Event{Kind: ollama.EventContent, Text: d.Content}) {
				return false
			}
		}
		if c.FinishReason != "" {
			st.reason = c.FinishReason
			// Вызовы — одним событием, как только сервер назвал конец ответа
			// (у всех четырёх серверов это finish_reason "tool_calls"; при
			// "length" вызовы тоже отдаются — агент сам решает, что с ними).
			if !st.emitCalls(send) {
				return false
			}
		}
	}
	return true
}

// addLogprobs — то же правило, что у клиента Ollama, его же функцией
// (ollama.Stats.AddLogprobs): считаются только токены ответа — куски с
// мышлением отсеивает apply до вызова.
func (st *streamState) addLogprobs(toks []ollama.TokenLogprob) {
	st.stats.AddLogprobs(toks)
}

// addToolCall приклеивает кусок вызова к его вызову по index. Без index кусок
// с новым id открывает новый вызов, без id — продолжает последний.
func (st *streamState) addToolCall(tc deltaToolCall) {
	var acc *toolAcc
	if tc.Index != nil {
		for _, a := range st.calls {
			if a.index == *tc.Index {
				acc = a
				break
			}
		}
		if acc == nil {
			acc = &toolAcc{index: *tc.Index}
			st.calls = append(st.calls, acc)
		}
	} else {
		n := len(st.calls)
		if n == 0 || (tc.ID != "" && st.calls[n-1].id != "" && st.calls[n-1].id != tc.ID) {
			acc = &toolAcc{index: n}
			st.calls = append(st.calls, acc)
		} else {
			acc = st.calls[n-1]
		}
	}
	if tc.ID != "" {
		acc.id = tc.ID
	}
	if tc.Function.Name != "" {
		acc.name = tc.Function.Name
	}
	acc.args.WriteString(tc.Function.Arguments)
}

// emitCalls отдаёт собранные вызовы одним EventToolCalls, один раз.
func (st *streamState) emitCalls(send func(ollama.Event) bool) bool {
	if st.emitted || len(st.calls) == 0 {
		return true
	}
	st.emitted = true
	sort.SliceStable(st.calls, func(i, j int) bool { return st.calls[i].index < st.calls[j].index })
	calls := make([]ollama.ToolCall, 0, len(st.calls))
	for _, a := range st.calls {
		calls = append(calls, ollama.ToolCall{
			ID: a.id,
			Function: ollama.ToolCallFunc{
				Index:     a.index,
				Name:      a.name,
				Arguments: parseArguments(a.args.String()),
			},
		})
	}
	return send(ollama.Event{Kind: ollama.EventToolCalls, ToolCalls: calls})
}

// parseArguments — строка аргументов в объект. Пустая — пустой объект;
// неразбираемая (модель оборвалась или ошиблась) — {"_raw": строка}: вызов
// дойдёт до агента, инструмент вернёт модели ошибку доводов, и она поправится,
// а строка не потеряется.
func parseArguments(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return map[string]any{"_raw": s}
	}
	if m == nil { // "null"
		return map[string]any{}
	}
	return m
}

// finish отдаёт несобранные ещё вызовы и EventDone.
//
// Длительностей OpenAI API не сообщает, поэтому они меряются часами клиента,
// и это приближение: PromptEvalDuration — от отправки запроса до первого куска
// ответа (сюда входят сеть и очередь на сервере, а не только чтение промпта),
// EvalDuration — от первого куска до конца потока, TotalDuration — весь запрос.
func (st *streamState) finish(send func(ollama.Event) bool) {
	if !st.emitCalls(send) {
		return
	}
	now := time.Now()
	s := st.stats
	s.DoneReason = st.reason
	s.TotalDuration = now.Sub(st.start).Nanoseconds()
	if !st.first.IsZero() {
		s.PromptEvalDuration = st.first.Sub(st.start).Nanoseconds()
		s.EvalDuration = now.Sub(st.first).Nanoseconds()
	}
	send(ollama.Event{Kind: ollama.EventDone, Stats: s})
}
