// Package agent реализует цикл общения с моделью, включая вызовы инструментов.
//
// Цикл: запрос к модели → если модель запросила инструменты, проверить
// разрешения, при необходимости спросить пользователя, выполнить и вернуть
// результаты модели → повторить. Число итераций ограничено настройкой
// agent.max_iterations, чтобы модель не зациклилась.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// EventKind — вид события, отдаваемого интерфейсу.
type EventKind int

// Виды событий агента.
const (
	EventContent     EventKind = iota // кусок текста ответа
	EventThinking                     // кусок рассуждений
	EventToolPlan                     // инструмент подготовлен к запуску
	EventToolConfirm                  // требуется подтверждение пользователя
	EventToolResult                   // инструмент отработал
	EventStats                        // статистика одного обмена с моделью
	EventTurnDone                     // ответ модели завершён окончательно
	EventRetry                        // сбой до начала ответа, запрос повторяется
	EventNotice                       // предупреждение по ходу обмена, ответ продолжается
	EventError                        // ошибка
)

// Event — событие агента.
type Event struct {
	Kind    EventKind
	Text    string
	Tool    *ToolEvent
	Confirm *ConfirmRequest
	Stats   ollama.Stats
	Err     error
}

// ToolEvent описывает вызов инструмента для показа в интерфейсе.
type ToolEvent struct {
	Name    string
	Title   string
	Args    string
	Output  string
	Preview string
	OK      bool
	Skipped bool
	Reason  string
}

// Answer — ответ пользователя на запрос подтверждения.
type Answer int

// Возможные ответы.
const (
	AnswerNo         Answer = iota // отклонить один раз
	AnswerYes                      // разрешить один раз
	AnswerAlways                   // разрешить это действие до конца сеанса
	AnswerAlwaysTool               // разрешить инструмент целиком до конца сеанса
)

// ConfirmRequest — запрос подтверждения у пользователя.
// Интерфейс обязан ровно один раз отправить ответ в Reply.
type ConfirmRequest struct {
	Tool    string
	Title   string
	Preview string
	Reason  string
	Kind    permissions.Kind
	Target  string
	Reply   chan Answer
}

// Runner выполняет обмен с моделью.
type Runner struct {
	Client        *ollama.Client
	Model         string
	KeepAlive     string
	Options       map[string]any
	Think         *bool
	Tools         *tools.Registry
	Guard         *permissions.Guard
	MaxIterations int
	// MaxRetries — сколько раз повторить обмен, если сервер сорвал генерацию
	// до того, как начал приходить ответ.
	MaxRetries int
	// ToolsSupported — есть ли у модели возможность "tools". Если нет,
	// описания инструментов не отправляются вовсе.
	ToolsSupported bool
	// VisionSupported — умеет ли модель смотреть картинки. Ставится на каждый
	// ход: модель меняют посреди сеанса, а картинку показывать не всякой можно.
	VisionSupported bool
}

// Run отправляет историю модели и обрабатывает вызовы инструментов.
// Канал закрывается после EventTurnDone или EventError.
func (r *Runner) Run(ctx context.Context, conv *session.Conversation) <-chan Event {
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		r.run(ctx, conv, out)
	}()
	return out
}

