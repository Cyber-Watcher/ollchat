package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Плановый архив коллекции с графом.
//
// Рабочий граф — недели работы видеокарты, и портится он молча. Лучше старый
// рабочий граф, чем никакой, поэтому снимок делается сам, пока ollchat
// запущен: раз в graph.archive_every, в фоне, чат при этом работает. Ядро —
// internal/graph/archive.go; здесь только расписание и значок.
//
// **Как решается «пора».** Раз в archiveCheckEvery смотрится каждая коллекция
// с графом: новейший её архив старше срока — и с ней никто не работает
// (сборка, векторы, темы, индексация — см. graph.Busy) — значит, пора.
// Занятая коллекция пропускается молча: сборка идёт часами, и сообщение
// «архив отложен» каждые пять минут приучило бы не читать ленту. Время суток
// не задаётся: с ollchat, который запускают не каждый день, «в три ночи»
// значило бы «никогда».
//
// **Значок.** Пока архив идёт, в строке состояния горит ⛁ с именем коллекции
// и секундами — это единственное место, где видно, что диск занят не зря.
// Итог — заметкой в ленте, но не посреди ответа модели: держится до конца
// хода, как и подсказки о здоровье графа.

const (
	// archiveFirstCheck — первая проверка после запуска: не сразу, чтобы
	// запуск не упирался в диск вместе с прогревом графа.
	archiveFirstCheck = 30 * time.Second
	// archiveCheckEvery — как часто перепроверять. Проверка — несколько
	// stat, повторять её дёшево; пять минут — предел опоздания архива.
	archiveCheckEvery = 5 * time.Minute
	// archiveIcon — значок архива в строке состояния: стопка дисков, одна
	// колонка ширины (замер lipgloss.Width 04.09.2026).
	archiveIcon = "⛁"
)

// archiveJob — идущий архив.
type archiveJob struct {
	coll    string
	manual  bool // по команде, а не по расписанию: об отказе говорим
	started time.Time
}

// archiveTickMsg — пора проверить, не пора ли.
type archiveTickMsg struct{}

// archiveDoneMsg — архив кончился.
type archiveDoneMsg struct {
	coll   string
	manual bool
	res    graph.ArchiveResult
	err    error
}

// archiveTickCmd назначает следующую проверку.
func archiveTickCmd(after time.Duration) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg { return archiveTickMsg{} })
}

// onArchiveTick запускает архив, если пора, и назначает следующую проверку.
func (m *Model) onArchiveTick() tea.Cmd {
	next := archiveTickCmd(archiveCheckEvery)
	if cmd := m.dueArchiveCmd(time.Now()); cmd != nil {
		return tea.Batch(cmd, next)
	}
	return next
}

// dueArchiveCmd — запуск планового архива; nil — не пора или нельзя.
func (m *Model) dueArchiveCmd(now time.Time) tea.Cmd {
	if m.archive != nil || m.kb.base == nil {
		return nil
	}
	every, err := m.cfg.Graph.ArchiveEveryDuration()
	if err != nil || every == 0 {
		return nil
	}
	coll := dueCollection(m.kb.base, m.cfg.Graph.ArchiveDirPath(), every, now)
	if coll == "" {
		return nil
	}
	return m.startArchive(coll, false)
}

// dueCollection — первая коллекция с графом, чей архив старше срока
// и с которой никто не работает. Пусто — такой нет.
func dueCollection(base *kb.Base, dir string, every time.Duration, now time.Time) string {
	names, err := base.Names()
	if err != nil {
		return ""
	}
	for _, n := range names {
		cd := base.CollectionDir(n)
		if !graph.HasAnyGraph(cd) {
			continue
		}
		if last, ok := graph.LastArchive(dir, n); ok && now.Sub(last.Time) < every {
			continue
		}
		if graph.Busy(cd) != "" {
			continue
		}
		return n
	}
	return ""
}

