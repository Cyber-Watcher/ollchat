package ui

import (
	"fmt"
	"os"
	"path/filepath"
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

// helpText собирает справку: список команд из общей таблицы плюс постоянная
// часть про окно контекста и клавиши.
//
// Список не пишется руками: он один и тот же у справки и у подсказки над
// строкой ввода, а два списка рядом расходятся в первую же правку —
// добавил команду, забыл про справку. См. `commandlist.go`.
// tuneHelpBlock — про крутилки отбора, и только про настроенные возможности.
//
// Собирается по частям: на машине без книг строки про /kb tune в справке
// не нужны, как не нужна и сама команда.
func tuneHelpBlock(has sectionCheck) string {
	var b strings.Builder
	if has.ok("graph") {
		b.WriteString("  /graph tune sense 1.5   вес уместности связи; 0 — не пересортировывать\n")
		b.WriteString("  /graph tune pool 8      ширина пула пересортировки\n")
	}
	if has.ok("kb") {
		b.WriteString("  /kb tune top_k 12       сколько фрагментов из книг возвращать\n")
		b.WriteString("  /kb tune min_cosine 0.35, /kb tune semantic_weight 1.5\n")
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("  reset возвращает значения из конфига; без доводов команда их показывает.\n")
	if has.ok("mix") {
		b.WriteString("  /mix show <вопрос>      что ушло бы модели с этим вопросом — не спрашивая её\n")
	}
	b.WriteString("  Смотреть надо на материал: ответ модели меняется и сам по себе, от температуры.\n")
	return "Подбор чисел отбора на сеанс (в конфиг не пишется):\n" + b.String() + "\n"
}

// kbHelpBlock — рассказ о базе знаний, если она вообще настроена.
//
// Раздела [kb] в конфиге нет — значит книг на этой машине не будет, и абзац
// про /kb add в справке лишний: он предлагает то, чего человек не просил
// и чего у него нет.
func kbHelpBlock(has sectionCheck) string {
	if !has.ok("kb") {
		return ""
	}
	return `База знаний по книгам:
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
`
}

func helpText(has sectionCheck) string {
	return commandsHelp(has) + `
Окно контекста меняется посреди сеанса командой /context:
  /context           сведения: ёмкость, максимум модели, сколько занято
  /context set 256k  поставить окно (можно числом: 262144; буква k или к)
  /context add 32k   прибавить к нынешнему окну
  /context max       поставить наибольшее, какое умеет модель
Больше максимума модели поставить нельзя: значение ограничивается им, и об этом
говорится прямо — Ollama на такой запрос не ругается, а молча берёт своё.
Ollama берёт размер окна из каждого запроса, поэтому со следующим вопросом она
перезагрузит модель с новым окном — это займёт время, а если не хватит
видеопамяти, часть модели уедет в оперативную и скорость упадёт (/ps покажет,
что вышло). В конфиг значение не пишется, только на текущий сеанс.

` + tuneHelpBlock(has) + `Клавиши:
  Enter          отправить          Alt+Enter   перенос строки (и Ctrl+J)
  Esc            прервать ответ     Shift+Tab   сменить режим
  Ctrl+T         скрыть/показать рассуждения
  Ctrl+S         выбор сервера      Ctrl+R      выбор модели
  F2             мышь приложению или терминалу
  F3             панель вложенных изображений
  F4             сохранить видимый ответ в PDF
  Shift+F4       то же вместе с вопросом и шапкой документа
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

` + kbHelpBlock(has) + `
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

Сохранение ответа в PDF:
  F4 открывает окно с именем файла и сохраняет туда видимый ответ — тот же
  самый, который взяло бы копирование по F5. Разметка превращается в вёрстку:
  заголовки, списки, цитаты, таблицы и блоки кода. Shift+F4 добавляет к
  документу шапку с вопросом, моделью и датой. Имя предлагается по вопросу и
  правится как обычно; расширение .pdf дописывается само. Имя без пути кладёт
  файл в рабочий каталог, а указанный путь берётся как есть — можно сохранить
  и наружу, например ~/Документы/ответ.pdf. Существующий файл не затирается
  молча: Enter спросит про перезапись ещё раз.
  Команда /savetopdf <файл.pdf> делает то же самое без окна и без шапки.

В окне подтверждения действия:
  y  выполнить один раз
  n  отклонить
  a  разрешить это действие до конца сеанса
  t  разрешить весь инструмент до конца сеанса — больше не спрашивать про него
Запреты из permissions.deny действуют всегда и ответами y/a/t не снимаются.`
}

// runCommand разбирает и выполняет слэш-команду.
// cmdHandler — одна команда: её написания и обработчик.
type cmdHandler struct {
	names []string
	run   func(m *Model, arg string) tea.Cmd
}

// commandHandlers — исполнение команд. Описания для меню и справки живут
// в таблице commands (commandlist.go); тест commanddrift сверяет одно
// с другим. До этапа 91 (R6.9) здесь стоял switch на тридцать три ветки,
// и таблица с ним расходилась молча.
func commandHandlers() []cmdHandler {
	return []cmdHandler{
		{names: []string{"help", "?"}, run: func(m *Model, _ string) tea.Cmd {
			m.addBlock(block{kind: blockNotice, text: helpText(m.sections())})
			return nil
		}},
		{names: []string{"confluencetoken", "token"}, run: (*Model).confluenceTokenCmd},
		{names: []string{"quit", "exit", "q"}, run: func(*Model, string) tea.Cmd { return tea.Quit }},
		{names: []string{"servers"}, run: func(m *Model, _ string) tea.Cmd { return m.openServerPicker() }},
		{names: []string{"server"}, run: func(m *Model, arg string) tea.Cmd {
			if arg == "" {
				return m.openServerPicker()
			}
			return m.switchServer(arg)
		}},
		// Список моделей всегда запрашивается заново: на сервере их добавляют
		// и удаляют, а показанный при запуске список к этому моменту врёт.
		{names: []string{"models"}, run: func(m *Model, _ string) tea.Cmd {
			m.statusMsg = "обновляю список моделей…"
			return m.refreshModelsCmd(modelsOpenPicker, "")
		}},
		{names: []string{"model"}, run: func(m *Model, arg string) tea.Cmd {
			m.statusMsg = "обновляю список моделей…"
			if arg == "" {
				return m.refreshModelsCmd(modelsOpenPicker, "")
			}
			return m.refreshModelsCmd(modelsSelect, arg)
		}},
		{names: []string{"ps"}, run: func(m *Model, _ string) tea.Cmd { return m.psCmd() }},
		{names: []string{"info"}, run: func(m *Model, _ string) tea.Cmd { return m.infoCmd() }},
		{names: []string{"context"}, run: (*Model).contextCmd},
		{names: []string{"mode"}, run: (*Model).modeCmd},
		{names: []string{"permissions"}, run: func(m *Model, _ string) tea.Cmd {
			m.addBlock(block{kind: blockNotice, text: m.permissionsReport()})
			return nil
		}},
		{names: []string{"tools"}, run: (*Model).toolsCmd},
		{names: []string{"think"}, run: (*Model).thinkCmd},
		{names: []string{"calc"}, run: (*Model).calcArgCmd},
		{names: []string{"mouse"}, run: (*Model).mouseCmd},
		{names: []string{"paste", "image"}, run: func(m *Model, _ string) tea.Cmd {
			m.statusMsg = "читаю буфер обмена…"
			return pasteImageCmd()
		}},
		{names: []string{"system"}, run: (*Model).systemCmd},
		{names: []string{"search", "найти"}, run: (*Model).searchCmd},
		{names: []string{"read", "читать"}, run: (*Model).readCmd},
		{names: []string{"kb"}, run: (*Model).kbCommand},
		{names: []string{"mix", "подмес"}, run: (*Model).mixCmd},
		{names: []string{"graph", "граф"}, run: (*Model).graphCommand},
		{names: []string{"add"}, run: (*Model).addFileCmd},
		{names: []string{"addimg", "addimage"}, run: (*Model).addImagesCmd},
		{names: []string{"log"}, run: (*Model).logCmd},
		{names: []string{"id"}, run: func(m *Model, _ string) tea.Cmd { return m.idCmd() }},
		{names: []string{"save"}, run: (*Model).saveCmd},
		{names: []string{"savetopdf", "savepdf"}, run: (*Model).saveToPDFCmd},
		{names: []string{"resume"}, run: func(m *Model, arg string) tea.Cmd {
			if arg == "" {
				return m.openSessionPicker()
			}
			return m.resumeSession(arg)
		}},
		{names: []string{"clear"}, run: (*Model).clearCmd},
		{names: []string{"compact"}, run: (*Model).compactCmd},
		{names: []string{"config"}, run: func(m *Model, _ string) tea.Cmd {
			m.addBlock(block{kind: blockNotice, text: "файл настроек: " + m.cfg.Path})
			return nil
		}},
	}
}

// lookupCommand находит обработчик по написанию команды.
func lookupCommand(name string) *cmdHandler {
	for _, h := range commandHandlers() {
		for _, n := range h.names {
			if n == name {
				h := h
				return &h
			}
		}
	}
	return nil
}

func (m *Model) runCommand(input string) tea.Cmd {
	fields := strings.Fields(input)
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	arg := strings.TrimSpace(strings.TrimPrefix(input, fields[0]))
	if h := lookupCommand(cmd); h != nil {
		return h.run(m, arg)
	}
	m.addBlock(block{kind: blockError, text: "неизвестная команда " + fields[0] + " — попробуйте /help"})
	return nil
}

// modeCmd — /mode: показать или сменить режим подтверждений.
func (m *Model) modeCmd(arg string) tea.Cmd {
	if arg == "" {
		m.addBlock(block{kind: blockNotice, text: "текущий режим: " + m.guard.Mode()})
		return nil
	}
	if err := m.guard.SetMode(arg); err != nil {
		m.fail("/mode", err)
		return nil
	}
	m.addBlock(block{kind: blockNotice, text: "режим подтверждений: " + m.guard.Mode()})
	return nil
}

// systemCmd — /system: показать или заменить системный промпт.
func (m *Model) systemCmd(arg string) tea.Cmd {
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
}

// saveCmd — /save: сохранить диалог на диск.
func (m *Model) saveCmd(_ string) tea.Cmd {
	path, err := m.store.Save(m.conv, m.server.Name, m.modelName)
	if err != nil {
		m.fail("/save", err)
		return nil
	}
	m.addBlock(block{kind: blockNotice, text: "сессия сохранена: " + path})
	return nil
}

// clearCmd — /clear: очистить историю и ленту.
func (m *Model) clearCmd(_ string) tea.Cmd {
	m.conv.Clear()
	m.blocks = nil
	m.rendered = nil
	m.meter.Reset()
	m.stats = ollama.Stats{}
	m.dropPendingImages()
	m.pastes = nil
	m.addBlock(block{kind: blockNotice, text: "история диалога очищена"})
	return nil
}

// compactCmd — /compact [N]: оставить в истории последние N сообщений.
func (m *Model) compactCmd(arg string) tea.Cmd {
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
	switch {
	case !hasCap(m.modelCaps, "tools"):
		fmt.Fprintf(&b, "Модель %s не поддерживает вызов инструментов — они не передаются серверу.\n", m.modelName)
	case len(m.modelCaps) > 0 && !m.modelRealTools:
		fmt.Fprintf(&b, "Модель %s объявляет поддержку инструментов, но её сборка не умеет их "+
			"разбирать: нет RENDERER и PARSER. Инструменты ей не передаются, найденное "+
			"в книгах подмешивается к вопросу.\n", m.modelName)
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
	fmt.Fprintf(&b, "Ограничение итераций: %s. Таймаут команд: %s.\n"+
		"Изменить на сеанс: /tools iterations <число|off>",
		iterationsHuman(m.cfg.Agent.MaxIterations), m.cfg.Agent.BashTimeoutDuration())
	return b.String()
}

// iterationsHuman — предел вызовов инструментов человеческими словами.
func iterationsHuman(n int) string {
	if n < 0 {
		return "без ограничения"
	}
	return fmt.Sprintf("%d", n)
}

// toolsCmd — /tools [iterations <число|off>].
//
// Предел вызовов меняется прямо из диалога, потому что упирается в него человек
// посреди работы: обход документации или разбор каталога делает вызовов больше,
// чем предполагает умолчание, и заставлять человека выходить, править конфиг
// и запускаться заново — значит терять начатый разговор.
//
// В конфиг это не пишется: сеансовая настройка, как и /think, /mouse, /mode.
func (m *Model) toolsCmd(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockNotice, text: m.toolsReport()})
		return nil
	}
	switch strings.ToLower(fields[0]) {
	case "iterations", "итерации", "предел":
		if len(fields) < 2 {
			m.addBlock(block{kind: blockNotice, text: "предел вызовов инструментов: " +
				iterationsHuman(m.cfg.Agent.MaxIterations) +
				"\n  /tools iterations 50    поднять предел\n" +
				"  /tools iterations off   снять предел совсем"})
			return nil
		}
		v := strings.ToLower(fields[1])
		if v == "off" || v == "выкл" || v == "нет" {
			m.cfg.Agent.MaxIterations = -1
			m.addBlock(block{kind: blockNotice, text: "предел вызовов инструментов снят на этот сеанс. " +
				"Прервать затянувшийся ход — Esc"})
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			m.addBlock(block{kind: blockError, text: "нужно число больше нуля или off: /tools iterations 50"})
			return nil
		}
		m.cfg.Agent.MaxIterations = n
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"предел вызовов инструментов на этот сеанс: %d", n)})
		return nil
	}
	m.addBlock(block{kind: blockError, text: "не понял: /tools [iterations <число|off>]"})
	return nil
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
// contextCmd — сведения об окне контекста и его изменение на сеанс.
//
// Одна команда вместо двух: `/context` показывает, `/context set|add|max`
// меняет. Прежняя `/addcontext` убрана — она делала ровно то же, что теперь
// `/context add`, но стояла в списке команд отдельно и находилась не там,
// где её ищут.
//
// Во всех трёх случаях значение **ограничивается максимумом модели**: Ollama
// на запрос большего окна не ругается, а молча берёт своё, и человек остаётся
// в уверенности, что у него 256k, когда у модели 40 960.
func (m *Model) contextCmd(arg string) tea.Cmd {
	fields := strings.Fields(strings.TrimSpace(arg))
	if len(fields) == 0 {
		m.addBlock(block{kind: blockNotice, text: m.contextReport()})
		return nil
	}

	sub := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), fields[0]))

	switch sub {
	case "max":
		if m.modelMaxCtx <= 0 {
			m.addBlock(block{kind: blockError, text: "/context max: максимум модели неизвестен — " +
				"дождитесь ответа сервера о модели (/info) или задайте num_ctx в конфиге"})
			return nil
		}
		m.applyContext(m.modelMaxCtx, false)
		return nil

	case "set", "add":
		if rest == "" {
			m.addBlock(block{kind: blockError, text: "использование: /context " + sub +
				" 256k — можно числом (262144), можно с буквой k или к"})
			return nil
		}
		n, err := parseTokens(rest)
		if err != nil {
			m.addBlock(block{kind: blockError, text: "/context " + sub + ": " + err.Error()})
			return nil
		}
		target := n
		if sub == "add" {
			cur, ok := m.currentCtx()
			if !ok {
				m.addBlock(block{kind: blockError, text: "/context add: текущее окно неизвестно — " +
					"дождитесь ответа сервера о модели или задайте num_ctx в конфиге"})
				return nil
			}
			target = cur + n
		}
		clamped := false
		if m.modelMaxCtx > 0 && target > m.modelMaxCtx {
			target, clamped = m.modelMaxCtx, true
		}
		m.applyContext(target, clamped)
		return nil
	}

	m.addBlock(block{kind: blockError, text: "/context: не знаю подкоманду «" + sub + "».\n" +
		"  /context           — сведения об окне\n" +
		"  /context set 256k  — поставить окно\n" +
		"  /context add 32k   — прибавить к окну\n" +
		"  /context max       — наибольшее окно модели"})
	return nil
}