func (r *Runner) run(ctx context.Context, conv *session.Conversation, out chan<- Event) {
	maxIter := r.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}

	var toolSpecs []ollama.Tool
	if r.ToolsSupported && r.Tools != nil {
		toolSpecs = r.Tools.Specs()
	}

	maxRetries := r.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	warnedImages := false

	for iter := 0; iter < maxIter; iter++ {
		msgs := conv.Request()
		// Модель могли сменить посреди диалога, а картинки в истории остались.
		// Сервер на такой запрос отвечает 400 целиком, и продолжать разговор
		// становится нельзя вовсе — приходится чистить историю. Поэтому
		// картинки убираем сами и говорим об этом один раз за ход.
		if !r.VisionSupported {
			var dropped int
			msgs, dropped = stripImages(msgs)
			if dropped > 0 && !warnedImages {
				warnedImages = true
				out <- Event{Kind: EventNotice, Text: fmt.Sprintf(
					"В истории %d картинок, а модель %s их не умеет — они не отправлены, "+
						"текст диалога сохранён. Вернётесь к модели с vision — картинки "+
						"снова будут видны.", dropped, r.Model)}
			}
		}
		req := ollama.ChatRequest{
			Model:     r.Model,
			Messages:  msgs,
			Tools:     toolSpecs,
			Think:     r.Think,
			KeepAlive: r.KeepAlive,
			Options:   r.Options,
		}

		res, err := r.exchangeWithRetry(ctx, req, maxRetries, out)
		// Возможности модели известны не сразу: сразу после переключения список
		// ещё не пришёл, и картинки уходят на сервер вслепую. Отказ сервера —
		// последний и самый надёжный признак, поэтому повторяем один раз
		// без картинок, вместо того чтобы обрывать разговор.
		if err != nil && isMultimodalRefusal(err) && res.content.Len() == 0 {
			if stripped, dropped := stripImages(req.Messages); dropped > 0 {
				if !warnedImages {
					warnedImages = true
					out <- Event{Kind: EventNotice, Text: fmt.Sprintf(
						"Сервер отказался принять %d картинок: модель %s их не умеет. "+
							"Повторяю без них, текст диалога сохранён.", dropped, r.Model)}
				}
				req.Messages = stripped
				res, err = r.exchangeWithRetry(ctx, req, maxRetries, out)
			}
		}
		if err != nil {
			// Сохраняем то, что успело прийти до ошибки, чтобы история
			// не потеряла частичный ответ.
			if res.content.Len() > 0 {
				conv.Append(ollama.Message{
					Role:     ollama.RoleAssistant,
					Content:  res.content.String(),
					Thinking: res.thinking.String(),
				})
			}
			out <- Event{Kind: EventError, Err: err}
			return
		}

		content, toolCalls := res.content, res.toolCalls
		conv.Append(ollama.Message{
			Role:      ollama.RoleAssistant,
			Content:   content.String(),
			Thinking:  res.thinking.String(),
			ToolCalls: toolCalls,
		})
		out <- Event{Kind: EventStats, Stats: res.stats}

		if len(toolCalls) == 0 {
			out <- Event{Kind: EventTurnDone}
			return
		}

		// Модель запросила инструменты — выполняем их и возвращаем результаты.
		for _, call := range toolCalls {
			if ctx.Err() != nil {
				out <- Event{Kind: EventError, Err: ollama.ErrCanceled}
				return
			}
			result, images, ok := r.executeCall(ctx, call, out)
			_ = ok
			if ctx.Err() != nil {
				// Ход прерван, пока работал инструмент. Дописывать историю
				// нельзя: интерфейс этот ход уже закрыл и мог начать новый.
				out <- Event{Kind: EventError, Err: ollama.ErrCanceled}
				return
			}
			conv.Append(ollama.Message{
				Role:       ollama.RoleTool,
				ToolName:   call.Function.Name,
				ToolCallID: call.ID,
				Content:    result,
			})
			// Картинку строкой не вернёшь: в ответе инструмента её быть не может.
			// Поэтому она идёт следующим сообщением, где для неё есть поле images.
			if len(images) > 0 {
				if !r.VisionSupported {
					conv.Append(ollama.Message{Role: ollama.RoleUser, Content: fmt.Sprintf(
						"Картинку показать нечем: модель %s не умеет смотреть изображения. "+
							"Скажи об этом пользователю и предложи выбрать модель с возможностью vision.",
						r.Model)})
					continue
				}
				conv.Append(ollama.Message{
					Role:    ollama.RoleUser,
					Content: "Вот запрошенная картинка. Опиши, что на ней, своими словами; если это таблица — перечисли строки и числа.",
					Images:  images,
				})
			}
		}
	}

	out <- Event{Kind: EventError,
		Err: fmt.Errorf("превышено ограничение agent.max_iterations (%d): модель продолжает запрашивать инструменты. "+
			"Увеличьте agent.max_iterations в файле настроек или уточните запрос", maxIter)}
}

