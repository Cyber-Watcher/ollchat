package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Большая вставка сворачивается в метку.
//
// **Зачем.** Вставка ста строк из буфера обмена приходит одним сообщением,
// но поле ввода трёхстрочное: текст в него не помещается, экран дёргается,
// а человек ждёт, пока всё это прорисуется, — и не видит ни начала, ни конца.
// Поэтому в промпте остаётся метка `[Текст01: 4321 знак]`, а сам текст лежит
// рядом и подставляется обратно при отправке.
//
// Устроено по образцу вложенных картинок (`images.go`): метка в тексте —
// **единственный источник правды**. Стёр метку — отменил вставку; метка без
// вставки остаётся обычным текстом, потому что человек мог написать её руками.
//
// **Свёртка — настройка, а не общее решение** (этап 100, 07.09.2026).
// `input.collapse_paste = false` вставляет текст как есть: кому-то важнее видеть
// вставленное целиком, чем держать поле читаемым. Независимо от настройки
// вставка запоминается в `pasteVault`, поэтому F6 разворачивает метку в текст,
// а Shift+F6 сворачивает текст обратно в метку — в обе стороны и в любом режиме.
//
// **Нумерация сквозная на сеанс**, а не своя у каждого вопроса. Так метка
// однозначна, и запись истории («стрелка вверх») находит по ней свой текст
// даже через десяток вопросов. Своя нумерация у вопроса дала бы два разных
// текста под одной меткой — и вопрос, отправленный из истории, ушёл бы
// с чужой вставкой.
//
// **Порог — не число знаков, а место в поле.** Поле ввода низкое (три строки);
// всё, что в него не влезает, сворачивается. Ширину при счёте ограничивает
// `pasteRefWidth`: иначе на широком терминале трёхстрочное поле вмещало бы
// сотни знаков одной простынёй, которой всё равно не видно целиком, и большой
// текст не сворачивался бы — ровно тот дефект, что чинится здесь.

// pastedText — свёрнутая вставка, ждущая отправки.
type pastedText struct {
	num  int
	text string
}

func (p pastedText) label() string {
	return fmt.Sprintf("[Текст%02d: %s]", p.num, charsWord(len([]rune(p.text))))
}

// charsWord склоняет «знак» — метку читает человек, а не программа.
func charsWord(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return fmt.Sprintf("%d знак", n)
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		return fmt.Sprintf("%d знака", n)
	default:
		return fmt.Sprintf("%d знаков", n)
	}
}

// describe — краткое описание для строки состояния.
func (p pastedText) describe() string {
	lines := strings.Count(p.text, "\n") + 1
	return fmt.Sprintf("%s, строк %d", charsWord(len([]rune(p.text))), lines)
}

// pasteFits сообщает, помещается ли вставка в отведённое полю место.
//
// Считается по ячейкам поля, а не по знакам текста: перевод строки занимает
// остаток строки, поэтому сто коротких строк не помещаются в трёхстрочное поле,
// сколько бы знаков в них ни было.
// pasteRefWidth — опорная ширина для оценки вместимости.
//
// Считать по всей ширине широкого терминала неверно: на окне в 200 колонок
// трёхстрочное поле вмещает под 600 знаков, и абзац «влезает» в него одной
// простынёй, которую всё равно не видно целиком. Порог должен зависеть от того,
// что поле низкое, а не от того, что терминал широкий, — поэтому ширина при
// счёте ограничена сверху: то, что не поместилось бы в поле обычной ширины,
// сворачивается на любом терминале.
const pasteRefWidth = 100

func pasteFits(text string, width, height int) bool {
	if width <= 0 || height <= 0 {
		return true // размеров ещё нет — не сворачиваем, пусть решает поле
	}
	if width > pasteRefWidth {
		width = pasteRefWidth
	}
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		n := len([]rune(line))
		rows += n/width + 1
		if rows > height {
			return false
		}
	}
	return rows <= height
}

