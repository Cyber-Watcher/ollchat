package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Night — один ночной прогон: набор задач × набор моделей × повторы.
type Night struct {
	Runner   *Runner
	Verifier *Verifier
	Store    *Store
	Client   *ollama.Client
	Doctor   *Doctor

	Suites   []*Suite
	Models   []ModelCard
	Repeats  int
	Deadline time.Time
	Log      func(format string, args ...any)

	// Watch перечитывает расписание перед каждой задачей: правку настроек
	// на ходу надо замечать, иначе «верните мне стенд» подействует только
	// после конца окна. nil — не следить (ручной прогон с явным пределом).
	Watch func() (time.Time, bool)

	failStreak int // сорванных попыток подряд — по ним лечим сервер
}

// SelectModels решает, кто участвует в прогоне. Список берётся с сервера,
// а не из настроек: на стенде появляются новые модели, и жёсткий список
// означал бы, что новинку никто не померит, пока о ней не вспомнят.
func SelectModels(models []ollama.ModelInfo, exclude []string) []ModelCard {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		if e = strings.TrimSpace(e); e != "" {
			skip[e] = true
		}
	}
	out := make([]ModelCard, 0, len(models))
	for _, m := range models {
		card := ModelCard{
			Name: m.Name, Digest: shortDigest(m.Digest),
			Quantization: m.Details.QuantizationLevel,
			ParameterSiz: m.Details.ParameterSize,
			SizeGiB:      float64(m.Size) / (1 << 30),
			Capabilities: m.Capabilities,
			CtxTrained:   m.Details.ContextLength,
		}
		switch {
		case skip[m.Name]:
			card.Skipped = "исключена настройкой прогона"
		case !m.HasCapability("completion"):
			card.Skipped = "не чат-модель (нет completion)"
		}
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Run прогоняет ночь целиком. Возвращает число выполненных попыток.
//
// Порядок — блоками по моделям: модель грузится один раз и отрабатывает весь
// свой набор. Перекладывать задачи между моделями нельзя: перезагрузка
// `qwen3.5:122b` с диска стоит минут, и на них ушла бы половина ночи.
func (n *Night) Run(ctx context.Context) (int, error) {
	var done int
	for _, card := range n.Models {
		if card.Skipped != "" {
			n.Log("модель %s пропущена: %s", card.Name, card.Skipped)
			continue
		}
		if n.outOfTime() {
			n.Log("время вышло, модель %s остаётся на следующую ночь", card.Name)
			break
		}
		n.Log("── модель %s (%s, %.1f ГиБ) ──", card.Name, card.Quantization, card.SizeGiB)
		cnt, err := n.runModel(ctx, card)
		done += cnt
		if unloadErr := n.unload(ctx, card.Name); unloadErr != nil {
			n.Log("не удалось выгрузить %s: %v", card.Name, unloadErr)
		}
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// Pending считает, сколько попыток ночи ещё не сделано.
//
// Нужен обвязке **до** закрытия сервера. Замерено 22.08.2026: ночь доделала
// всю работу к утру, а тик каждые четверть часа продолжал закрывать Ollama
// на localhost, обнаруживать ноль работы и открывать обратно — 26 таких
// заходов за одно субботнее окно, каждый с двумя перезапусками службы.
// Чужой запрос в этот момент обрывается.
func (n *Night) Pending() int {
	var left int
	for _, card := range n.Models {
		if card.Skipped != "" {
			continue
		}
		for _, suite := range n.Suites {
			for i := range suite.Tasks {
				task := &suite.Tasks[i]
				if missingCapability(card, task) != "" {
					continue
				}
				for rep := 1; rep <= n.Repeats; rep++ {
					if !n.Store.Done(card.Name, task.ID, rep) {
						left++
					}
				}
			}
		}
	}
	return left
}

func (n *Night) runModel(ctx context.Context, card ModelCard) (int, error) {
	var done int
	checkedSpill := false
	for _, suite := range n.Suites {
		for i := range suite.Tasks {
			task := &suite.Tasks[i]
			if miss := missingCapability(card, task); miss != "" {
				n.Log("  %s: пропуск, модель не умеет %s", task.ID, miss)
				continue
			}
			for rep := 1; rep <= n.Repeats; rep++ {
				if !n.scheduleStillOurs() {
					return done, nil
				}
				if n.outOfTime() {
					n.Log("  время вышло на %s r%d", task.ID, rep)
					return done, nil
				}
				if n.Store.Done(card.Name, task.ID, rep) {
					continue // уже прогнано в прошлый заход — докатываем, а не начинаем заново
				}
				if err := ctx.Err(); err != nil {
					return done, err
				}
				if err := n.attempt(ctx, card, suite, task, rep); err != nil {
					return done, err
				}
				done++

				// Выезд в оперативную память проверяется сразу после того, как
				// модель загрузилась: дальше мерить бессмысленно — цифры будут
				// не про модель, а про скорость шины.
				if !checkedSpill && n.Doctor != nil {
					checkedSpill = true
					if spilled, details := n.Doctor.Spilled(ctx); spilled {
						n.Log("  модель выехала в оперативную память — %s", details)
						if n.Doctor.Cfg.SpillAction == "restart" {
							if err := n.Doctor.Restart(ctx); err != nil {
								n.Log("  %v", err)
							}
						}
						n.Log("  пропускаю модель %s: замер на вытесненной модели ничего не стоит", card.Name)
						return done, nil
					}
				}

				if err := n.treat(ctx); err != nil {
					return done, err
				}
			}
		}
	}
	return done, nil
}

func (n *Night) attempt(ctx context.Context, card ModelCard, suite *Suite, task *Task, rep int) error {
	dir := n.Store.AttemptDir(card.Name, task.ID, rep)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Отметка «прогон жив» обновляется на каждой попытке: по ней служба возврата
	// отличает работающий прогон от умершего посреди окна.
	_ = WriteHeartbeat(n.Store.Root, Heartbeat{
		PID: os.Getpid(), Night: n.Store.Night, Model: card.Name, Task: task.ID,
	})
	a := Attempt{Model: card.Name, Task: task, Suite: suite.Name, Repeat: rep, Dir: dir}

	m, err := n.Runner.Run(ctx, a)
	if err != nil {
		return err
	}

	answer, _ := os.ReadFile(dir + "/answer.md")
	res := n.Verifier.Verify(ctx, task, dir, string(answer))
	m.Score, m.NeedsReview, m.Verdict = res.Score, res.NeedsReview, res.Verdict
	if err := WriteJSON(dir, "verify.json", res); err != nil {
		return err
	}
	// Метрики пишутся последними: их наличие и означает «попытка сделана».
	if err := WriteJSON(dir, "metrics.json", m); err != nil {
		return err
	}
	if err := n.Store.AppendIndex(m); err != nil {
		return err
	}

	if m.Error != "" {
		n.failStreak++
	} else {
		n.failStreak = 0
	}

	n.Log("  %s r%d: балл %.2f, %.0f с, %.1f ток/с, %d токенов%s",
		task.ID, rep, m.Score, m.WallSeconds, m.TokensPerSecond, m.EvalTokens, errSuffix(m))
	return nil
}

// treat лечит сервер, если попытки срываются подряд. Одна ошибка — дело
// житейское (модель сорвала генерацию, и это часть замера устойчивости),
// а несколько подряд означают, что сервер завис или карта застряла.
func (n *Night) treat(ctx context.Context) error {
	if n.Doctor == nil || n.Doctor.Cfg.RestartAfterErrors <= 0 {
		return nil
	}
	if n.failStreak < n.Doctor.Cfg.RestartAfterErrors {
		return nil
	}
	n.Log("  подряд сорвано попыток: %d — лечу сервер", n.failStreak)
	n.failStreak = 0
	if err := n.Doctor.Restart(ctx); err != nil {
		n.Log("  %v", err)
		return err
	}
	// Прогон продолжается с того места, где остановился: сделанные попытки
	// видны по их metrics.json и заново не гоняются.
	return nil
}

func errSuffix(m *Metrics) string {
	switch {
	case m.Error != "":
		return ", ошибка: " + m.Error
	case m.Refused:
		return ", отказ отвечать"
	case m.MixedScriptWords > 0:
		return fmt.Sprintf(", смешение алфавитов в %d словах", m.MixedScriptWords)
	}
	return ""
}

// missingCapability сообщает, какой возможности модели не хватает для задачи.
func missingCapability(card ModelCard, task *Task) string {
	for _, need := range task.Needs {
		found := false
		for _, c := range card.Capabilities {
			if c == need {
				found = true
				break
			}
		}
		if !found {
			return need
		}
	}
	return ""
}

func (n *Night) outOfTime() bool {
	return !n.Deadline.IsZero() && time.Now().After(n.Deadline)
}

// scheduleStillOurs перечитывает расписание и сообщает, наше ли ещё время.
// Правку настроек делают обычно затем, чтобы забрать стенд прямо сейчас, —
// такое надо замечать до следующей задачи, а не в конце окна.
func (n *Night) scheduleStillOurs() bool {
	if n.Watch == nil {
		return true
	}
	deadline, ok := n.Watch()
	if !ok {
		n.Log("окно закрыто правкой настроек — останавливаюсь, недоделанное докатится потом")
		return false
	}
	if !deadline.Equal(n.Deadline) {
		n.Log("предел изменён настройками: %s → %s",
			n.Deadline.Format("15:04 Mon"), deadline.Format("15:04 Mon"))
		n.Deadline = deadline
	}
	return true
}

// unload снимает модель с карты. Иначе последняя модель ночи держала бы
// десятки гигабайт до вечера, а карта нужна людям с утра.
func (n *Night) unload(ctx context.Context, model string) error {
	return unloadModel(ctx, n.Client, model)
}

// unloadModel просит Ollama забыть модель: пустой запрос с keep_alive "0s".
func unloadModel(ctx context.Context, c *ollama.Client, model string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req := ollama.ChatRequest{Model: model, Messages: []ollama.Message{}, KeepAlive: "0s"}
	for ev := range c.Chat(reqCtx, req) {
		if ev.Kind == ollama.EventError {
			return ev.Err
		}
	}
	return nil
}

// unloadLoaded снимает с карты модели, забытые в памяти до начала прогона.
//
// Такая модель больше не запрещает старт: карта при ней не считает. Но место
// она занимает настоящее, и первая же модель ночи не поместилась бы целиком.
func unloadLoaded(ctx context.Context, c *ollama.Client, log func(string)) {
	running, err := c.PS(ctx)
	if err != nil || len(running) == 0 {
		return
	}
	for _, m := range running {
		log(fmt.Sprintf("снимаю с карты забытую модель %s (%.1f ГиБ)",
			m.Name, float64(m.SizeVRAM)/(1<<30)))
		if err := unloadModel(ctx, c, m.Name); err != nil {
			log("не удалось выгрузить " + m.Name + ": " + err.Error())
		}
	}
}