// currentCtx — окно, действующее прямо сейчас.
//
// Сначала num_ctx запроса (его и шлём серверу), потом ёмкость, о которой
// сообщил сервер: до первого ответа второго ещё нет.
func (m *Model) currentCtx() (int, bool) {
	if n, ok := m.server.NumCtx(); ok && n > 0 {
		return n, true
	}
	if m.meter.Capacity > 0 {
		return m.meter.Capacity, true
	}
	return 0, false
}

// applyContext ставит новое окно и объясняет последствия.
//
// Об ограничении максимумом сообщается **подсказкой**, а не обычной строкой:
// человек просил одно, получил другое, и заметить это он должен сразу.
func (m *Model) applyContext(target int, clamped bool) {
	cur, _ := m.currentCtx()
	if target == cur {
		text := fmt.Sprintf("окно контекста уже равно %d токенам", cur)
		if clamped {
			text = fmt.Sprintf("окно контекста уже равно максимуму модели — %d токенов", cur)
		}
		m.addBlock(block{kind: blockNotice, text: text})
		return
	}

	m.setNumCtx(target)

	if clamped {
		m.addBlock(block{kind: blockHint, text: fmt.Sprintf(
			"запрошено больше, чем умеет модель: максимум %d токенов, окно поставлено в него",
			m.modelMaxCtx)})
	}
	text := fmt.Sprintf("окно контекста: %d → %d токенов", cur, target)
	text += "\nНовое значение вступит в силу со следующим вопросом: сервер перезагрузит" +
		" модель с этим окном, это займёт время.\nЕсли видеопамяти не хватит, часть модели" +
		" уедет в оперативную и скорость упадёт — посмотреть можно командой /ps." +
		"\nВ конфиг не записано, только на этот сеанс."
	m.addBlock(block{kind: blockNotice, text: text})
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

// parseTokens — разбор записи числа токенов. Живёт в config, потому что
// то же самое читает ключ --num-ctx командной строки.
func parseTokens(s string) (int, error) { return config.ParseTokens(s) }

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
		m.fail("/add", err)
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
		m.fail("/add", err)
		return nil
	}
	rel := m.guard.Sandbox().Rel(abs)
	maxPDF, maxFile := m.guard.Sandbox().MaxPDFBytes(), m.guard.Sandbox().MaxFileBytes()
	canView := m.registry != nil && m.registry.Has(tools.NameViewImage)

	// Чтение и разбор идут в фоне: PDF до max_pdf_mb разбирается секундами,
	// и держать на этом ленту нельзя (этап 91, R6.4). В историю кладёт
	// обработчик attachMsg — уже в цикле событий.
	return func() tea.Msg {
		// Документ прикладывается извлечённым текстом, а не байтами файла.
		if kind := document.DetectFile(abs); kind != document.KindNone {
			doc, err := document.Read(abs, maxPDF)
			if err != nil {
				return errorMsg{err: fmt.Errorf("/add: %w", err)}
			}
			return attachMsg{rel: rel, body: doc.Header(canView) + "\n\n" + doc.Text,
				notice: strings.ToLower(string(kind)) + " " + rel + " приложен к контексту"}
		}
		if info.Size() > maxFile {
			return errorMsg{err: fmt.Errorf("/add: файл слишком велик: %d байт, предел %d", info.Size(), maxFile)}
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/add: %w", err)}
		}
		return attachMsg{rel: rel, body: string(data), notice: "файл " + rel + " приложен к контексту"}
	}
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
		pattern := m.cfg.Log.FilePattern
		if pattern == "" {
			pattern = m.cfg.Log.Pattern + " (устаревшая раскладка Go, см. file_pattern)"
		}
		text := fmt.Sprintf("Журнал %s\n  файл: %s\n  каталог: %s\n  шаблон имени: %s",
			state, m.logger.CurrentPath(), m.cfg.Log.Dir, pattern)
		if err := m.logger.LastError(); err != nil {
			text += "\n  последняя ошибка: " + err.Error()
		}
		m.addBlock(block{kind: blockNotice, text: text})
	default:
		m.addBlock(block{kind: blockError, text: "использование: /log [on|off]"})
	}
	return nil
}

