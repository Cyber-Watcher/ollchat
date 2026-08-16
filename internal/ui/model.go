package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/itpro/ollchat/internal/agent"
	"github.com/itpro/ollchat/internal/chatlog"
	"github.com/itpro/ollchat/internal/config"
	"github.com/itpro/ollchat/internal/ctxmeter"
	"github.com/itpro/ollchat/internal/kb"
	"github.com/itpro/ollchat/internal/kbembed"
	"github.com/itpro/ollchat/internal/ollama"
	"github.com/itpro/ollchat/internal/permissions"
	"github.com/itpro/ollchat/internal/session"
	"github.com/itpro/ollchat/internal/tools"
	"github.com/itpro/ollchat/internal/vram"
)

// Model — состояние TUI.
type Model struct {
	cfg      *config.Config
	guard    *permissions.Guard
	registry *tools.Registry
	logger   *chatlog.Logger
	store    *session.Store
	conv     *session.Conversation
	meter    ctxmeter.Meter

	// vramProfile — замеры olldiagtools, если они есть. Их читает /calc.
	vramProfile *vram.Profile

	// Текущий сервер и модель.
	server    *config.Server
	client    *ollama.Client
	modelName string
	modelCaps []string
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
	startedAt  time.Time
	iterations int
	// answeredBy — модель, которой был отправлен текущий вопрос. Запоминается
	// на момент отправки: за время ответа выбранная модель может смениться,
	// а в журнале должна остаться та, что действительно отвечала.
	answeredBy string

	// Подтверждение действия.
	confirm       *agent.ConfirmRequest
	confirmScroll int

	// pending — картинки, вставленные в ещё не отправленный вопрос.
	// Живут до отправки: в сообщение попадут только те, чьи метки остались
	// в тексте.
	pending []pendingImage

	// Выбор из списка (серверы, модели, сессии).
	picker *picker

	// files — список файлов под токеном «@…» в строке ввода. Показывается
	// над полем ввода и не забирает набор текста себе.
	files *fileMenu

	// images — панель вложенных изображений (F3). Стоит там же, где список
	// файлов, и потому взаимно исключает его.
	images *imagePanel

	// draggingBar — пользователь удерживает бегунок полосы прокрутки.
	draggingBar bool

	// mouseOn — запрошен ли у терминала отчёт о мыши. Пока он включён, колесо
	// и бегунок работают, но выделить текст мышью нельзя: события достаются
	// приложению, а не терминалу. Переключается клавишей F2 и командой /mouse.
	mouseOn bool

	// runGen — номер текущего хода. События с чужим номером игнорируются:
	// так прерванный ход не может дорисовать что-то поверх нового.
	runGen int

	// srvGen — номер текущего выбора сервера. Запросы к серверу живут секунды
	// и десятки секунд, а сервер можно переключить в любой момент: ответ
	// брошенного сервера обязан быть отброшен, иначе он подпишется именем
	// нового (и ошибка недоступного сервера всплывёт под именем работающего)
	// или затрёт его список моделей. Сверять по имени модели недостаточно —
	// на разных серверах модели зовутся одинаково.
	srvGen int

	showThinking bool
	think        *bool
	statusMsg    string
	quitConfirm  bool

	// База знаний по книгам. Коллекция открывается по требованию и держится
	// открытой: у большой коллекции чтение указателей занимает секунды.
	kbBase   *kb.Base
	kbColl   *kb.Collection
	kbUse    string // выбранная коллекция
	kbAutoOn bool   // подмешивать найденное перед каждым вопросом
	kbEmb    *kbembed.Embedder
	kbEmbURL string // адрес, под который собран эмбеддер

	// job — идущая долгая задача (индексация книг). Она живёт отдельно от хода
	// генерации: ресурсы разные, поэтому чат во время индексации работает.
	job    *kbJob
	jobGen int
}

