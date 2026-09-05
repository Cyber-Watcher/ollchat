package ui

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Долгая фоновая задача.
//
// До этого в приложении была ровно одна длительная работа — ход генерации,
// и вся её обвязка (поколения, канал событий, отмена, спиннер, Esc) была
// написана под неё одну. Индексация книг устроена так же, но живёт своей
// жизнью: чат во время неё работает, потому что ресурсы разные — индексация
// жмёт диск и процессор, а чат ждёт сеть.
//
// Схема повторяет ход генерации намеренно, вплоть до имён: поколение отбрасывает
// сообщения брошенной задачи, канал дренируется, чтобы горутина не залипла
// на отправке, а Esc отменяет контекст.

// kbJob — идущая индексация.
type kbJob struct {
	gen      int
	title    string
	cancel   context.CancelFunc
	events   <-chan kb.Progress
	last     kb.Progress
	blockIdx int
	started  time.Time
}

// jobProgressMsg — очередное сообщение о ходе работы.
type jobProgressMsg struct {
	gen int
	p   kb.Progress
}

// jobDoneMsg — работа завершилась.
type jobDoneMsg struct {
	gen int
	p   kb.Progress
	err error
}

// waitForJob ждёт следующего сообщения задачи. Точная копия схемы waitForEvent:
// команда перевыдаётся после каждого сообщения, пока канал не закроется.
func waitForJob(gen int, ch <-chan kb.Progress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return jobDoneMsg{gen: gen}
		}
		if p.Done || p.Err != nil {
			return jobDoneMsg{gen: gen, p: p, err: p.Err}
		}
		return jobProgressMsg{gen: gen, p: p}
	}
}

// startJob запускает работу в отдельной горутине и заводит блок хода в ленте.
func (m *Model) startJob(title string, run func(ctx context.Context, report func(kb.Progress)) error) tea.Cmd {
	if m.job != nil {
		p := m.job.last
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"уже идёт: %s (%d из %d) — остановить можно клавишей Esc или командой /kb stop",
			m.job.title, p.DocsDone, p.DocsTotal)})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan kb.Progress, 16)
	m.gen.job++
	gen := m.gen.job

	idx := m.addBlock(block{kind: blockNotice, text: title + ": подготовка…"})
	m.job = &kbJob{gen: gen, title: title, cancel: cancel, events: events,
		blockIdx: idx, started: time.Now()}

	go func() {
		defer close(events)
		err := run(ctx, func(p kb.Progress) {
			select {
			case events <- p:
			case <-ctx.Done():
			}
		})
		if err != nil {
			select {
			case events <- kb.Progress{Done: true, Err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return tea.Batch(waitForJob(gen, events), m.spin.Tick)
}

// stopJob прерывает работу. Канал дренируется, иначе горутина индексации
// залипнет на отправке очередного сообщения — та же причина, что и у хода
// генерации.
func (m *Model) stopJob(reason string) {
	if m.job == nil {
		return
	}
	job := m.job
	if job.cancel != nil {
		job.cancel()
	}
	go func(ch <-chan kb.Progress) {
		for range ch {
		}
	}(job.events)

	m.gen.job++
	m.job = nil
	m.updateBlock(job.blockIdx, block{kind: blockNotice,
		text: fmt.Sprintf("%s — %s (за %s)", job.title, reason, since(job.started))})
}

// handleJobProgress обновляет блок хода на месте: лента не растёт, сколько бы
// сообщений ни пришло.
func (m *Model) handleJobProgress(msg jobProgressMsg) tea.Cmd {
	if m.job == nil || msg.gen != m.gen.job {
		return nil
	}
	m.job.last = msg.p
	m.updateBlock(m.job.blockIdx, block{kind: blockNotice, text: jobLine(m.job.title, msg.p)})
	return waitForJob(m.gen.job, m.job.events)
}

// handleJobDone закрывает задачу.
func (m *Model) handleJobDone(msg jobDoneMsg) {
	if m.job == nil || msg.gen != m.gen.job {
		return
	}
	job := m.job
	m.job = nil
	m.gen.job++

	switch {
	case msg.err != nil:
		m.updateBlock(job.blockIdx, block{kind: blockError,
			text: fmt.Sprintf("%s — сбой: %s", job.title, msg.err.Error())})
	case msg.p.Canceled:
		m.updateBlock(job.blockIdx, block{kind: blockNotice,
			text: fmt.Sprintf("%s — остановлено (за %s)", job.title, since(job.started))})
	default:
		p := msg.p
		if p.DocsDone == 0 && job.last.DocsDone > 0 {
			p = job.last
		}
		m.updateBlock(job.blockIdx, block{kind: blockNotice, text: jobResult(job, p)})
	}
	if m.kb.coll != nil {
		m.kb.coll = nil // сведения о коллекции устарели, перечитаем при надобности
	}
	// После индексации кусков стало больше, а граф считает по их числу:
	// открытый экземпляр держит прежнее и откажется от новой привязки.
	m.closeGraph()
}

// jobLine — строка хода работы.
func jobLine(title string, p kb.Progress) string {
	var b strings.Builder
	b.WriteString(title)
	if p.Phase != "" {
		fmt.Fprintf(&b, " · %s", p.Phase)
	}
	if p.DocsTotal > 0 {
		fmt.Fprintf(&b, " %d/%d", p.DocsDone, p.DocsTotal)
	}
	if p.Chunks > 0 {
		fmt.Fprintf(&b, " · кусков %d", p.Chunks)
	}
	if p.Current != "" {
		fmt.Fprintf(&b, "\n  %s", textx.ShortenMiddle(p.Current, 60))
	}
	return b.String()
}

// jobResult — итоговая строка.
func jobResult(job *kbJob, p kb.Progress) string {
	var parts []string
	if p.Added > 0 {
		parts = append(parts, fmt.Sprintf("добавлено книг: %d", p.Added))
	}
	if p.Chunks > 0 {
		parts = append(parts, fmt.Sprintf("кусков: %d", p.Chunks))
	}
	// Повторы называются отдельной строкой, а не числом в общем ряду:
	// человек должен увидеть, какая именно книга не попала в коллекцию
	// и почему, иначе он будет искать её в выдаче.
	if len(p.Duplicates) > 0 {
		parts = append(parts, fmt.Sprintf("пропущено повторов: %d", len(p.Duplicates)))
	}
	if p.Scans > 0 {
		parts = append(parts, fmt.Sprintf("сканов без текста: %d", p.Scans))
	}
	if p.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("пропущено: %d", p.Skipped))
	}
	if p.Errors > 0 {
		parts = append(parts, fmt.Sprintf("сбоев: %d", p.Errors))
	}
	if len(parts) == 0 {
		parts = append(parts, "новых книг не нашлось")
	}
	return fmt.Sprintf("%s — готово за %s\n  %s", job.title, since(job.started), strings.Join(parts, ", "))
}

func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0f с", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%d мин %d с", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
}

// jobStatus — сегмент строки состояния, пока задача идёт.
func (m *Model) jobStatus() string {
	if m.job == nil {
		return ""
	}
	p := m.job.last
	if p.DocsTotal > 0 {
		return fmt.Sprintf("%s %d/%d · Esc — остановить", p.Phase, p.DocsDone, p.DocsTotal)
	}
	if p.Phase != "" {
		return p.Phase + " · Esc — остановить"
	}
	return "индексация · Esc — остановить"
}
