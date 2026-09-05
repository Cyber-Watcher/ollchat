package ui

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/confluence"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
	"github.com/Cyber-Watcher/ollchat/internal/vram"
)

// gens — поколения фоновых работ: ответ на брошенный вопрос, сервер или
// поиск отбрасывается, если его поколение не совпало с текущим. Раньше пять
// счётчиков лежали россыпью среди девяноста полей Model (этап 91, R6.11).
type gens struct {
	run     int // ход вопроса
	srv     int // смена сервера
	mix     int // подмешивание
	find    int // поиск /search
	job     int // индексация книг
	compact int // сжатие истории сводкой
}

// kbState — база знаний в этом сеансе.
type kbState struct {
	base   *kb.Base
	coll   *kb.Collection
	use    string // выбранная коллекция
	picked bool   // выбрана сама, а не настройкой: единственная в базе
	autoOn bool   // подмешивать найденное перед каждым вопросом
	emb    *kbembed.Embedder
	embURL string // адрес, под который собран эмбеддер
}

// graphState — открытый граф и подмешивание карты понятий.
type graphState struct {
	autoOn bool
	open   *graph.Graph
	dir    string
	stamp  int64
	cache  *graph.Cache
}

// Model — состояние TUI.
type Model struct {
	// gen — поколения фоновых работ.
	gen gens

	cfg      *config.Config
	guard    *permissions.Guard
	registry *tools.Registry

	// embed, embedModel — состояние модели эмбеддингов для строки состояния.
	// Проверяется при запуске и дальше по кругу (kb.embed_check): смысловой
	// поиск отваливается молча, и знать об этом надо сразу, а не по качеству
	// ответов.
	embed      embedState
	embedModel string

	// rerank — состояние службы переранжирования. Показывается коротким «rr»:
	// в строке состояния тесно, а имя службы там не нужно.
	rerank embedState

	// live — числа отбора, меняемые на ходу командами /graph tune и /kb tune.
	// Тот же экземпляр держит реестр инструментов, поэтому крутилка действует
	// сразу и на поиск в диалоге, и на инструменты модели, и на подмешивание.
	live   *tools.Live
	logger *chatlog.Logger
	steps  *steplog.Writer // журнал шагов; nil — выключен
	store  *session.Store
	conv   *session.Conversation
	meter  ctxmeter.Meter

	// vramProfile — замеры olldiagtools, если они есть. Их читает /calc.
	vramProfile *vram.Profile

	// Текущий сервер и модель.
	server *config.Server
	client *ollama.Client

	// Загрузка модели в память карты: пока её там нет, ответа не будет вовсе,
	// и это надо отличать от зависания. Подробности — в modelload.go.
	residency ollama.Residency
	gotOutput bool // пришёл ли хоть один кусок ответа или рассуждения
	// saidLoad — про эту загрузку уже объяснили.
	//
	// Сбрасывается не при каждом вопросе, а когда модель **увидена
	// загруженной**: иначе предупреждение со старта и объяснение посреди
	// ожидания сказали бы человеку одно и то же дважды подряд. Зато после
	// выгрузки по таймауту он предупреждается снова — а это уже другая загрузка.
	saidLoad  bool
	modelName string
	modelCaps []string
	// modelRealTools — умеет ли модель вызывать инструменты на самом деле.
	modelRealTools bool
	// modelMaxCtx — окно, на которое модель обучена (из /api/show). Это не то же
	// самое, что действующая ёмкость: она задаётся num_ctx и может быть меньше.
	modelMaxCtx int
	srvVersion  string
	models      []ollama.ModelInfo

	// Виджеты.
	vp   viewport.Model
	ta   textarea.Model
	spin spinner.Model
	rend *renderer

	// hist — перебор набранного стрелками, как в оболочке. Живёт один запуск,
	// на диск не пишется; устройство и причины — в history.go.
	hist inputHistory

	// cfSession — токен Confluence на сеанс, если его задали командой.
	cfSession *confluence.Session

	width, height int
	ready         bool

	// Лента диалога.
	blocks   []block
	rendered []string

	// Состояние генерации.
	streaming  bool
	events     <-chan agent.Event
	cancel     context.CancelFunc
	liveIdx    int // индекс блока ответа, который сейчас наполняется
	thinkIdx   int // индекс блока рассуждений текущего ответа
	turnAnswer strings.Builder
	turnThink  strings.Builder
	stats      ollama.Stats
	// speed — счётчик скорости, пока идёт ответ. Итог приходит от сервера
	// в конце обмена, а до него цифру даёт только он.
	speed      liveSpeed
	startedAt  time.Time
	iterations int
	// answeredBy — модель, которой был отправлен текущий вопрос. Запоминается
	// на момент отправки: за время ответа выбранная модель может смениться,
	// а в журнале должна остаться та, что действительно отвечала.
	answeredBy string
	// turnID — идентификатор текущего обмена, выданный журналом в send.
	// Им помечаются блоки ленты, чтобы копия по Shift+F5 и запись в файле
	// сослались на один и тот же обмен.
	turnID string

	// Подтверждение действия.
	confirm       *agent.ConfirmRequest
	confirmScroll int

	// pending — картинки, вставленные в ещё не отправленный вопрос.
	// Живут до отправки: в сообщение попадут только те, чьи метки остались
	// в тексте.
	pending []pendingImage

	// pastes — большие вставки из буфера обмена, свёрнутые в метку
	// «[Текст01: 4321 знак]». Живут до отправки: в вопрос попадут только те,
	// чьи метки остались в тексте. Устройство — в pastebig.go.
	pastes []pastedText

	// Выбор из списка (серверы, модели, сессии).
	picker *picker

	// files — список файлов под токеном «@…» в строке ввода. Показывается
	// над полем ввода и не забирает набор текста себе.
	files *fileMenu

	// cmds — подсказка по командам, пока строка ввода начинается с «/».
	// Отдельным полем, а не общей панелью с files: открываются они по разным
	// признакам и одновременно быть не могут, но проверять это в одном месте
	// проще, чем разделять одно поле на два смысла.
	cmds *cmdMenu

	// savePDF — окно запроса имени файла при сохранении ответа в PDF (F4).
	// Пока открыто, забирает себе клавиатуру и заменяет собой поле ввода.
	savePDF *savePDFPrompt

	// finds — последняя выдача /search: по ней работают панель Ctrl+F,
	// /read <номер> и подсказки под выдачей. Держится ровно одна: две подряд
	// путали бы, к какой относится номер.
	finds     []find.Excerpt
	findQuery string
	findPane  *findPanel

	// review — окно разбора пар (/graph review), см. reviewpanel.go.
	review *reviewPanel

	// images — панель вложенных изображений (F3). Стоит там же, где список
	// файлов, и потому взаимно исключает его.
	images *imagePanel

	// draggingBar — пользователь удерживает бегунок полосы прокрутки.
	draggingBar bool

	// mouseOn — запрошен ли у терминала отчёт о мыши. Пока он включён, колесо
	// и бегунок работают, но выделить текст мышью нельзя: события достаются
	// приложению, а не терминалу. Переключается клавишей F2 и командой /mouse.
	mouseOn bool

	showThinking bool
	think        *bool
	statusMsg    string
	quitConfirm  bool

	// osc52Noted — подсказка про запасной путь копирования уже показана.
	// Второй раз за сеанс её показывать незачем: буфер обмена всё равно
	// обслуживается тем же способом до конца работы.
	osc52Noted bool

	// sshPasteNoted — подсказка про вставку картинок по SSH уже показана.
	// Ровно та же причина: за сеанс графическая сессия не появится.
	sshPasteNoted bool

	// База знаний по книгам. Коллекция открывается по требованию и держится
	// открытой: у большой коллекции чтение указателей занимает секунды.
	// kb — база знаний в этом сеансе, см. kbState.
	kb kbState

	// healthAdvice — что не в порядке с графом и смыслами. Считается в фоне,
	// обновляется раз в healthEvery. Пусто — всё хорошо.
	healthAdvice []string
	// healthShown — что уже показано в ленте: одно и то же не повторяем,
	// иначе сообщение об известной беде будет всплывать каждые десять минут.
	healthShown string
	// healthWaiting — сообщение придержано до конца ответа модели: влезать
	// в середину генерации нельзя, человек читает ответ.
	healthWaiting bool

	// Граф понятий поверх коллекции. Держится открытым между вопросами:
	// чтение реестра понятий это десятки мегабайт, а привратник смотрит в него
	// на каждый вопрос. graphStamp — размер реестра, по которому замечается
	// доливка графа идущей рядом сборкой.
	// gr — открытый граф и подмешивание карты понятий, см. graphState.
	gr graphState

	// Подмешивание считается отдельной командой, вне цикла событий: открытие
	// графа это десятки секунд, и в обработчике Enter ему не место.
	// mixGen отделяет ответ на нынешний вопрос от ответа на отменённый.
	mixing        bool
	mixOpening    bool // придётся открывать граф — говорим об этом человеку
	pendingImages []pendingImage
	// Начало открытия графа: по нему в полосе хода видно, сколько уже идёт.
	graphBarStart time.Time

	// job — идущая долгая задача (индексация книг). Она живёт отдельно от хода
	// генерации: ресурсы разные, поэтому чат во время индексации работает.
	job *kbJob

	// archive — идущий архив коллекции с графом, см. archive.go.
	archive *archiveJob
	// archiveErrShown — какой отказ планового архива уже показан: один и тот
	// же каждые пять минут приучил бы не читать ленту.
	archiveErrShown string
	// heldNotes — заметки, придержанные до конца ответа модели.
	heldNotes []block
}

