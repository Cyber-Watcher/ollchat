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
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"strings"
	"time"

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
	// Tools — счёт вызовов инструментов за ход; заполняется в EventTurnDone.
	Tools *ToolStats
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

// ToolStats — счёт вызовов инструментов за один ход.
//
// Книги советуют считать две ошибки порознь: «не тот инструмент» и «не те
// аргументы» лечатся разными местами описания. Здесь — первая ступень:
// сколько вызовов было, сколько из них не дошли до выполнения (правила, отказ
// человека, негодные аргументы) и сколько дошли, но упали.
type ToolStats struct {
	Calls    int // сколько вызовов запросила модель
	Rejected int // не подготовлен, запрещён правилами или отклонён человеком
	Failed   int // разрешён и запущен, но завершился ошибкой
}

// callOutcome — чем кончился один вызов инструмента.
type callOutcome int

const (
	callOK       callOutcome = iota
	callRejected             // до выполнения не дошло
	callFailed               // выполнение началось и сорвалось
)

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

// Chatter — то, что умеет вести один обмен с моделью: единственный метод
// клиента Ollama, который нужен циклу. Интерфейс объявлен здесь, у потребителя,
// а не у клиента («accept interfaces, return structs»): так агент тестируется
// сценарием событий без HTTP, а *ollama.Client подходит без обёрток.
type Chatter interface {
	Chat(ctx context.Context, req ollama.ChatRequest) <-chan ollama.Event
}

// Runner выполняет обмен с моделью.
type Runner struct {
	Client        Chatter
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
	// Steps — журнал шагов: обмен с моделью и каждый вызов инструмента
	// с исходом. nil — выключен. Turn — идентификатор обмена для этих записей.
	Steps *steplog.Writer
	Turn  string
}

