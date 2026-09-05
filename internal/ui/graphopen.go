package ui

import (
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Во что обошлось открытие графа.
//
// **Зачем это видеть.** Обе величины — время и память — растут вместе
// с библиотекой, и однажды упрутся: замер 29.08.2026 на четверти библиотеки
// дал 11.5 с и 160 МБ удержанных при пике процесса 1.03 ГБ, а на всей библиотеке
// ожидается 45–60 с и пик 4–5 ГБ (оценка).
// Порог, за которым стоит смотреть в сторону встраиваемой базы, назван
// в HowGraphBuildRuns.md — но заметить его можно, только если числа
// видны каждый день, а не когда кто-то соберётся их померить.
//
// **Цветом, а не текстом.** Строка появляется при каждом открытии графа,
// и читать её каждый раз никто не станет. Цвет замечается боковым зрением:
// пока песочный тусклый — всё как обычно, оранжевый — стало заметно дольше,
// красный — пора что-то делать.
//
// **Показывается только когда граф есть.** Нет графа — нет и открытия,
// и говорить не о чем.

// GraphOpenNote — строка о том, во что обошлось открытие графа.
//
// Экспортирована ради команд командной строки: они открывают граф чаще всего,
// и цвет порогов должен быть тем же самым, а не переписанным заново.
//
// Пустая строка означает «показывать нечего»: граф не открывали (создали
// пустым) или замер не снят.
func GraphOpenNote(st graph.OpenStats, cfg *config.Graph) string {
	return maint.OpenNote(st, cfg)
}

// graphMemoryStatus — значок для строки состояния: сколько памяти держит
// открытый граф.
//
// Показывается, **только когда граф действительно открыт** и не выключено
// настройкой graph.show_memory. Закрытый или отсутствующий граф памяти
// не занимает, и писать «GR: 0» значило бы сообщать о том, чего нет.
//
// Единица выбирается сама: мегабайты, пока их сотни, гигабайты дальше.
// Порог у нынешней библиотеки — 160 МБ, у полной ожидается под гигабайт,
// и переключение не должно требовать правки настроек.
func (m *Model) graphMemoryStatus() string {
	if !m.cfg.Graph.ShowMemory || m.gr.open == nil {
		return ""
	}
	st := m.gr.open.Opened()
	if st.Heap == 0 {
		return ""
	}
	return "GR: " + humanBytes(st.Heap)
}

// humanSeconds — время открытия словами человека: до минуты в секундах
// с десятой долей, дальше в минутах.
func humanSeconds(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1f с", d.Seconds())
	}
	return fmt.Sprintf("%d мин %d с", int(d.Minutes()), int(d.Seconds())%60)
}

// humanBytes — занятая память в мегабайтах или гигабайтах.
func humanBytes(n uint64) string {
	const mb = 1 << 20
	switch {
	case n == 0:
		return "меньше мегабайта"
	case n < mb:
		return fmt.Sprintf("%d КБ", n/1024)
	case n < 1024*mb:
		return fmt.Sprintf("%d МБ", n/mb)
	default:
		return fmt.Sprintf("%.2f ГБ", float64(n)/float64(1024*mb))
	}
}

// ── Полоса хода открытия графа ───────────────────────────────────────────────

// genWarm — поколение прогрева графа при запуске. Отрицательное, чтобы никогда
// не совпасть с поколением вопроса: прогрев идёт сам по себе, и вопрос,
// заданный в это же время, не должен принять его сообщения за свои.
const genWarm = -1

// graphProgressMsg — очередное сообщение о ходе открытия графа.
// ok == false означает, что канал закрыт: открытие кончилось, полосу убираем.
type graphProgressMsg struct {
	gen int
	p   graph.OpenProgress
	ok  bool
	ch  <-chan graph.OpenProgress // чтобы подписаться на следующее сообщение
}

// waitGraphProgress ждёт следующее сообщение о ходе. Одно сообщение — одна
// команда, как и у событий агента: цикл Bubble Tea не терпит долгих ожиданий
// внутри себя, зато прекрасно справляется с потоком коротких сообщений.
func waitGraphProgress(gen int, ch <-chan graph.OpenProgress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		return graphProgressMsg{gen: gen, p: p, ok: ok, ch: ch}
	}
}

// graphBarText рисует полосу хода для строки ввода.
//
// Полоса стоит именно там, куда человек смотрит, нажав Enter. Крутилка в
// строке состояния этого не решает: взгляд в момент отправки вопроса лежит
// на поле ввода, а не на нижней кромке экрана.
//
// Без знаменателя (шаг, у которого байты не считаются) полоса не рисуется:
// пустая рамка, которая никуда не движется, выглядит хуже честной строки
// «идёт такой-то шаг».
func graphBarText(p graph.OpenProgress, elapsed time.Duration, width int) string {
	stage := p.Stage
	if stage == "" {
		stage = "открываю граф"
	}
	secs := fmt.Sprintf("%d с", int(elapsed.Seconds()))
	if p.Total <= 0 || p.Done <= 0 {
		return fmt.Sprintf("открываю граф · %s · %s", stage, secs)
	}

	frac := float64(p.Done) / float64(p.Total)
	if frac > 1 {
		frac = 1
	}
	// Ширина полосы — то, что осталось от строки после подписей; в узком окне
	// полосы не будет вовсе, останутся проценты.
	label := fmt.Sprintf("открываю граф · %s  %3.0f%% · %s ", stage, frac*100, secs)
	bar := width - lipgloss.Width(label) - 2
	if bar < 8 {
		return label
	}
	if bar > 40 {
		bar = 40
	}
	full := int(frac * float64(bar))
	return label + "[" + strings.Repeat("█", full) + strings.Repeat("░", bar-full) + "]"
}
