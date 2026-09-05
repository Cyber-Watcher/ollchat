//go:build live

package live

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sweep — развёртка одного параметра: какие значения перебираем и на каких
// задачах. Задачи разные не для красоты: точность видна на факте и арифметике,
// формат — на JSON, разнообразие — на выдумке, повторы — на рассказе.
type sweep struct {
	param  string
	values []any
	probes []string
}

var sweeps = []sweep{
	{"temperature", []any{0.0, 0.3, 0.6, 1.0, 1.5, 2.0},
		[]string{"факт", "арифметика", "json", "формат", "выдумка", "рассказ"}},
	{"top_p", []any{0.1, 0.5, 0.9, 1.0}, []string{"факт", "json", "выдумка"}},
	{"top_k", []any{1, 5, 40, 100}, []string{"факт", "json", "выдумка"}},
	{"min_p", []any{0.0, 0.05, 0.2}, []string{"факт", "выдумка", "рассказ"}},
	{"repeat_penalty", []any{1.0, 1.1, 1.5}, []string{"рассказ", "перечень"}},
	{"presence_penalty", []any{0.0, 1.5}, []string{"рассказ", "перечень"}},
	{"frequency_penalty", []any{0.0, 1.5}, []string{"рассказ", "перечень"}},
	{"typical_p", []any{1.0, 0.5}, []string{"факт", "рассказ"}},
}

