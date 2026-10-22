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
	"runtime/debug"
	"sort"
	"strings"
	"sync"
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
	EventSteer                        // реплика человека из очереди легла в историю (Х1)
)

// SteerQueue — реплики, набранные человеком посреди хода (этап 112, Х1; у Pi —
// очередь steer: «Injects before the next assistant response; the current
// assistant's tools finish first», «Learning Agent Engineering from Pi», 2026,
// разд. 15). Интерфейс кладёт, цикл агента забирает после результатов
// инструментов; что не забрано до конца хода, интерфейс отправит следующим
// вопросом (follow-up) или по Esc вернёт в поле ввода.
type SteerQueue struct {
	mu   sync.Mutex
	msgs []string
}

// Push ставит реплику в очередь.
func (q *SteerQueue) Push(text string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, text)
}

// Drain забирает все реплики и очищает очередь.
func (q *SteerQueue) Drain() []string {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs = nil
	return out
}

// Len — сколько реплик ждёт.
func (q *SteerQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}

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

// RejectedNoRetry — приписка к результату вызова, отклонённого человеком:
// её видит только модель (этап 112, Ч1).
const RejectedNoRetry = "Не повторяй этот вызов."

// DeniedNoBypass — приписка к результату вызова, запрещённого правилами
// настроек: её видит только модель (этап 112, Ч1, журналы рабочей машины).
const DeniedNoBypass = "Не пытайся обойти запрет другой командой или другим инструментом: " +
	"скажи пользователю, что это действие запрещено настройками, и что нужно сделать ему самому."

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
	// Logprobs — просить у сервера логарифмы вероятностей токенов; сводка по
	// ответу придёт в Stats события EventStats. Только для замеров (`--ask
	// --logprobs`, этап 112, Ч15): в работе выключено.
	Logprobs bool
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

	// Защита и пределы хода (этап 112; ключи agent.* — см. config.Agent).
	// JournalFailClosed — запись, bash и сеть не исполняются, если журнал шагов
	// перестал писаться (Н6). UntrustedGate — после чужого текста в разговоре
	// исходящие инструменты только с вопросом (Т7, О15). PrivateEgressCheck —
	// отказ при частном адресе или имени хоста в исходящем вызове (Н16).
	// MaxWritesNoask — после стольких правок за ход в noask следующая
	// спрашивается (Ч3; 0 и меньше — без предела). MaxTurnTokens — потолок
	// входных токенов за ход (Т11; 0 и меньше — без предела).
	JournalFailClosed  bool
	UntrustedGate      bool
	PrivateEgressCheck bool
	MaxWritesNoask     int
	MaxTurnTokens      int
	// OutHead — сколько рун вывода инструмента писать в журнал шагов (ключ
	// log.steps_out_head): 0 — умолчание toolOutHead, отрицательное — не
	// писать. Пишется не у всех инструментов — см. outHeadTools.
	OutHead int

	// Steer — очередь реплик, набранных посреди хода (этап 112, Х1); nil — нет.
	Steer *SteerQueue

	// writes — правок файлов в текущем ходе (для MaxWritesNoask); tainted — в
	// разговор вошёл чужой текст (для UntrustedGate).
	writes  int
	tainted bool
}