// New создаёт модель интерфейса.
func New(cfg *config.Config, guard *permissions.Guard, registry *tools.Registry,
	logger *chatlog.Logger, steps *steplog.Writer, store *session.Store, srv *config.Server, modelName string) *Model {

	ta := textarea.New()
	ta.Placeholder = inputPlaceholder
	ta.Prompt = "› "
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.Focus()
	// Enter отправляет сообщение, поэтому перенос строки переезжает на другие
	// клавиши. Именно переназначаем штатную привязку, а не вставляем "\n" руками:
	// вставка мимо Update не двигает внутренний просмотр поля, и курсор уезжает
	// ниже видимой области — со стороны это выглядит как «Alt+Enter не работает».
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	// Ctrl+F отбирается у поля ввода: там это «на символ вправо», и та же
	// стрелка остаётся на месте. Взамен клавиша открывает список найденного
	// последним поиском — привычка из emacs стоит дешевле, чем ещё одна
	// незанятая пара клавиш.
	ta.KeyMap.CharacterForward.SetKeys("right")
	applyCursor(&ta, cfg.Input.Cursor)

	// Курсив: терминал, который его не умеет, показывает вместо него инверсию —
	// строка выглядит залитой серым. Решается один раз при запуске, до первой
	// отрисовки; настройка theme.italic позволяет задать это руками.
	applyItalics(ItalicsEnabled(cfg.Theme.Italic, termName()))

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	m := &Model{
		cfg:      cfg,
		guard:    guard,
		registry: registry,
		live: tools.NewLive(
			graph.NeighborRank{
				SenseWeight: cfg.Graph.NeighborSenseWeight,
				Pool:        cfg.Graph.NeighborPool,
			},
			cfg.KB.TopK, cfg.KB.MaxPerBook, cfg.KB.MinCosine, cfg.KB.SemanticWeight),
		logger:    logger,
		steps:     steps,
		store:     store,
		conv:      session.New(srv.SystemPrompt),
		server:    srv,
		modelName: modelName,
		ta:        ta,
		spin:      sp,
		// Оформление собираем здесь, до старта цикла событий Bubble Tea:
		// стиль auto определяется запросом к терминалу — подробности
		// в комментарии к detectStyle.
		rend:         newRenderer(80, cfg.General.RenderMarkdown, cfg.Theme),
		showThinking: cfg.General.ShowThinking,
		think:        srv.Think,
		mouseOn:      cfg.Input.Mouse,
		liveIdx:      -1,
		thinkIdx:     -1,
	}
	m.conv.SetToday(cfg.General.Today)
	m.client = ollama.NewWithStall(srv.URL, srv.TimeoutDuration(), srv.ChatTimeoutDuration(),
		srv.StallTimeoutDuration(), srv.Headers)
	m.vramProfile = loadVRAMProfile(cfg)
	// База знаний открывается всегда: сама по себе она ничего не стоит,
	// а коллекции читаются по требованию.
	if base, err := kb.OpenBase(cfg.KB.Dir); err == nil {
		m.kb.base = base
		m.kb.use = cfg.KB.Default
		// Одна коллекция — выбирать нечего, и требовать /kb use ради этого
		// незачем: инструменты модели и так берут единственную. Без выбора
		// молчало бы и подмешивание, а человек об этом не догадался бы.
		if m.kb.use == "" {
			if names, err := base.Names(); err == nil && len(names) == 1 {
				m.kb.use = names[0]
				m.kb.picked = true
			}
		}
		m.kb.autoOn = cfg.Mix.Books
		m.gr.autoOn = cfg.Mix.Graph
	}
	if n, ok := srv.NumCtx(); ok {
		m.meter.SetCapacity(n, ctxmeter.SourceConfig)
	}
	// Советы при запуске можно выключить целиком: на машинах, где конфиг
	// сокращён намеренно, они советуют вернуть убранное — см. general.startup_hints.
	if cfg.General.HintsAtStartup() {
		if hint := toolDriftHint(cfg, registry); hint != "" {
			m.addBlock(block{kind: blockHint, text: hint})
		}
	}
	if cfg.General.HintsAtStartup() && cfg.Log.LegacyPatternIgnored() {
		m.addBlock(block{kind: blockHint, text: "в разделе [log] заданы обе настройки имени файла: " +
			"действует file_pattern = \"" + cfg.Log.FilePattern + "\", устаревшая pattern не влияет ни на что " +
			"и её можно убрать"})
	}
	return m
}

