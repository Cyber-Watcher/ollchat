//go:build live

package live

import (
	"context"
	"testing"
	"time"
)

// Замер взаимодействия параметров между собой.
//
// В основных сетках (params_test.go) параметры меняются по одному — это
// правильный способ понять, что делает каждый параметр сам по себе, но он
// не отвечает на вопрос «а если их скомбинировать». Здесь — четыре
// содержательные комбинации, выбранные так, чтобы проверить конкретные
// утверждения, уже сделанные в статье по итогам одиночных сеток:
//
//  1. temperature=0 обещает железную повторяемость — переживёт ли это
//     обещание высокий repeat_penalty, который специально понижает шансы
//     уже сказанных слов и тем самым может сдвинуть лидера?
//  2. min_p назван в статье «современной заменой top_p, которая лучше
//     держит связность на высоких температурах» — заявление до сих пор не
//     проверено: одиночный замер min_p шёл при обычной температуре, а замер
//     высокой температуры — со всеми отсекателями снятыми разом. Проверяем
//     прицельно: температура 3.0, top_k и top_p сняты, min_p один на страже.
//  3. repeat_penalty и frequency_penalty порознь примерно удваивали длину
//     ответа — что будет, если наказывать одновременно и по недавности,
//     и по частоте?
//  4. Правило пайплайна гласит: отсекатели решают раньше temperature, но
//     это про то, что дают отсекателям выбирать. О частоте выбора внутри
//     оставшегося пула речи не было. Проверяем: высокая температура вместе
//     с узким (не нулевым) top_k — получится ли разнообразие без риска
//     распада, показанного при полностью снятых отсекателях.

// combo — одна комбинация параметров для замера взаимодействия.
type combo struct {
	name   string
	base   map[string]any
	probes []string
}

var combos = []combo{
	{
		name:   "temp=0 + repeat_penalty=1.0 (контроль)",
		base:   map[string]any{"temperature": 0.0, "repeat_penalty": 1.0, "num_predict": defaultNumPredict},
		probes: []string{"факт", "выдумка"},
	},
	{
		name:   "temp=0 + repeat_penalty=1.5",
		base:   map[string]any{"temperature": 0.0, "repeat_penalty": 1.5, "num_predict": defaultNumPredict},
		probes: []string{"факт", "выдумка"},
	},
	{
		name:   "temp=3.0, все отсекатели сняты (контроль — уже известно)",
		base:   map[string]any{"temperature": 3.0, "top_k": 0, "top_p": 1.0, "min_p": 0.0, "num_predict": defaultNumPredict},
		probes: []string{"факт", "арифметика", "json"},
	},
	{
		name:   "temp=3.0, top_k/top_p сняты, min_p=0.1 на страже",
		base:   map[string]any{"temperature": 3.0, "top_k": 0, "top_p": 1.0, "min_p": 0.1, "num_predict": defaultNumPredict},
		probes: []string{"факт", "арифметика", "json"},
	},
	{
		name:   "repeat_penalty=1.0 + frequency_penalty=0 (контроль)",
		base:   map[string]any{"repeat_penalty": 1.0, "frequency_penalty": 0.0, "num_predict": 800},
		probes: []string{"перечень"},
	},
	{
		name:   "repeat_penalty=1.5 + frequency_penalty=1.5",
		base:   map[string]any{"repeat_penalty": 1.5, "frequency_penalty": 1.5, "num_predict": 800},
		probes: []string{"перечень"},
	},
	{
		name:   "temp=1.5 + top_k=100 (контроль)",
		base:   map[string]any{"temperature": 1.5, "top_k": 100, "num_predict": defaultNumPredict},
		probes: []string{"факт", "арифметика", "выдумка"},
	},
	{
		name:   "temp=1.5 + top_k=10 (узкий, не нулевой)",
		base:   map[string]any{"temperature": 1.5, "top_k": 10, "num_predict": defaultNumPredict},
		probes: []string{"факт", "арифметика", "выдумка"},
	},
}

// TestParamInteractions гоняет комбинации на всех моделях и печатает точность,
// разнообразие и точные совпадения между повторами — последнее нужно именно
// для проверки повторяемости под repeat_penalty.
func TestParamInteractions(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false

	for _, model := range models(t) {
		t.Logf("═══ модель %s ═══", model)
		for _, cb := range combos {
			for _, pname := range cb.probes {
				p := probeByName(pname)
				opts := options(cb.base)

				var answers []string
				okCount := 0
				var sumRepeat float64
				for attempt := 1; attempt <= repeats(); attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					ans, _, st, err := ask(ctx, c, model, p.prompt, opts, &no)
					cancel()

					r := run{
						Model: model, Param: "combo", Value: cb.name, Options: opts,
						Probe: pname, Attempt: attempt, Answer: ans,
						EvalTokens: st.EvalCount, TokPerSec: st.TokensPerSecond(),
						DoneReason: st.DoneReason,
					}
					if err != nil {
						r.Err = err.Error()
					} else {
						answers = append(answers, ans)
						if p.check != nil && p.check(ans) {
							r.OK = true
							okCount++
						}
						sumRepeat += repeatRatio(ans)
					}
					res.add(r)
				}

				exact := 0
				for _, a := range answers {
					if len(answers) > 0 && a == answers[0] {
						exact++
					}
				}
				line := "%-52s · %-11s ·"
				args := []any{cb.name, pname}
				if p.check != nil {
					line += " верно %d/%d ·"
					args = append(args, okCount, repeats())
				}
				line += " точных совпадений %d/%d · различных %.0f%%"
				args = append(args, exact, len(answers), uniqueRatio(answers)*100)
				t.Logf(line, args...)
			}
		}
		unload(t, c, model)
	}
	res.save(t, "interactions.json")
}