func repeats() int {
	if s := os.Getenv("OLLCHAT_PARAM_REPEATS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return defaultRepeats
}

// probeByName достаёт задачу из набора.
func probeByName(name string) probe {
	for _, p := range probes {
		if p.name == name {
			return p
		}
	}
	panic("нет задачи " + name)
}

// TestParamSweeps — главный замер: как параметры влияют на ответы.
//
// Сетка гоняется при прочих равных: меняется ровно один параметр, остальные
// не задаются вовсе, то есть действуют значения, зашитые в модель. Иначе
// невозможно сказать, что именно подействовало.
func TestParamSweeps(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false // рассуждения выключены: они не ответ, а их длина смазала бы замер

	// Фильтр развёрток: замер длинный, и добирать недостающий срез разумно
	// отдельным прогоном, а не повторять всю сетку.
	only := map[string]bool{}
	for _, s := range strings.Split(os.Getenv("OLLCHAT_PARAM_SWEEPS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			only[s] = true
		}
	}

	for _, model := range models(t) {
		t.Logf("═══ модель %s ═══", model)
		start := time.Now()

		for _, sw := range sweeps {
			if len(only) > 0 && !only[sw.param] {
				continue
			}
			t.Logf("── %s ──", sw.param)
			for _, val := range sw.values {
				for _, pname := range sw.probes {
					p := probeByName(pname)
					predict := defaultNumPredict
					if p.predict > 0 {
						predict = p.predict
					}
					opts := options(map[string]any{"num_predict": predict}, sw.param, val)

					var answers []string
					okCount, errCount := 0, 0
					var sumRepeat, sumTokSec float64
					for attempt := 1; attempt <= repeats(); attempt++ {
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
						ans, think, st, err := ask(ctx, c, model, p.prompt, opts, &no)
						cancel()

						r := run{
							Model: model, Param: sw.param, Value: val, Options: opts,
							Probe: pname, Attempt: attempt, Answer: ans, Thinking: think,
							PromptTokens: st.PromptEvalCount, EvalTokens: st.EvalCount,
							TokPerSec: st.TokensPerSecond(), PromptTokSec: st.PromptTokensPerSecond(),
							DoneReason: st.DoneReason,
						}
						if err != nil {
							r.Err = err.Error()
							errCount++
						} else {
							answers = append(answers, ans)
							if p.check != nil && p.check(ans) {
								r.OK = true
								okCount++
							}
							sumRepeat += repeatRatio(ans)
							sumTokSec += st.TokensPerSecond()
						}
						res.add(r)
					}

					n := len(answers)
					line := "%s = %-5v · %-11s ·"
					args := []any{sw.param, val, pname}
					if p.check != nil {
						line += " верно %d/%d ·"
						args = append(args, okCount, repeats())
					}
					line += " различных %.0f%% · повторов %.1f%%"
					args = append(args, uniqueRatio(answers)*100, avg(sumRepeat, n)*100)
					if errCount > 0 {
						line += " · ОШИБОК %d"
						args = append(args, errCount)
					}
					t.Logf(line, args...)
				}
			}
		}

		t.Logf("модель %s: прогон занял %s", model, time.Since(start).Round(time.Second))
		unload(t, c, model)
	}

	res.save(t, "sweeps.json")
}

// TestTemperatureWithoutFilters проверяет, почему высокая температура сама
// по себе не ломает ответы.
//
// В первом прогоне точность держалась даже при temperature=2, и это выглядело
// странно. Объяснение напрашивалось такое: у моделей зашиты top_k и top_p,
// которые отсекают хвост распределения раньше, чем до него доберётся
// температура. Проверяем прямо: та же сетка со снятыми ограничителями.
func TestTemperatureWithoutFilters(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false

	// top_k = 0 отключает отбор по числу кандидатов, top_p = 1 и min_p = 0 —
	// по вероятности. Остаётся одна температура, без страховки.
	free := map[string]any{"top_k": 0, "top_p": 1.0, "min_p": 0.0, "num_predict": defaultNumPredict}
	guarded := map[string]any{"num_predict": defaultNumPredict}

	for _, model := range models(t) {
		for _, mode := range []struct {
			name string
			base map[string]any
		}{
			{"фильтры модели", guarded},
			{"без фильтров", free},
		} {
			for _, temp := range []float64{0.6, 1.0, 1.5, 2.0, 3.0} {
				for _, pname := range []string{"факт", "арифметика", "json"} {
					p := probeByName(pname)
					opts := options(mode.base, "temperature", temp)
					ok := 0
					for attempt := 1; attempt <= repeats(); attempt++ {
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
						ans, _, st, err := ask(ctx, c, model, p.prompt, opts, &no)
						cancel()
						r := run{Model: model, Param: "temperature/" + mode.name, Value: temp,
							Options: opts, Probe: pname, Attempt: attempt, Answer: ans,
							EvalTokens: st.EvalCount, DoneReason: st.DoneReason}
						if err != nil {
							r.Err = err.Error()
						} else if p.check(ans) {
							r.OK = true
							ok++
						}
						res.add(r)
					}
					t.Logf("%-15s · temperature %.1f · %-11s · верно %d/%d",
						mode.name, temp, pname, ok, repeats())
				}
			}
		}
		unload(t, c, model)
	}
	res.save(t, "temperature_filters.json")
}

// TestSeedAtHighTemperature уточняет, при каких условиях seed действительно
// повторяет ответ.
//
// При temperature 1.0 совпадение вышло полным, а раньше на этой же модели
// при 1.5 два прогона разошлись одним словом. Проверяем прицельно, потому
// что «seed гарантирует повтор» и «seed обычно повторяет» — разные
// утверждения, и в статью должно попасть верное.
func TestSeedAtHighTemperature(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false
	p := probeByName("выдумка")

	for _, model := range models(t) {
		for _, temp := range []float64{1.0, 1.5, 2.0} {
			for _, free := range []bool{false, true} {
				opts := options(nil, "seed", 42, "temperature", temp, "num_predict", 60)
				name := "фильтры модели"
				if free {
					opts["top_k"] = 0
					opts["top_p"] = 1.0
					opts["min_p"] = 0.0
					name = "без фильтров"
				}
				var answers []string
				for attempt := 1; attempt <= repeats(); attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					ans, _, st, err := ask(ctx, c, model, p.prompt, opts, &no)
					cancel()
					if err != nil {
						t.Logf("%s / seed t=%.1f %s: %v", model, temp, name, err)
						continue
					}
					answers = append(answers, ans)
					res.add(run{Model: model, Param: "seed_high_temp", Value: temp, Options: opts,
						Probe: p.name, Attempt: attempt, Answer: ans,
						EvalTokens: st.EvalCount, DoneReason: st.DoneReason})
				}
				same := 0
				for _, a := range answers {
					if len(answers) > 0 && a == answers[0] {
						same++
					}
				}
				t.Logf("%s · seed=42 · temperature %.1f · %-15s · совпало посимвольно %d из %d",
					model, temp, name, same, len(answers))
			}
		}
		unload(t, c, model)
	}
	res.save(t, "seed_high_temp.json")
}

