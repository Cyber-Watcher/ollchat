package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Защита окна подтверждения от упреждающего набора.
//
// Поле ввода живёт и во время генерации: человек набирает следующий вопрос,
// пока модель отвечает. Окно подтверждения появляется посреди набора и
// забирает клавиатуру себе — и следующая же буква становилась ответом:
// «е» из слова «привет» разрешала bash до конца сеанса, «н» и Enter
// означали «да» (аудит 07.10.2026, находка 4). Раскладка ответов остаётся
// прежней — «н» стоит на клавише Y, и отвечать, не переключая раскладку,
// удобно, — поэтому защита держится не на выборе клавиш, а на времени.
//
// **Правило.** Окно принимает ответ, только когда клавиатура помолчала
// confirmQuiet. Отсчёт идёт от появления окна и начинается заново с каждым
// отброшенным нажатием: пока человек печатает, окно не слушает. Нажатие,
// которое окну ничего не говорит (буква не из ответов, вставка текста),
// взводит защиту снова: значит, человек набирает вопрос и окна не заметил,
// и буква «е» из его слова не должна разрешить инструмент.
//
// Пока защита взведена, в окне вместо строки клавиш стоит объяснение:
// нажатие без видимого следствия читается как поломка.

// confirmQuiet — сколько клавиатура должна помолчать, прежде чем окно начнёт
// принимать ответы. При наборе промежутки между нажатиями — десятые доли
// секунды, а человеку, который увидел окно и решил ответить, лишние полсекунды
// незаметны.
const confirmQuiet = 600 * time.Millisecond

// confirmQuietHint — строка окна, пока ответы не принимаются.
const confirmQuietHint = "… ответ примется после полсекунды без нажатий: набранное раньше не засчитывается"

// confirmQuietMsg — пора проверить, выдержана ли тишина. seq отличает
// последний взвод защиты от прежних: устаревшие сообщения отбрасываются.
type confirmQuietMsg struct{ seq int }

// now — часы интерфейса. В тестах подменяются: проверять защиту настоящими
// паузами значило бы засыпать в каждом тесте на полсекунды.
func (m *Model) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// armConfirm взводит защиту или начинает отсчёт тишины заново.
func (m *Model) armConfirm() tea.Cmd {
	was := m.confirmArmed
	m.confirmArmed = true
	m.confirmQuietAt = m.now()
	m.confirmSeq++
	if !was {
		// Строка клавиш сменилась подсказкой: высота окна могла измениться.
		m.syncHeights()
	}
	return confirmQuietTick(confirmQuiet, m.confirmSeq)
}

// disarmConfirm снимает защиту: окно слушает клавиши.
func (m *Model) disarmConfirm() {
	if !m.confirmArmed {
		return
	}
	m.confirmArmed = false
	m.syncHeights()
}

// confirmQuietTick назначает проверку тишины.
func confirmQuietTick(after time.Duration, seq int) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg { return confirmQuietMsg{seq: seq} })
}

// confirmTyping сообщает, что нажатие пришло, пока клавиатура не помолчала.
//
// Тишина могла выдержаться раньше, чем дошло сообщение таймера (цикл событий
// был занят), — тогда защита снимается прямо здесь, и ответ принимается.
func (m *Model) confirmTyping() bool {
	if !m.confirmArmed {
		return false
	}
	if m.now().Sub(m.confirmQuietAt) < confirmQuiet {
		return true
	}
	m.disarmConfirm()
	return false
}

// onConfirmQuiet снимает защиту, если тишина выдержана; иначе ждёт остаток.
func (m *Model) onConfirmQuiet(msg confirmQuietMsg) tea.Cmd {
	if m.confirm == nil || !m.confirmArmed || msg.seq != m.confirmSeq {
		return nil
	}
	if left := confirmQuiet - m.now().Sub(m.confirmQuietAt); left > 0 {
		return confirmQuietTick(left, msg.seq)
	}
	m.disarmConfirm()
	return nil
}