// retryPause — сколько ждать перед повторной попыткой обмена.
//
// Без паузы быстрый отказ сервера (5xx за миллисекунды) съедал все попытки
// подряд, и повтор ничего не давал. Формула та же, что у эмбеддера в kbembed:
// растущая пауза, чтобы занятому чужой работой серверу не добавлять нагрузки.
// Переменная, а не функция, — шов для тестов: им ждать секунды незачем.
var retryPause = func(attempt int) time.Duration {
	return time.Duration(attempt+1) * time.Second
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

// emit отдаёт событие интерфейсу и говорит, жив ли ещё ход.
//
// Отправка без выбора между каналом и отменой висела бы навсегда, если
// получатель перестал читать: интерфейс закрыл ход по Esc и ушёл дальше.
// Раньше от этого спасал только дренаж канала на стороне интерфейса; теперь
// отмена контекста освобождает горутину сама, и дренаж остаётся вторым поясом.
func emit(ctx context.Context, out chan<- Event, ev Event) bool {
	// Сначала без ожидания: если в буфере есть место, событие уходит даже
	// после отмены — иначе select выбирал бы готовую отмену наравне с готовым
	// каналом, и завершающая ошибка хода терялась бы через раз.
	select {
	case out <- ev:
		return true
	default:
	}
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Runner) run(ctx context.Context, conv *session.Conversation, out chan<- Event) {
	// Отрицательное значение — «без ограничения»: цепочка идёт, пока модель
	// просит инструменты. Заведено по просьбе владельца 03.09.2026: обход
	// документации или разбор большого каталога упирались в потолок на середине
	// работы, а предохранитель от зацикливания у нас и без того есть — Esc
	// прерывает ход в любой момент, а таймаут команды ограничивает каждый вызов.
	//
	// Ноль означает «настройка не задана» и получает умолчание: так конфиг,
	// написанный до появления ключа, ведёт себя как раньше.
	maxIter := r.MaxIterations
	unlimited := maxIter < 0
	if maxIter == 0 {
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
	var stats ToolStats
	step := 0

	// Дата в системном сообщении берётся один раз на ход: иначе цепочка
	// вызовов, пересёкшая полночь, получала бы разные системные сообщения
	// в соседних итерациях одного и того же хода.
	now := time.Now()

	for iter := 0; unlimited || iter < maxIter; iter++ {
		msgs := conv.RequestAt(now)
		// Модель могли сменить посреди диалога, а картинки в истории остались.
		// Сервер на такой запрос отвечает 400 целиком, и продолжать разговор
		// становится нельзя вовсе — приходится чистить историю. Поэтому
		// картинки убираем сами и говорим об этом один раз за ход.
		if !r.VisionSupported {
			var dropped int
			msgs, dropped = stripImages(msgs)
			if dropped > 0 && !warnedImages {
				warnedImages = true
				if !emit(ctx, out, Event{Kind: EventNotice, Text: fmt.Sprintf(
					"В истории %d картинок, а модель %s их не умеет — они не отправлены, "+
						"текст диалога сохранён. Вернётесь к модели с vision — картинки "+
						"снова будут видны.", dropped, r.Model)}) {
					return
				}
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
					if !emit(ctx, out, Event{Kind: EventNotice, Text: fmt.Sprintf(
						"Сервер отказался принять %d картинок: модель %s их не умеет. "+
							"Повторяю без них, текст диалога сохранён.", dropped, r.Model)}) {
						return
					}
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
			emit(ctx, out, Event{Kind: EventError, Err: err})
			return
		}

		content, toolCalls := res.content, res.toolCalls
		conv.Append(ollama.Message{
			Role:      ollama.RoleAssistant,
			Content:   content.String(),
			Thinking:  res.thinking.String(),
			ToolCalls: toolCalls,
		})
		if !emit(ctx, out, Event{Kind: EventStats, Stats: res.stats}) {
			return
		}
		step++
		r.Steps.Write(steplog.Step{Turn: r.Turn, Step: step, Kind: steplog.KindChat, Model: r.Model,
			TokensIn: res.stats.PromptEvalCount, TokensOut: res.stats.EvalCount,
			MS:    res.stats.TotalDuration / 1e6,
			Extra: map[string]any{"tool_calls": len(toolCalls), "done": res.stats.DoneReason}})

		if len(toolCalls) == 0 {
			emit(ctx, out, Event{Kind: EventTurnDone, Tools: &stats})
			return
		}

		// Модель запросила инструменты — выполняем их и возвращаем результаты.
		for _, call := range toolCalls {
			if ctx.Err() != nil {
				emit(ctx, out, Event{Kind: EventError, Err: ollama.ErrCanceled})
				return
			}
			result, images, info := r.executeCall(ctx, call, out)
			stats.Calls++
			switch info.outcome {
			case callRejected:
				stats.Rejected++
			case callFailed:
				stats.Failed++
			}
			step++
			r.Steps.Write(steplog.Step{Turn: r.Turn, Step: step, Kind: steplog.KindTool, Model: r.Model,
				Tool: call.Function.Name, Args: call.Function.ArgumentsJSON(),
				Outcome: info.status, MS: info.ms, Note: info.note})
			if ctx.Err() != nil {
				// Ход прерван, пока работал инструмент. Дописывать историю
				// нельзя: интерфейс этот ход уже закрыл и мог начать новый.
				emit(ctx, out, Event{Kind: EventError, Err: ollama.ErrCanceled})
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

	// Сюда попадаем только с конечным пределом: при -1 цикл не кончается сам.
	emit(ctx, out, Event{Kind: EventError,
		Err: fmt.Errorf("превышено ограничение agent.max_iterations (%d): модель продолжает запрашивать инструменты.\n"+
			"Поднять на сеанс: /tools iterations 50 (или /tools iterations off — без ограничения).\n"+
			"Насовсем: agent.max_iterations в файле настроек; -1 означает без ограничения", maxIter)})
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
		if !emit(ctx, out, Event{Kind: EventRetry, Err: err,
			Text: fmt.Sprintf("сбой на стороне сервера: %v. Повторяю запрос (попытка %d из %d)",
				err, attempt+2, maxRetries+1)}) {
			return res, ollama.ErrCanceled
		}

		select {
		case <-time.After(retryPause(attempt)):
		case <-ctx.Done():
			return res, ollama.ErrCanceled
		}
	}
}

// exchange проводит один обмен с моделью и собирает результат.
func (r *Runner) exchange(ctx context.Context, req ollama.ChatRequest, out chan<- Event) (*exchangeResult, error) {
	res := &exchangeResult{}
	finished := false

	ch := r.Client.Chat(ctx, req)
	for ev := range ch {
		switch ev.Kind {
		case ollama.EventContent:
			res.content.WriteString(ev.Text)
			if !emit(ctx, out, Event{Kind: EventContent, Text: ev.Text}) {
				go drain(ch)
				return res, ollama.ErrCanceled
			}
		case ollama.EventThinking:
			res.thinking.WriteString(ev.Text)
			if !emit(ctx, out, Event{Kind: EventThinking, Text: ev.Text}) {
				go drain(ch)
				return res, ollama.ErrCanceled
			}
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
		// Поток кончился без завершающего чанка — тот же обрыв, что клиент
		// помечает повторяемым у себя; здесь он должен считаться таким же.
		return res, ollama.MarkRetryable(errors.New("ответ модели не завершён"))
	}
	return res, nil
}

// drain дочитывает брошенный поток, чтобы его отправитель не повис на канале.
func drain(ch <-chan ollama.Event) {
	for range ch {
	}
}

// callInfo — чем кончился вызов инструмента: исход для счёта, статус
// и пояснение для журнала шагов, время выполнения.
type callInfo struct {
	outcome callOutcome
	status  string // steplog.Outcome*
	note    string
	ms      int64
}

func rejected(status, note string) callInfo {
	return callInfo{outcome: callRejected, status: status, note: note}
}

// executeCall готовит, согласует и выполняет один вызов инструмента.
// Возвращаемая строка всегда пригодна для отправки модели: и успех, и ошибка.
func (r *Runner) executeCall(ctx context.Context, call ollama.ToolCall, out chan<- Event) (string, []string, callInfo) {
	name := call.Function.Name
	argsJSON := call.Function.ArgumentsJSON()

	if r.Tools == nil {
		msg := "Инструменты отключены в настройках."
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: name, Args: argsJSON,
			Output: msg, Reason: msg}})
		return msg, nil, rejected(steplog.OutcomeInvalid, "инструменты отключены")
	}

	plan, err := r.Tools.Plan(name, call.Function.Arguments)
	if err != nil {
		msg := fmt.Sprintf("Ошибка: %v", err)
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name,
			Title: fmt.Sprintf("%s(%s)", name, textx.Shorten(argsJSON, 60)), Args: argsJSON,
			Output: msg, Reason: err.Error()}})
		return msg, nil, rejected(steplog.OutcomeInvalid, err.Error())
	}

	if !emit(ctx, out, Event{Kind: EventToolPlan, Tool: &ToolEvent{
		Name: name, Title: plan.Title, Args: argsJSON, Preview: plan.Preview,
	}}) {
		return "Пользователь прервал выполнение.", nil, rejected(steplog.OutcomeCancelled, "")
	}

	decision := r.Guard.Check(plan.Req)
	switch decision.Decision {
	case permissions.DecisionDeny:
		msg := fmt.Sprintf("Действие запрещено настройками: %s", decision.Reason)
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
			Output: msg, Reason: decision.Reason, Skipped: true}})
		return msg, nil, rejected(steplog.OutcomeDenied, decision.Reason)

	case permissions.DecisionAsk:
		reply := make(chan Answer, 1)
		if !emit(ctx, out, Event{Kind: EventToolConfirm, Confirm: &ConfirmRequest{
			Tool:    name,
			Title:   plan.Title,
			Preview: plan.Preview,
			Reason:  decision.Reason,
			Kind:    plan.Req.Kind,
			Target:  plan.Req.Target,
			Reply:   reply,
		}}) {
			return "Пользователь прервал выполнение.", nil, rejected(steplog.OutcomeCancelled, "")
		}

		var answer Answer
		select {
		case answer = <-reply:
		case <-ctx.Done():
			msg := "Пользователь прервал выполнение."
			emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
				Output: msg, Skipped: true, Reason: msg}})
			return msg, nil, rejected(steplog.OutcomeCancelled, "")
		}

		switch answer {
		case AnswerNo:
			msg := "Пользователь отклонил выполнение этого действия."
			emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
				Output: msg, Skipped: true, Reason: "отклонено пользователем"}})
			return msg, nil, rejected(steplog.OutcomeRejected, "отклонено пользователем")
		case AnswerAlways:
			if err := r.Guard.GrantSession(plan.Req.Kind, plan.Req.Target); err != nil {
				msg := fmt.Sprintf("Действие не выполнено: %v", err)
				emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
					Output: msg, Skipped: true, Reason: err.Error()}})
				return msg, nil, rejected(steplog.OutcomeDenied, err.Error())
			}
		case AnswerAlwaysTool:
			if err := r.Guard.GrantSessionTool(name); err != nil {
				msg := fmt.Sprintf("Действие не выполнено: %v", err)
				emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
					Output: msg, Skipped: true, Reason: err.Error()}})
				return msg, nil, rejected(steplog.OutcomeDenied, err.Error())
			}
		}
	}

	started := time.Now()
	output, err := plan.Run(ctx)
	ms := time.Since(started).Milliseconds()
	var images []string
	if plan.Images != nil {
		images = plan.Images()
	}
	if err != nil {
		msg := fmt.Sprintf("Ошибка выполнения: %v", err)
		if strings.TrimSpace(output) != "" {
			msg += "\n\n" + output
		}
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
			Output: msg, Reason: err.Error()}})
		return msg, nil, callInfo{outcome: callFailed, status: steplog.OutcomeFailed, note: err.Error(), ms: ms}
	}
	// Чужой текст помечается здесь, в одном месте, а не в каждом инструменте:
	// так новый источник не остаётся непомеченным по забывчивости.
	if plan.Foreign {
		output = tools.MarkUntrusted(output)
	}

	emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
		Output: output, OK: true}})
	return output, images, callInfo{outcome: callOK, status: steplog.OutcomeOK, ms: ms}
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
