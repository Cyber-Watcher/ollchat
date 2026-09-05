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

	m.runCommand("/context add 32k")

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

	m.runCommand("/context add 32k")
	m.runCommand("/context add 32768")

	if got, _ := m.server.NumCtx(); got != 98304 {
		t.Errorf("num_ctx = %d, ожидалось 98304", got)
	}
}

// Выше максимума модели поднимать бессмысленно — упираемся в него и говорим об этом.
func TestAddContextClampsToModelMaximum(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(131072))
	m.modelMaxCtx = 262144

	m.runCommand("/context add 256k")

	if got, _ := m.server.NumCtx(); got != 262144 {
		t.Errorf("num_ctx = %d, ожидалось ограничение максимумом 262144", got)
	}
	// Об ограничении сообщается подсказкой — тем же видом блока, каким лента
	// показывает предупреждения: человек просил 256k сверху, а получил потолок,
	// и заметить это он должен сразу, а не вычитать в общем сообщении.
	var warned bool
	for _, b := range m.blocks {
		if b.kind == blockHint && strings.Contains(b.text, "максимум") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("об ограничении надо предупредить подсказкой:\n%s", m.blocks[len(m.blocks)-1].text)
	}
}

func TestAddContextAtMaximumDoesNothing(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(262144))
	m.modelMaxCtx = 262144

	m.runCommand("/context add 32k")

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

	m.runCommand("/context add 8k")

	if got, _ := m.server.NumCtx(); got != 16384 {
		t.Errorf("num_ctx = %d, ожидалось 16384", got)
	}
}

func TestAddContextRejectsGarbage(t *testing.T) {
	for _, arg := range []string{"", "много", "-5", "0", "32kb"} {
		m := newTestModelWith(t, withNumCtx(32768))
		m.runCommand("/context add " + arg)

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

// /context set ставит окно, а не прибавляет к нему.
func TestContextSetReplacesWindow(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(32768))
	m.modelMaxCtx = 262144

	m.runCommand("/context set 128k")

	if got, _ := m.server.NumCtx(); got != 131072 {
		t.Errorf("num_ctx = %d, ожидалось 131072 (поставить, а не прибавить)", got)
	}
}

// Русская «к» работает наравне с латинской: раскладку ради одной буквы
// переключать не хочется.
func TestContextSetAcceptsRussianK(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(8192))
	m.modelMaxCtx = 262144

	m.runCommand("/context set 64к")

	if got, _ := m.server.NumCtx(); got != 65536 {
		t.Errorf("num_ctx = %d, ожидалось 65536 при вводе с русской «к»", got)
	}
}

// /context set выше максимума модели упирается в максимум и предупреждает.
func TestContextSetClampsToModelMaximum(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(8192))
	m.modelMaxCtx = 40960

	m.runCommand("/context set 256k")

	if got, _ := m.server.NumCtx(); got != 40960 {
		t.Errorf("num_ctx = %d, ожидался максимум модели 40960", got)
	}
	var warned bool
	for _, b := range m.blocks {
		if b.kind == blockHint && strings.Contains(b.text, "40960") {
			warned = true
		}
	}
	if !warned {
		t.Error("надо предупредить, что окно ограничено максимумом модели")
	}
}

// /context max ставит наибольшее окно модели.
func TestContextMaxSetsModelMaximum(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(8192))
	m.modelMaxCtx = 40960

	m.runCommand("/context max")

	if got, _ := m.server.NumCtx(); got != 40960 {
		t.Errorf("num_ctx = %d, ожидалось 40960", got)
	}
}

// Максимум неизвестен — честный отказ, а не тихая установка чего попало.
func TestContextMaxWithoutKnownMaximum(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(8192))
	m.modelMaxCtx = 0

	m.runCommand("/context max")

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Errorf("ожидалась ошибка, получено %v: %s", last.kind, last.text)
	}
	if got, _ := m.server.NumCtx(); got != 8192 {
		t.Errorf("окно не должно было измениться, стало %d", got)
	}
}

// Без доводов команда показывает сведения и ничего не меняет.
func TestContextWithoutArgsReports(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(8192))
	before := len(m.blocks)

	m.runCommand("/context")

	if len(m.blocks) != before+1 {
		t.Fatalf("ожидался один блок сведений, добавлено %d", len(m.blocks)-before)
	}
	if got, _ := m.server.NumCtx(); got != 8192 {
		t.Errorf("сведения не должны менять окно, стало %d", got)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "Контекстное окно") {
		t.Errorf("это не отчёт об окне:\n%s", m.blocks[len(m.blocks)-1].text)
	}
}

// Неизвестная подкоманда объясняет, что бывает, а не молчит.
func TestContextUnknownSubcommand(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(8192))

	m.runCommand("/context вверх")

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "/context set") {
		t.Errorf("ожидалось объяснение подкоманд, получено:\n%s", last.text)
	}
}

// Советы при запуске выключаются настройкой.
//
// Проверка нужна ради машины, где конфиг сокращён намеренно: там совет
// «допишите kb_search в agent.tools» предлагает вернуть убранное, и человек
// читает его при каждом запуске.
func TestStartupHintsCanBeTurnedOff(t *testing.T) {
	on := newTestModelWith(t, func(c *config.Config) { c.General.StartupHints = "on" })
	off := newTestModelWith(t, func(c *config.Config) { c.General.StartupHints = "off" })

	count := func(m *Model) int {
		n := 0
		for _, b := range m.blocks {
			if b.kind == blockHint {
				n++
			}
		}
		return n
	}
	if count(off) != 0 {
		t.Errorf("при startup_hints = off подсказок быть не должно, их %d", count(off))
	}
	if count(on) < count(off) {
		t.Errorf("при on подсказок не меньше, чем при off: %d против %d", count(on), count(off))
	}
}
