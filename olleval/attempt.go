package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Attempt — одна попытка: одна задача, одна модель, один повтор.
type Attempt struct {
	Model  string
	Task   *Task
	Suite  string
	Repeat int
	Dir    string
}

// Metrics — что замерено на попытке. Пишется в metrics.json рядом с ответом
// и строкой в index.jsonl. Балл ставится позже, проверкой.
type Metrics struct {
	Night     string    `json:"night"`
	Suite     string    `json:"suite"`
	Model     string    `json:"model"`
	Task      string    `json:"task"`
	Level     int       `json:"level"`
	Repeat    int       `json:"repeat"`
	StartedAt time.Time `json:"started_at"`

	TTFTSeconds     float64 `json:"ttft_seconds"`      // время до первого куска ответа
	LoadSeconds     float64 `json:"load_seconds"`      // из него — загрузка модели с диска
	WallSeconds     float64 `json:"wall_seconds"`      // всего на попытку
	TokensPerSecond float64 `json:"tokens_per_second"` // по данным сервера
	PromptTokens    int     `json:"prompt_tokens"`
	EvalTokens      int     `json:"eval_tokens"`
	NumCtx          int     `json:"num_ctx"`

	AnswerChars   int `json:"answer_chars"`
	ThinkingChars int `json:"thinking_chars"`
	ToolCalls     int `json:"tool_calls"`

	MixedScriptWords int  `json:"mixed_script_words"`
	Words            int  `json:"words"`
	Refused          bool `json:"refused"`

	Retries int    `json:"retries"`
	Error   string `json:"error,omitempty"`

	// TimedOut — попытка упёрлась в предел времени, а не сорвалась.
	// Это не сбой стенда, а результат про модель: `deepseek-r1:70b`
	// 22.08.2026 дважды израсходовал все десять минут на рассуждения
	// (42 тысячи знаков, в одном случае — зациклился на «1024*1024*…»)
	// и не выдал ни одного знака ответа. Считать это сбоем значило бы
	// свалить поведение модели на оборудование.
	TimedOut bool `json:"timed_out,omitempty"`

	// Заполняет проверка.
	Score       float64 `json:"score"`
	NeedsReview bool    `json:"needs_review"`
	Verdict     string  `json:"verdict,omitempty"`
}

// Runner — то, что нужно попытке от прогона: клиент, окно, повторы.
type Runner struct {
	Client   *ollama.Client
	Store    *Store
	Fixtures string
	NumCtx   int
	Retries  int           // сколько раз повторять обмен, сорванный до первого куска
	Timeout  time.Duration // предел на одну генерацию
	KeepFor  string        // keep_alive на время блока модели
	Verbose  bool
}