// retryPause — сколько ждать перед повторной попыткой обмена.
//
// Без паузы быстрый отказ сервера (5xx за миллисекунды) съедал все попытки
// подряд, и повтор ничего не давал. Формула та же, что у эмбеддера в kbembed:
// растущая пауза, чтобы занятому чужой работой серверу не добавлять нагрузки.
// С 16.10.2026 — удвоение со случайной добавкой (ollama.Backoff, этап 112, А2):
// 1, 2, 4 с плюс 0…0,5 с вместо ровных 1 и 2 с.
// Переменная, а не функция, — шов для тестов: им ждать секунды незачем.
var retryPause = func(attempt int) time.Duration {
	return ollama.Backoff(attempt, time.Second)
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
	// Что сделано за ход — для отчёта на потолке шагов (этап 112, Ф1, О18).
	byTool := map[string]int{}
	var lastTool, lastResult string
	// Состояние проверок хода (этап 112): правки считаются заново, а чужой текст,
	// вошедший раньше, остаётся в разговоре — значит, и в силе. Пересчёт из
	// истории не видит того, что /compact уже заменил сводкой, поэтому в силе
	// и липкая метка разговора (аудит 16.10.2026, находка 14).
	r.writes = 0
	r.tainted = conv.Foreign() || tainted(conv.Messages())
	if r.tainted {
		conv.MarkForeign()
	}
	tokensIn := 0

	// Дата в системном сообщении берётся один раз на ход: иначе цепочка
	// вызовов, пересёкшая полночь, получала бы разные системные сообщения
	// в соседних итерациях одного и того же хода.
	now := time.Now()

	// Конец хода и его причина — запись журнала шагов (этап 112, Ф2). Пишется
	// отложенным вызовом, чтобы без записи не остался ни один выход из цикла;
	// выход, не названный ниже явно, — снятие хода (контекст отменён).
	exchanges, lengthCuts := 0, 0
	reason := steplog.TurnCancelled
	defer func() {
		r.Steps.Write(steplog.Step{Turn: r.Turn, Kind: steplog.KindTurn, Model: r.Model,
			Outcome: reason, MS: time.Since(now).Milliseconds(),
			Extra: map[string]any{"steps": step, "exchanges": exchanges, "tool_calls": stats.Calls,
				"rejected": stats.Rejected, "failed": stats.Failed}})
	}()

	for iter := 0; unlimited || iter < maxIter; iter++ {
		exchanges = iter + 1
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
			Logprobs:  r.Logprobs,
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
			reason = steplog.TurnError
			if errors.Is(err, ollama.ErrCanceled) || ctx.Err() != nil {
				reason = steplog.TurnCancelled
			}
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
			MS: res.stats.TotalDuration / 1e6,
			// Время чтения промпта и загрузки модели — отдельно от общего: по ним
			// делятся попадание в кеш префикса и холодный старт (этап 112, У10, Ц12;
			// до 15.10.2026 они лежали в ollama.Stats и в журнал не уходили).
			Extra: map[string]any{"tool_calls": len(toolCalls), "done": res.stats.DoneReason,
				"prompt_eval_ms": res.stats.PromptEvalDuration / 1e6, "load_ms": res.stats.LoadDuration / 1e6}})

		// Т11: потолок входных токенов за ход. Один ход на рабочей машине съел
		// 15,7 % всех входных токенов за месяц журналов; после потолка новые
		// вызовы не исполняются — человек получает отчёт и решает сам.
		tokensIn += res.stats.PromptEvalCount
		if r.MaxTurnTokens > 0 && tokensIn > r.MaxTurnTokens && len(toolCalls) > 0 {
			reason = steplog.TurnLimit
			emit(ctx, out, Event{Kind: EventError,
				Err: fmt.Errorf("превышен потолок входных токенов за ход (agent.max_turn_tokens = %d, набрано %d): "+
					"модель продолжает запрашивать инструменты.\n%s"+
					"Поднять или снять (-1): agent.max_turn_tokens в файле настроек",
					r.MaxTurnTokens, tokensIn, limitReport(stats, byTool, lastTool, lastResult))})
			return
		}

		cut := res.stats.DoneReason == doneLength
		if len(toolCalls) == 0 {
			reason = steplog.TurnAnswer
			if cut {
				reason = steplog.TurnLength
				if !emit(ctx, out, Event{Kind: EventNotice, Text: lengthNotice}) {
					return
				}
			}
			emit(ctx, out, Event{Kind: EventTurnDone, Tools: &stats})
			return
		}

		// Ответ оборван пределом вывода, а в нём вызовы инструментов: их
		// аргументы могли не дописаться, и исполнять их нельзя (этап 112, Х2;
		// «The call does not execute; an output-limit error toolResult is
		// generated for each call, and a retry in the next turn is allowed»,
		// «Learning Agent Engineering from Pi», 2026, разд. 15). Каждый вызов
		// получает результат-пояснение, иначе история с вызовом без ответа
		// сломала бы следующий запрос, и модель может повторить вызов короче.
		// Второй обрыв подряд — ход заканчивается: предел вывода не даёт
		// дописать вызов, и повторы только жгут карту.
		if cut {
			lengthCuts++
			for _, call := range toolCalls {
				step++
				r.Steps.Write(steplog.Step{Turn: r.Turn, Step: step, Kind: steplog.KindTool, Model: r.Model,
					Tool: call.Function.Name, Outcome: steplog.OutcomeInvalid, Note: "ответ оборван пределом вывода (length), вызов не выполнен"})
				conv.Append(ollama.Message{
					Role:       ollama.RoleTool,
					ToolName:   call.Function.Name,
					ToolCallID: call.ID,
					Content:    lengthToolResult,
				})
			}
			if lengthCuts >= 2 {
				reason = steplog.TurnLength
				emit(ctx, out, Event{Kind: EventError, Err: errors.New(lengthNotice)})
				return
			}
			if !emit(ctx, out, Event{Kind: EventNotice, Text: fmt.Sprintf(
				"Ответ модели оборван пределом длины вывода, вызовов в нём %d — не выполнены. "+
					"Модель получила пояснение и может повторить вызов.", len(toolCalls))}) {
				return
			}
			continue
		}
		lengthCuts = 0

		// Модель запросила инструменты — выполняем их и возвращаем результаты.
		for _, call := range toolCalls {
			if ctx.Err() != nil {
				emit(ctx, out, Event{Kind: EventError, Err: ollama.ErrCanceled})
				return
			}
			result, images, info := r.executeCall(ctx, call, out)
			stats.Calls++
			byTool[call.Function.Name]++
			lastTool, lastResult = call.Function.Name, result
			switch info.outcome {
			case callRejected:
				stats.Rejected++
			case callFailed:
				stats.Failed++
			}
			step++
			// Что ушло модели (этап 112, С2): до 16.10.2026 выход инструмента был
			// только в журнале чата, где исход сведён к «успешно/ошибка», и по
			// журналу шагов нельзя было сказать, на чём сломался разбор результата
			// (Ч10). Начало — не целиком: журнал шагов читают сводки по всем дням.
			extra := map[string]any{"out_bytes": len(result)}
			if head, ok := r.outHead(call.Function.Name, info.outcome, result); ok && !info.hidden {
				extra["out_head"] = head
			}
			if info.confirm != "" {
				extra["confirm"], extra["confirm_ms"] = info.confirm, info.confirmMs
			}
			if info.decision != "" {
				extra["kind"], extra["target"], extra["decision"] = info.kind, info.target, info.decision
			}
			// Инструмент стороннего сервера MCP — сервер отдельным полем (этап 121,
			// Б6): сводки по журналу шагов группируют вызовы по нему, не разбирая имя.
			if srv, _, ok := tools.SplitExternalName(call.Function.Name); ok {
				extra["server"] = srv
			}
			r.Steps.Write(steplog.Step{Turn: r.Turn, Step: step, Kind: steplog.KindTool, Model: r.Model,
				Tool: call.Function.Name, Args: info.args,
				Outcome: info.status, MS: info.ms, Note: info.note, Extra: extra})
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
			// Метка чужого текста — в сам разговор, липкой: /compact заменит
			// это сообщение сводкой, а метка останется (находка 14).
			if r.tainted {
				conv.MarkForeign()
			}
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

		// Реплика человека, набранная посреди хода (Х1): результаты всех вызовов
		// уже в истории, значит, она встаёт после них — перед следующим ответом
		// модели, и цепочка assistant{tool_calls} → tool не рвётся.
		for _, text := range r.Steer.Drain() {
			conv.Append(ollama.Message{Role: ollama.RoleUser, Content: text})
			if !emit(ctx, out, Event{Kind: EventSteer, Text: text}) {
				return
			}
		}
	}

	// Сюда попадаем только с конечным пределом: при -1 цикл не кончается сам.
	reason = steplog.TurnLimit
	emit(ctx, out, Event{Kind: EventError,
		Err: fmt.Errorf("превышено ограничение agent.max_iterations (%d): модель продолжает запрашивать инструменты.\n"+
			"%s"+
			"Поднять на сеанс: /tools iterations 50 (или /tools iterations off — без ограничения).\n"+
			"Насовсем: agent.max_iterations в файле настроек; -1 означает без ограничения",
			maxIter, limitReport(stats, byTool, lastTool, lastResult))})
}

