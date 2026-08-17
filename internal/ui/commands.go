package ui

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/document"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// helpText — справка по командам, показывается по /help.
const helpText = `Команды:
  /help                    эта справка
  /servers                 список серверов из конфига (Ctrl+S)
  /server <имя>            переключиться на сервер
  /models                  список моделей — запрашивается у сервера заново (Ctrl+R)
  /model <имя>             переключиться на модель
  /ps                      что загружено в память сервера
  /info                    подробности о текущей модели
  /context                 сведения о заполнении контекстного окна
  /addcontext <N>          увеличить окно контекста на N токенов (/addcontext 32k)
  /calc [N]                какое окно дать каждому из N пользователей (без N — одному)
  /mode [safe|auto-edit|yolo]  режим подтверждений (Shift+Tab — по кругу)
  /permissions             действующие правила разрешений
  /tools                   доступные инструменты
  /think [on|off]          режим рассуждений модели
  /mouse [on|off]          мышь приложению или терминалу (F2)
  /paste                   вставить изображение из буфера обмена (Ctrl+V)
  /system [текст]          показать или изменить системный промпт
  /kb [подкоманда]         база знаний по книгам (/kb help — подробности)
  /add <путь>              приложить файл к контексту (PDF и EPUB — текстом)
  /addimg <путь> [стр.]    приложить рисунки из PDF или EPUB (/addimg книга.pdf 40-60)
  /log [on|off]            путь к журналу, включение и выключение
  /save                    сохранить текущую сессию
  /resume [id]             восстановить сессию (без id — выбор из списка)
  /clear                   очистить историю диалога
  /compact [N]             оставить последние N сообщений (по умолчанию 6)
  /config                  путь к файлу настроек
  /quit                    выход

Окно контекста можно увеличить и посреди сеанса: /addcontext 32k прибавит к нему
32768 токенов. Ollama берёт размер окна из каждого запроса, поэтому со следующим
вопросом она перезагрузит модель с новым окном — это займёт время, а если не
хватит видеопамяти, часть модели уедет в оперативную и скорость упадёт (/ps
покажет, что вышло). В конфиг значение не пишется, только на текущий сеанс.

Клавиши:
  Enter          отправить          Alt+Enter   перенос строки (и Ctrl+J)
  Esc            прервать ответ     Shift+Tab   сменить режим
  Ctrl+T         скрыть/показать рассуждения
  Ctrl+S         выбор сервера      Ctrl+R      выбор модели
  F2             мышь приложению или терминалу
  F3             панель вложенных изображений
  F5             копировать видимый ответ в буфер обмена
  Shift+F5       то же вместе с вопросом, в виде записи журнала
  Ctrl+V         вставить изображение из буфера обмена
  Ctrl+C ×2      выход

Файлы по «@»:
  Наберите @ — над полем ввода появится список рабочего каталога. Стрелки ↑↓
  выбирают, Enter или Tab вставляет, Esc закрывает. Список фильтруется по мере
  набора: @int оставит только начинающиеся на «int». Выбранный каталог
  подставляется со слешем и список показывает его содержимое — так путь
  набирается вглубь. За пределы рабочего каталога список не выходит.
  Вставляется только путь; чтобы приложить к контексту само содержимое файла,
  используйте /add (он принимает и относительный, и абсолютный путь).

База знаний по книгам:
  Поиск по личной библиотеке. Коллекции собираются по требованию, а не по всей
  библиотеке разом: /kb add go /путь/к/книгам — и модель сможет искать в этих
  книгах инструментом kb_search, отвечая со ссылками на книгу и страницу.
  Индексация идёт в фоне: чат при этом работает, ход виден в строке состояния,
  Esc или /kb stop останавливает. Доливка книг стоит ровно столько, сколько
  новых книг: /kb sync go перечитает только то, что появилось, а пропавшее
  уберёт из выдачи. Каталоги, откуда можно брать книги, перечисляются
  в настройке kb.roots — ни модель, ни команда этот список не расширяют.
  /kb use go выбирает коллекцию для модели, /kb auto on подмешивает найденное
  перед каждым вопросом (годится и для моделей без поддержки инструментов).
  Полный список подкоманд — /kb help.

Документы PDF и книги EPUB:
  read_file и /add читают их сами, без внешних программ: из PDF извлекается
  текстовый слой постранично («── страница N ──»), из книги — главы по порядку
  чтения («── раздел N: заголовок ──»). Формат определяется по содержимому,
  расширение не важно. Предел размера свой, sandbox.max_pdf_mb: в контекст
  идёт извлечённый текст, а не файл.
  В PDF таблицы собираются по координатам: ячейки строки остаются в одной
  строке, столбцы — друг под другом, двухколоночная вёрстка разделяется.
  В книгах структуру задаёт разметка: абзацы, списки, таблицы, заголовки глав.
  Рисунки отмечаются в тексте меткой «[рисунок 4.1: 1653×2338]» — страница и
  номер на ней. Показать такую картинку модели можно двумя способами: она сама
  вызовет инструмент view_image, увидев метку, либо вы приложите картинки
  к вопросу командой /addimg книга.pdf 44 (у книги — номер раздела) — они
  вкладываются как [Image01], [Image02], и дальше о них можно спрашивать
  словами. Обоим нужна модель с возможностью vision.
  Это важнее, чем кажется: таблицы цен в коммерческих предложениях нередко
  свёрстаны картинкой целиком, и в текстовом слое их нет вовсе.
  Если страницы PDF — картинки (скан), об этом говорится прямо: текст даст
  только распознавание, но саму страницу можно показать модели с vision.

Изображения:
  Ctrl+V кладёт картинку из буфера обмена в промпт меткой [Image01], дальше
  пишите вопрос обычным текстом и ссылайтесь на метку словами. Меток может быть
  несколько: [Image01], [Image02]. Стёртая метка отменяет вложение. При отправке
  картинки уходят модели вместе с текстом; нужна модель с возможностью vision.
  F3 открывает и закрывает панель вложений: там видно, что реально приложено
  к вопросу, стрелки ↑↓ выбирают, Del или Ctrl+D убирает картинку вместе с её
  меткой в промпте. В строке состояния одно вложение показано подробно,
  несколько — пометкой [Images attached].
  Нужен xclip (X11) или wl-clipboard (Wayland). Если ollchat запущен по SSH,
  буфер обмена остался на вашей машине: подключайтесь с пробросом X11 (ssh -X,
  для долгих сеансов -Y) и поставьте xclip на удалённой машине. Внутри tmux
  и screen переменную DISPLAY после переподключения нужно обновлять вручную.

Прокрутка ответа:
  колесо мыши    прокрутка          PgUp/PgDn   страница вверх/вниз
  Ctrl+U/Ctrl+D  полстраницы        Ctrl+Home   в начало
  Ctrl+G         в конец ответа (и Ctrl+End)
  бегунок справа показывает положение в ответе; его можно тянуть мышью,
  а щелчок по полосе переносит в это место
Прокрутка работает и во время генерации: если отлистать вверх, автопрокрутка
встаёт на паузу, а в разделителе показывается, сколько строк осталось ниже.
Ctrl+G возвращает в конец и снова включает слежение за ответом.
Выделение текста мышью: нажмите F2 (или /mouse off) — мышь вернётся терминалу,
и текст ленты можно выделять и копировать как обычно; колесо и бегунок при этом
не работают, а прокрутка остаётся на клавишах. F2 возвращает мышь приложению.
Во многих терминалах то же самое даёт перетаскивание с удерживаемым Shift.

Копирование ответа в буфер обмена:
  F5 кладёт в буфер обмена ответ, видимый на экране сейчас, — это может быть
  и последний ответ, и любой, к которому вы отлистали ленту. Когда на экране
  видно несколько ответов, берётся занимающий больше всего места, а при равной
  доле — нижний. Копируется он целиком, даже если на экране помещается не весь.
  Ответ, разорванный вызовами инструментов, склеивается из своих частей;
  рассуждения и вывод инструментов в буфер не попадают. Shift+F5 копирует то же
  вместе с вопросом, в том виде, в каком запись ложится в журнал чата.
  Нужен xclip (X11) или wl-clipboard (Wayland); без них текст отдаётся самому
  терминалу по OSC 52 — так умеют не все терминалы, и длинный текст многие
  обрезают.

В окне подтверждения действия:
  y  выполнить один раз
  n  отклонить
  a  разрешить это действие до конца сеанса
  t  разрешить весь инструмент до конца сеанса — больше не спрашивать про него
Запреты из permissions.deny действуют всегда и ответами y/a/t не снимаются.`