// Run выполняет попытку целиком: собирает запрос, стримит ответ, считает метрики
// и складывает сырьё. Ошибка генерации не прерывает ночь — она записывается
// в метрики и становится частью замера устойчивости.
func (r *Runner) Run(ctx context.Context, a Attempt) (*Metrics, error) {
	messages, err := r.buildMessages(a.Task)
	if err != nil {
		return nil, err
	}

	numCtx := r.NumCtx
	if a.Task.NumCtx > 0 {
		numCtx = a.Task.NumCtx
	}
	req := ollama.ChatRequest{
		Model:     a.Model,
		Messages:  messages,
		Tools:     toolsFor(a.Task),
		KeepAlive: r.KeepFor,
		Options:   map[string]any{"num_ctx": numCtx},
	}

	m := &Metrics{
		Night: r.Store.Night, Suite: a.Suite, Model: a.Model, Task: a.Task.ID,
		Level: a.Task.Level, Repeat: a.Repeat, StartedAt: time.Now(), NumCtx: numCtx,
	}
	if err := WriteJSON(a.Dir, "request.json", req); err != nil {
		return nil, err
	}

	limit := a.Task.Timeout.Get(r.Timeout)
	var answer, thinking strings.Builder

	for try := 0; ; try++ {
		answer.Reset()
		thinking.Reset()
		m.ToolCalls = 0

		genCtx, cancel := context.WithTimeout(ctx, limit)
		start := time.Now()
		var ttft time.Duration
		var stats ollama.Stats
		var genErr error

		raw, err := os.Create(filepath.Join(a.Dir, "stream.jsonl"))
		if err != nil {
			cancel()
			return nil, err
		}

		for ev := range r.Client.Chat(genCtx, req) {
			switch ev.Kind {
			case ollama.EventContent:
				if ttft == 0 {
					ttft = time.Since(start)
				}
				answer.WriteString(ev.Text)
				fmt.Fprintf(raw, "{\"content\":%s}\n", jsonString(ev.Text))
			case ollama.EventThinking:
				if ttft == 0 {
					ttft = time.Since(start)
				}
				thinking.WriteString(ev.Text)
				fmt.Fprintf(raw, "{\"thinking\":%s}\n", jsonString(ev.Text))
			case ollama.EventToolCalls:
				m.ToolCalls += len(ev.ToolCalls)
				for _, tc := range ev.ToolCalls {
					fmt.Fprintf(raw, "{\"tool_call\":%s,\"arguments\":%s}\n",
						jsonString(tc.Function.Name), tc.Function.ArgumentsJSON())
				}
			case ollama.EventDone:
				stats = ev.Stats
				fmt.Fprintf(raw, "{\"done\":true,\"eval_count\":%d,\"prompt_eval_count\":%d}\n",
					stats.EvalCount, stats.PromptEvalCount)
			case ollama.EventError:
				genErr = ev.Err
				fmt.Fprintf(raw, "{\"error\":%s}\n", jsonString(ev.Err.Error()))
			}
		}
		raw.Close()
		cancel()

		m.WallSeconds = time.Since(start).Seconds()
		m.TTFTSeconds = ttft.Seconds()
		m.TokensPerSecond = stats.TokensPerSecond()
		// Первая задача блока платит за загрузку модели с диска — у 122b это
		// минуты. Без отдельной цифры её время выглядело бы как медлительность.
		m.LoadSeconds = float64(stats.LoadDuration) / 1e9
		m.PromptTokens = stats.PromptEvalCount
		m.EvalTokens = stats.EvalCount

		// Повтор безопасен ровно тогда, когда сорвалось до первого куска ответа:
		// то же правило, по которому повторяет обмен сам ollchat.
		nothingShown := answer.Len() == 0 && thinking.Len() == 0 && m.ToolCalls == 0
		if genErr != nil && ollama.Retryable(genErr) && nothingShown && try < r.Retries {
			m.Retries++
			time.Sleep(2 * time.Second)
			continue
		}
		if genErr != nil {
			m.Error = genErr.Error()
			// Предел времени виден по тому, что контекст попытки истёк:
			// сам клиент отдаёт обрыв потока одинаково, чем бы он ни был
			// вызван.
			if genCtxErr := genCtx.Err(); errors.Is(genCtxErr, context.DeadlineExceeded) {
				m.TimedOut = true
				m.Error = fmt.Sprintf("предел времени %s: ответа не было, рассуждений %d знаков",
					limit, len([]rune(thinking.String())))
			}
		}
		break
	}

	ans, think := answer.String(), thinking.String()
	m.AnswerChars = len([]rune(ans))
	m.ThinkingChars = len([]rune(think))
	m.MixedScriptWords = MixedScriptWords(ans)
	m.Words = WordCount(ans)
	m.Refused = Refused(ans)

	if err := WriteText(a.Dir, "answer.md", ans); err != nil {
		return nil, err
	}
	if think != "" {
		if err := WriteText(a.Dir, "thinking.md", think); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// toolsFor собирает список инструментов задачи в вид, понятный Ollama.
// Задача, где инструментов нет, отправляется без поля tools вовсе: модель
// без возможности их вызывать иначе спотыкается на пустом списке.
func toolsFor(t *Task) []ollama.Tool {
	if len(t.Tools) == 0 {
		return nil
	}
	out := make([]ollama.Tool, 0, len(t.Tools))
	for _, spec := range t.Tools {
		out = append(out, ollama.Tool{Type: "function", Function: spec})
	}
	return out
}

// buildMessages собирает вопрос: системный промпт задачи, приложенные файлы,
// затем сама постановка. Файлы идут перед вопросом — так их видно как условие,
// а не как приписку.
func (r *Runner) buildMessages(t *Task) ([]ollama.Message, error) {
	var msgs []ollama.Message
	if s := strings.TrimSpace(t.System); s != "" {
		msgs = append(msgs, ollama.Message{Role: ollama.RoleSystem, Content: s})
	}

	var b strings.Builder
	for _, rel := range t.Attach {
		path := filepath.Join(r.Fixtures, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("задача %s: приложение %s: %w", t.ID, rel, err)
		}
		fmt.Fprintf(&b, "Файл `%s`:\n\n```\n%s\n```\n\n", rel, strings.TrimRight(string(data), "\n"))
	}
	b.WriteString(strings.TrimSpace(t.Prompt))
	msgs = append(msgs, ollama.Message{Role: ollama.RoleUser, Content: b.String()})
	return msgs, nil
}

// jsonString кодирует строку для сырого потока: писать его руками дешевле,
// чем городить структуру ради одного поля.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
