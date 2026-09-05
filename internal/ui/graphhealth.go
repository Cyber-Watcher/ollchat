package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// graphHealthMsg — итог фоновой проверки состояния графа и смыслов.
type graphHealthMsg struct {
	advice []string
}

// healthEvery — как часто перепроверять.
//
// Граф и смыслы портятся не только между запусками: сборка идёт часами, книги
// доливаются посреди работы, и состояние меняется под руками. Проверка стоит
// килобайта чтения, поэтому повторять её дёшево, а десять минут — тот срок,
// за который человек успевает что-то доделать, но ещё не успевает забыть.
const healthEvery = 10 * time.Minute

// checkGraphHealthCmd считает состояние в фоне.
//
// **Почему в горутине, а не при сборке модели.** Проверка читает несколько
// файлов и открывает коллекцию — доли секунды, но запуск обязан быть мгновенным,
// а к этим долям добавится ещё и первое чтение каталога с диска, который может
// оказаться сетевым или спящим. Интерфейс не должен ждать ничего.
func (m *Model) checkGraphHealthCmd() tea.Cmd {
	base, use := m.kb.base, m.kb.use
	if base == nil || use == "" {
		return nil
	}
	return func() tea.Msg {
		return graphHealthMsg{advice: healthAdvice(base, use, m.cfg.Graph.Name)}
	}
}

// nextGraphHealthCmd назначает следующую проверку.
func nextGraphHealthCmd() tea.Cmd {
	return tea.Tick(healthEvery, func(time.Time) tea.Msg { return healthTickMsg{} })
}

// healthTickMsg — пора проверить снова.
type healthTickMsg struct{}

// graphHealthHint — что сказать при запуске о состоянии графа и смыслов.
//
// **Зачем при запуске.** Всё, что здесь проверяется, портится молча: ошибок нет,
// просто ответы тихо становятся хуже. Понятия, добавленные после последнего счёта
// векторов, находятся лишь точным написанием; куски долитых книг ищутся одними
// словами; понятия, появившиеся после разметки тем, не попадают в обзор вовсе.
// Замер 02.09.2026: 101 тысяча понятий из 161 оказалась вне тем, и обзор
// несколько дней работал по трети графа, ни разу об этом не сказав.
//
// **Цена — килобайт чтения.** Граф не открывается (это 25 секунд и четверть
// гигабайта), читаются только паспорта: graph.meta, entities.vecmeta и начало
// файла тем. Запуск от этого не замедляется.
//
// Пусто — всё в порядке. Предупреждение, которое горит всегда, перестают читать.
func healthAdvice(base *kb.Base, use, graphName string) []string {
	coll, err := base.Open(use)
	if err != nil {
		return nil
	}
	h, ok := graph.QuickHealth(coll.Dir(), graphName, coll.ChunkCount())
	if !ok {
		return nil // графа нет — он не обязателен, и советовать тут нечего
	}
	// Векторы самих кусков коллекции: их состояние знает коллекция, а не граф.
	adv := h.Advice(use)
	// Пары, ждущие человека (связывание при сборке и ночной разбор двойников):
	// по файлу журнала, без открытия графа.
	if n := graph.LinkQueueSize((graph.Rules{Name: graphName}).Dir(coll.Dir())); n > 0 {
		adv = append(adv, fmt.Sprintf("в очереди разбора %d %s — /graph review",
			n, plural(n, "пара", "пары", "пар")))
	}
	if st := coll.Stats(); st.Vectors > 0 && st.Vectors < st.Chunks {
		n := st.Chunks - st.Vectors
		adv = append(adv, fmt.Sprintf(
			"у %d %s книг нет смыслов — ищутся только по словам: ollchat --kb-embed %s",
			n, plural(n, "куска", "кусков", "кусков"), use))
	}
	return adv
}

// healthHintText — то же советами, но одним блоком для ленты.
func healthHintText(adv []string, use string) string {
	if len(adv) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("состояние базы знаний — граф и смыслы стоят часов карты, " +
		"и портятся они молча:")
	for _, a := range adv {
		b.WriteString("\n  · " + a)
	}
	b.WriteString("\nподробно и с советами: ollchat --graph-doctor " + use)
	return b.String()
}