// runCommand разбирает и выполняет слэш-команду.
func (m *Model) runCommand(input string) tea.Cmd {
	fields := strings.Fields(input)
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	arg := strings.TrimSpace(strings.TrimPrefix(input, fields[0]))

	switch cmd {
	case "help", "?":
		m.addBlock(block{kind: blockNotice, text: helpText})
		return nil

	case "quit", "exit", "q":
		return tea.Quit

	case "servers":
		return m.openServerPicker()

	case "server":
		if arg == "" {
			return m.openServerPicker()
		}
		return m.switchServer(arg)

	// Список моделей всегда запрашивается заново: на сервере их добавляют
	// и удаляют, а показанный при запуске список к этому моменту врёт.
	case "models":
		m.statusMsg = "обновляю список моделей…"
		return m.refreshModelsCmd(modelsOpenPicker, "")

	case "model":
		m.statusMsg = "обновляю список моделей…"
		if arg == "" {
			return m.refreshModelsCmd(modelsOpenPicker, "")
		}
		return m.refreshModelsCmd(modelsSelect, arg)

	case "ps":
		return m.psCmd()

	case "info":
		return m.infoCmd()

	case "context":
		m.addBlock(block{kind: blockNotice, text: m.contextReport()})
		return nil

	case "mode":
		if arg == "" {
			m.addBlock(block{kind: blockNotice, text: "текущий режим: " + m.guard.Mode()})
			return nil
		}
		if err := m.guard.SetMode(arg); err != nil {
			m.addBlock(block{kind: blockError, text: err.Error()})
			return nil
		}
		m.addBlock(block{kind: blockNotice, text: "режим подтверждений: " + m.guard.Mode()})
		return nil

	case "permissions":
		m.addBlock(block{kind: blockNotice, text: m.permissionsReport()})
		return nil

	case "tools":
		m.addBlock(block{kind: blockNotice, text: m.toolsReport()})
		return nil

	case "think":
		return m.thinkCmd(arg)

	case "addcontext":
		return m.addContextCmd(arg)

	case "calc":
		return m.calcArgCmd(arg)

	case "mouse":
		return m.mouseCmd(arg)

	case "paste", "image":
		m.statusMsg = "читаю буфер обмена…"
		return pasteImageCmd()

	case "system":
		if arg == "" {
			cur := m.conv.System()
			if cur == "" {
				cur = "(не задан)"
			}
			m.addBlock(block{kind: blockNotice, text: "системный промпт:\n" + cur})
			return nil
		}
		m.conv.SetSystem(arg)
		m.addBlock(block{kind: blockNotice, text: "системный промпт обновлён"})
		return nil

	case "kb":
		return m.kbCommand(arg)

	case "add":
		return m.addFileCmd(arg)

	case "addimg", "addimage":
		return m.addImagesCmd(arg)

	case "log":
		return m.logCmd(arg)

	case "save":
		path, err := m.store.Save(m.conv, m.server.Name, m.modelName)
		if err != nil {
			m.addBlock(block{kind: blockError, text: "сохранение сессии: " + err.Error()})
			return nil
		}
		m.addBlock(block{kind: blockNotice, text: "сессия сохранена: " + path})
		return nil

	case "resume":
		if arg == "" {
			return m.openSessionPicker()
		}
		return m.resumeSession(arg)

	case "clear":
		m.conv.Clear()
		m.blocks = nil
		m.rendered = nil
		m.meter.Reset()
		m.stats = ollama.Stats{}
		m.dropPendingImages()
		m.addBlock(block{kind: blockNotice, text: "история диалога очищена"})
		return nil

	case "compact":
		keep := 6
		if arg != "" {
			if _, err := fmt.Sscanf(arg, "%d", &keep); err != nil || keep < 0 {
				m.addBlock(block{kind: blockError, text: "укажите число сообщений: /compact 6"})
				return nil
			}
		}
		dropped := m.conv.Compact(keep)
		m.meter.Used = ctxmeter.EstimateChars(m.conv.EstimatedChars())
		m.meter.Exact = false
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"история сокращена: отброшено сообщений %d, осталось %d", dropped, m.conv.Len())})
		return nil

	case "config":
		m.addBlock(block{kind: blockNotice, text: "файл настроек: " + m.cfg.Path})
		return nil

	default:
		m.addBlock(block{kind: blockError, text: "неизвестная команда " + fields[0] + " — попробуйте /help"})
		return nil
	}
}

