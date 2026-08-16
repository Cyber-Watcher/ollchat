// Package ctxmeter вычисляет заполнение контекстного окна модели.
//
// Точных данных о числе токенов до отправки запроса Ollama 0.32.7 не даёт —
// endpoint токенизации отсутствует. Поэтому:
//   - после ответа модели используется точное значение из финального чанка
//     (prompt_eval_count + eval_count);
//   - до отправки — оценка по длине текста, помеченная в интерфейсе знаком «≈».
package ctxmeter

import (
	"fmt"
	"strings"
)

// Источник значения ёмкости окна — от самого достоверного к наименее.
type CapacitySource int

// Источники ёмкости.
const (
	SourceUnknown CapacitySource = iota
	SourcePS                     // /api/ps — фактически загруженное окно
	SourceConfig                 // options.num_ctx из конфига
	SourceShow                   // /api/show → model_info["*.context_length"]
	SourceTags                   // /api/tags → details.context_length
)

// String возвращает название источника для команды /context.
func (s CapacitySource) String() string {
	switch s {
	case SourcePS:
		return "загружено на сервере (/api/ps)"
	case SourceConfig:
		return "num_ctx из конфига"
	case SourceShow:
		return "паспорт модели (/api/show)"
	case SourceTags:
		return "список моделей (/api/tags)"
	default:
		return "неизвестен"
	}
}

// Meter — состояние индикатора контекста.
type Meter struct {
	Capacity int            // размер окна в токенах, 0 — неизвестен
	Source   CapacitySource // откуда взята ёмкость
	Used     int            // точное число занятых токенов из последнего ответа
	Exact    bool           // true, если Used получен от сервера, а не оценён
}

// SetCapacity задаёт ёмкость окна с указанием источника.
func (m *Meter) SetCapacity(n int, src CapacitySource) {
	if n > 0 {
		m.Capacity = n
		m.Source = src
	}
}

// Reset сбрасывает занятость (например, после /clear).
func (m *Meter) Reset() {
	m.Used = 0
	m.Exact = false
}

// Observe принимает точные счётчики из финального чанка ответа.
func (m *Meter) Observe(promptEval, eval int) {
	if promptEval <= 0 && eval <= 0 {
		return
	}
	m.Used = promptEval + eval
	m.Exact = true
}

// Estimate грубо оценивает число токенов в тексте.
//
// Коэффициент 3 байта на токен выбран как консервативная оценка для смеси
// русского и английского текста с кодом: для латиницы токен обычно длиннее,
// для кириллицы в UTF-8 — короче. Оценка намеренно завышает занятость,
// чтобы индикатор не обещал больше свободного места, чем есть.
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 2) / 3
}

// EstimateChars оценивает число токенов по известной длине текста в байтах.
func EstimateChars(n int) int {
	if n <= 0 {
		return 0
	}
	return (n + 2) / 3
}

// AddEstimate добавляет к занятости оценку ещё не отправленного текста.
func (m *Meter) AddEstimate(text string) {
	if text == "" {
		return
	}
	m.Used += Estimate(text)
	m.Exact = false
}

// Percent возвращает заполнение окна в процентах; -1, если ёмкость неизвестна.
func (m Meter) Percent() int {
	if m.Capacity <= 0 {
		return -1
	}
	p := m.Used * 100 / m.Capacity
	if p > 100 {
		p = 100
	}
	return p
}

// Bar рисует полосу заполнения заданной ширины.
func (m Meter) Bar(width int) string {
	if width <= 0 {
		return ""
	}
	p := m.Percent()
	if p < 0 {
		return strings.Repeat("░", width)
	}
	filled := p * width / 100
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// FormatTokens печатает число токенов компактно: 1234 → "1.2k".
func FormatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%dk", n/1000)
	}
}

// String формирует строку индикатора для статус-бара,
// например: "ctx ≈12.4k/32k ███████░░░ 39%".
func (m Meter) String(barWidth int) string {
	if m.Capacity <= 0 {
		if m.Used == 0 {
			return "ctx —"
		}
		return fmt.Sprintf("ctx %s/? ", FormatTokens(m.Used))
	}
	mark := ""
	if !m.Exact {
		mark = "≈"
	}
	return fmt.Sprintf("ctx %s%s/%s %s %d%%",
		mark, FormatTokens(m.Used), FormatTokens(m.Capacity), m.Bar(barWidth), m.Percent())
}