// startArchive снимает архив коллекции в фоне.
func (m *Model) startArchive(coll string, manual bool) tea.Cmd {
	dir := m.kb.base.CollectionDir(coll)
	o := graph.ArchiveOpts{Dir: m.cfg.Graph.ArchiveDirPath(), Keep: m.cfg.Graph.ArchiveKeep}
	m.archive = &archiveJob{coll: coll, manual: manual, started: time.Now()}
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		res, err := graph.Archive(dir, o)
		return archiveDoneMsg{coll: coll, manual: manual, res: res, err: err}
	})
}

// onArchiveDone показывает итог.
func (m *Model) onArchiveDone(msg archiveDoneMsg) {
	m.archive = nil
	switch {
	case msg.err == nil:
		m.holdNote(block{kind: blockNotice, text: archiveIcon + " " + gmaint.ArchiveSummary(msg.coll, msg.res)})
	case msg.manual:
		m.fail("/graph archive", msg.err)
	case errors.Is(msg.err, graph.ErrBusy):
		// Плановый архив под работой — не беда, а ожидание: следующая
		// проверка снимет его, когда коллекция освободится.
	default:
		text := "плановый архив коллекции " + msg.coll + " не снялся: " + msg.err.Error() +
			"\n  проверьте каталог graph.archive_dir; руками — /graph archive " + msg.coll
		if text != m.archiveErrShown {
			m.archiveErrShown = text
			m.holdNote(block{kind: blockHint, text: text})
		}
	}
}

// archiveStatus — сегмент строки состояния; пусто — архив не идёт.
func (m *Model) archiveStatus(now time.Time) string {
	if m.archive == nil {
		return ""
	}
	return fmt.Sprintf("%s архив %s %ds", archiveIcon, m.archive.coll,
		int(now.Sub(m.archive.started).Seconds()))
}

// holdNote кладёт заметку в ленту — или придерживает до конца ответа
// модели: в середину ответа не влезаем, человек читает.
func (m *Model) holdNote(b block) {
	if m.streaming {
		m.heldNotes = append(m.heldNotes, b)
		return
	}
	m.addBlock(b)
}

// flushHeldNotes выкладывает придержанное после хода: заметки об архиве
// и подсказку о здоровье графа, которая пришла посреди ответа.
func (m *Model) flushHeldNotes() {
	for _, b := range m.heldNotes {
		m.addBlock(b)
	}
	m.heldNotes = nil
	if m.healthWaiting {
		m.healthWaiting = false
		if text := healthHintText(m.healthAdvice, m.kb.use); text != "" && text != m.healthShown {
			m.addBlock(block{kind: blockHint, text: text})
			m.healthShown = text
		}
	}
}

// graphArchiveCmd — /graph archive [коллекция]: архив руками, в фоне.
func (m *Model) graphArchiveCmd(rest string) tea.Cmd {
	name, err := m.resolveKBName(rest)
	if err != nil {
		m.fail("/graph archive", err)
		return nil
	}
	if m.archive != nil {
		m.addBlock(block{kind: blockError, text: "уже идёт архив коллекции " + m.archive.coll})
		return nil
	}
	if b := graph.Busy(m.kb.base.CollectionDir(name)); b != "" {
		m.addBlock(block{kind: blockError, text: "с коллекцией " + name + " идёт работа: " + b +
			"\n  архив снимается только со спокойной коллекции — повторите, когда работа кончится"})
		return nil
	}
	m.addBlock(block{kind: blockNotice, text: archiveIcon + " снимаю архив коллекции " + name +
		" в " + m.cfg.Graph.ArchiveDirPath() + " — до минуты, чат работает"})
	return m.startArchive(name, true)
}

// graphArchivesCmd — /graph archives [коллекция]: что есть в каталоге архивов.
func (m *Model) graphArchivesCmd(rest string) tea.Cmd {
	name := strings.TrimSpace(rest)
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(gmaint.ArchivesList(m.cfg, name), "\n") +
		"\n  восстановить: ollchat --graph-restore <файл> (закрыв ollchat и ollmcp)"})
	return nil
}