func (m *Model) psCmd() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(20)
		defer cancel()
		running, err := client.PS(ctx)
		if err != nil {
			return errorMsg{err: err}
		}
		if len(running) == 0 {
			return noticeMsg{text: "на сервере нет загруженных моделей"}
		}
		var b strings.Builder
		b.WriteString("Загружено в память сервера:\n")
		for _, r := range running {
			fmt.Fprintf(&b, "  %s — окно %s, в видеопамяти %.1f ГБ, выгрузка в %s\n",
				r.Name, ctxmeter.FormatTokens(r.ContextLength),
				float64(r.SizeVRAM)/(1024*1024*1024), shortTime(r.ExpiresAt))
		}
		return noticeMsg{text: strings.TrimRight(b.String(), "\n")}
	}
}

func (m *Model) infoCmd() tea.Cmd {
	client := m.client
	model := m.modelName
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(20)
		defer cancel()
		show, err := client.Show(ctx, model)
		if err != nil {
			return errorMsg{err: err}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Модель %s\n", model)
		fmt.Fprintf(&b, "  семейство: %s, параметров: %s, квантование: %s\n",
			show.Details.Family, show.Details.ParameterSize, show.Details.QuantizationLevel)
		if n, ok := ollama.ContextLengthFromShow(show); ok {
			fmt.Fprintf(&b, "  окно контекста по паспорту: %s токенов\n", ctxmeter.FormatTokens(n))
		}
		fmt.Fprintf(&b, "  возможности: %s\n", strings.Join(show.Capabilities, ", "))
		if strings.TrimSpace(show.Parameters) != "" {
			fmt.Fprintf(&b, "  параметры по умолчанию:\n")
			for _, line := range strings.Split(strings.TrimSpace(show.Parameters), "\n") {
				fmt.Fprintf(&b, "    %s\n", strings.TrimSpace(line))
			}
		}
		return noticeMsg{text: strings.TrimRight(b.String(), "\n")}
	}
}