// SetLive подключает общий набор живых настроек.
//
// Реестр инструментов собирается раньше интерфейса, поэтому экземпляр заводится
// снаружи и отдаётся обоим: иначе крутилка меняла бы поиск в диалоге, а модель
// продолжала бы искать по-старому — расхождение, которое не заметишь.
// SetGraphCache отдаёт интерфейсу тот же кэш открытых графов, что и модели.
//
// Без него в памяти жили бы два одинаковых графа по 160 МБ: свой у инструментов
// и свой у интерфейса. Необязателен: при graph.cache = false кэша нет вовсе,
// и поиск открывает граф сам.
func (m *Model) SetGraphCache(c *graph.Cache) { m.gr.cache = c }

func (m *Model) SetLive(l *tools.Live) {
	if l != nil {
		m.live = l
	}
}

// sections отдаёт проверку «есть ли раздел в конфиге» для списка команд.
//
// Команды возможности, которую человек в конфиге не описал, в меню и справке
// не показываются: на машине без библиотеки книг `/kb` и `/graph` только
// сбивают с толку — их видно, их зовут, и они отвечают «коллекций нет».
func (m *Model) sections() sectionCheck {
	if m.cfg == nil {
		return nil
	}
	return m.cfg.Has
}

// toolDriftHint предупреждает, что сборка умеет больше, чем разрешает конфиг.
//
// Список agent.tools заменяет значение по умолчанию целиком — иначе нельзя было
// бы осознанно отказаться от правил deny. Побочное следствие: конфиг, однажды
// созданный --init-config, навсегда замораживает набор возможностей, и новые
// инструменты в него не попадают никогда.
//
// Молчать об этом дорого. Живой случай: подсказка к документу звала посмотреть
// картинку инструментом view_image, которого в конфиге той машины не было;
// модель приняла имя за программу и попросила запустить его через оболочку.
func toolDriftHint(cfg *config.Config, registry *tools.Registry) string {
	if !cfg.Agent.Enabled || registry == nil || len(registry.Names()) == 0 {
		return ""
	}
	var off []string
	for _, n := range tools.AllNames() {
		if !registry.Has(n) {
			off = append(off, n)
		}
	}
	if len(off) == 0 {
		return ""
	}
	return fmt.Sprintf("Выключены инструменты: %s. Они есть в сборке, но не перечислены "+
		"в agent.tools — вероятно, файл настроек создан прежней версией. "+
		"Чтобы включить, допишите их в %s; что делает каждый — /tools.",
		strings.Join(off, ", "), cfg.Path)
}

