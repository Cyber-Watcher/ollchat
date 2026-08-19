package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Update обрабатывает события Bubble Tea.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		return m, m.handleResize(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	// Вставка текста средствами терминала (Ctrl+Shift+V, Shift+Insert, средняя
	// кнопка мыши) приходит отдельным сообщением, а не набором нажатий: терминал
	// оборачивает её в «скобочную вставку». Если его не передать полю ввода,
	// вставка молча пропадает — снаружи это выглядит как «буфер не работает».
	case tea.PasteMsg:
		return m, m.handlePaste(msg)

	case tea.MouseMsg:
		return m, m.handleMouse(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.streaming {
			return m, cmd
		}
		return m, nil

	case agentEventMsg:
		if msg.gen != m.runGen {
			return m, nil // событие прерванного хода — молча отбрасываем
		}
		cmd := m.handleAgentEvent(msg.ev)
		if m.events != nil {
			return m, tea.Batch(cmd, waitForEvent(m.runGen, m.events))
		}
		return m, cmd

	case streamClosedMsg:
		if msg.gen != m.runGen {
			return m, nil
		}
		if m.streaming {
			m.finishTurn()
		}
		return m, nil

	case kbPendingMsg:
		m.addBlock(block{kind: blockHint, text: msg.text})
		return m, nil

	case serverInfoMsg:
		if msg.gen != m.srvGen {
			return m, nil // ответ брошенного сервера
		}
		if msg.err != nil {
			m.addBlock(block{kind: blockError, text: "сервер " + m.server.Name + ": " + msg.err.Error()})
			return m, nil
		}
		m.srvVersion = msg.version
		m.models = msg.models
		if m.modelName == "" && len(msg.models) > 0 {
			m.modelName = msg.models[0].Name
		}
		if !m.modelExists(m.modelName) && len(msg.models) > 0 {
			m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
				"модель %q на сервере не найдена — выбрана %s", m.modelName, msg.models[0].Name)})
			m.modelName = msg.models[0].Name
		}
		return m, m.loadModelInfoCmd()

	case modelInfoMsg:
		if msg.gen != m.srvGen || msg.model != m.modelName {
			return m, nil // ответ относится к уже смененному серверу или модели
		}
		if msg.err != nil {
			m.addBlock(block{kind: blockError, text: "сведения о модели: " + msg.err.Error()})
		}
		// Уточнение ёмкости приходит без списка возможностей и без максимума —
		// не затираем ни то, ни другое.
		if msg.caps != nil {
			m.modelCaps = msg.caps
		}
		if msg.maxCtx > 0 {
			m.modelMaxCtx = msg.maxCtx
		}
		if msg.capacity > 0 {
			m.meter.SetCapacity(msg.capacity, msg.source)
		}
		if m.cfg.Agent.Enabled && !hasCap(m.modelCaps, "tools") {
			m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
				"модель %s не поддерживает инструменты — агентный режим для неё отключён", m.modelName)})
		}
		return m, nil

	case modelsMsg:
		if msg.gen != m.srvGen {
			return m, nil // список моделей брошенного сервера
		}
		return m, m.handleModelsRefreshed(msg)

	case jobProgressMsg:
		return m, m.handleJobProgress(msg)

	case jobDoneMsg:
		m.handleJobDone(msg)
		return m, nil

	case calcMsg:
		m.statusMsg = ""
		m.addBlock(block{kind: blockNotice, text: m.calcReport(msg)})
		return m, nil

	case imagePastedMsg:
		m.handleImagePasted(msg)
		return m, nil

	case answerCopiedMsg:
		return m, m.handleAnswerCopied(msg)

	case savedPDFMsg:
		return m, m.handleSavedPDF(msg)

	case noticeMsg:
		m.addBlock(block{kind: blockNotice, text: msg.text})
		return m, nil

	case errorMsg:
		m.addBlock(block{kind: blockError, text: msg.err.Error()})
		return m, nil
	}

	return m, nil
}