// contextReport подробно описывает состояние контекстного окна.
func (m *Model) contextReport() string {
	var b strings.Builder
	b.WriteString("Контекстное окно\n")
	if m.meter.Capacity > 0 {
		fmt.Fprintf(&b, "  ёмкость: %d токенов (источник: %s)\n", m.meter.Capacity, m.meter.Source)
	} else {
		b.WriteString("  ёмкость: неизвестна\n")
	}
	// Максимум модели и действующее окно — разные величины, и путать их дорого:
	// «ollama show» показывает первое, строка статуса — второе.
	if m.modelMaxCtx > 0 {
		fmt.Fprintf(&b, "  максимум модели: %d токенов\n", m.modelMaxCtx)
		if m.meter.Capacity > 0 && m.meter.Capacity < m.modelMaxCtx {
			fmt.Fprintf(&b, "  используется %d%% возможного окна — поднимается настройкой num_ctx\n",
				m.meter.Capacity*100/m.modelMaxCtx)
		}
	}
	kind := "оценка по объёму текста"
	if m.meter.Exact {
		kind = "точные счётчики сервера"
	}
	fmt.Fprintf(&b, "  занято: %d токенов (%s)\n", m.meter.Used, kind)
	if m.meter.Capacity > 0 {
		fmt.Fprintf(&b, "  заполнено: %d%%\n", m.meter.Percent())
	}
	if m.stats.PromptEvalCount > 0 || m.stats.EvalCount > 0 {
		fmt.Fprintf(&b, "  последний обмен: промпт %d, ответ %d токенов\n",
			m.stats.PromptEvalCount, m.stats.EvalCount)
	}
	fmt.Fprintf(&b, "  сообщений в истории: %d\n", m.conv.Len())
	if n, ok := m.server.NumCtx(); ok {
		fmt.Fprintf(&b, "  num_ctx из конфига: %d\n", n)
	} else {
		b.WriteString("  num_ctx в конфиге не задан — сервер использует своё значение\n")
	}
	b.WriteString("\nOllama не предоставляет подсчёт токенов до отправки запроса,\nпоэтому до первого ответа показывается оценка со знаком «≈».")
	return b.String()
}

