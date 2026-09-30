package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Сервер общий: одну модель разные клиенты перезагружают с разными окнами,
// и /api/ps сразу после нашего ответа показывает окно чужого запроса.
// Индикатор обязан держать окно, с которым уйдёт НАШ следующий запрос
// (этап 115).

// psSays — уточнение ёмкости, каким его шлёт refreshCapacityCmd.
func psSays(m *Model, window int) {
	m.Update(modelInfoMsg{gen: m.gen.srv, model: m.modelName,
		capacity: window, source: ctxmeter.SourcePS})
}

// noticesAbout считает строки ленты, где названы оба окна.
func noticesAbout(m *Model, foreign, own string) int {
	n := 0
	for _, b := range m.blocks {
		if strings.Contains(b.text, foreign) && strings.Contains(b.text, own) &&
			strings.Contains(b.text, "сервер") {
			n++
		}
	}
	return n
}

func TestForeignServerWindowDoesNotReplaceSessionWindow(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(65536))
	m.modelMaxCtx = 262144
	m.runCommand("/context set 256k")

	psSays(m, 32768)

	if m.meter.Capacity != 262144 {
		t.Errorf("ёмкость индикатора = %d, ожидалось окно сеанса 262144", m.meter.Capacity)
	}
	if got, _ := m.server.NumCtx(); got != 262144 {
		t.Errorf("num_ctx запроса = %d, ожидалось 262144", got)
	}
}

func TestForeignServerWindowDoesNotReplaceConfigWindow(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(65536))
	m.modelMaxCtx = 262144

	psSays(m, 32768)

	if m.meter.Capacity != 65536 {
		t.Errorf("ёмкость индикатора = %d, ожидалось окно из конфига 65536", m.meter.Capacity)
	}
	if m.meter.Source != ctxmeter.SourceConfig {
		t.Errorf("источник ёмкости = %v, ожидался конфиг", m.meter.Source)
	}
}

// Без num_ctx своё окно неизвестно, и /api/ps — единственный честный источник.
func TestServerWindowStillRefinesWhenNumCtxUnset(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) { cfg.Servers[0].Options = nil })
	if _, ok := m.server.NumCtx(); ok {
		t.Fatal("подготовка: num_ctx не должен быть задан")
	}
	before := len(m.blocks)

	psSays(m, 8192)

	if m.meter.Capacity != 8192 || m.meter.Source != ctxmeter.SourcePS {
		t.Errorf("ёмкость = %d (источник %v), ожидалось 8192 из /api/ps",
			m.meter.Capacity, m.meter.Source)
	}
	if len(m.blocks) != before {
		t.Errorf("без num_ctx расхождения нет, а в ленте появилась строка: %q",
			m.blocks[len(m.blocks)-1].text)
	}
}

// О чужом окне говорим один раз на значение: иначе строка повторялась бы
// после каждого ответа.
func TestForeignServerWindowIsNamedOncePerValue(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(262144))
	m.modelMaxCtx = 262144

	psSays(m, 32768)
	psSays(m, 32768)
	if got := noticesAbout(m, "32768", "262144"); got != 1 {
		t.Fatalf("строк о чужом окне 32768: %d, ожидалась одна", got)
	}

	psSays(m, 262144) // модель снова стоит с нашим окном
	psSays(m, 32768)  // и снова перезагружена чужим запросом
	if got := noticesAbout(m, "32768", "262144"); got != 2 {
		t.Errorf("после возврата к своему окну новое расхождение не названо: строк %d, ожидалось 2", got)
	}

	psSays(m, 65536)
	if got := noticesAbout(m, "65536", "262144"); got != 1 {
		t.Errorf("другое чужое окно (65536) не названо: строк %d", got)
	}
}

// Совпадение с запрошенным — не событие.
func TestMatchingServerWindowIsSilent(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(65536))
	m.modelMaxCtx = 262144
	before := len(m.blocks)

	psSays(m, 65536)

	if len(m.blocks) != before {
		t.Errorf("окна совпали, а в ленте появилась строка: %q", m.blocks[len(m.blocks)-1].text)
	}
	if m.meter.Capacity != 65536 {
		t.Errorf("ёмкость = %d, ожидалось 65536", m.meter.Capacity)
	}
}

// Запрошено больше, чем умеет модель: сервер сам урезает окно до максимума,
// и это его честный ответ, а не чужая перезагрузка.
func TestServerClampToModelMaximumIsNotForeign(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(500000))
	m.modelMaxCtx = 262144
	before := len(m.blocks)

	psSays(m, 262144)

	if m.meter.Capacity != 262144 {
		t.Errorf("ёмкость = %d, ожидался максимум модели 262144", m.meter.Capacity)
	}
	if len(m.blocks) != before {
		t.Errorf("урезание до максимума принято за чужое окно: %q", m.blocks[len(m.blocks)-1].text)
	}
}

// Следствие подмены: заполнение считалось от чужих 32k, и в агентном режиме
// вопрос не уходил вовсе, хотя в своём окне места было достаточно.
func TestForeignServerWindowDoesNotBlockSending(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) {
		withNumCtx(262144)(cfg)
		cfg.Agent.Enabled = true
		cfg.Agent.CompactAt = 0.88
		cfg.Agent.CompactKeep = 6
	})
	m.modelMaxCtx = 262144
	m.modelCaps = []string{"completion", "tools"}
	m.modelRealTools = true
	if !m.toolsSupported() {
		t.Fatal("подготовка: нужен агентный режим с инструментами")
	}
	for i := 0; i < 12; i++ {
		m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})
	}
	m.meter.Observe(172000, 1000) // 173k из 262k — 66 %, ниже порога

	psSays(m, 32768)

	if _, wait := m.compactBeforeSend("следующий вопрос"); wait {
		t.Errorf("вопрос отклонён: заполнение посчитано от чужого окна (ёмкость %d)", m.meter.Capacity)
	}
}

// Если индикатор всё же разошёлся с окном сеанса, /context set с тем же
// числом обязан его выровнять, а не только ответить «уже равно».
func TestContextSetSameValueRealignsMeter(t *testing.T) {
	m := newTestModelWith(t, withNumCtx(262144))
	m.modelMaxCtx = 262144
	m.meter.Capacity, m.meter.Source = 32768, ctxmeter.SourcePS // как было до правки

	m.runCommand("/context set 256k")

	if m.meter.Capacity != 262144 {
		t.Errorf("ёмкость индикатора = %d, ожидалось 262144", m.meter.Capacity)
	}
	if last := m.blocks[len(m.blocks)-1].text; !strings.Contains(last, "уже равно") {
		t.Errorf("ожидалось сообщение «уже равно», получено: %q", last)
	}
}