// takeBigPaste сворачивает вставку в метку, если она не помещается.
//
// Возвращает признак того, что вставка перехвачена: тогда в поле ввода уходит
// метка, а не текст.
func (m *Model) takeBigPaste(msg tea.PasteMsg) bool {
	text := msg.Content
	if text == "" || pasteFits(text, m.ta.Width(), m.ta.Height()) {
		return false
	}

	m.pasteSeq++
	p := pastedText{num: m.pasteSeq, text: text}
	m.rememberPaste(p)

	// Настройка решает только начальное состояние: при false текст идёт в поле
	// обычным путём, но уже известен нам — Shift+F6 свернёт его в метку.
	if !m.cfg.Input.CollapsePaste {
		m.statusMsg = "вставлено: " + p.describe() + " (Shift+F6 свернёт)"
		return false
	}

	m.pastes = append(m.pastes, p)
	m.ta.InsertString(p.label())
	m.statusMsg = "вставлено: " + p.describe() + " (F6 развернёт)"
	return true
}

// rememberPaste кладёт вставку в хранилище сеанса.
//
// Хранилище нужно истории: при отправке `m.pastes` сбрасывается, а вопрос
// в истории остаётся с меткой. Без хранилища стрелка вверх возвращала бы
// мёртвую метку, и повторная отправка ушла бы модели строкой «[Текст01: …]»
// вместо текста (дефект, найденный при разборе этапа 100).
func (m *Model) rememberPaste(p pastedText) {
	if m.pasteVault == nil {
		m.pasteVault = make(map[string]string)
	}
	m.pasteVault[p.label()] = p.text
}

// syncPastes приводит список вставок в соответствие с тем, что стоит в поле.
//
// Метки исчезли — вставки выбрасываются (стёр метку, значит отменил). Метка
// появилась из хранилища — например, вопрос вернулся стрелкой вверх — вставка
// поднимается обратно, иначе отправка ушла бы с мёртвой меткой.
func (m *Model) syncPastes() {
	text := m.ta.Value()

	kept := make([]pastedText, 0, len(m.pastes))
	seen := make(map[string]bool, len(m.pastes))
	for _, p := range m.pastes {
		if strings.Contains(text, p.label()) {
			kept = append(kept, p)
			seen[p.label()] = true
		}
	}
	for label, body := range m.pasteVault {
		if seen[label] || !strings.Contains(text, label) {
			continue
		}
		kept = append(kept, pastedText{num: labelNum(label), text: body})
	}
	m.pastes = kept
}

// labelNum достаёт номер из метки. Номер нужен только порядку в списке,
// поэтому неразобранная метка — не ошибка, а ноль.
func labelNum(label string) int {
	var n int
	if _, err := fmt.Sscanf(label, "[Текст%d:", &n); err != nil {
		return 0
	}
	return n
}

// expandInPrompt разворачивает метки в поле ввода в полный текст (F6).
//
// Возвращает, было ли что разворачивать: строка состояния должна говорить
// «нечего разворачивать», а не молчать, — иначе нажатие выглядит как сбой.
func (m *Model) expandInPrompt() bool {
	text := m.ta.Value()
	out := m.expandPastes(text)
	if out == text {
		return false
	}
	m.setInput(out)
	return true
}

// collapseInPrompt сворачивает известные вставки обратно в метки (Shift+F6).
//
// Ищутся тексты из хранилища сеанса: только их границы нам достоверно
// известны. Кусок, набранный руками, свернуть нельзя — и не нужно.
func (m *Model) collapseInPrompt() bool {
	text := m.ta.Value()
	out := text
	for label, body := range m.pasteVault {
		if body == "" || !strings.Contains(out, body) {
			continue
		}
		out = strings.ReplaceAll(out, body, label)
	}
	if out == text {
		return false
	}
	m.setInput(out)
	return true
}

// expandPastes подставляет свёрнутые вставки обратно перед отправкой.
//
// Подстановка идёт по метке, а не по порядку: человек мог переставить метки
// местами или дописать между ними текст.
func (m *Model) expandPastes(text string) string {
	for _, p := range m.pastes {
		text = strings.ReplaceAll(text, p.label(), p.text)
	}
	return text
}
