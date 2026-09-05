package ui

import (
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/pdfout"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Сохранение видимого на экране ответа в PDF: F4 — только ответ,
// Shift+F4 — вместе с вопросом и шапкой, /savetopdf — то же самое,
// но имя файла задано сразу в команде.
//
// Выбор «какой ответ считать видимым» целиком переиспользован из copyanswer.go:
// это ровно тот же вопрос, что у копирования в буфер обмена, и второй его
// реализации быть не должно.

// pdfPayload — отобранный ответ вместе с тем, что нужно для шапки документа.
//
// Это снимок на момент нажатия клавиши. Пока пользователь набирает имя файла,
// ответ может дописываться дальше, но сохранить он просил именно то,
// что видел на экране.
type pdfPayload struct {
	markdown string
	question string
	model    string
	at       time.Time
	live     bool // ответ ещё не закончен
	noUser   bool // вопрос к этому ответу в ленте не сохранился
}

// visibleAnswerPayload собирает видимый на экране ответ для PDF.
//
// Повторяет цепочку из copyVisibleAnswer, но отдаёт сырой markdown:
// обёртка в формат журнала там нужна буферу обмена, а у документа
// своя шапка, и разметку она бы только испортила.
func (m *Model) visibleAnswerPayload() (pdfPayload, bool) {
	spans := blockSpans(m.blocks, m.rendered)
	idx := visibleAnswer(spans, m.vp.YOffset(), m.vp.Height())
	if idx < 0 {
		return pdfPayload{}, false
	}

	turn := turnAround(m.blocks, idx)
	text := answerText(m.blocks, turn)
	if strings.TrimSpace(text) == "" {
		return pdfPayload{}, false
	}

	p := pdfPayload{
		markdown: text,
		model:    m.blocks[idx].model,
		at:       stampOf(m.blocks[idx]),
		live:     m.streaming && m.liveIdx >= 0,
	}
	if turn.user >= 0 {
		p.question = m.blocks[turn.user].text
	} else {
		p.noUser = true
	}
	return p, true
}

// options собирает настройки набора документа.
func (p pdfPayload) options(withHeader bool) pdfout.Options {
	return pdfout.Options{
		WithHeader: withHeader,
		Meta: pdfout.Meta{
			Title:    pdfTitle(p.question),
			Question: p.question,
			Model:    p.model,
			At:       p.at,
		},
	}
}

// pdfTitle делает заголовок документа из вопроса.
//
// Вопрос бывает на несколько абзацев, а в заголовок годится одна строка.
func pdfTitle(question string) string {
	line := strings.TrimSpace(question)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "Ответ модели"
	}
	const maxTitle = 80
	r := []rune(line)
	if len(r) > maxTitle {
		return string(r[:maxTitle]) + "…"
	}
	return line
}

// ── Разрешение пути ──────────────────────────────────────────────────────────

// pdfTarget — разобранный и проверенный путь для сохранения.
type pdfTarget struct {
	abs    string
	rel    string // как показать пользователю
	exists bool   // файл уже есть
}

// addPDFExt дописывает расширение, если его не указали.
func addPDFExt(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".pdf") {
		return name
	}
	return name + ".pdf"
}

