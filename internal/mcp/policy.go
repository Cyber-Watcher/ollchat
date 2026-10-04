package mcp

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Политика службы (этап 109): пределы числовых параметров, потолок ответа
// и общий срок вызова. Задаются файлом настроек службы (ollmcp.toml), а не
// кодом инструментов: инструменты те же, что у модели внутри ollchat, и её
// поведение этим не задевается.
//
// Пределы соблюдаются здесь, до вызова, и каждая поправка видна в ответе:
// «top_k = 50 больше предела службы 20 — взято 20». Молчаливая подмена
// (как делали инструменты сами) прятала от модели, что её поправили.

// Limit — допустимые значения одного числового параметра.
type Limit struct{ Min, Max int }

// Policy — то, что служба соблюдает поверх инструментов. Нули — «не задано».
type Policy struct {
	// Limits: инструмент → параметр → предел.
	Limits map[string]map[string]Limit
	// OutputBytes — потолок текста ответа; 0 — без потолка.
	OutputBytes int
	// CallTimeout — общий срок вызова; 0 — без срока.
	CallTimeout time.Duration
}

// limitOf — предел параметра, если он задан.
func (p Policy) limitOf(tool, param string) (Limit, bool) {
	l, ok := p.Limits[tool][param]
	return l, ok
}

var rangeRe = regexp.MustCompile(`\d+\.\.\d+`)

// describe переписывает в описании параметра диапазон на действующий: модель
// должна видеть те числа, которые служба соблюдает, а не те, что вшиты в текст.
func describe(desc string, l Limit) string {
	r := fmt.Sprintf("%d..%d", l.Min, l.Max)
	if rangeRe.MatchString(desc) {
		return rangeRe.ReplaceAllString(desc, r)
	}
	return strings.TrimSpace(desc + " (" + r + ")")
}

// adjust приводит аргументы к пределам и возвращает пометки о каждой поправке.
func (p Policy) adjust(tool string, args map[string]any) []string {
	var notes []string
	for param, l := range p.Limits[tool] {
		v, ok := args[param]
		if !ok || v == nil {
			continue
		}
		n, ok := asInt(v)
		switch {
		case !ok:
			delete(args, param)
			notes = append(notes, fmt.Sprintf("«%s»: ждали целое число, получено %v — взято умолчание", param, v))
		case n < l.Min:
			delete(args, param)
			notes = append(notes, fmt.Sprintf("«%s» = %d меньше %d — взято умолчание", param, n, l.Min))
		case n > l.Max:
			args[param] = float64(l.Max)
			notes = append(notes, fmt.Sprintf("«%s» = %d больше предела службы %d — взято %d", param, n, l.Max, l.Max))
		}
	}
	return notes
}

// asInt — целое ли это число в любом из видов, в каких его шлют клиенты.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

// finish дописывает пометки и обрезает ответ по потолку.
func (p Policy) finish(text string, notes []string) string {
	if len(notes) > 0 {
		text += "\n\n[служба: " + strings.Join(notes, "; ") + "]"
	}
	return tools.Truncate(text, p.OutputBytes)
}

// run выполняет инструмент в общем сроке. Истёк срок — ответ «не успел»,
// а сам инструмент получает отмену через контекст.
func (p Policy) run(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
	if p.CallTimeout <= 0 {
		return fn(ctx)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, p.CallTimeout)
	defer cancel()
	type out struct {
		text string
		err  error
	}
	done := make(chan out, 1)
	go func() {
		t, err := fn(ctx)
		done <- out{t, err}
	}()
	select {
	case r := <-done:
		return r.text, r.err
	case <-ctx.Done():
		if parent.Err() != nil {
			return "", parent.Err() // клиент ушёл сам — срок тут ни при чём
		}
		return "", fmt.Errorf("служба не успела за %s (call_timeout в ollmcp.toml). "+
			"Если служба только что запущена, граф понятий ещё прогревается — "+
			"повторите вызов через минуту", p.CallTimeout)
	}
}