// Init запускает начальные команды.
//
// Приветственную строку не выводим: сервер, модель и каталог уже показаны
// в шапке, а подсказка про /help есть в поле ввода.
func (m *Model) Init() tea.Cmd {
	warm := make(chan graph.OpenProgress, 64)
	m.graphBarStart = time.Now()
	return tea.Batch(
		textarea.Blink,
		m.spin.Tick,
		m.loadServerInfoCmd(),
		m.kbPendingCmd(),
		// Проверка эмбеддера: смысловой поиск отваливается молча, и человек
		// узнаёт об этом по ухудшившимся ответам, а не по сообщению.
		m.checkEmbedderCmd(),
		m.checkRerankerCmd(),
		// Состояние графа и смыслов: килобайт чтения, но в фоне — запуск
		// не должен ждать ни диска, ни разбора файлов.
		m.checkGraphHealthCmd(),
		// Загружена ли модель — спрашиваем сразу, не дожидаясь первого вопроса:
		// иначе ответ приходит уже посреди ожидания, когда человек успел
		// решить, что программа зависла. Подробности — в modelload.go.
		checkResidency(genStartup, m.client, m.modelName),
		// Граф открывается заранее, в фоне: иначе за его открытие платит
		// первый же вопрос — десятки секунд ожидания там, где человек ждёт
		// ответа модели.
		m.warmGraphCmd(warm),
		waitGraphProgress(genWarm, warm),
		// Плановый архив коллекции с графом: первая проверка через полминуты,
		// дальше раз в пять минут. См. archive.go.
		archiveTickCmd(archiveFirstCheck),
	)
}

// ── Сообщения ────────────────────────────────────────────────────────────────

type agentEventMsg struct {
	gen int
	ev  agent.Event
}
type streamClosedMsg struct{ gen int }
type noticeMsg struct{ text string }

// fail показывает ошибку команды с её именем: «нет такого файла» без
// указания, какая команда и что искала, читается как загадка (этап 91, R6.7).
func (m *Model) fail(where string, err error) {
	m.addBlock(block{kind: blockError, text: where + ": " + err.Error()})
}

// attachMsg — файл прочитан в фоне и готов лечь в историю (этап 91, R6.4).
type attachMsg struct{ rel, body, notice string }

// graphRemovedMsg — граф удалён в фоне; модели осталось закрыть свой экземпляр.
type graphRemovedMsg struct {
	name string
	size int64
}

// compactDoneMsg — сводка истории готова (или не получилась); вопрос,
// ради которого сжимали, отправляется следом (этап 91, R9.1).
type compactDoneMsg struct {
	gen     int
	text    string
	summary string
	stats   ollama.Stats
	err     error
}

// sessionLoadedMsg — сохранённая сессия прочитана с диска (этап 91, R6.6).
type sessionLoadedMsg struct{ rec *session.Saved }

// rawMsg — готовый текст в ленту, каким его составили.
//
// Отличие от noticeMsg: лента его не переносит по ширине и не красит. Нужен
// тем, кто уже разложил текст сам — отчёту `/kb doctor` с его отступами
// и цветными названиями книг.
type rawMsg struct{ text string }
type errorMsg struct{ err error }

type serverInfoMsg struct {
	gen     int
	version string
	models  []ollama.ModelInfo
	err     error
}

type modelInfoMsg struct {
	gen   int
	model string
	caps  []string
	// realTools — модель действительно умеет вызывать инструменты, а не просто
	// объявляет это в списке возможностей. Разбор — в ollama.ShowResponse.RealTools.
	realTools bool
	capacity  int
	maxCtx    int // на какое окно модель обучена
	source    ctxmeter.CapacitySource
	err       error
}

// modelsAction — что делать со свежим списком моделей.
type modelsAction int

const (
	modelsOpenPicker modelsAction = iota // показать список выбора
	modelsSelect                         // переключиться на модель из target
)

// modelsMsg — свежий список моделей с сервера.
type modelsMsg struct {
	gen    int
	models []ollama.ModelInfo
	err    error
	action modelsAction
	target string // имя модели для modelsSelect
}

// ── Команды ──────────────────────────────────────────────────────────────────

// loadServerInfoCmd опрашивает версию сервера и список моделей.
func (m *Model) loadServerInfoCmd() tea.Cmd {
	client := m.client
	gen := m.gen.srv
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		version, err := client.Version(ctx)
		if err != nil {
			return serverInfoMsg{gen: gen, err: err}
		}
		models, err := client.Tags(ctx)
		if err != nil {
			return serverInfoMsg{gen: gen, version: version, err: err}
		}
		return serverInfoMsg{gen: gen, version: version, models: models}
	}
}