// resolvePDFPath разбирает имя файла по правилам команды.
//
// Голое имя без разделителей кладётся в корень песочницы — то есть в каталог,
// из которого запущен ollchat. Указанный путь берётся как есть, включая
// абсолютный за пределами песочницы: имя вводит человек, а не модель, и запрет
// здесь не по адресу. Проверить путь всё равно надо — каталог должен
// существовать, цель не должна быть каталогом, а правила deny соблюдаются.
func (m *Model) resolvePDFPath(name string) (pdfTarget, error) {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, `"'`)
	if name == "" {
		return pdfTarget{}, errors.New("укажите имя файла, например: /savetopdf ответ.pdf")
	}
	name = addPDFExt(fsx.ExpandHome(name))

	sb := m.guard.Sandbox()
	var abs string
	if !strings.ContainsRune(name, filepath.Separator) && !filepath.IsAbs(name) {
		// Голое имя — через песочницу: она заодно проверит символические
		// ссылки и корректно отработает на ещё не существующем файле.
		resolved, err := sb.Resolve(name)
		if err != nil {
			return pdfTarget{}, err
		}
		abs = resolved
	} else if filepath.IsAbs(name) {
		abs = filepath.Clean(name)
	} else {
		abs = filepath.Clean(filepath.Join(sb.Root(), name))
	}

	dir := filepath.Dir(abs)
	info, err := os.Stat(dir)
	if err != nil {
		// Каталоги молча не создаём: пользователь мог просто опечататься
		// в пути, и лишнее дерево каталогов ему точно не нужно.
		return pdfTarget{}, fmt.Errorf("каталог не найден: %s", dir)
	}
	if !info.IsDir() {
		return pdfTarget{}, fmt.Errorf("это не каталог: %s", dir)
	}

	target := pdfTarget{abs: abs, rel: sb.Rel(abs)}
	if st, err := os.Stat(abs); err == nil {
		if st.IsDir() {
			return pdfTarget{}, fmt.Errorf("это каталог, а не файл: %s", abs)
		}
		if !st.Mode().IsRegular() {
			return pdfTarget{}, fmt.Errorf("это не обычный файл: %s", abs)
		}
		target.exists = true
	}

	res := m.guard.Check(permissions.Request{
		Kind: permissions.KindWrite, Target: abs, Tool: "savetopdf",
	})
	if res.Decision == permissions.DecisionDeny {
		return pdfTarget{}, errors.New("запись запрещена: " + res.Reason)
	}
	return target, nil
}

// ── Сохранение ───────────────────────────────────────────────────────────────

// savedPDFMsg — итог сохранения.
type savedPDFMsg struct {
	rel     string
	pages   int
	size    int
	missing []rune
	live    bool
	noUser  bool
	err     error
}

// pdfWriteFile — шов для тестов: подменяя его, тест проверяет, что и куда
// уходит, не трогая диск.
var pdfWriteFile = pdfout.WriteFile

// writePDFCmd набирает документ и пишет файл в отдельной горутине.
//
// Набор сотни страниц — это миллисекунды, но в цикле событий им всё равно
// не место: пока идёт набор, интерфейс обязан отвечать.
func writePDFCmd(p pdfPayload, t pdfTarget, withHeader, overwrite bool) tea.Cmd {
	return func() tea.Msg {
		res, err := pdfWriteFile(t.abs, p.markdown, p.options(withHeader), overwrite)
		msg := savedPDFMsg{rel: t.rel, live: p.live, noUser: p.noUser && withHeader, err: err}
		if res != nil {
			msg.pages, msg.size, msg.missing = res.Pages, len(res.Data), res.Missing
		}
		return msg
	}
}

// handleSavedPDF отчитывается о результате сохранения.
func (m *Model) handleSavedPDF(msg savedPDFMsg) tea.Cmd {
	m.statusMsg = ""
	if msg.err != nil {
		m.addBlock(block{kind: blockError, text: "сохранение в PDF: " + msg.err.Error()})
		return nil
	}

	text := fmt.Sprintf("ответ сохранён: %s — %d %s, %s",
		msg.rel, msg.pages, plural(msg.pages, "страница", "страницы", "страниц"),
		fsx.HumanSize(int64(msg.size)))
	if msg.live {
		text += " · ответ ещё не закончен"
	}
	if msg.noUser {
		text += " · вопрос в ленте не найден"
	}
	if n := len(msg.missing); n > 0 {
		// Молча терять символы нельзя: пользователь должен знать, что
		// в документе на их месте стоят прямоугольники.
		text += fmt.Sprintf("\n  символов без глифа: %d (%s) — заменены на □",
			n, string(msg.missing[:min(n, 8)]))
	}
	m.addBlock(block{kind: blockNotice, text: text})
	_ = m.logger.Write(chatlog.KindSystem, "Ответ сохранён в PDF: "+msg.rel)
	return nil
}