// handlePaste отдаёт вставленный текст полю ввода.
//
// Пока открыт список выбора или панель подтверждения, вводить некуда: там ждут
// ответа одной клавишей, и вставка целого абзаца только запутала бы.
func (m *Model) handlePaste(msg tea.PasteMsg) tea.Cmd {
	if m.picker != nil || m.confirm != nil {
		return nil
	}
	// Вставка в окно сохранения — это имя файла, а не текст вопроса: путь
	// удобно приносить из буфера обмена целиком.
	if m.savePDF != nil {
		var cmd tea.Cmd
		m.savePDF.input, cmd = m.savePDF.input.Update(msg)
		return cmd
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.syncFileMenu()
	m.syncPendingImages()
	return cmd
}

// handleMouse обрабатывает колесо и перетаскивание бегунка полосы прокрутки.
//
// Захват бегунка запоминается: пока кнопка не отпущена, лента следует за
// вертикальным положением указателя, даже если он ушёл в сторону от полосы, —
// так ведут себя привычные полосы прокрутки.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.picker != nil || m.confirm != nil || m.savePDF != nil {
		return nil
	}

	// Колесо над списком файлов прокручивает список, а не ленту: указатель
	// стоит на нём, значит и крутят его.
	if wheel, ok := msg.(tea.MouseWheelMsg); ok && m.onFileMenu(wheel.Y) {
		switch wheel.Button {
		case tea.MouseWheelUp:
			m.files.move(-1)
		case tea.MouseWheelDown:
			m.files.move(1)
		}
		return nil
	}

	switch e := msg.(type) {
	case tea.MouseReleaseMsg:
		m.draggingBar = false
		return nil

	case tea.MouseMotionMsg:
		if m.draggingBar {
			m.scrollToRow(e.Y - transcriptTop)
		}
		return nil

	case tea.MouseClickMsg:
		if e.Button == tea.MouseLeft && m.onScrollbar(e.X, e.Y) {
			m.draggingBar = true
			m.scrollToRow(e.Y - transcriptTop)
			return nil
		}
	}

	// Всё остальное (прежде всего колесо) отдаём ленте.
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return cmd
}

// onScrollbar сообщает, попал ли щелчок в колонку полосы прокрутки.
func (m *Model) onScrollbar(x, y int) bool {
	if !m.scrollbarState().visible() {
		return false
	}
	if x != m.scrollbarColumn() {
		return false
	}
	return y >= transcriptTop && y < transcriptTop+m.vp.Height()
}

// scrollToRow ставит ленту так, чтобы бегунок оказался на указанной строке.
func (m *Model) scrollToRow(row int) {
	bar := m.scrollbarState()
	if !bar.visible() {
		return
	}
	m.vp.SetYOffset(bar.offsetForRow(clampInt(row, 0, bar.height-1)))
}

func (m *Model) handleResize(msg tea.WindowSizeMsg) tea.Cmd {
	m.width, m.height = msg.Width, msg.Height
	m.ta.SetWidth(msg.Width - 2)
	if m.savePDF != nil {
		m.savePDF.input.SetWidth(savePDFInputWidth(msg.Width))
	}
	// Крайняя колонка отдана полосе прокрутки, поэтому текст переносим уже.
	m.rend.setWidth(msg.Width - 3)

	vpHeight := m.viewportHeight()
	vpWidth := msg.Width - 1
	if vpWidth < 1 {
		vpWidth = 1
	}
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(vpWidth), viewport.WithHeight(vpHeight))
		m.ready = true
	} else {
		m.vp.SetWidth(vpWidth)
		m.vp.SetHeight(vpHeight)
	}
	m.rerenderAll()
	m.vp.GotoBottom()
	return nil
}