// loadModelInfoCmd определяет возможности модели и ёмкость контекстного окна.
func (m *Model) loadModelInfoCmd() tea.Cmd {
	client := m.client
	model := m.modelName
	gen := m.gen.srv
	cfgCtx, hasCfgCtx := m.server.NumCtx()
	tagsCache := m.models

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		out := modelInfoMsg{gen: gen, model: model}

		show, err := client.Show(ctx, model)
		if err != nil {
			out.err = err
		} else {
			out.caps = show.Capabilities
			// Строка «tools» в описании бывает ложной: у сборки нет ни
			// RENDERER, ни PARSER, и вызвать инструмент модель не сможет
			// (DeepSeekFakeTools.md). Проверяем по описанию сборки,
			// а не по объявлению.
			out.realTools = show.RealTools()
			// Максимум модели забираем всегда, независимо от того, какой
			// источник победит в определении действующей ёмкости.
			if n, ok := ollama.ContextLengthFromShow(show); ok {
				out.maxCtx = n
			}
		}

		// Ёмкость окна: значение из конфига авторитетнее, потому что именно оно
		// отправляется в options запроса и заставляет сервер загрузить модель
		// с этим размером окна.
		switch {
		case hasCfgCtx:
			out.capacity, out.source = cfgCtx, ctxmeter.SourceConfig
		default:
			if running, err := client.PS(ctx); err == nil {
				for _, r := range running {
					if r.Name == model || r.Model == model {
						if r.ContextLength > 0 {
							out.capacity, out.source = r.ContextLength, ctxmeter.SourcePS
						}
					}
				}
			}
			if out.capacity == 0 && show != nil {
				if n, ok := ollama.ContextLengthFromShow(show); ok {
					out.capacity, out.source = n, ctxmeter.SourceShow
				}
			}
			if out.capacity == 0 {
				for _, t := range tagsCache {
					if t.Name == model && t.Details.ContextLength > 0 {
						out.capacity, out.source = t.Details.ContextLength, ctxmeter.SourceTags
					}
				}
			}
		}
		return out
	}
}

// refreshModelsCmd запрашивает список моделей у сервера прямо сейчас.
//
// Список нельзя брать из того, что было прочитано при запуске: модели на
// сервере добавляют и удаляют, и приложение показывало бы несуществующие.
func (m *Model) refreshModelsCmd(action modelsAction, target string) tea.Cmd {
	client := m.client
	gen := m.gen.srv
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		models, err := client.Tags(ctx)
		return modelsMsg{gen: gen, models: models, err: err, action: action, target: target}
	}
}

// waitForEvent читает очередное событие агента. Номер поколения едет вместе
// с событием, чтобы получатель мог отличить свой ход от прерванного.
func waitForEvent(gen int, ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{gen: gen}
		}
		return agentEventMsg{gen: gen, ev: ev}
	}
}

// ── Работа с лентой ──────────────────────────────────────────────────────────

// addBlockAndShow добавляет блок и перематывает ленту к нему.
//
// Для того, что человек попросил показать прямо сейчас: прочитанный кусок,
// открытая карточка. Обычный addBlock уважает прокрутку — если человек ушёл
// вверх читать, ответ модели не должен дёргать его вниз, — но здесь наоборот:
// не показать по просьбе значит не выполнить её.
func (m *Model) addBlockAndShow(b block) int {
	i := m.addBlock(b)
	if m.ready {
		m.vp.GotoBottom()
	}
	return i
}

func (m *Model) addBlock(b block) int {
	m.blocks = append(m.blocks, b)
	m.rendered = append(m.rendered, m.rend.Render(b, m.showThinking))
	m.refreshViewport(true)
	return len(m.blocks) - 1
}

func (m *Model) updateBlock(i int, b block) {
	if i < 0 || i >= len(m.blocks) {
		return
	}
	m.blocks[i] = b
	m.rendered[i] = m.rend.Render(b, m.showThinking)
	m.refreshViewport(true)
}

func (m *Model) rerenderAll() {
	m.rendered = m.rendered[:0]
	for _, b := range m.blocks {
		m.rendered = append(m.rendered, m.rend.Render(b, m.showThinking))
	}
	m.refreshViewport(false)
}

func (m *Model) refreshViewport(stick bool) {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	parts := make([]string, 0, len(m.rendered))
	for _, r := range m.rendered {
		if strings.TrimSpace(r) == "" {
			continue
		}
		parts = append(parts, r)
	}
	m.vp.SetContent(strings.Join(parts, "\n\n"))
	if stick && atBottom {
		m.vp.GotoBottom()
	}
}

// relayout пересчитывает высоту ленты под текущий состав панелей.
//
// Вызывать обязательно при каждом появлении и исчезновении панели над полем
// ввода. Забыть — значит собрать экран выше окна терминала: нижние зоны просто
// уезжают за нижний край, и это выглядит как «панель вылезла за экран».
func (m *Model) relayout() {
	if !m.ready {
		return
	}
	// Слежение за концом ленты проверяется ДО смены высоты.
	//
	// Замер 30.08.2026: `refreshViewport` спрашивает `AtBottom()` уже после
	// `SetHeight`, а панель, забравшая пять строк, сдвигает низ содержимого —
	// и лента, стоявшая в конце, оказывается «прокрученной вверх». Дальше
	// добавленное в ленту молча уходило за нижний край: человек открывал
	// панель найденного, нажимал Enter и видел, что «ничего не читается»,
	// хотя кусок был прочитан и лежал ниже экрана.
	atBottom := m.vp.AtBottom()
	m.vp.SetHeight(m.viewportHeight())
	m.refreshViewport(true)
	if atBottom {
		m.vp.GotoBottom()
	}
}

// inputPlaceholder — обычная подсказка поля ввода. Названа отдельно потому,
// что на время открытия графа её место занимает полоса хода, и вернуть надо
// ровно эту строку, а не похожую.
const inputPlaceholder = "Спросите модель или введите /help для списка команд"