// limitReport — частичный итог хода, упёршегося в потолок шагов (этап 112,
// Ф1, О18): сколько вызовов и каких, что отклонено и упало, чем кончился
// последний. Собирается кодом, без обращения к модели: модель, у которой
// кончились шаги, дописала бы недостающее сама («Agent Systems», `706#521`).
// Ход не продолжается — человек решает, поднять потолок или сменить просьбу.
func limitReport(stats ToolStats, byTool map[string]int, lastTool, lastResult string) string {
	if stats.Calls == 0 {
		return ""
	}
	names := make([]string, 0, len(byTool))
	for n := range byTool {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if byTool[names[i]] != byTool[names[j]] {
			return byTool[names[i]] > byTool[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s ×%d", n, byTool[n])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Сделано за ход: вызовов %d (%s)", stats.Calls, strings.Join(parts, ", "))
	if stats.Rejected > 0 || stats.Failed > 0 {
		fmt.Fprintf(&b, "; отклонено %d, с ошибкой %d", stats.Rejected, stats.Failed)
	}
	b.WriteString(".\n")
	if lastTool != "" {
		last := strings.Join(strings.Fields(lastResult), " ")
		if rs := []rune(last); len(rs) > 200 {
			last = string(rs[:200]) + "…"
		}
		fmt.Fprintf(&b, "Последний вызов — %s: %s\n", lastTool, last)
	}
	b.WriteString("Ответа модель не дала: итог выше собран из журнала хода, без догадок.\n")
	return b.String()
}

// doneLength — done_reason Ollama, когда генерацию оборвал предел вывода
// (options.num_predict), а не модель закончила сама.
const doneLength = "length"

// lengthNotice — что сказать человеку об оборванном ответе.
const lengthNotice = "Ответ модели оборван пределом длины вывода (num_predict): дальше она не писала. " +
	"Поднять предел: options.num_predict в настройках сервера; -1 — без предела."

// lengthToolResult — что получает модель вместо результата недописанного вызова.
const lengthToolResult = "Вызов не выполнен: твой ответ оборван пределом длины вывода, " +
	"аргументы могли не дописаться. Повтори вызов короче или ответь словами."

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
		started := time.Now()
		res, err := r.exchange(ctx, req, out)
		if err == nil {
			return res, nil
		}

		canRetry := attempt < maxRetries &&
			ollama.Retryable(err) &&
			ctx.Err() == nil &&
			res.content.Len() == 0 &&
			len(res.toolCalls) == 0
		pause := retryPause(attempt)
		r.logRetry(ctx, attempt, maxRetries, err, canRetry, pause, time.Since(started))

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
		case <-time.After(pause):
		case <-ctx.Done():
			return res, ollama.ErrCanceled
		}
	}
}