// permissionsReport перечисляет действующие правила.
func (m *Model) permissionsReport() string {
	allow, ask, deny := m.guard.Set().Rules()
	var b strings.Builder
	fmt.Fprintf(&b, "Режим: %s. Песочница: %s\n", m.guard.Mode(), m.guard.Sandbox().Root())
	writeRules := func(title string, rules []permissions.Rule) {
		fmt.Fprintf(&b, "\n%s (%d):\n", title, len(rules))
		if len(rules) == 0 {
			b.WriteString("  —\n")
			return
		}
		items := make([]string, 0, len(rules))
		for _, r := range rules {
			items = append(items, r.Source)
		}
		sort.Strings(items)
		for _, s := range items {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	writeRules("Запрещено (deny — не обходится ничем)", deny)
	writeRules("Разрешено (allow)", allow)
	writeRules("Спрашивать (ask)", ask)

	granted := m.guard.GrantedRules()
	grantedTools := m.guard.GrantedTools()

	if len(grantedTools) > 0 {
		fmt.Fprintf(&b, "\nИнструменты, разрешённые целиком на время сеанса (%d):\n", len(grantedTools))
		for _, t := range grantedTools {
			fmt.Fprintf(&b, "  %s — подтверждение больше не запрашивается\n", t)
		}
	}
	if len(granted) > 0 {
		fmt.Fprintf(&b, "\nОтдельные действия, разрешённые на время сеанса (%d):\n", len(granted))
		for _, r := range granted {
			fmt.Fprintf(&b, "  %s\n", r.Source)
		}
	}
	if len(granted) > 0 || len(grantedTools) > 0 {
		b.WriteString("\nЭти разрешения действуют только до выхода и в файл настроек не пишутся.\n" +
			"Чтобы закрепить их насовсем, добавьте правила в permissions.allow файла настроек.\n" +
			"Правила deny продолжают действовать в любом случае.")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) toolsReport() string {
	var b strings.Builder
	if !m.cfg.Agent.Enabled {
		b.WriteString("Агентный режим выключен настройкой agent.enabled.\n")
	}
	if !hasCap(m.modelCaps, "tools") {
		fmt.Fprintf(&b, "Модель %s не поддерживает вызов инструментов — они не передаются серверу.\n", m.modelName)
	}
	fmt.Fprintf(&b, "Включённые инструменты (%d):\n", len(m.registry.Names()))
	for _, n := range m.registry.Names() {
		if m.guard.ToolGranted(n) {
			fmt.Fprintf(&b, "  %s — разрешён целиком до конца сеанса\n", n)
			continue
		}
		fmt.Fprintf(&b, "  %s\n", n)
	}
	// Выключенные показываем отдельно: конфиг, созданный прежней версией,
	// не знает о новых инструментах, и молчание об этом дорого обходится —
	// модель читает подсказку, не находит инструмента и ищет обходной путь.
	var off []string
	for _, n := range tools.AllNames() {
		if !m.registry.Has(n) {
			off = append(off, n)
		}
	}
	if len(off) > 0 {
		fmt.Fprintf(&b, "Выключены настройкой agent.tools (%d): %s\n",
			len(off), strings.Join(off, ", "))
	}
	fmt.Fprintf(&b, "Ограничение итераций: %d. Таймаут команд: %s.",
		m.cfg.Agent.MaxIterations, m.cfg.Agent.BashTimeoutDuration())
	return b.String()
}

func (m *Model) thinkCmd(arg string) tea.Cmd {
	if !hasCap(m.modelCaps, "thinking") {
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"модель %s не поддерживает режим рассуждений", m.modelName)})
		return nil
	}
	switch strings.ToLower(arg) {
	case "on", "вкл", "да":
		v := true
		m.think = &v
	case "off", "выкл", "нет":
		v := false
		m.think = &v
	case "":
		state := "по умолчанию (решает сервер)"
		if m.think != nil {
			if *m.think {
				state = "включён"
			} else {
				state = "выключен"
			}
		}
		m.addBlock(block{kind: blockNotice, text: "режим рассуждений: " + state})
		return nil
	default:
		m.addBlock(block{kind: blockError, text: "использование: /think on|off"})
		return nil
	}
	state := "выключен"
	if *m.think {
		state = "включён"
	}
	m.addBlock(block{kind: blockNotice, text: "режим рассуждений " + state})
	return nil
}