// showGraphBar рисует полосу хода открытия графа в строке ввода.
//
// Полоса появляется ТОЛЬКО на настоящем открытии: сообщения о ходе идут из
// самого чтения файлов графа, и когда граф уже в памяти, их нет вовсе.
func (m *Model) showGraphBar(p graph.OpenProgress) {
	width := m.width
	if width <= 0 {
		width = 80
	}
	m.ta.Placeholder = graphBarText(p, time.Since(m.graphBarStart), width)
}

// hideGraphBar возвращает полю ввода обычную подсказку.
func (m *Model) hideGraphBar() {
	m.ta.Placeholder = inputPlaceholder
}

// ── Отправка запроса ─────────────────────────────────────────────────────────

// send отправляет вопрос модели.
func (m *Model) send(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Картинки, на которые ссылается текст. Проверяем до записи чего-либо:
	// отказ должен возвращать пользователю его вопрос целиком, а не половину.
	images := m.imagesFor(text)
	if len(images) > 0 && m.visionUnsupported() {
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"модель %s не умеет смотреть изображения — выберите модель с возможностью vision (Ctrl+R)",
			m.modelName)})
		m.ta.SetValue(text)
		return nil
	}

	// Свёрнутые вставки разворачиваются здесь, а не в поле ввода: в промпте
	// стояла метка, но модели, ленте и журналу нужен текст целиком. Позже
	// проверки vision, потому что её отказ возвращает вопрос в поле — и вернуть
	// он обязан метку, а не сто строк, ради которых всё и затевалось.
	text = m.expandPastes(text)
	m.pastes = nil

	// Окно почти полно — сперва сжать историю сводкой, потом спрашивать
	// (этап 91, R9.1). В агентном режиме — отказ с подсказкой.
	if cmd, wait := m.compactBeforeSend(text); wait {
		return cmd
	}

	// Обмен начинается здесь: журнал пометит этим идентификатором всё до
	// EndTurn — вопрос, вложения, вызовы инструментов, рассуждения и ответ.
	m.turnID = m.logger.BeginTurn()

	// Одно время на блок ленты и на запись журнала: копирование по Shift+F5
	// собирает запись из блока, и отметки обязаны совпасть с журналом.
	asked := time.Now()
	m.addBlock(block{kind: blockUser, text: text, at: asked, turn: m.turnID})
	if err := m.logger.WriteAt(asked, chatlog.KindQuestion, text); err != nil {
		m.statusMsg = "журнал: " + err.Error()
	}

	// Подмешивание считается ОТДЕЛЬНОЙ командой, а не здесь.
	//
	// Внутри него открытие графа — 41 секунда на библиотеке из 465 тысяч кусков
	// (замер 02.09.2026), плюс поиск по книгам, запрос к эмбеддеру и вторая
	// ступень. Пока это делалось прямо в обработчике Enter, всё стояло внутри
	// цикла событий Bubble Tea: интерфейс не перерисовывался, и человек, нажав
	// Enter, видел ровно ничего — ни своего вопроса, ни признака работы.
	// Больше всех платил первый вопрос после запуска: граф ещё не открыт.
	if job, ok := m.mixPlan(); ok {
		m.gen.mix++
		m.mixing = true
		m.mixOpening = job.needsOpen()
		m.pendingImages = images
		prog := make(chan graph.OpenProgress, 64)
		m.graphBarStart = time.Now()
		return tea.Batch(runMixCmd(m.gen.mix, text, job, prog),
			waitGraphProgress(m.gen.mix, prog), m.spin.Tick)
	}

	return m.startExchange(text, images, mixResult{})
}

// startExchange отправляет вопрос модели вместе с уже посчитанным подмесом.
//
// Отделено от send потому, что между ними лежит ожидание: подмешивание
// считается командой, и продолжить обмен можно только когда она ответит.
func (m *Model) startExchange(text string, images []pendingImage, mix mixResult) tea.Cmd {
	// Карта понятий и выдержки из книг уходят отдельным сообщением ПЕРЕД
	// вопросом. Так библиотека работает и с моделями без поддержки
	// инструментов. Серая строка под вопросом говорит, что именно ушло.
	if !mix.Empty() {
		m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: mix.Text})
		m.addBlock(block{kind: blockNotice, text: mixLine(mix)})
	}

	msg := ollama.Message{Role: ollama.RoleUser, Content: text}
	for _, p := range images {
		msg.Images = append(msg.Images, p.base64())
		// В журнале от картинки остаётся описание: сами байты туда не кладём,
		// но по записи должно быть понятно, что модель видела не только текст.
		_ = m.logger.Write(chatlog.KindSystem, "Вложение "+p.label()+": "+p.describe())
	}
	m.conv.Append(msg)
	m.dropPendingImages()

	// Пока сервер не сообщил точные счётчики, показываем оценку по объёму истории.
	if !m.meter.Exact || m.meter.Used == 0 {
		m.meter.Used = ctxmeter.EstimateChars(m.conv.EstimatedChars())
		m.meter.Exact = false
	}

	runner := &agent.Runner{
		Client:          m.client,
		Model:           m.modelName,
		KeepAlive:       m.server.KeepAlive,
		Options:         m.server.Options,
		Think:           m.effectiveThink(),
		Tools:           m.registry,
		Guard:           m.guard,
		MaxIterations:   m.cfg.Agent.MaxIterations,
		MaxRetries:      m.cfg.Agent.MaxRetries,
		ToolsSupported:  m.toolsSupported(),
		VisionSupported: !m.visionUnsupported(),
		Steps:           m.steps,
		Turn:            m.turnID,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.gen.run++
	m.events = runner.Run(ctx, m.conv)
	m.streaming = true
	m.answeredBy = m.modelName
	// residency НЕ сбрасываем: ответ со старта (или с прошлого обмена) верен
	// с точностью до выгрузки по таймауту, а свежая проверка уточнит его через
	// доли секунды. Обнулив здесь, мы вернули бы ровно ту немоту, из-за которой
	// всё и затевалось.
	m.gotOutput = false
	m.startedAt = time.Now()
	m.speed.Start(m.startedAt)
	m.liveIdx = -1
	m.thinkIdx = -1
	m.iterations = 0
	m.turnAnswer.Reset()
	m.turnThink.Reset()

	return tea.Batch(waitForEvent(m.gen.run, m.events), m.spin.Tick,
		checkResidency(m.gen.run, m.client, m.modelName))
}