// exchangeResult — то, что удалось получить за один обмен с моделью.
type exchangeResult struct {
	content   strings.Builder
	thinking  strings.Builder
	toolCalls []ollama.ToolCall
	stats     ollama.Stats
}

// exchangeWithRetry выполняет обмен с моделью, повторяя его при сбоях сервера.
//
// Повтор допускается, только если сбой произошёл до появления ответа: ни одного
// куска текста и ни одного вызова инструмента получить не успели, значит
// повторный запрос ничего не продублирует и никаких действий выполнено не было.
// Если часть ответа уже показана пользователю, повтор запрещён — иначе текст
// пришёл бы дважды.
func (r *Runner) exchangeWithRetry(ctx context.Context, req ollama.ChatRequest,
	maxRetries int, out chan<- Event) (*exchangeResult, error) {

	for attempt := 0; ; attempt++ {
		res, err := r.exchange(ctx, req, out)
		if err == nil {
			return res, nil
		}

		canRetry := attempt < maxRetries &&
			ollama.Retryable(err) &&
			ctx.Err() == nil &&
			res.content.Len() == 0 &&
			len(res.toolCalls) == 0

		if !canRetry {
			if attempt > 0 {
				return res, fmt.Errorf("%w (после %d попыток)", err, attempt+1)
			}
			return res, err
		}

		// Рассуждения, успевшие прийти до сбоя, показывать не нужно:
		// интерфейс уберёт их по этому событию и начнёт ответ заново.
		out <- Event{Kind: EventRetry, Err: err,
			Text: fmt.Sprintf("сбой на стороне сервера: %v. Повторяю запрос (попытка %d из %d)",
				err, attempt+2, maxRetries+1)}
	}
}

// exchange проводит один обмен с моделью и собирает результат.
func (r *Runner) exchange(ctx context.Context, req ollama.ChatRequest, out chan<- Event) (*exchangeResult, error) {
	res := &exchangeResult{}
	finished := false

	for ev := range r.Client.Chat(ctx, req) {
		switch ev.Kind {
		case ollama.EventContent:
			res.content.WriteString(ev.Text)
			out <- Event{Kind: EventContent, Text: ev.Text}
		case ollama.EventThinking:
			res.thinking.WriteString(ev.Text)
			out <- Event{Kind: EventThinking, Text: ev.Text}
		case ollama.EventToolCalls:
			res.toolCalls = append(res.toolCalls, ev.ToolCalls...)
		case ollama.EventDone:
			res.stats = ev.Stats
			finished = true
		case ollama.EventError:
			return res, ev.Err
		}
	}

	if !finished {
		return res, errors.New("ответ модели не завершён")
	}
	return res, nil
}