// logRetry пишет в журнал шагов неудавшуюся попытку обмена (этап 112, М5).
//
// До 13.10.2026 повтор уходил только событием в интерфейс, а журнал получал
// лишь удачный обмен: ни stepstat.py, ни любой подсчёт не видел ни одного
// обрыва, и замеры «сколько обрывов в сутки», «доля успеха со второй попытки»
// (А1–А3) снять было нечем. Снятие хода человеком — не сбой и не пишется:
// его видно по записи конца хода.
func (r *Runner) logRetry(ctx context.Context, attempt, maxRetries int, err error, again bool, pause, took time.Duration) {
	if errors.Is(err, ollama.ErrCanceled) || ctx.Err() != nil {
		return
	}
	outcome := steplog.RetryGaveUp
	if again {
		outcome = steplog.RetryAgain
	} else {
		pause = 0
	}
	note := err.Error()
	if rs := []rune(note); len(rs) > 300 {
		note = string(rs[:300]) + "…"
	}
	r.Steps.Write(steplog.Step{Turn: r.Turn, Kind: steplog.KindRetry, Model: r.Model,
		Outcome: outcome, MS: took.Milliseconds(), Note: note,
		Extra: map[string]any{"attempt": attempt + 1, "of": maxRetries + 1,
			"retryable": ollama.Retryable(err), "pause_ms": pause.Milliseconds()}})
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
	// args — что писать о вызове в журнал шагов: аргументы модели или то,
	// что инструмент дал вместо них (tools.Plan.LogArgs).
	args string
	// hidden — аргументы подменены (LogArgs, SafeArgs): инструмент работает
	// с персональными данными, и начало его вывода в журнал шагов не идёт.
	hidden bool
	// confirm — ответ человека на вопрос о вызове («yes», «no», «always»,
	// «always_tool», «cancel»); пусто — вопроса не было. confirmMs — сколько он
	// думал: по ним видно «пролистывание» подтверждений (этап 112, Н14, О16).
	confirm   string
	confirmMs int64
	// kind, target, decision — вид и цель действия и решение правил до человека
	// («allow», «ask», «deny»; этап 112, И6): по ним в журнале видно, разрешён
	// вызов правилом или одобрен человеком; пусто — до проверки прав не дошло.
	kind, target, decision string
}