// ── Команда /savetopdf ───────────────────────────────────────────────────────

// saveToPDFCmd сохраняет видимый ответ под именем, заданным прямо в команде.
//
// Окно ввода здесь не нужно — имя уже названо. Команда равносильна F4:
// сохраняется только ответ, без служебной шапки.
func (m *Model) saveToPDFCmd(arg string) tea.Cmd {
	payload, ok := m.visibleAnswerPayload()
	if !ok {
		m.statusMsg = "на экране нет ответа модели"
		return nil
	}

	target, err := m.resolvePDFPath(arg)
	if err != nil {
		m.fail("/savetopdf", err)
		return nil
	}
	if target.exists {
		// Молча затирать чужой файл нельзя, а спрашивать «да/нет» из команды
		// значило бы завести второй модальный путь ради одного случая.
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"файл уже существует: %s — укажите другое имя или сохраните клавишей F4, "+
				"там можно подтвердить перезапись", target.rel)})
		return nil
	}

	m.statusMsg = "сохраняю в PDF…"
	return writePDFCmd(payload, target, false, false)
}

// ── Модальное окно с именем файла ────────────────────────────────────────────

// savePDFPrompt — окно запроса имени файла по F4 и Shift+F4.
//
// Окно забирает себе все клавиши: пока оно открыто, человек набирает имя, а не
// вопрос модели. Ответ уже отобран и лежит в payload — лента под окном может
// дописываться дальше, сохранится всё равно то, что было видно при нажатии.
type savePDFPrompt struct {
	input      textinput.Model
	payload    pdfPayload
	withHeader bool
	err        string
	// overwrite — цель, для которой ждём согласия на перезапись. Подтверждение
	// спрашивается тут же, вторым нажатием Enter: заводить ради этого ещё одно
	// окно поверх окна незачем.
	overwrite *pdfTarget
}

// savePDFHeight — высота панели: рамка, заголовок, поле, строка подсказки.
//
// Высота постоянная, и это не случайность: по ней вычисляется строка курсора,
// а плавающая высота увела бы курсор в чужую строку.
const savePDFHeight = 5

// openSavePDF открывает окно сохранения видимого ответа.
func (m *Model) openSavePDF(withHeader bool) {
	if m.savePDF != nil || m.picker != nil || m.confirm != nil {
		return
	}
	payload, ok := m.visibleAnswerPayload()
	if !ok {
		m.statusMsg = "на экране нет ответа модели"
		return
	}

	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "имя файла.pdf"
	in.CharLimit = 0
	in.SetWidth(savePDFInputWidth(m.width))
	applyInputCursor(&in, m.cfg.Input.Cursor)
	in.SetValue(defaultPDFName(payload.question))
	in.CursorEnd()
	in.Focus()

	m.savePDF = &savePDFPrompt{input: in, payload: payload, withHeader: withHeader}
	m.relayout()
}

// savePDFInputWidth — сколько места остаётся полю имени внутри рамки:
// рамка и отступы съедают 4 колонки, приглашение «› » — ещё 2, и колонку
// оставляем курсору, чтобы он не упирался в рамку.
func savePDFInputWidth(width int) int {
	w := width - 7
	if w < 10 {
		w = 10
	}
	return w
}

// closeSavePDF убирает окно и возвращает ленте её высоту.
func (m *Model) closeSavePDF() {
	m.savePDF = nil
	m.relayout()
}