// addContextCmd увеличивает окно контекста прямо посреди сеанса.
//
// Так можно потому, что Ollama берёт num_ctx из каждого запроса: увидев новое
// значение, она перезагружает модель с этим окном. Значит окно не приковано
// к моменту запуска — платой будет перезагрузка модели перед следующим ответом.
//
// В файл настроек ничего не пишется: это решение на текущий сеанс.
func (m *Model) addContextCmd(arg string) tea.Cmd {
	if strings.TrimSpace(arg) == "" {
		m.addBlock(block{kind: blockError, text: "использование: /addcontext 32k — " +
			"добавить к окну контекста ещё 32768 токенов (можно и числом: /addcontext 32768)"})
		return nil
	}

	delta, err := parseTokens(arg)
	if err != nil {
		m.addBlock(block{kind: blockError, text: "/addcontext: " + err.Error()})
		return nil
	}

	// Отталкиваемся от того, что реально действует сейчас: num_ctx из конфига,
	// а если его нет — от ёмкости, которую сообщил сервер.
	current, ok := m.server.NumCtx()
	if !ok || current <= 0 {
		current = m.meter.Capacity
	}
	if current <= 0 {
		m.addBlock(block{kind: blockError, text: "/addcontext: текущее окно неизвестно — " +
			"дождитесь ответа сервера о модели или задайте num_ctx в конфиге"})
		return nil
	}

	target := current + delta
	clamped := false
	if m.modelMaxCtx > 0 && target > m.modelMaxCtx {
		target, clamped = m.modelMaxCtx, true
	}
	if target == current {
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"окно контекста уже равно максимуму модели — %d токенов", current)})
		return nil
	}

	m.setNumCtx(target)

	text := fmt.Sprintf("окно контекста: %d → %d токенов", current, target)
	if clamped {
		text += fmt.Sprintf(" (ограничено максимумом модели %d)", m.modelMaxCtx)
	}
	text += "\nНовое значение вступит в силу со следующим вопросом: сервер перезагрузит" +
		" модель с этим окном, это займёт время.\nЕсли видеопамяти не хватит, часть модели" +
		" уедет в оперативную и скорость упадёт — посмотреть можно командой /ps." +
		"\nВ конфиг не записано, только на этот сеанс."
	m.addBlock(block{kind: blockNotice, text: text})
	_ = m.logger.Write(chatlog.KindSystem, fmt.Sprintf(
		"Окно контекста изменено: %d → %d токенов.", current, target))
	return nil
}

// setNumCtx запоминает новое окно для текущего сервера.
//
// Значение живёт в options сервера — именно оттуда оно уходит в запрос,
// и оно же авторитетнее прочих источников для индикатора заполнения.
func (m *Model) setNumCtx(n int) {
	if m.server.Options == nil {
		m.server.Options = map[string]any{}
	}
	m.server.Options["num_ctx"] = n
	m.meter.SetCapacity(n, ctxmeter.SourceConfig)
}

