package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
)

// Окно контекста можно менять посреди сеанса: Ollama берёт num_ctx из каждого
// запроса и при изменении перезагружает модель. Проверяем, что новое значение
// действительно уходит в запрос, а не только рисуется в индикаторе.

func withNumCtx(n int) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Servers[0].Options = map[string]any{"num_ctx": n}
	}
}

func TestAddContextRaisesWindow(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(32768))
	m.modelMaxCtx = 262144

	m.runCommand("/addcontext 32k")

	got, ok := m.server.NumCtx()
	if !ok || got != 65536 {
		t.Fatalf("num_ctx = %d (%v), ожидалось 65536", got, ok)
	}
	if m.meter.Capacity != 65536 {
		t.Errorf("индикатор показывает ёмкость %d, ожидалось 65536", m.meter.Capacity)
	}
	last := m.blocks[len(m.blocks)-1].text
	for _, want := range []string{"32768", "65536", "перезагрузит", "конфиг не записано"} {
		if !strings.Contains(last, want) {
			t.Errorf("в сообщении нет %q:\n%s", want, last)
		}
	}
}

// Прибавка складывается: команду можно давать несколько раз подряд.
func TestAddContextAccumulates(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(32768))
	m.modelMaxCtx = 262144

	m.runCommand("/addcontext 32k")
	m.runCommand("/addcontext 32768")

	if got, _ := m.server.NumCtx(); got != 98304 {
		t.Errorf("num_ctx = %d, ожидалось 98304", got)
	}
}

// Выше максимума модели поднимать бессмысленно — упираемся в него и говорим об этом.
func TestAddContextClampsToModelMaximum(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(131072))
	m.modelMaxCtx = 262144

	m.runCommand("/addcontext 256k")

	if got, _ := m.server.NumCtx(); got != 262144 {
		t.Errorf("num_ctx = %d, ожидалось ограничение максимумом 262144", got)
	}
	if last := m.blocks[len(m.blocks)-1].text; !strings.Contains(last, "максимум") {
		t.Errorf("об ограничении надо сказать:\n%s", last)
	}
}

func TestAddContextAtMaximumDoesNothing(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(262144))
	m.modelMaxCtx = 262144

	m.runCommand("/addcontext 32k")

	if got, _ := m.server.NumCtx(); got != 262144 {
		t.Errorf("num_ctx = %d, значение меняться не должно", got)
	}
	if last := m.blocks[len(m.blocks)-1].text; !strings.Contains(last, "уже равно максимуму") {
		t.Errorf("надо сказать, что расти некуда:\n%s", last)
	}
}

// Без num_ctx в конфиге отталкиваемся от ёмкости, которую сообщил сервер.
func TestAddContextStartsFromServerCapacity(t *testing.T) {
	m := newTestModel(t)
	m.modelMaxCtx = 262144
	m.meter.SetCapacity(8192, ctxmeter.SourcePS)

	m.runCommand("/addcontext 8k")

	if got, _ := m.server.NumCtx(); got != 16384 {
		t.Errorf("num_ctx = %d, ожидалось 16384", got)
	}
}

func TestAddContextRejectsGarbage(t *testing.T) {
	for _, arg := range []string{"", "много", "-5", "0", "32kb"} {
		m := newTestModelWith(t, withNumCtx(32768))
		m.runCommand("/addcontext " + arg)

		if got, _ := m.server.NumCtx(); got != 32768 {
			t.Errorf("аргумент %q не должен менять окно, стало %d", arg, got)
		}
		if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
			t.Errorf("аргумент %q должен давать ошибку, получено: %+v", arg, last)
		}
	}
}

func TestParseTokens(t *testing.T) {
	ok := map[string]int{
		"32768": 32768,
		"32k":   32768,
		"32K":   32768,
		"32к":   32768, // русская «к» — раскладку ради одной буквы не переключаем
		"128k":  131072,
		"1m":    1048576,
		" 64k ": 65536,
	}
	for in, want := range ok {
		got, err := parseTokens(in)
		if err != nil || got != want {
			t.Errorf("parseTokens(%q) = %d, %v — ожидалось %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1", "0", "k", "32kb"} {
		if got, err := parseTokens(in); err == nil {
			t.Errorf("parseTokens(%q) = %d, ожидалась ошибка", in, got)
		}
	}
}