// toolsSupported сообщает, можно ли передавать модели инструменты.
//
// Проверяется не только объявленная возможность, но и устройство сборки:
// у `deepseek-r1` в списке возможностей есть «tools», а RENDERER и PARSER
// у сборки нет, и вызов инструмента она не разберёт. Слать их такой модели —
// значит занять контекст описаниями, которыми она не воспользуется.
func (m *Model) toolsSupported() bool {
	if !m.cfg.Agent.Enabled || m.registry == nil || len(m.registry.Names()) == 0 {
		return false
	}
	return hasCap(m.modelCaps, "tools") && m.modelRealTools
}

// effectiveThink определяет значение поля think для запроса.
func (m *Model) effectiveThink() *bool {
	if !hasCap(m.modelCaps, "thinking") {
		return nil
	}
	return m.think
}

func hasCap(caps []string, name string) bool {
	for _, c := range caps {
		if c == name {
			return true
		}
	}
	return false
}

// stopStreaming прерывает генерацию и сразу освобождает интерфейс.
//
// Отменить контекст недостаточно: инструмент может отреагировать на отмену
// не мгновенно, а пользователь всё это время остаётся заперт — именно так
// приложение однажды провисело всю ночь на команде `dotnet run`. Поэтому ход
// закрывается здесь же, а события уже брошенного хода отбрасываются
// по номеру поколения.
func (m *Model) stopStreaming() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// Дочитываем канал брошенного хода, иначе его горутина может остаться
	// заблокированной на отправке события, которое никто не заберёт.
	if m.events != nil {
		go func(ch <-chan agent.Event) {
			for range ch {
			}
		}(m.events)
	}

	m.gen.run++
	m.events = nil
	m.confirm = nil
	m.confirmScroll = 0

	// Обмен могли прервать, пока считалось подмешивание: ответ команды придёт,
	// но относиться будет к брошенному вопросу. Отделяем его поколением, а
	// журнальный обмен закрываем здесь — иначе он остался бы открытым до конца
	// сеанса и пометил своим номером всё, что человек сделает дальше.
	if m.mixing {
		m.mixing = false
		m.mixOpening = false
		m.gen.mix++
		m.pendingImages = nil
		m.hideGraphBar()
		m.logger.EndTurn()
	}

	if m.streaming {
		m.finishTurn()
	}
}

// finishTurn завершает обмен: пишет ответ в журнал и сбрасывает состояние.
func (m *Model) finishTurn() {
	m.streaming = false
	// Придержанное сообщение о состоянии графа показываем теперь: в середину
	// ответа влезать нельзя, а забывать о беде — тем более.
	if m.healthWaiting {
		m.healthWaiting = false
		if text := healthHintText(m.healthAdvice, m.kb.use); text != "" && text != m.healthShown {
			defer func() {
				m.addBlock(block{kind: blockHint, text: text})
				m.healthShown = text
			}()
		}
	}
	m.events = nil
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	// Живой блок ответа перерисовываем через markdown.
	if m.liveIdx >= 0 && m.liveIdx < len(m.blocks) {
		b := m.blocks[m.liveIdx]
		m.updateBlock(m.liveIdx, b)
	}

	// Одно время на журнал и на блоки ленты, чтобы копия из буфера обмена
	// и запись в журнале были помечены одинаково.
	answered := time.Now()
	m.stampTurn(answered)

	// Ответ и рассуждения помечаем моделью: в одном журнале могут соседствовать
	// ответы разных моделей и серверов.
	if m.cfg.Log.LogThinking && strings.TrimSpace(m.turnThink.String()) != "" {
		_ = m.logger.WriteFromAt(answered, chatlog.KindThinking, m.answeredBy, m.turnThink.String())
	}
	if strings.TrimSpace(m.turnAnswer.String()) != "" {
		if err := m.logger.WriteFromAt(answered, chatlog.KindAnswer, m.answeredBy, m.turnAnswer.String()); err != nil {
			m.statusMsg = "журнал: " + err.Error()
		}
	}
	// Обмен закрыт: дальнейшие записи снова сеансовые. Строго после записи
	// ответа и рассуждений — иначе они получили бы номер 00.
	m.logger.EndTurn()

	m.liveIdx = -1
	m.thinkIdx = -1
}

