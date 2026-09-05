package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
)

// Копирование видимого ответа в буфер обмена: F5 — ответ, Shift+F5 — вместе
// с вопросом, в том же виде, в каком запись ложится в журнал чата.
//
// Арифметика «строка экрана ↔ блок ленты» вынесена в чистые функции, как это
// сделано в scrollbar.go: проверить её тестами без терминала иначе нельзя,
// а ошибка проявляется тем, что копируется не тот ответ.

// answerGlue разделяет куски одного ответа, разорванного вызовами инструментов.
// Пустая строка нужна, чтобы соседние абзацы markdown не слиплись в один.
const answerGlue = "\n\n"

// maxCopyBytes — предел размера копии. Ответ на несколько мегабайт в буфер
// обмена класть незачем: это почти наверняка выгруженный инструментом файл.
const maxCopyBytes = 4 << 20

// osc52SafeBytes — размер, свыше которого запасной путь через терминал
// становится ненадёжным: tmux и часть терминалов режут длинную
// последовательность OSC 52 молча.
const osc52SafeBytes = 64 << 10

// blockSpan — место одного блока ленты в строках содержимого.
type blockSpan struct {
	idx   int // индекс в m.blocks
	kind  blockKind
	start int // первая строка блока в содержимом ленты
	lines int // сколько строк он занимает
}

// blockSpans раскладывает блоки по строкам ленты.
//
// Обход обязан повторять refreshViewport буква в букву: пустые rendered
// пропускаются (скрытые рассуждения выпадают из ленты целиком), остальные
// склеиваются через "\n\n" — то есть между соседями появляется одна пустая
// строка. Мягкий перенос у viewport выключен, а текст переносит wrap/glamour
// заранее, поэтому строка содержимого — это ровно строка экрана.
func blockSpans(blocks []block, rendered []string) []blockSpan {
	spans := make([]blockSpan, 0, len(blocks))
	line := 0
	first := true
	for i := range blocks {
		if i >= len(rendered) || strings.TrimSpace(rendered[i]) == "" {
			continue
		}
		if !first {
			line++ // пустая строка-разделитель между блоками
		}
		first = false
		n := strings.Count(rendered[i], "\n") + 1
		spans = append(spans, blockSpan{idx: i, kind: blocks[i].kind, start: line, lines: n})
		line += n
	}
	return spans
}

// visibleIn сообщает, сколько строк блока попало в окно [offset, offset+height).
func (s blockSpan) visibleIn(offset, height int) int {
	if height <= 0 {
		return 0
	}
	top, bottom := s.start, s.start+s.lines
	if top < offset {
		top = offset
	}
	if bottom > offset+height {
		bottom = offset + height
	}
	if bottom <= top {
		return 0
	}
	return bottom - top
}

// visibleAnswer выбирает ответ модели, занимающий больше всего видимых строк.
// При равенстве выигрывает нижний: он ближе к тому, что читают сейчас.
// Возвращает -1, если ответов на экране нет.
func visibleAnswer(spans []blockSpan, offset, height int) int {
	best, bestLines := -1, 0
	for _, s := range spans {
		if s.kind != blockAssistant {
			continue
		}
		// Нестрогое сравнение и есть правило «при равной доле берём нижний»:
		// блоки идут сверху вниз, и поздний вытесняет раннего.
		if v := s.visibleIn(offset, height); v > 0 && v >= bestLines {
			best, bestLines = s.idx, v
		}
	}
	return best
}

// lastUserBlock возвращает индекс последнего вопроса пользователя в ленте
// или -1, если вопросов в ней нет.
func lastUserBlock(blocks []block) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].kind == blockUser {
			return i
		}
	}
	return -1
}

// answerTurn — границы одного хода: вопрос и все куски ответа на него.
type answerTurn struct {
	user    int   // блок вопроса, -1 если вопроса в ленте нет
	answers []int // блоки ответа этого хода по порядку
}

// turnAround собирает ход, которому принадлежит блок answerIdx: назад до
// ближайшего вопроса пользователя, вперёд — до следующего вопроса или конца
// ленты. Один ход даёт несколько блоков ответа, если его разорвали вызовы
// инструментов.
func turnAround(blocks []block, answerIdx int) answerTurn {
	t := answerTurn{user: -1}
	if answerIdx < 0 || answerIdx >= len(blocks) {
		return t
	}

	start := 0
	for i := answerIdx; i >= 0; i-- {
		if blocks[i].kind == blockUser {
			t.user = i
			start = i + 1
			break
		}
	}

	for i := start; i < len(blocks); i++ {
		if blocks[i].kind == blockUser {
			break
		}
		if blocks[i].kind == blockAssistant && strings.TrimSpace(blocks[i].text) != "" {
			t.answers = append(t.answers, i)
		}
	}
	return t
}

// answerText склеивает текст ответа хода: все куски по порядку, без рассуждений
// и без вывода инструментов.
func answerText(blocks []block, t answerTurn) string {
	parts := make([]string, 0, len(t.answers))
	for _, i := range t.answers {
		if s := strings.TrimRight(blocks[i].text, " \t\n"); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, answerGlue)
}

// copyPayload — то, что уходит в буфер обмена, и чем об этом отчитаться.
type copyPayload struct {
	text   string
	lines  int
	chars  int
	live   bool // ответ ещё не закончен: копируется то, что успело прийти
	noUser bool // вопрос к этому ответу в ленте не сохранился
}