// executeCall готовит, согласует и выполняет один вызов инструмента.
// Возвращаемая строка всегда пригодна для отправки модели: и успех, и ошибка.
func (r *Runner) executeCall(ctx context.Context, call ollama.ToolCall, out chan<- Event) (string, []string, bool) {
	name := call.Function.Name
	argsJSON := call.Function.ArgumentsJSON()

	if r.Tools == nil {
		msg := "Инструменты отключены в настройках."
		out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: name, Args: argsJSON,
			Output: msg, Reason: msg}}
		return msg, nil, false
	}

	plan, err := r.Tools.Plan(name, call.Function.Arguments)
	if err != nil {
		msg := fmt.Sprintf("Ошибка: %v", err)
		out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name,
			Title: fmt.Sprintf("%s(%s)", name, shorten(argsJSON, 60)), Args: argsJSON,
			Output: msg, Reason: err.Error()}}
		return msg, nil, false
	}

	out <- Event{Kind: EventToolPlan, Tool: &ToolEvent{
		Name: name, Title: plan.Title, Args: argsJSON, Preview: plan.Preview,
	}}

	decision := r.Guard.Check(plan.Req)
	switch decision.Decision {
	case permissions.DecisionDeny:
		msg := fmt.Sprintf("Действие запрещено настройками: %s", decision.Reason)
		out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
			Output: msg, Reason: decision.Reason, Skipped: true}}
		return msg, nil, false

	case permissions.DecisionAsk:
		reply := make(chan Answer, 1)
		out <- Event{Kind: EventToolConfirm, Confirm: &ConfirmRequest{
			Tool:    name,
			Title:   plan.Title,
			Preview: plan.Preview,
			Reason:  decision.Reason,
			Kind:    plan.Req.Kind,
			Target:  plan.Req.Target,
			Reply:   reply,
		}}

		var answer Answer
		select {
		case answer = <-reply:
		case <-ctx.Done():
			msg := "Пользователь прервал выполнение."
			out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
				Output: msg, Skipped: true, Reason: msg}}
			return msg, nil, false
		}

		switch answer {
		case AnswerNo:
			msg := "Пользователь отклонил выполнение этого действия."
			out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
				Output: msg, Skipped: true, Reason: "отклонено пользователем"}}
			return msg, nil, false
		case AnswerAlways:
			if err := r.Guard.GrantSession(plan.Req.Kind, plan.Req.Target); err != nil {
				msg := fmt.Sprintf("Действие не выполнено: %v", err)
				out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
					Output: msg, Skipped: true, Reason: err.Error()}}
				return msg, nil, false
			}
		case AnswerAlwaysTool:
			if err := r.Guard.GrantSessionTool(name); err != nil {
				msg := fmt.Sprintf("Действие не выполнено: %v", err)
				out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
					Output: msg, Skipped: true, Reason: err.Error()}}
				return msg, nil, false
			}
		}
	}

	output, err := plan.Run(ctx)
	var images []string
	if plan.Images != nil {
		images = plan.Images()
	}
	if err != nil {
		msg := fmt.Sprintf("Ошибка выполнения: %v", err)
		if strings.TrimSpace(output) != "" {
			msg += "\n\n" + output
		}
		out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
			Output: msg, Reason: err.Error()}}
		return msg, nil, false
	}

	out <- Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
		Output: output, OK: true}}
	return output, images, true
}

func shorten(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// stripImages убирает картинки из истории для модели без vision.
//
// Ollama отвергает такой запрос целиком («Multimodal data provided, but model
// does not support multimodal requests»), а не пропускает картинки мимо. Значит
// после смены модели посреди диалога разговор обрывается насовсем: падает
// каждый следующий вопрос, и спасает только очистка истории. Найдено на живом
// сеансе при переключении на nemotron.
//
// История не правится: меняется только копия для запроса. Вернувшись к модели
// с vision, пользователь снова увидит картинки на своих местах.
func stripImages(msgs []ollama.Message) ([]ollama.Message, int) {
	dropped := 0
	for _, m := range msgs {
		dropped += len(m.Images)
	}
	if dropped == 0 {
		return msgs, 0
	}
	out := make([]ollama.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		out[i].Images = nil
		// Без пометки остаётся текст вида «Вот запрошенная картинка», за которым
		// ничего нет: модель начинает описывать то, чего ей не показали.
		out[i].Content = strings.TrimSpace(out[i].Content) +
			"\n[картинка не приложена: выбранная модель не умеет смотреть изображения]"
	}
	return out, dropped
}

// isMultimodalRefusal распознаёт отказ сервера принять картинки.
//
// Формулировка приходит от Ollama и от совместимых с ней прослоек, поэтому
// смотрим на устойчивую часть, а не на всё сообщение целиком.
func isMultimodalRefusal(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "multimodal") ||
		(strings.Contains(low, "image") && strings.Contains(low, "not support"))
}