// parseTokens разбирает запись числа токенов: 32768, 32k, 128К, 1m.
// Суффиксы считаются двоичными: k — это 1024, как принято для размеров окна.
func parseTokens(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.ReplaceAll(t, " ", "")
	runes := []rune(t)
	if len(runes) == 0 {
		return 0, fmt.Errorf("пустое значение")
	}

	// Буквы принимаем и латинские, и русские: раскладку ради одной клавиши
	// переключать не хочется.
	mult := 1
	switch runes[len(runes)-1] {
	case 'k', 'к':
		mult, runes = 1024, runes[:len(runes)-1]
	case 'm', 'м':
		mult, runes = 1024*1024, runes[:len(runes)-1]
	}

	n, err := strconv.Atoi(string(runes))
	if err != nil {
		return 0, fmt.Errorf("не понимаю %q — ожидается число токенов, например 32768 или 32k", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("прибавка должна быть положительной, получено %d", n)
	}
	return n * mult, nil
}

// mouseCmd включает и выключает отчёт о мыши.
//
// Выключить его — единственный способ вернуть терминалу выделение текста:
// пока отчёт включён, терминал отдаёт все события мыши приложению и не
// выделяет ничего сам.
func (m *Model) mouseCmd(arg string) tea.Cmd {
	switch strings.ToLower(arg) {
	case "on", "вкл", "да":
		m.setMouse(true)
	case "off", "выкл", "нет":
		m.setMouse(false)
	case "":
		m.addBlock(block{kind: blockNotice, text: "мышь " + m.mouseStateText()})
	default:
		m.addBlock(block{kind: blockError, text: "использование: /mouse on|off"})
	}
	return nil
}

// setMouse переключает режим мыши и объясняет, что изменилось.
func (m *Model) setMouse(on bool) {
	if m.mouseOn == on {
		m.statusMsg = "мышь " + m.mouseStateText()
		return
	}
	m.mouseOn = on
	// Захват бегунка мог остаться с прошлого режима: отпускания кнопки
	// приложение уже не увидит.
	m.draggingBar = false
	// Только строка состояния: переключение бывает частым, и дублировать его
	// ещё и в ленте — засорять историю диалога служебными строками.
	m.statusMsg = "мышь " + m.mouseStateText()
}

// mouseStateText описывает текущий режим мыши словами пользователя, а не кода.
func (m *Model) mouseStateText() string {
	if m.mouseOn {
		return "у приложения — работают колесо и бегунок"
	}
	return "у терминала — можно выделять и копировать текст"
}

// addFileCmd прикладывает файл к контексту как сообщение пользователя.
func (m *Model) addFileCmd(arg string) tea.Cmd {
	if arg == "" {
		m.addBlock(block{kind: blockError, text: "использование: /add путь/к/файлу"})
		return nil
	}
	abs, err := m.guard.Sandbox().Resolve(arg)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	// Приложение файла — это чтение, поэтому оно подчиняется тем же правилам.
	res := m.guard.Check(permissions.Request{Kind: permissions.KindRead, Target: abs, Tool: "add"})
	if res.Decision == permissions.DecisionDeny {
		m.addBlock(block{kind: blockError, text: "чтение запрещено: " + res.Reason})
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	rel := m.guard.Sandbox().Rel(abs)

	// Документ прикладывается извлечённым текстом, а не байтами файла.
	if kind := document.DetectFile(abs); kind != document.KindNone {
		doc, err := document.Read(abs, m.guard.Sandbox().MaxPDFBytes())
		if err != nil {
			m.addBlock(block{kind: blockError, text: err.Error()})
			return nil
		}
		return m.attach(rel, doc.Header(m.registry != nil && m.registry.Has(tools.NameViewImage))+"\n\n"+doc.Text,
			strings.ToLower(string(kind))+" "+rel+" приложен к контексту")
	}

	if info.Size() > m.guard.Sandbox().MaxFileBytes() {
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"файл слишком велик: %d байт, предел %d", info.Size(), m.guard.Sandbox().MaxFileBytes())})
		return nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	return m.attach(rel, string(data), "файл "+rel+" приложен к контексту")
}

// attach добавляет содержимое файла в историю диалога как сообщение пользователя.
func (m *Model) attach(rel, body, notice string) tea.Cmd {
	content := fmt.Sprintf("Содержимое файла %s:\n\n```\n%s\n```", rel, body)
	m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: content})
	m.meter.Used = ctxmeter.EstimateChars(m.conv.EstimatedChars())
	m.meter.Exact = false
	m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
		"%s (%d байт, ≈%s токенов)",
		notice, len(body), ctxmeter.FormatTokens(ctxmeter.Estimate(content)))})
	_ = m.logger.Write(chatlog.KindSystem, "К контексту приложен файл "+rel)
	return nil
}

func (m *Model) logCmd(arg string) tea.Cmd {
	switch strings.ToLower(arg) {
	case "on", "вкл":
		m.logger.SetEnabled(true)
		m.addBlock(block{kind: blockNotice, text: "журнал включён: " + m.logger.CurrentPath()})
	case "off", "выкл":
		m.logger.SetEnabled(false)
		m.addBlock(block{kind: blockNotice, text: "журнал выключен"})
	case "":
		state := "включён"
		if !m.logger.Enabled() {
			state = "выключен"
		}
		text := fmt.Sprintf("Журнал %s\n  файл: %s\n  каталог: %s\n  шаблон имени: %s",
			state, m.logger.CurrentPath(), m.cfg.Log.Dir, m.cfg.Log.Pattern)
		if err := m.logger.LastError(); err != nil {
			text += "\n  последняя ошибка: " + err.Error()
		}
		m.addBlock(block{kind: blockNotice, text: text})
	default:
		m.addBlock(block{kind: blockError, text: "использование: /log [on|off]"})
	}
	return nil
}

// newClient создаёт клиента по описанию сервера.
func newClient(srv *config.Server) *ollama.Client {
	return ollama.New(srv.URL, srv.TimeoutDuration(), srv.Headers)
}

// shortTime приводит метку времени Ollama к виду ЧЧ:ММ.
func shortTime(s string) string {
	if len(s) >= 16 {
		return strings.Replace(s[11:16], "T", " ", 1)
	}
	return s
}
