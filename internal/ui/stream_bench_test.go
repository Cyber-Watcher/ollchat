package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// benchAnswer — ответ модели размером около 4 КБ markdown: заголовки, список,
// таблица и блок кода. После glamour он занимает десятки килобайт — ровно
// то, что лента держит на каждый обмен длинного сеанса.
func benchAnswer(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Ответ номер %d\n\n", n)
	b.WriteString("Горутины и каналы — **основа** конкурентности в Go. Ниже разбор по шагам, " +
		"с примером и таблицей сравнения, как это обычно и выглядит в ответе модели.\n\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "%d. Пункт списка с `кодом` и пояснением, достаточно длинным, чтобы перенестись на вторую строку ленты.\n", i)
	}
	b.WriteString("\n| Приём | Когда | Цена |\n|---|---|---|\n")
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "| мьютекс %d | общий счётчик | дёшево |\n", i)
	}
	b.WriteString("\n```go\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "\tch <- worker(ctx, %d) // строка примера\n", i)
	}
	b.WriteString("```\n\n")
	for i := 0; i < 6; i++ {
		b.WriteString("Абзац пояснения: канал передаёт владение данными, а не делит их, " +
			"поэтому гонок меньше, а рассуждать о программе проще.\n\n")
	}
	return b.String()
}

// benchHistory собирает ленту из exchanges готовых обменов с markdown.
func benchHistory(b *testing.B, exchanges int) *Model {
	b.Helper()
	m := newTestModelWith(b, func(cfg *config.Config) {
		cfg.General.RenderMarkdown = true
		cfg.Theme.Style = "dark"
	})
	// Блоки кладутся напрямую и отрисовываются разом: через addBlock сборка
	// ленты в пятьсот обменов сама стоила бы минуты, а мерить надо не её.
	for i := 0; i < exchanges; i++ {
		m.blocks = append(m.blocks,
			block{kind: blockUser, text: fmt.Sprintf("вопрос номер %d про горутины", i)},
			block{kind: blockAssistant, text: benchAnswer(i)})
	}
	m.rerenderAll()
	return m
}

// benchmarkStreamChunk меряет один кусок потока ответа поверх длинной ленты.
//
// Замер 07.10.2026 (Xeon 2.1 ГГц, ответ 4.5 КБ markdown → 60 КБ после glamour):
// пока каждый кусок пересобирал всю ленту через viewport.SetContent, он стоил
// 17.7 мс при 50 обменах и 180 мс при 500 — интерфейс упирался в процессор
// уже к середине рабочего дня. С окном feedView — 0.2 мс в обоих случаях:
// цена куска больше не зависит от длины истории.
func benchmarkStreamChunk(b *testing.B, exchanges int) {
	m := benchHistory(b, exchanges)
	m.streaming = true
	m.liveIdx, m.thinkIdx = -1, -1
	chunk := agent.Event{Kind: agent.EventContent, Text: "слово "}
	m.handleAgentEvent(chunk)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Живой блок не растёт бесконечно: ответ модели — килобайты, а не
		// мегабайты, и замер должен быть про ленту, а не про один ответ.
		if i%500 == 499 {
			lb := m.blocks[m.liveIdx]
			lb.text = "слово "
			m.blocks[m.liveIdx] = lb
		}
		m.handleAgentEvent(chunk)
	}
}

func BenchmarkStreamChunk50(b *testing.B)  { benchmarkStreamChunk(b, 50) }
func BenchmarkStreamChunk500(b *testing.B) { benchmarkStreamChunk(b, 500) }