// defaultPDFName предлагает имя файла по вопросу.
//
// Имя целиком редактируется, поэтому лучше предложить хоть что-то осмысленное,
// чем заставлять набирать его с нуля после каждого ответа.
func defaultPDFName(question string) string {
	title := strings.TrimSpace(question)
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}

	var b strings.Builder
	for _, r := range title {
		switch {
		case r == ' ' || r == '\t':
			b.WriteRune('_')
		case r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r):
			// Разделители пути и запрещённые в именах символы выбрасываем:
			// предложенное имя обязано оставаться именем, а не путём.
		default:
			b.WriteRune(r)
		}
	}

	name := strings.Trim(b.String(), "_.")
	const maxName = 48
	if r := []rune(name); len(r) > maxName {
		name = strings.TrimRight(string(r[:maxName]), "_.")
	}
	if name == "" {
		name = "ответ"
	}
	return name + ".pdf"
}

// handleSavePDFKey обрабатывает клавиши, пока открыто окно сохранения.
func (m *Model) handleSavePDFKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.savePDF
	switch msg.String() {
	case "esc":
		if p.overwrite != nil {
			// Отказ от перезаписи возвращает к правке имени, а не закрывает
			// окно: человек уже набрал имя и хочет его поправить.
			p.overwrite = nil
			return m, nil
		}
		m.closeSavePDF()
		return m, nil

	case "enter":
		return m, m.submitSavePDF()
	}

	// Любая правка имени отменяет уже данное согласие на перезапись: согласие
	// было на конкретный файл, а имя стало другим.
	p.overwrite = nil
	p.err = ""

	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return m, cmd
}

// submitSavePDF разбирает набранное имя и запускает сохранение.
func (m *Model) submitSavePDF() tea.Cmd {
	p := m.savePDF

	if p.overwrite != nil {
		target := *p.overwrite
		payload, withHeader := p.payload, p.withHeader
		m.closeSavePDF()
		m.statusMsg = "сохраняю в PDF…"
		return writePDFCmd(payload, target, withHeader, true)
	}

	target, err := m.resolvePDFPath(p.input.Value())
	if err != nil {
		// Ошибка остаётся в окне: имя под рукой, поправить его — одна клавиша.
		p.err = err.Error()
		return nil
	}
	if target.exists {
		p.overwrite = &target
		p.err = ""
		return nil
	}

	payload, withHeader := p.payload, p.withHeader
	m.closeSavePDF()
	m.statusMsg = "сохраняю в PDF…"
	return writePDFCmd(payload, target, withHeader, false)
}

// savePDFView рисует окно запроса имени файла.
func (m *Model) savePDFView() string {
	p := m.savePDF
	if p == nil {
		return ""
	}

	title := "Сохранить ответ в PDF"
	if p.withHeader {
		title = "Сохранить вопрос и ответ в PDF"
	}

	var hint string
	switch {
	case p.overwrite != nil:
		hint = styStatusWarn.Render("файл уже есть: "+p.overwrite.rel) +
			styPickerHint.Render(" · Enter перезаписать · Esc изменить имя")
	case p.err != "":
		hint = styError.Render(p.err)
	default:
		hint = styPickerHint.Render("каталог: " + m.guard.Sandbox().Root() +
			" · Enter сохранить · Esc отмена")
	}

	// Рамка занимает 2 колонки, отступы стиля — ещё 2.
	inner := m.width - 4
	var b strings.Builder
	b.WriteString(styPickerTitle.Render(title) + "\n")
	b.WriteString(clip(p.input.View(), inner) + "\n")
	b.WriteString(clip(hint, inner))
	return styPickerBox.Width(m.width - 2).Render(b.String())
}

// savePDFCursor переводит курсор поля имени в координаты всего экрана.
func (m *Model) savePDFCursor(top int) *tea.Cursor {
	c := m.savePDF.input.Cursor()
	if c == nil {
		return nil
	}
	c.X += 2       // рамка и отступ стиля
	c.Y += top + 2 // рамка и строка заголовка
	return c
}