// copyVisibleAnswer собирает ответ, видимый на экране сейчас.
//
// Второе значение — false, если ответов на экране нет вовсе.
//
// Копируется исходный текст блоков, а не m.rendered: там управляющие
// последовательности оформления и жёсткий перенос по ширине окна — в буфере
// обмена такому тексту делать нечего.
func (m *Model) copyVisibleAnswer(withQuestion bool) (copyPayload, bool) {
	spans := blockSpans(m.blocks, m.rendered)
	idx := visibleAnswer(spans, m.vp.YOffset(), m.vp.Height())
	if idx < 0 {
		return copyPayload{}, false
	}

	turn := turnAround(m.blocks, idx)
	answer := answerText(m.blocks, turn)
	if strings.TrimSpace(answer) == "" {
		return copyPayload{}, false
	}

	p := copyPayload{live: m.streaming && m.liveIdx >= 0}
	if !withQuestion {
		p.text = answer
	} else {
		var sb strings.Builder
		if turn.user >= 0 {
			q := m.blocks[turn.user]
			sb.WriteString(chatlog.FormatEntry(q.turn, stampOf(q), chatlog.KindQuestion, "", q.text))
		} else {
			p.noUser = true
		}
		a := m.blocks[idx]
		sb.WriteString(chatlog.FormatEntry(a.turn, stampOf(a), chatlog.KindAnswer, a.model, answer))
		// Журнал разделяет записи пустыми строками, а в буфере обмена хвост
		// из них не нужен: вставится он ровно в конец вставляемого текста.
		p.text = strings.TrimRight(sb.String(), "\n") + "\n"
	}

	p.lines = strings.Count(strings.TrimRight(p.text, "\n"), "\n") + 1
	p.chars = utf8.RuneCountInString(p.text)
	return p, true
}

// stampOf возвращает отметку времени блока, а у непомеченного — текущее время.
// Непомеченным блок остаётся, например, если ход прервали до его завершения.
func stampOf(b block) time.Time {
	if b.at.IsZero() {
		return time.Now()
	}
	return b.at
}

// ── Запись в буфер обмена ────────────────────────────────────────────────────

// answerCopiedMsg — результат записи ответа внешней утилитой.
type answerCopiedMsg struct {
	payload copyPayload
	err     error
}

// clipboardWrite — шов для тестов. Подменяя его, тест проверяет, что в буфер
// ушёл именно нужный текст, и не трогает настоящий буфер обмена машины.
var clipboardWrite = clipboard.WriteText

// copyAnswerCmd отдаёт текст утилите в отдельной горутине: запускать внешнюю
// программу прямо в цикле событий нельзя.
func copyAnswerCmd(p copyPayload) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(5)
		defer cancel()
		return answerCopiedMsg{payload: p, err: clipboardWrite(ctx, p.text)}
	}
}

// copyAnswer — обработчик F5 (ответ) и Shift+F5 (ответ вместе с вопросом).
func (m *Model) copyAnswer(withQuestion bool) tea.Cmd {
	p, ok := m.copyVisibleAnswer(withQuestion)
	if !ok {
		m.statusMsg = "на экране нет ответа модели"
		return nil
	}
	if len(p.text) > maxCopyBytes {
		m.statusMsg = fmt.Sprintf("ответ слишком велик для буфера обмена: %.1f МБ",
			float64(len(p.text))/(1<<20))
		return nil
	}
	m.statusMsg = "копирую…"
	return copyAnswerCmd(p)
}

// handleAnswerCopied отчитывается о результате и при неудаче внешней утилиты
// уходит на запасной путь — отдаёт текст самому терминалу по OSC 52.
//
// Решение об этом принимается здесь, а не в пакете clipboard: терминал доступен
// только через команду Bubble Tea.
func (m *Model) handleAnswerCopied(msg answerCopiedMsg) tea.Cmd {
	p := msg.payload
	what := "скопировано"
	if p.noUser {
		what = "скопировано без вопроса (его нет в ленте)"
	}

	done := func(prefix string) string {
		s := fmt.Sprintf("%s: %d %s, %d %s", prefix,
			p.lines, plural(p.lines, "строка", "строки", "строк"),
			p.chars, plural(p.chars, "символ", "символа", "символов"))
		if p.live {
			s += " · ответ ещё не закончен"
		}
		return s
	}

	if msg.err == nil {
		m.statusMsg = done(what)
		return nil
	}

	// Утилиты нет вовсе — это ожидаемый случай (работа по ssh, голая система),
	// поэтому не ошибка, а запасной путь с однократной подсказкой.
	var cmds []tea.Cmd
	if errors.Is(msg.err, clipboard.ErrNoWriter) {
		m.statusMsg = done("скопировано через терминал")
		if !m.osc52Noted {
			m.osc52Noted = true
			m.addBlock(block{kind: blockHint, text: msg.err.Error() +
				". Текст отдан терминалу по OSC 52 — так умеют не все терминалы, " +
				"и длинный текст многие обрезают."})
		}
	} else {
		m.statusMsg = msg.err.Error() + " — отдаю терминалу"
	}
	if len(p.text) > osc52SafeBytes {
		m.statusMsg += " · терминал может обрезать"
	}
	cmds = append(cmds, tea.SetClipboard(p.text))
	return tea.Batch(cmds...)
}