// viewportHeight вычисляет высоту области истории с учётом остальных панелей.
func (m *Model) viewportHeight() int {
	h := m.height
	h -= 1 // шапка
	h -= 1 // разделитель
	h -= 1 // статус-бар
	if m.picker != nil {
		h -= m.picker.height()
	} else if m.savePDF != nil {
		h -= savePDFHeight
	} else {
		h -= m.ta.Height()
		if m.confirm != nil {
			h -= m.confirmHeight()
		}
		if m.files != nil {
			h -= m.files.height()
		}
		h -= m.imagesHeight()
	}
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Выход по второму Ctrl+C.
	//
	// Проверка подтверждения идёт первой и не зависит ни от генерации, ни от
	// открытых панелей: два нажатия обязаны закрыть приложение в любом
	// состоянии. Раньше во время генерации Ctrl+C уходил в ветку прерывания и
	// не доходил до выхода — при зависшем инструменте выйти было нельзя вовсе.
	if key == "ctrl+c" {
		if m.quitConfirm {
			return m, tea.Quit
		}
		m.quitConfirm = true
		if m.streaming {
			m.stopStreaming()
			m.addBlock(block{kind: blockNotice, text: "генерация прервана"})
		}
		m.statusMsg = "нажмите Ctrl+C ещё раз для выхода"
		return m, nil
	}
	m.quitConfirm = false

	// Окно сохранения в PDF перехватывает управление первым: пока в нём
	// набирают имя файла, Esc обязан закрывать окно, а не прерывать генерацию.
	if m.savePDF != nil {
		return m.handleSavePDFKey(msg)
	}
	// Панель подтверждения перехватывает управление.
	if m.confirm != nil {
		return m.handleConfirmKey(key)
	}
	// Список выбора перехватывает управление.
	if m.picker != nil {
		return m.handlePickerKey(msg)
	}
	// Панель вложений перехватывает перемещение и удаление.
	if m.images != nil && m.handleImagePanelKey(key) {
		return m, nil
	}
	// Список файлов перехватывает только перемещение и выбор: Enter при
	// открытом списке вставляет путь, а не отправляет вопрос.
	if m.files != nil {
		if cmd, taken := m.handleFileMenuKey(key); taken {
			return m, cmd
		}
	}

	switch key {
	case "esc":
		if m.streaming {
			m.stopStreaming()
			m.addBlock(block{kind: blockNotice, text: "генерация прервана"})
			return m, nil
		}
		// Вне генерации Esc останавливает индексацию книг: другой длительной
		// работы в приложении нет.
		if m.job != nil {
			m.stopJob("остановлено")
			return m, nil
		}
		return m, nil

	case "enter":
		if m.streaming {
			m.statusMsg = "дождитесь ответа или нажмите Esc"
			return m, nil
		}
		text := m.ta.Value()
		if strings.TrimSpace(text) == "" {
			return m, nil
		}
		m.ta.Reset()
		m.closeFileMenu() // вопрос ушёл — список файлов больше не о чем
		if strings.HasPrefix(strings.TrimSpace(text), "/") {
			return m, m.runCommand(strings.TrimSpace(text))
		}
		return m, m.send(text)

	case "shift+tab":
		mode := m.guard.NextMode()
		m.statusMsg = "режим: " + mode
		return m, nil

	case "ctrl+t":
		m.showThinking = !m.showThinking
		m.rerenderAll()
		if m.showThinking {
			m.statusMsg = "рассуждения показаны"
		} else {
			m.statusMsg = "рассуждения скрыты"
		}
		return m, nil

	case "f2":
		m.setMouse(!m.mouseOn)
		return m, nil

	case "f3":
		m.toggleImagePanel()
		return m, nil

	// Сохранение видимого ответа в PDF: открывается окно запроса имени файла.
	case "f4":
		m.openSavePDF(false)
		return m, nil

	// То же вместе с вопросом и шапкой документа. Как и у Shift+F5, шифт-имён
	// три: «shift+f4» от современных терминалов и старая последовательность
	// CSI 29~, которую разборщик отдаёт клавишей f16 — с модификатором и без.
	case "shift+f4", "shift+f16", "f16":
		m.openSavePDF(true)
		return m, nil

	// Копирование видимого ответа в буфер обмена.
	case "f5":
		return m, m.copyAnswer(false)

	// То же вместе с вопросом. Shift+F5 приходит под тремя разными именами:
	// современные терминалы шлют CSI 15;2~ («shift+f5»), а часть терминалов —
	// старую последовательность CSI 31~, которую разборщик отдаёт отдельной
	// клавишей f17, вовсе без модификатора. Ловим все три, иначе на половине
	// терминалов клавиша молча не работает.
	case "shift+f5", "shift+f17", "f17":
		return m, m.copyAnswer(true)

	// Вставка изображения из буфера обмена. Через терминал картинка прийти не
	// может — её забирает внешняя утилита, поэтому это отдельная команда,
	// а не обычная вставка текста.
	case "ctrl+v":
		m.statusMsg = "читаю буфер обмена…"
		return m, pasteImageCmd()

	case "ctrl+s":
		return m, m.openServerPicker()

	// Выбор модели. По смыслу просился бы Ctrl+M, но он исторически — тот же
	// байт, что и Enter (0x0D): различить их можно только через расширения
	// клавиатуры (modifyOtherKeys, протокол kitty), а терминалы на VTE
	// (gnome-terminal, tilix) их не поддерживают — там Ctrl+M просто отправит
	// вопрос. Поэтому клавиша одна и она работает везде.
	case "ctrl+r":
		m.statusMsg = "обновляю список моделей…"
		return m, m.refreshModelsCmd(modelsOpenPicker, "")

	case "pgup":
		m.vp.PageUp()
		return m, nil
	case "pgdown":
		m.vp.PageDown()
		return m, nil
	case "ctrl+u":
		m.vp.HalfPageUp()
		return m, nil
	case "ctrl+d":
		m.vp.HalfPageDown()
		return m, nil
	case "ctrl+home":
		m.vp.GotoTop()
		return m, nil

	// Быстрый переход в конец ответа. Ctrl+G — короткая клавиша, Ctrl+End —
	// привычная; обе возвращают автопрокрутку во время генерации.
	case "ctrl+g", "ctrl+end":
		m.vp.GotoBottom()
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	// Строка ввода изменилась: пересобираем список файлов под токеном «@…»
	// и проверяем, не стёрли ли метку вложения.
	m.syncFileMenu()
	m.syncPendingImages()
	return m, cmd
}

// handleFileMenuKey обрабатывает клавиши, пока открыт список файлов.
//
// Забираем себе только перемещение и выбор: всё остальное уходит в поле ввода,
// чтобы список фильтровался по мере набора, а не превращался в отдельный режим.
// Второе значение — брать ли клавишу себе.
func (m *Model) handleFileMenuKey(key string) (tea.Cmd, bool) {
	switch key {
	case "up", "ctrl+p":
		m.files.move(-1)
		return nil, true
	case "down", "ctrl+n":
		m.files.move(1)
		return nil, true
	case "enter", "tab":
		m.acceptFileMenu()
		return nil, true
	case "esc":
		m.closeFileMenu()
		return nil, true
	}
	return nil, false
}

// handleConfirmKey обрабатывает ответ на запрос подтверждения.
func (m *Model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	// Русские буквы — те, что находятся на тех же клавишах: пользователь может
	// не переключать раскладку. Плюс «д»/«н» как первые буквы да/нет.
	answer := agent.AnswerNo
	switch key {
	case "y", "Y", "н", "Н", "д", "Д", "enter":
		answer = agent.AnswerYes
	case "a", "A", "ф", "Ф":
		answer = agent.AnswerAlways
	case "t", "T", "е", "Е":
		answer = agent.AnswerAlwaysTool
	case "n", "N", "т", "Т", "esc":
		answer = agent.AnswerNo
	case "up", "k":
		if m.confirmScroll > 0 {
			m.confirmScroll--
		}
		return m, nil
	case "down", "j":
		m.confirmScroll++
		return m, nil
	default:
		return m, nil
	}

	req := m.confirm
	m.confirm = nil
	m.confirmScroll = 0
	if m.ready {
		m.vp.SetHeight(m.viewportHeight())
		m.refreshViewport(true)
	}
	req.Reply <- answer
	return m, nil
}

// handleAgentEvent обновляет ленту по событиям агента.
func (m *Model) handleAgentEvent(ev agent.Event) tea.Cmd {
	switch ev.Kind {

	case agent.EventContent:
		m.turnAnswer.WriteString(ev.Text)
		if m.liveIdx < 0 {
			// Модель проставляем сразу, а не в конце хода: копировать ответ
			// можно и посреди генерации.
			m.liveIdx = m.addBlock(block{kind: blockAssistant, text: ev.Text, model: m.answeredBy})
			// Во время потока markdown не рендерим — текст показывается как есть.
			m.rendered[m.liveIdx] = wrap(ev.Text, m.rend.width)
			m.refreshViewport(true)
		} else {
			b := m.blocks[m.liveIdx]
			b.text += ev.Text
			m.blocks[m.liveIdx] = b
			m.rendered[m.liveIdx] = wrap(b.text, m.rend.width)
			m.refreshViewport(true)
		}
		return nil

	case agent.EventThinking:
		m.turnThink.WriteString(ev.Text)
		if m.thinkIdx < 0 {
			m.thinkIdx = m.addBlock(block{kind: blockThinking, text: ev.Text})
		} else {
			b := m.blocks[m.thinkIdx]
			b.text += ev.Text
			m.updateBlock(m.thinkIdx, b)
		}
		return nil

	case agent.EventToolPlan:
		// Завершаем текущий блок ответа: дальше пойдёт вызов инструмента.
		m.closeLiveBlocks()
		m.addBlock(block{kind: blockTool, title: ev.Tool.Title, status: ""})
		return nil

	case agent.EventToolConfirm:
		m.confirm = ev.Confirm
		m.confirmScroll = 0
		if m.ready {
			m.vp.SetHeight(m.viewportHeight())
			m.refreshViewport(true)
		}
		return nil

	case agent.EventToolResult:
		status := "ok"
		switch {
		case ev.Tool.Skipped:
			status = "skip"
		case !ev.Tool.OK:
			status = "fail"
		}
		// Обновляем последний блок инструмента с тем же заголовком.
		idx := m.lastToolBlock(ev.Tool.Title)
		b := block{kind: blockTool, title: ev.Tool.Title, status: status, preview: ev.Tool.Output}
		if idx >= 0 {
			m.updateBlock(idx, b)
		} else {
			m.addBlock(b)
		}
		if m.cfg.Log.LogTools {
			_ = m.logger.WriteTool(ev.Tool.Name, ev.Tool.Args, ev.Tool.Output, ev.Tool.OK)
		}
		m.iterations++
		return nil

	case agent.EventStats:
		m.stats = ev.Stats
		m.meter.Observe(ev.Stats.PromptEvalCount, ev.Stats.EvalCount)
		return nil

	case agent.EventNotice:
		m.addBlock(block{kind: blockHint, text: ev.Text})

	case agent.EventRetry:
		// Сбой случился до появления ответа. Убираем рассуждения неудачной
		// попытки, чтобы в ленте не оставалось оборванного текста, и говорим,
		// что запрос повторяется.
		m.dropLiveBlocks()
		m.turnThink.Reset()
		m.turnAnswer.Reset()
		m.addBlock(block{kind: blockNotice, text: ev.Text})
		m.statusMsg = "повтор запроса"
		return nil

	case agent.EventTurnDone:
		m.closeLiveBlocks()
		m.finishTurn()
		return m.refreshCapacityCmd()

	case agent.EventError:
		m.closeLiveBlocks()
		if ev.Err != nil && ev.Err.Error() == ollama.ErrCanceled.Error() {
			m.addBlock(block{kind: blockNotice, text: "генерация прервана"})
		} else {
			m.addBlock(block{kind: blockError, text: ev.Err.Error()})
			_ = m.logger.Write(chatlog.KindSystem, "Ошибка: "+ev.Err.Error())
		}
		m.finishTurn()
		return nil
	}
	return nil
}

// closeLiveBlocks перерисовывает накопленный ответ через markdown.
func (m *Model) closeLiveBlocks() {
	if m.liveIdx >= 0 && m.liveIdx < len(m.blocks) {
		m.updateBlock(m.liveIdx, m.blocks[m.liveIdx])
		m.liveIdx = -1
	}
	m.thinkIdx = -1
}

// dropLiveBlocks удаляет блоки текущей неудачной попытки ответа: рассуждения
// и накопленный текст. Удаляются только блоки в конце ленты — всё, что было
// до вопроса, остаётся нетронутым.
func (m *Model) dropLiveBlocks() {
	drop := map[int]bool{}
	if m.thinkIdx >= 0 {
		drop[m.thinkIdx] = true
	}
	if m.liveIdx >= 0 {
		drop[m.liveIdx] = true
	}
	if len(drop) == 0 {
		return
	}

	blocks := make([]block, 0, len(m.blocks))
	rendered := make([]string, 0, len(m.rendered))
	for i := range m.blocks {
		if drop[i] {
			continue
		}
		blocks = append(blocks, m.blocks[i])
		if i < len(m.rendered) {
			rendered = append(rendered, m.rendered[i])
		}
	}
	m.blocks, m.rendered = blocks, rendered
	m.liveIdx, m.thinkIdx = -1, -1
	m.refreshViewport(true)
}

// lastToolBlock находит последний блок инструмента с указанным заголовком.
func (m *Model) lastToolBlock(title string) int {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool && m.blocks[i].title == title && m.blocks[i].status == "" {
			return i
		}
	}
	return -1
}