// idCmd показывает идентификатор обмена: по нему запись в журнале находится
// целиком — вопрос, вызовы инструментов, рассуждения и ответ помечены одним
// значением.
func (m *Model) idCmd() tea.Cmd {
	var b strings.Builder
	fmt.Fprintf(&b, "Сеанс %s, обменов: %d", m.logger.SessionID(), m.logger.Turns())
	if m.logger.Turns() > 0 {
		fmt.Fprintf(&b, "\n  последний обмен: %s", m.logger.LastTurnID())
	} else {
		b.WriteString("\n  вопросов ещё не было")
	}
	if m.logger.Enabled() {
		path := m.logger.CurrentPath()
		fmt.Fprintf(&b, "\n  журнал: %s", path)
		// В подсказке — имя файла, а не полный путь: путь уже строкой выше,
		// а вместе с ним команда не влезает в ширину ленты.
		fmt.Fprintf(&b, "\n  найти обмен целиком: grep \"^\\[%s\\]\" %s",
			m.logger.LastTurnID(), filepath.Base(path))
	} else {
		b.WriteString("\n  журнал выключен — записи не ведутся")
	}
	m.addBlock(block{kind: blockNotice, text: b.String()})
	return nil
}

// newClient создаёт клиента по описанию сервера.
func newClient(srv *config.Server) *ollama.Client {
	return ollama.NewWithStall(srv.URL, srv.TimeoutDuration(), srv.ChatTimeoutDuration(),
		srv.StallTimeoutDuration(), srv.Headers)
}

// shortTime приводит метку времени Ollama к виду ЧЧ:ММ.
func shortTime(s string) string {
	if len(s) >= 16 {
		return strings.Replace(s[11:16], "T", " ", 1)
	}
	return s
}