// stampTurn помечает блоки ответа завершившегося хода временем и моделью.
//
// Границу хода ищем от конца ленты до ближайшего вопроса, а не по запомненному
// индексу: dropLiveBlocks физически выбрасывает блоки из среза, и любой
// сохранённый снаружи индекс после неудачной попытки указывает не туда.
func (m *Model) stampTurn(ts time.Time) {
	from := lastUserBlock(m.blocks) + 1
	last := -1
	for i := from; i < len(m.blocks); i++ {
		if m.blocks[i].kind != blockAssistant {
			continue
		}
		if m.blocks[i].at.IsZero() {
			m.blocks[i].at = ts
		}
		if m.blocks[i].model == "" {
			m.blocks[i].model = m.answeredBy
		}
		if m.blocks[i].turn == "" {
			m.blocks[i].turn = m.turnID
		}
		if strings.TrimSpace(m.blocks[i].text) != "" {
			last = i
		}
	}
	// Идентификатор показывается один раз за обмен — под последним куском
	// ответа. Ответ, разорванный вызовами инструментов, лежит в нескольких
	// блоках, и метка под каждым из них была бы шумом.
	if last >= 0 && m.blocks[last].turn != "" {
		b := m.blocks[last]
		b.showTurnID = true
		m.updateBlock(last, b)
	}
}

// needsCompaction — пора ли сжимать: порог задан, число заполнения точное
// (после ответа сервера, не оценка), окно известно и история длиннее хвоста.
func needsCompaction(at float64, keep int, meter ctxmeter.Meter, history int) bool {
	if at <= 0 || meter.Capacity <= 0 || !meter.Exact {
		return false
	}
	if history <= keep {
		return false
	}
	return float64(meter.Used) >= at*float64(meter.Capacity)
}

// compactBeforeSend решает, отправлять ли вопрос сейчас. true во втором
// значении — ждать: либо идёт сжатие (команда вернётся compactDoneMsg
// и отправит вопрос сама), либо ход отклонён и вопрос возвращён в поле.
func (m *Model) compactBeforeSend(text string) (tea.Cmd, bool) {
	keep := m.cfg.Agent.CompactKeep
	if !needsCompaction(m.cfg.Agent.CompactAt, keep, m.meter, m.conv.Len()) {
		return nil, false
	}
	if m.toolsSupported() {
		// Сводка теряет точность истории вызовов: модель потом ссылается на
		// файлы, которых в сводке нет. Решение владельца 04.09.2026.
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"окно заполнено на %d%% (порог agent.compact_at = %.2f): в агентном режиме история "+
				"не сжимается сводкой. Освободите место сами: /compact [N] или /clear, "+
				"либо расширьте окно: /context add 32k", 100*m.meter.Used/m.meter.Capacity, m.cfg.Agent.CompactAt)})
		m.ta.SetValue(text)
		return nil, true
	}
	model := m.cfg.Agent.CompactModel
	if model == "" {
		model = m.modelName
	}
	older := m.conv.Older(keep)
	client := m.client
	m.gen.compact++
	gen := m.gen.compact
	m.statusMsg = "сжимаю историю сводкой…"
	m.addBlock(block{kind: blockHint, text: fmt.Sprintf(
		"окно заполнено на %d%% — сжимаю %d сообщений сводкой моделью %s, вопрос уйдёт следом",
		100*m.meter.Used/m.meter.Capacity, len(older), model)})
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		ctx, cancel := contextWithTimeout(300)
		defer cancel()
		summary, stats, err := session.Summarize(ctx, client, model, older)
		return compactDoneMsg{gen: gen, text: text, summary: summary, stats: stats, err: err}
	}), true
}

// onCompactDone кладёт сводку в историю (или обрезает, если сводки нет)
// и отправляет отложенный вопрос.
func (m *Model) onCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen.compact {
		return m, nil
	}
	m.statusMsg = ""
	keep := m.cfg.Agent.CompactKeep
	var dropped int
	if msg.err != nil {
		// Сводки нет — сервер занят или модель не справилась. Вопрос всё равно
		// должен уйти, поэтому обрезаем как /compact и говорим об этом.
		dropped = m.conv.Compact(keep)
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"сводка не удалась (%v) — история обрезана до %d сообщений, отброшено %d",
			msg.err, m.conv.Len(), dropped)})
		_ = m.logger.Write(chatlog.KindSystem, fmt.Sprintf("История обрезана без сводки: отброшено %d сообщений (%v).", dropped, msg.err))
	} else {
		dropped = m.conv.CompactWith(keep, msg.summary)
		m.addBlock(block{kind: blockHint, text: fmt.Sprintf(
			"история сжата: %d сообщений → сводка (≈%s токенов), оставлено %d последних",
			dropped, ctxmeter.FormatTokens(ctxmeter.Estimate(msg.summary)), keep)})
		_ = m.logger.Write(chatlog.KindSystem, fmt.Sprintf("История сжата сводкой: %d сообщений.\n%s", dropped, msg.summary))
	}
	m.steps.Write(steplog.Step{Turn: m.turnID, Kind: steplog.KindCompact, Model: m.modelName,
		PromptID: session.CompactPromptID, TokensIn: msg.stats.PromptEvalCount, TokensOut: msg.stats.EvalCount,
		MS: msg.stats.TotalDuration / 1e6, Outcome: map[bool]string{true: steplog.OutcomeOK, false: steplog.OutcomeFailed}[msg.err == nil],
		Extra: map[string]any{"dropped": dropped, "keep": keep}})
	// До следующего ответа сервера заполнение — оценка: точное число теперь
	// врёт, и повторного сжатия на нём не будет.
	m.meter.Used = ctxmeter.EstimateChars(m.conv.EstimatedChars())
	m.meter.Exact = false
	return m, m.send(msg.text)
}