// refreshCapacityCmd уточняет ёмкость окна по фактически загруженной модели.
func (m *Model) refreshCapacityCmd() tea.Cmd {
	client := m.client
	model := m.modelName
	gen := m.srvGen
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(10)
		defer cancel()
		running, err := client.PS(ctx)
		if err != nil {
			return nil
		}
		for _, r := range running {
			if (r.Name == model || r.Model == model) && r.ContextLength > 0 {
				return modelInfoMsg{gen: gen, model: model,
					capacity: r.ContextLength, source: ctxmeter.SourcePS, caps: nil}
			}
		}
		return nil
	}
}

// handleModelsRefreshed принимает свежий список моделей и делает то, ради чего
// его запрашивали: открывает список выбора или переключает модель.
func (m *Model) handleModelsRefreshed(msg modelsMsg) tea.Cmd {
	m.statusMsg = ""

	if msg.err != nil {
		m.addBlock(block{kind: blockError, text: "список моделей: " + msg.err.Error()})
		// Проверить не смогли, но раз пользователь назвал модель явно —
		// не мешаем ему переключиться.
		if msg.action == modelsSelect && msg.target != "" {
			m.addBlock(block{kind: blockNotice, text: "список обновить не удалось, переключаюсь без проверки"})
			return m.switchModel(msg.target)
		}
		return nil
	}

	previous := m.models
	m.models = msg.models

	// Если текущая модель исчезла с сервера, сказать об этом сразу: иначе
	// пользователь узнает об этом только по ошибке в ответ на свой вопрос.
	if m.modelName != "" && !m.modelExists(m.modelName) {
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"модель %s больше не найдена на сервере %s — выберите другую", m.modelName, m.server.Name)})
	}
	if note := modelsDiffNote(previous, msg.models); note != "" {
		m.addBlock(block{kind: blockNotice, text: note})
	}

	switch msg.action {
	case modelsSelect:
		if !m.modelExists(msg.target) {
			m.addBlock(block{kind: blockError, text: fmt.Sprintf(
				"модель %q не найдена на сервере %s", msg.target, m.server.Name)})
			return nil
		}
		return m.switchModel(msg.target)
	default:
		return m.openModelPicker()
	}
}