func avg(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// TestSeedDeterminism проверяет, насколько seed делает ответ воспроизводимым.
//
// Проверяется отдельно от сеток, потому что здесь важна не доля верных ответов,
// а совпадение текстов между прогонами.
func TestSeedDeterminism(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false
	p := probeByName("выдумка")

	for _, model := range models(t) {
		for _, mode := range []struct {
			name string
			opts map[string]any
		}{
			{"seed=42, temp=1.0", options(nil, "seed", 42, "temperature", 1.0, "num_predict", 60)},
			{"seed=42, temp=0", options(nil, "seed", 42, "temperature", 0.0, "num_predict", 60)},
			{"без seed, temp=1.0", options(nil, "temperature", 1.0, "num_predict", 60)},
			{"без seed, temp=0", options(nil, "temperature", 0.0, "num_predict", 60)},
		} {
			var answers []string
			for attempt := 1; attempt <= repeats(); attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				ans, _, st, err := ask(ctx, c, model, p.prompt, mode.opts, &no)
				cancel()
				if err != nil {
					t.Logf("%s / %s: ошибка %v", model, mode.name, err)
					continue
				}
				answers = append(answers, ans)
				res.add(run{Model: model, Param: "seed", Value: mode.name, Options: mode.opts,
					Probe: p.name, Attempt: attempt, Answer: ans,
					EvalTokens: st.EvalCount, TokPerSec: st.TokensPerSecond(), DoneReason: st.DoneReason})
			}
			same := 0
			for _, a := range answers {
				if len(answers) > 0 && norm(a) == norm(answers[0]) {
					same++
				}
			}
			t.Logf("%s · %-20s · совпало с первым: %d из %d · различных: %.0f%%",
				model, mode.name, same, len(answers), uniqueRatio(answers)*100)
		}
		unload(t, c, model)
	}
	res.save(t, "seed.json")
}

// TestNumPredictStopAndThinking проверяет три вещи, которые видны не по тексту
// ответа, а по служебным полям: обрезку по num_predict, обрыв по stop и цену
// рассуждений.
func TestNumPredictStopAndThinking(t *testing.T) {
	c := client(t)
	res := newResults(c)
	yes, no := true, false
	p := probeByName("рассказ")

	for _, model := range models(t) {
		for _, tc := range []struct {
			name  string
			opts  map[string]any
			think *bool
		}{
			{"num_predict=20", options(nil, "num_predict", 20), &no},
			{"num_predict=200", options(nil, "num_predict", 200), &no},
			{"num_predict=-1", options(nil, "num_predict", -1), &no},
			// Слово, которое в ответе про Go встретится наверняка: смысл проверки
			// в том, что генерация обрывается на нём, а не в том, повезёт ли.
			{"stop=[\"канал\"]", options(nil, "num_predict", 400, "stop", []string{"канал", "Канал"}), &no},
			{"think=true, num_predict=200", options(nil, "num_predict", 200), &yes},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			ans, think, st, err := ask(ctx, c, model, p.prompt, tc.opts, tc.think)
			cancel()
			if err != nil {
				t.Logf("%s / %s: ошибка %v", model, tc.name, err)
				continue
			}
			res.add(run{Model: model, Param: "num_predict/stop/think", Value: tc.name, Options: tc.opts,
				Probe: p.name, Attempt: 1, Answer: ans, Thinking: think,
				PromptTokens: st.PromptEvalCount, EvalTokens: st.EvalCount,
				TokPerSec: st.TokensPerSecond(), DoneReason: st.DoneReason})
			t.Logf("%s · %-28s · токенов %4d · рассуждений %5d симв · причина конца: %-8s · ответ: %s",
				model, tc.name, st.EvalCount, think, st.DoneReason, shorten(ans, 60))
		}
		unload(t, c, model)
	}
	res.save(t, "predict_stop_think.json")
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// TestBuiltinParameters снимает, какие параметры зашиты в каждую модель.
//
// Это ответ на вопрос «у всех ли моделей одинаковый набор»: набор параметров
// один на весь сервер, а вот значения по умолчанию модели несут свои.
func TestBuiltinParameters(t *testing.T) {
	c := client(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	for _, model := range models(t) {
		show, err := c.Show(ctx, model)
		if err != nil {
			t.Logf("%s: %v", model, err)
			continue
		}
		params := map[string]string{}
		for _, line := range strings.Split(show.Parameters, "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				params[f[0]] = strings.Join(f[1:], " ")
			}
		}
		names := make([]string, 0, len(params))
		for k := range params {
			names = append(names, k)
		}
		sort.Strings(names)
		var sb strings.Builder
		for _, k := range names {
			sb.WriteString(k + "=" + params[k] + " ")
		}
		t.Logf("%-18s · возможности: %v · зашито: %s", model, show.Capabilities, strings.TrimSpace(sb.String()))
	}
}