// New создаёт модель интерфейса.
func New(cfg *config.Config, guard *permissions.Guard, registry *tools.Registry,
	logger *chatlog.Logger, store *session.Store, srv *config.Server, modelName string) *Model {

	ta := textarea.New()
	ta.Placeholder = "Спросите модель или введите /help для списка команд"
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
	applyCursor(&ta, cfg.Input.Cursor)

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	m := &Model{
		cfg:       cfg,
		guard:     guard,
		registry:  registry,
		logger:    logger,
		store:     store,
		conv:      session.New(srv.SystemPrompt),
		server:    srv,
		modelName: modelName,
		ta:        ta,
		spin:      sp,
		// Тему определяем здесь, до старта цикла событий Bubble Tea, —
		// подробности в комментарии к detectStyle.
		rend:         newRenderer(80, cfg.General.RenderMarkdown, detectStyle()),
		showThinking: cfg.General.ShowThinking,
		think:        srv.Think,
		mouseOn:      cfg.Input.Mouse,
		liveIdx:      -1,
		thinkIdx:     -1,
	}
	m.client = ollama.New(srv.URL, srv.TimeoutDuration(), srv.Headers)
	m.vramProfile = loadVRAMProfile(cfg)
	// База знаний открывается всегда: сама по себе она ничего не стоит,
	// а коллекции читаются по требованию.
	if base, err := kb.OpenBase(cfg.KB.Dir); err == nil {
		m.kbBase = base
		m.kbUse = cfg.KB.Default
		m.kbAutoOn = cfg.KB.Auto
	}
	if n, ok := srv.NumCtx(); ok {
		m.meter.SetCapacity(n, ctxmeter.SourceConfig)
	}
	if hint := toolDriftHint(cfg, registry); hint != "" {
		m.addBlock(block{kind: blockHint, text: hint})
	}
	return m
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
	return tea.Batch(
		textarea.Blink,
		m.spin.Tick,
		m.loadServerInfoCmd(),
		m.kbPendingCmd(),
	)
}

// ── Сообщения ────────────────────────────────────────────────────────────────

type agentEventMsg struct {
	gen int
	ev  agent.Event
}
type streamClosedMsg struct{ gen int }
type noticeMsg struct{ text string }
type errorMsg struct{ err error }

type serverInfoMsg struct {
	gen     int
	version string
	models  []ollama.ModelInfo
	err     error
}

type modelInfoMsg struct {
	gen      int
	model    string
	caps     []string
	capacity int
	maxCtx   int // на какое окно модель обучена
	source   ctxmeter.CapacitySource
	err      error
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
	gen := m.srvGen
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
	gen := m.srvGen
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
	gen := m.srvGen
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
	m.vp.SetHeight(m.viewportHeight())
	m.refreshViewport(true)
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

	m.addBlock(block{kind: blockUser, text: text})
	if err := m.logger.Write(chatlog.KindQuestion, text); err != nil {
		m.statusMsg = "журнал: " + err.Error()
	}

	// Режим подмешивания: найденное в книгах уходит отдельным сообщением перед
	// вопросом. Так база работает и с моделями без поддержки инструментов.
	// Поиск идёт по тексту вопроса и стоит миллисекунды, поэтому делается
	// прямо здесь, а не отдельной командой.
	if found, n := m.kbAutoContext(text); n > 0 {
		m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: found})
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"подмешано фрагментов из книг: %d (≈%s токенов) — /kb auto off отключает",
			n, ctxmeter.FormatTokens(ctxmeter.Estimate(found)))})
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
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.runGen++
	m.events = runner.Run(ctx, m.conv)
	m.streaming = true
	m.answeredBy = m.modelName
	m.startedAt = time.Now()
	m.liveIdx = -1
	m.thinkIdx = -1
	m.iterations = 0
	m.turnAnswer.Reset()
	m.turnThink.Reset()

	return tea.Batch(waitForEvent(m.runGen, m.events), m.spin.Tick)
}

// toolsSupported сообщает, можно ли передавать модели инструменты.
func (m *Model) toolsSupported() bool {
	if !m.cfg.Agent.Enabled || m.registry == nil || len(m.registry.Names()) == 0 {
		return false
	}
	return hasCap(m.modelCaps, "tools")
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

	m.runGen++
	m.events = nil
	m.confirm = nil
	m.confirmScroll = 0

	if m.streaming {
		m.finishTurn()
	}
}

// finishTurn завершает обмен: пишет ответ в журнал и сбрасывает состояние.
func (m *Model) finishTurn() {
	m.streaming = false
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

	// Ответ и рассуждения помечаем моделью: в одном журнале могут соседствовать
	// ответы разных моделей и серверов.
	if m.cfg.Log.LogThinking && strings.TrimSpace(m.turnThink.String()) != "" {
		_ = m.logger.WriteFrom(chatlog.KindThinking, m.answeredBy, m.turnThink.String())
	}
	if strings.TrimSpace(m.turnAnswer.String()) != "" {
		if err := m.logger.WriteFrom(chatlog.KindAnswer, m.answeredBy, m.turnAnswer.String()); err != nil {
			m.statusMsg = "журнал: " + err.Error()
		}
	}
	m.liveIdx = -1
	m.thinkIdx = -1
}