// decisionName — решение правил словом для журнала шагов.
func decisionName(d permissions.Decision) string {
	switch d {
	case permissions.DecisionAllow:
		return "allow"
	case permissions.DecisionDeny:
		return "deny"
	}
	return "ask"
}

// answerName — ответ человека словом для журнала шагов.
func answerName(a Answer) string {
	switch a {
	case AnswerNo:
		return "no"
	case AnswerYes:
		return "yes"
	case AnswerAlways:
		return "always"
	case AnswerAlwaysTool:
		return "always_tool"
	}
	return "?"
}

// toolOutHead — сколько рун вывода инструмента пишется в запись вызова (С2),
// когда Runner.OutHead не задан (ноль).
const toolOutHead = 200

// outHeadTools — инструменты, чей УСПЕШНЫЙ вывод написан нашим кодом, а не
// взят из чужого содержимого: сводка правки и список имён каталога. Только у
// них начало вывода идёт в журнал шагов (аудит 16.10.2026, находка 16, выбор
// владельца 17.10 — «В»): у read_file, grep, bash, http_fetch, поиска по книгам
// и прочих в вывод попадает содержимое файлов, страниц и книг, и журнал без
// срока хранения копил бы его начало. Список задан перечислением: новый
// инструмент по умолчанию в журнал своего вывода не пишет.
var outHeadTools = map[string]bool{
	tools.NameWriteFile: true, tools.NameEditFile: true, tools.NameListDir: true,
}

// outHead — начало вывода для записи вызова в журнал шагов; ok == false —
// не писать. Отказ и ошибка пишутся у любого инструмента: их текст составил
// наш код (правила, человек, обёртка ошибки), чужого содержимого в нём нет.
func (r *Runner) outHead(name string, outcome callOutcome, result string) (string, bool) {
	n := r.OutHead
	if n == 0 {
		n = toolOutHead
	}
	if n < 0 || (outcome == callOK && !outHeadTools[name]) {
		return "", false
	}
	return textx.Shorten(result, n), true
}

func rejected(status, note string) callInfo {
	return callInfo{outcome: callRejected, status: status, note: note}
}

// planTool готовит вызов инструмента. Переменная, а не прямой вызов, — шов
// для тестов: подложить инструмент, который падает, без настоящего набора.
var planTool = func(reg *tools.Registry, name string, args map[string]any) (*tools.Plan, error) {
	return reg.Plan(name, args)
}

// toolPanic — паника внутри инструмента, превращённая в ошибку. Стек идёт
// в журнал шагов: по одному значению паники место поломки не найти.
type toolPanic struct {
	value any
	stack []byte
}

func (p *toolPanic) Error() string {
	return fmt.Sprintf("внутренняя ошибка инструмента (паника): %v", p.value)
}