// modelsDiffNote сообщает, что изменилось на сервере с прошлого обновления.
// Пустая строка означает «сравнивать не с чем» или «изменений нет».
func modelsDiffNote(before, after []ollama.ModelInfo) string {
	if len(before) == 0 {
		return ""
	}
	was := make(map[string]bool, len(before))
	for _, mi := range before {
		was[mi.Name] = true
	}
	now := make(map[string]bool, len(after))
	for _, mi := range after {
		now[mi.Name] = true
	}

	var added, removed []string
	for _, mi := range after {
		if !was[mi.Name] {
			added = append(added, mi.Name)
		}
	}
	for _, mi := range before {
		if !now[mi.Name] {
			removed = append(removed, mi.Name)
		}
	}

	var parts []string
	if len(added) > 0 {
		parts = append(parts, "появились: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "удалены: "+strings.Join(removed, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "список моделей изменился — " + strings.Join(parts, "; ")
}

func (m *Model) modelExists(name string) bool {
	if name == "" {
		return false
	}
	for _, mi := range m.models {
		if mi.Name == name || mi.Model == name {
			return true
		}
	}
	return len(m.models) == 0 // список ещё не загружен — не мешаем работе
}

// contextWithTimeout — короткая обёртка для фоновых запросов к серверу.
func contextWithTimeout(seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}