// guarded вызывает часть инструмента и превращает панику в ошибку.
//
// **Почему.** Инструмент — это разбор PDF, граф, база знаний, сеть: много
// чужого ввода и много кода. Паника в нём шла прямо в горутину агента, а там
// recover не было, и падал весь процесс: интерфейс оставался в alt-screen,
// терминал приходилось сбрасывать вручную, диалог пропадал. Ошибкой же
// модель получает объяснение, а ход продолжается.
func guarded[T any](f func() (T, error)) (out T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &toolPanic{value: p, stack: debug.Stack()}
		}
	}()
	return f()
}

// noteOf — пояснение для журнала шагов; у паники — со стеком.
func noteOf(err error) string {
	var p *toolPanic
	if errors.As(err, &p) {
		return err.Error() + "\n" + string(p.stack)
	}
	return err.Error()
}

// executeCall готовит, согласует и выполняет один вызов инструмента.
// Возвращаемая строка всегда пригодна для отправки модели: и успех, и ошибка.
func (r *Runner) executeCall(ctx context.Context, call ollama.ToolCall, out chan<- Event) (_ string, _ []string, info callInfo) {
	name := call.Function.Name
	argsJSON := call.Function.ArgumentsJSON()
	var confirm string
	var asked time.Time
	var audit [3]string // вид, цель, решение правил (И6)
	defer func() {
		info.args = argsJSON
		info.hidden = argsJSON != call.Function.ArgumentsJSON()
		if !asked.IsZero() {
			info.confirm, info.confirmMs = confirm, time.Since(asked).Milliseconds()
		}
		info.kind, info.target, info.decision = audit[0], audit[1], audit[2]
	}()

	if r.Tools == nil {
		msg := "Инструменты отключены в настройках."
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: name, Args: argsJSON,
			Output: msg, Reason: msg}})
		return msg, nil, rejected(steplog.OutcomeInvalid, "инструменты отключены")
	}

	plan, err := guarded(func() (*tools.Plan, error) {
		return planTool(r.Tools, name, call.Function.Arguments)
	})
	if err != nil {
		// Плана нет — нет и plan.LogArgs, а аргументы с персональными
		// данными не должны уйти в событие и журналы и на этой ветке:
		// инструмент спрашивается напрямую.
		safe, safeErr := guarded(func() (string, error) {
			return r.Tools.SafeArgs(name, call.Function.Arguments), nil
		})
		switch {
		case safeErr != nil:
			// Прятать нечем — аргументы не показываем вовсе: в них могут
			// быть персональные данные.
			argsJSON = "(аргументы скрыты: инструмент упал)"
		case safe != "":
			argsJSON = safe
		}
		msg := fmt.Sprintf("Ошибка: %v", err)
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name,
			Title: fmt.Sprintf("%s(%s)", name, textx.Shorten(argsJSON, 60)), Args: argsJSON,
			Output: msg, Reason: err.Error()}})
		return msg, nil, rejected(steplog.OutcomeInvalid, noteOf(err))
	}

	// Аргументы с персональными данными (имена из подсказок модели
	// scan_redact) не уходят ни в окно подтверждения, ни в журналы: туда идёт
	// то, что дал инструмент. Модели они нужны — ей вызов отдан целиком.
	if plan.LogArgs != "" {
		argsJSON = plan.LogArgs
	}
	if !emit(ctx, out, Event{Kind: EventToolPlan, Tool: &ToolEvent{
		Name: name, Title: plan.Title, Args: argsJSON, Preview: plan.Preview,
	}}) {
		return "Пользователь прервал выполнение.", nil, rejected(steplog.OutcomeCancelled, "")
	}

	decision := r.Guard.Check(plan.Req)
	for _, req := range plan.Extra {
		d := r.Guard.Check(req)
		if d.Decision == permissions.DecisionDeny {
			decision = d
			break
		}
		if d.Decision == permissions.DecisionAsk && decision.Decision == permissions.DecisionAllow {
			decision = d
		}
	}
	// Защита и пределы хода (этап 112) — только ужесточают решение правил.
	decision = r.gate(name, plan, call.Function.ArgumentsJSON(), decision)
	audit = [3]string{string(plan.Req.Kind), textx.Shorten(plan.Req.Target, 200), decisionName(decision.Decision)}
	switch decision.Decision {
	case permissions.DecisionDeny:
		msg := fmt.Sprintf("Действие запрещено настройками: %s", decision.Reason)
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
			Output: msg, Reason: decision.Reason, Skipped: true}})
		// Модели — запрет обходить (этап 112, Ч1, журналы рабочей машины 14.10.2026:
		// после запрета правилом в 29 случаях из 35 модель сразу звала тот же
		// инструмент с другими доводами — перебор apt-get/wget/sudo/curl).
		// Человек видит прежний текст: фраза обращена к модели.
		return msg + " " + DeniedNoBypass, nil, rejected(steplog.OutcomeDenied, decision.Reason)

	case permissions.DecisionAsk:
		reply := make(chan Answer, 1)
		asked, confirm = time.Now(), "cancel"
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
			confirm = answerName(answer)
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
			// Модели — с запретом повторять («AI Agents, Built by Hand», 2026,
			// разд. 14: «The user did not allow this action. Do not retry it.»).
			// Человек видит прежний текст: фраза обращена к модели, не к нему.
			// Замер этапа 112, Ч1: в журналах шагов повтора после отказа 0 из 9,
			// но ходы интерактивного клиента туда почти не пишутся.
			return msg + " " + RejectedNoRetry, nil, rejected(steplog.OutcomeRejected, "отклонено пользователем")
		case AnswerAlways:
			for _, req := range append([]permissions.Request{plan.Req}, plan.Extra...) {
				if err := r.Guard.GrantSession(req.Kind, req.Target); err != nil {
					msg := fmt.Sprintf("Действие не выполнено: %v", err)
					emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
						Output: msg, Skipped: true, Reason: err.Error()}})
					return msg, nil, rejected(steplog.OutcomeDenied, err.Error())
				}
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
	// Журнал и ход — с контекстом: поиск под инструментом пишет свою запись (Ч17).
	runCtx := steplog.NewContext(ctx, r.Steps, r.Turn)
	output, err := guarded(func() (string, error) { return plan.Run(runCtx) })
	ms := time.Since(started).Milliseconds()
	var images []string
	if plan.Images != nil && err == nil {
		images, err = guarded(func() ([]string, error) { return plan.Images(), nil })
	}
	// Чужой текст помечается здесь, в одном месте, а не в каждом инструменте:
	// так новый источник не остаётся непомеченным по забывчивости. И на ветке
	// ошибки тоже: упавший инструмент мог успеть вернуть чужой текст, и до
	// 15.10.2026 он уходил модели без пометки (этап 112, Ч4,
	// TestFakeForeignOutputOnFailureIsMarked).
	if plan.Foreign && (err == nil || strings.TrimSpace(output) != "") {
		output = tools.MarkUntrusted(output)
	}
	if err != nil {
		msg := fmt.Sprintf("Ошибка выполнения: %v", err)
		if strings.TrimSpace(output) != "" {
			msg += "\n\n" + output
		}
		emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
			Output: msg, Reason: err.Error()}})
		return msg, nil, callInfo{outcome: callFailed, status: steplog.OutcomeFailed, note: noteOf(err), ms: ms}
	}

	emit(ctx, out, Event{Kind: EventToolResult, Tool: &ToolEvent{Name: name, Title: plan.Title, Args: argsJSON,
		Output: output, OK: true}})
	// Для проверок хода (этап 112): правка файла — к пределу Ч3, вывод
	// источника чужого текста — к UntrustedGate (Т7, О15).
	if plan.Req.Kind == permissions.KindWrite {
		r.writes++
	}
	if isForeign(name) {
		r.tainted = true
	}
	// Пометка инструмента о пути исполнения — «служба недоступна, искал по
	// своим файлам» (этап 121, А4) — ложится в note записи журнала шагов.
	return output, images, callInfo{outcome: callOK, status: steplog.OutcomeOK, ms: ms, note: plan.Note}
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
