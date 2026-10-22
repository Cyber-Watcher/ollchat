package mixer

import (
	"context"
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Подмешивание знаний к вопросу: обычный RAG и граф понятий.
//
// Модель с инструментами добывает знание сама — зовёт kb_search и graph_search
// там, где они нужны. Но два случая инструментами не закрываются.
//
// Первый: модель их не умеет. У deepseek-r1 в сборке нет ни renderer, ни parser,
// и строка «tools» в её описании ложная (DeepSeekFakeTools.md); без
// подмешивания библиотека для такой модели закрыта наглухо.
//
// Второй: модель не догадывается позвать. Она отвечает по памяти, уверенно
// и без ссылок — а книги лежат рядом.
//
// Отсюда подмешивание: перед отправкой вопроса ollchat ищет сам и кладёт
// найденное отдельным сообщением. Цена — контекст, и она платится на КАЖДЫЙ
// вопрос, включая «спасибо». Поэтому здесь стоит привратник.
//
// Привратник работает в два шага, оба местные и оба стоят микросекунды.
// Сперва отсекаются распоряжения о здешнем коде — graph.WorkRequest; замер
// на живом графе показал, что без этого шага «сделай коммит и запушь»
// связывается с понятием «коммит», а «почему тест падает на строке 42» —
// с понятием «тест». Затем вопрос связывается с понятиями графа: не связался
// ни с одним — значит, он не про библиотеку, и не подмешивается ничего:
// ни карта, ни выдержки из книг.
//
// Пока граф не собран, привратника нет: решает одна настройка mix.books,
// как было до появления графа. Отсутствие графа не повод отнимать у человека
// то, что у него работало.

// Один пакет на две дороги. Раньше это жило в интерфейсе и было доступно
// только диалогу; команда `--ask` получила бы свою копию, а две копии одной
// политики расходятся в первую же правку. Здесь зависимости передаются явно
// (коллекция, граф, эмбеддер, числа отбора), и обе дороги зовут одно и то же.

// Settings — числа отбора и оформления. Приходят из конфига, но могут быть
// изменены на сеанс или ключом командной строки.
type Settings struct {
	// Chain — класть ли в карту понятий цепочку между понятиями вопроса
	// (этап 101, D1). Настройка mix.chain; пусто в конфиге — включено.
	Chain bool

	// RelationYears — печатать у связей карты год самой свежей подтверждающей
	// книги (mix.relation_years; этап 104, П10.3). Цитат это не добавляет.
	RelationYears bool

	// MapPlain — карта без чисел (mix.map_style = "plain"): ни «упоминаний N»,
	// ни «подтверждений N», и шапка запрещает пересказывать карту. Этап 105,
	// Б1.б: судья 20.09.2026 предпочёл ответы без карты 17:1, потому что модель
	// пересказывала её числа вместо ответа. Умолчание — прежняя карта с числами,
	// пока судья не сравнил обе подачи одним бинарём.
	MapPlain bool

	// MapPairOnly — карту класть только на вопрос о связи понятий
	// (mix.map_when = "pair"): найдено не меньше двух понятий и между ними
	// есть связь или цепочка. Вопрос об одном понятии карты не получает —
	// книги подмешиваются как прежде. Порядок Хуанга (этап 105, Б1).
	MapPairOnly bool

	// Карта понятий.
	Entities  int                // сколько понятий брать
	Neighbors int                // сколько связей у каждого
	Rank      graph.NeighborRank // как ранжировать связи по вопросу

	// Выдержки из книг.
	TopK           int
	MaxPerBook     int
	MinCosine      float64
	SemanticWeight float64
	Semantic       bool
	AnswerStyle    string
	TableBoost     float64 // надбавка кускам-таблицам; 0 — умолчание
	ExpandLimit    int     // сколькими именами понятий дополнять вопрос (kb.expand_limit, раскрытое); 0 — не расширять
	DedupeCosine   float64 // порог повтора кусков выдачи (kb.dedupe_cosine); 0 — не проверять

	// QueryTimeout — сколько ждать вектор вопроса; 0 — умолчание пакета kb.
	QueryTimeout time.Duration

	// QuotesWithoutTools — сколько выдержек добавлять модели без инструментов.
	QuotesWithoutTools int

	// Воздержание привратника: если реранкер оценил лучший кусок ниже порога,
	// в библиотеке об этом нет, и подмешивать нечего — ни выдержек, ни карты
	// (этап 105, Ж9). Порог и его замер — те же, что у kb_search
	// (kb.abstain_score, −2 по замеру 04.09.2026 на 457 вопросах).
	//
	// **Зачем это привратнику.** Он пропускает вопрос, если нашлось хоть одно
	// понятие с двумя упоминаниями, а это ловится бытовыми словами: «какая
	// погода в Москве» цепляет понятие `weather` (в книгах про ИИ-агентов
	// погодный инструмент — стандартный пример) и получает карту про
	// NetworkPolicy и функции активации (замер Ж6, 24.09.2026).
	//
	// Abstain выключено — прежнее поведение; порог берётся из AbstainScore
	// и AbstainGap.
	Abstain      bool
	AbstainScore *float64
	AbstainGap   float64

	// SenseEntry — считать вектор вопроса и входить в граф не только
	// по написанию слов, но и по смыслу (этап 105, Ж10).
	//
	// **Почему это отдельная настройка, а не всегда.** До 24.09.2026 подмес
	// входил в граф ТОЛЬКО по словам: `QueryVector` ставили `find.Search`,
	// инструменты и команды графа, а `mixer.Build` — нет. Отсюда и замер Ж6:
	// в карту приходит 2,2 понятия при разрешённых шести — ограничивает
	// не настройка, а словесный вход. Цена смыслового входа — вызов эмбеддера
	// перед КАЖДЫМ вопросом (0,1–0,3 с на тёплой модели, секунды на холодной),
	// поэтому включение — решение по замеру, а не по умолчанию.
	SenseEntry bool

	// RerankOpts — числа второй ступени: сколько кандидатов и что ей давать.
	RerankOpts kb.RerankOpts

	// Collection — имя коллекции, оно печатается в карте понятий.
	Collection string

	// Pin — куски по ссылкам («books/53#144»), которые подать модели ВМЕСТО
	// найденных поиском. Прибор «потолка поиска» (этап 112, О7): ответ по верному
	// куску показывает, как отвечала бы система при безупречном поиске, и разница
	// с обычным ответом — цена поиска. С Pin нет ни привратника, ни карты, ни
	// поиска: всё, что могло бы не пропустить кусок, и есть то, что меряется.
	Pin []string

	// FullChunks — подавать куски целиком, а не выдержкой вокруг совпавших слов
	// (kb.Snippet: 320 знаков при куске около тысячи). Отделяет потерю от
	// обрезки куска от потери от поиска (этап 112, О7).
	FullChunks bool

	// SenseSnippet — выдержку собирать из предложений, лучших по реранкеру,
	// а не окном вокруг слов вопроса (этап 112, Ч21). Только для замера:
	// умолчание false — как всегда.
	SenseSnippet bool
}

// Deps — что нужно для сборки подмеса.
type Deps struct {
	Coll     kb.Source
	Graph    *graph.Graph // nil — графа нет, работает прежний порядок
	Embedder kb.Embedder
	// Translator — перевод вопроса для словесной ветки (kb.translate); nil — без.
	Translator find.Translator
	// Reranker — вторая ступень отбора; nil — её нет.
	//
	// Подмешивание получает её с 01.09.2026. До этого реранкер работал только
	// у инструмента kb_search, то есть у моделей, которые **умеют** инструменты.
	// Моделям без инструментов — а им выдержки и подмешивают — доставалась
	// выдача одной ступени, хотя замер 27.08.2026 на 457 вопросах говорит,
	// что вторая поднимает верный кусок со среднего 2.6 места на 1.7.
	Reranker kb.Reranker

	BooksOn bool // подмешивать выдержки (mix.books / kb auto)
	GraphOn bool // подмешивать карту понятий (mix.graph / graph auto)
	NoTools bool // модель не умеет инструментов

	// Steps, Turn — журнал шагов и номер хода: одна запись `search` на вопрос
	// с оценкой реранкера (этап 112, Ч17, отчёт о пробелах библиотеки). nil — не
	// писать. До 16.10.2026 подмешивание журнала не знало вовсе, и отчёт видел
	// только поиски службы MCP по `projectdocs`: поиски по книгам идут почти все
	// отсюда. Пишет только вопрос человека в интерфейсе: `/mix show` задвоил бы
	// вопрос, а прогоны `--ask` залили бы отчёт вопросами наборов.
	Steps *steplog.Writer
	Turn  string

	// Map — карта понятий от службы общей библиотеки (kb.server_url, этап 121,
	// А2). Своего графа при ней нет (Graph — nil): граф держит служба, и сюда
	// приходит готовый результат поиска по нему вместе с цепочкой между
	// понятиями вопроса (А6). Размер карты — Settings.Entities и Neighbors
	// клиента: за контекст платит он. Привратник, отбор книг и отрисовка — те же,
	// что со своим графом. nil — карту считает свой граф, если он есть.
	Map MapSource

	// LocalGraph — свой граф на случай, когда служба не ответила (решение
	// владельца, этап 121, А4): берётся только при переходе, из общего кэша
	// графов, и отпускается в конце Build — до выхода он в памяти не держится.
	// nil — своего графа нет, и карты при недоступной службе не будет.
	LocalGraph func() (*graph.Graph, func(), error)

	probe *searchProbe // первый поиск по книгам за Build — для записи в журнал
}

// MapSource — служба, отдающая карту понятий по вопросу: kbremote.Client.
//
// Ноль в entities и neighbors — «как настроено у службы». Ошибка с меткой
// kb.ErrUnreachable означает «служба не ответила» — по ней Build переходит
// на свой граф; прочие ошибки — отказ по смыслу, и перехода по ним нет.
type MapSource interface {
	GraphSearch(ctx context.Context, collection, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error)
}

// mapTimeout — сколько ждать карту от службы; столько же Build ждёт книги.
const mapTimeout = 30 * time.Second

// searchProbe — сигналы первого поиска по книгам за один Build. Поисков за
// вопрос бывает два (проба привратника и сами выдержки) по тому же вопросу и
// с тем же реранкером; в журнал идёт первый, чтобы вопрос не считался дважды.
type searchProbe struct {
	done bool
	sig  find.Signals
}

// Result — что подмешано к вопросу.
type Result struct {
	Text string // сообщение для модели; пусто — не подмешивать ничего

	Entities  int  // понятий графа в карте
	Relations int  // связей в карте
	Chain     int  // шагов в цепочке между понятиями вопроса, 0 — цепочки не было
	Chunks    int  // выдержек из книг
	NoTools   bool // выдержки добавлены потому, что модель не умеет инструментов
	Tokens    int  // оценка цены в токенах

	// Refs — ссылки на поданные куски («books/53#144») в порядке подачи.
	// По ним замер видит, дошёл ли до модели нужный кусок и каким по счёту:
	// «не нашёлся» и «нашёлся, но утонул среди прочих» лечатся разным
	// (этап 112, О7 и О9).
	Refs []string

	// From, To — годы самой старой и самой новой из приведённых книг,
	// Note — оговорка о давности, когда она нужна. Знание из книги датировано,
	// а в ответе выглядит вечным: год ставится рядом со ссылкой, а оговорка —
	// одна на всю выдачу, чтобы не шуметь у каждой цитаты.
	From, To int
	Note     string

	// Remote — что сказать о службе библиотеки: «служба недоступна — искал
	// по своим файлам» (kb.FallbackNote) или её отказ по смыслу. Пусто —
	// служба ответила или её нет вовсе. Строка идёт человеку под вопросом
	// и в журнал шагов (этап 121, А4); бывает и при пустом подмесе.
	Remote string
}

// Empty сообщает, что подмешивать нечего.
func (r Result) Empty() bool { return strings.TrimSpace(r.Text) == "" }

// autoMix собирает то, что уйдёт модели вместе с вопросом.
func Build(question string, d Deps, s Settings) (mixed Result) {
	if strings.TrimSpace(question) == "" || d.Coll == nil {
		return Result{}
	}
	// nil конкретного типа (kbembed.New, kbrerank.New без настройки) — не
	// эмбеддер и не реранкер: иначе привратник мерил бы выдачу порогом
	// реранкера, которого нет (см. kb.RerankerOrNil).
	d.Embedder, d.Reranker = kb.EmbedderOrNil(d.Embedder), kb.RerankerOrNil(d.Reranker)
	if d.Steps != nil {
		started := time.Now()
		d.probe = &searchProbe{}
		defer func() { logSearch(question, d, s, mixed, time.Since(started)) }()
	}
	// Заметка о службе ставится последней — после всех возвратов, но до записи
	// в журнал (отложенные вызовы идут в обратном порядке): книги могли уйти
	// на свои файлы в любом месте ниже.
	var remote string
	defer func() {
		if remote == "" && kb.Fell(d.Coll) {
			remote = kb.FallbackNote
		}
		mixed.Remote = remote
	}()
	coll := d.Coll
	if len(s.Pin) > 0 {
		return pinned(coll, question, s, d.Reranker)
	}

	// Модель без инструментов ничего не дозапросит, поэтому ей нужна не только
	// карта понятий, но и сами выдержки — иначе ссылаться будет не на что.
	quotesNoTools := d.NoTools && s.QuotesWithoutTools > 0

	g := d.Graph
	// Карта выключена (mix.graph = false) — у службы её не спрашиваем, а при
	// недоступной службе не открываем и свой граф через d.LocalGraph: каждый
	// вопрос открывал бы граф, хотя карта не нужна (рецензия этапа 121).
	// Здесь, а не в вызывающих: d.Map ещё и метит журнал шагов «service».
	fromService := g == nil && d.Map != nil && d.GraphOn
	if g == nil && !fromService {
		// Графа нет — прежний порядок: решает подмешивание книг.
		if !d.BooksOn {
			return Result{}
		}
		out, _ := books(coll, question, question, 0, d, s)
		return out
	}

	if graph.WorkRequest(question) {
		// Вопрос про здешний код: книги ему не помогут, а карта понятий
		// займёт место в контексте.
		return Result{}
	}

	var res graph.SearchResult
	var chain []graph.PathStep
	served := false // карта и цепочка пришли от службы готовыми
	if fromService {
		var release func()
		res, chain, served, g, release, remote = serviceMap(question, d, s)
		if release != nil {
			defer release()
		}
		if !served && g == nil {
			// Карты нет ни у службы, ни своей — прежний порядок без графа.
			if !d.BooksOn {
				return Result{}
			}
			out, _ := books(coll, question, question, 0, d, s)
			return out
		}
	}

	if !served {
		// Вектор вопроса под граф: одно место на всех — find.QueryVector, оно же
		// сверяет модель эмбеддера с векторами графа (разные модели дают
		// бессмысленные расстояния).
		var qv []int8
		if s.SenseEntry && d.Embedder != nil {
			qctx, cancel := context.WithTimeout(context.Background(), queryTimeout(s))
			qv, _, _ = find.QueryVector(qctx, find.Deps{Graph: g, Embedder: d.Embedder}, question,
				find.Opts{Semantic: true, QueryTimeout: queryTimeout(s)})
			cancel()
		}

		res = g.Search(question, graph.SearchOpts{
			QueryVector:  qv,
			TopEntities:  s.Entities,
			TopNeighbors: s.Neighbors,
			// Ранжирование связей — то же, что у поиска и у инструментов модели.
			// До 28.08.2026 подмешивание его не читало вовсе: настройка меняла
			// выдачу /graph find, а карта понятий в вопросе оставалась прежней,
			// и расхождение было ничем не видно.
			Neighbors: s.Rank,
			TopChunks: 1, // куски здесь не печатаются, но поиск их не считает даром
			// Одноразовые понятия в карту не берём: их 59% от всех, и это
			// в основном шум разбора. Замер: вопрос «чем RAG отличается
			// от дообучения» приводил в карту понятие «отличается».
			MinMentions: 2,
		})
	}
	if len(res.Entities) == 0 {
		// Привратник: вопрос не о том, что есть в библиотеке.
		return Result{}
	}

	// Что подмешивать из книг: сколько просил пользователь либо укороченный
	// набор для модели без инструментов.
	want := 0
	switch {
	case d.BooksOn:
		want = s.TopK
	case quotesNoTools:
		want = s.QuotesWithoutTools
	}

	// Вторая проверка привратника: нашлись ли в книгах куски, которые реранкер
	// считает относящимися к делу (этап 105, Ж9).
	//
	// **Зачем поверх первой.** Первая пропускает вопрос, если нашлось хоть одно
	// понятие с двумя упоминаниями, а это ловится бытовыми словами: «какая
	// погода в Москве на выходных» цепляет понятие `weather` — в книгах про
	// ИИ-агентов погодный инструмент стандартный пример — и получает карту про
	// NetworkPolicy и функции активации Keras (замер Ж6, 24.09.2026).
	// Оценка реранкера сопоставима между запросами (по делу около +1, не по делу
	// около −11), и порог −2 снят замером на 457 вопросах (этап 89, шаг 4).
	//
	// Проба стоит одного поиска с реранкером: в режиме с выдержками её результат
	// используется дальше, в режиме «только карта» это лишний поиск — цена
	// включения, и она замерена (Ж9).
	var probe Result
	var probeWant int
	if s.Abstain && d.Reranker != nil && (s.AbstainScore != nil || s.AbstainGap > 0) {
		probeWant = want
		if probeWant <= 0 {
			probeWant = 3 // проба: нам нужна оценка, а не выдержки
		}
		q, sig := books(coll, question, question, probeWant, d, s)
		if note := find.AbstainScoreNote(sig, s.AbstainScore, true); note != "" {
			return Result{}
		}
		if note := find.AbstainNote(sig, s.AbstainGap); note != "" {
			return Result{}
		}
		probe = q
	}

	if !d.GraphOn {
		// Граф выключен пользователем, но привратник уже сказал «вопрос по
		// делу» — выдержки подмешать можно, если их вообще просили.
		if want == 0 {
			return Result{}
		}
		out, _ := books(coll, question, question, want, d, s)
		out.NoTools = !d.BooksOn
		return out
	}

	// Цепочка между двумя понятиями вопроса считается заранее: она нужна
	// и карте (s.Chain), и решению «вопрос ли это о связи» (MapPairOnly).
	// От службы она приходит вместе с картой, посчитанная тем же find.Chain.
	if (s.Chain || s.MapPairOnly) && !served {
		chain = find.Chain(g, res.Entities, res.Relations, find.Opts{})
	}
	if s.MapPairOnly && !aboutPair(res, chain) {
		// Вопрос об одном понятии: карта ему не нужна, только книги.
		if want == 0 {
			return Result{}
		}
		out, _ := books(coll, question, question, want, d, s)
		out.NoTools = !d.BooksOn
		return out
	}

	out := Result{Entities: len(res.Entities), Relations: len(res.Relations)}
	var b strings.Builder
	// Про отсутствие цитат сказано прямо. Первая редакция просила «ссылайся
	// на книги и страницы», а страниц в карте нет вовсе — и модель их
	// придумывала бы, выполняя указание.
	head := "Карта понятий из книг пользователя по этому вопросу — опора для ответа, " +
		"а не сам ответ. Самих цитат здесь нет: за ними зови kb_search, " +
		"за подробностями об одном понятии — graph_entity, за связью двух — graph_path. " +
		"Ссылайся только на то, что действительно прочитал.\n\n"
	if want > 0 {
		head = "Карта понятий из книг пользователя по этому вопросу и выдержки из них — " +
			"опора для ответа, а не сам ответ. Ссылайся на книги и страницы из выдержек; " +
			"чего в них нет — говори от себя и так и помечай.\n\n"
	}
	if s.MapPlain {
		// Запрет прямой: без него модель отвечает «в вашей карте есть связь
		// X → Y» вместо ответа на вопрос (замер 20.09.2026).
		head += "Саму карту, её устройство и то, что она есть, в ответе не упоминай " +
			"и не пересказывай — отвечай на вопрос по существу.\n\n"
	}
	b.WriteString(head)
	renderOpts := graph.RenderOpts{Collection: s.Collection, Plain: s.MapPlain}
	// Возраст знания модель видит так же, как человек в /search: «свежайшее
	// 2021 г.» у связи. Без оценок и предупреждений — сегодняшняя дата у модели
	// есть в системном сообщении, сопоставит сама. Источник кусков даётся
	// отрисовке только ради года (YearsOnly): цитат в карте по-прежнему нет.
	// Коллекция приходит сюда интерфейсом поиска; год берётся, только если она
	// умеет отдавать кусок по ссылке (настоящая коллекция умеет, подставная
	// в тестах — не обязана).
	if chunks, ok := coll.(graph.Chunks); ok && s.RelationYears {
		renderOpts.YearsOnly = true
		b.WriteString(graph.Render(chunks, res, renderOpts))
	} else {
		b.WriteString(graph.Render(nil, res, renderOpts))
	}

	// Цепочка между двумя понятиями вопроса — то же, что показывает `/search`
	// (этап 101, D1). Модель может добыть её сама вызовом graph_path, но это
	// лишний ход: вопрос «как связаны X и Y» задают часто, а цепочка коротка.
	//
	// Подтверждения (книга и страница) намеренно не печатаются: в карте понятий
	// цитат нет вовсе, и строка со страницей провоцировала бы ссылаться на то,
	// что модель не читала. За цитатами — kb_search, так и написано в шапке.
	if s.Chain && len(chain) > 0 {
		b.WriteString("\n")
		b.WriteString(graph.RenderPath(nil, chain[0].From, chain[len(chain)-1].To, chain, true,
			graph.RenderOpts{Collection: s.Collection}))
		out.Chain = len(chain)
	}

	if want > 0 {
		search := question
		if s.ExpandLimit > 0 {
			search = find.Expand(question, res.Entities, find.Opts{ExpandLimit: s.ExpandLimit})
		}
		q := probe
		if q.Empty() || probeWant != want || s.ExpandLimit > 0 {
			// Проба искала по самому вопросу; с расширением запроса именами
			// понятий поиск другой, и брать пробу вместо него нельзя.
			q, _ = books(coll, search, question, want, d, s)
		}
		if !q.Empty() {
			b.WriteString("\n")
			b.WriteString(q.Text)
			out.Chunks, out.Refs = q.Chunks, q.Refs
			out.NoTools = !d.BooksOn
			out.From, out.To, out.Note = q.From, q.To, q.Note
		}
	}
	if out.Note != "" {
		fmt.Fprintf(&b, "\nСамой старой из приведённых книг %s — сверься со временем: "+
			"версии, имена инструментов и «как принято» с тех пор могли измениться.\n",
			strings.TrimPrefix(out.Note, "книге "))
	}

	out.Text = b.String()
	out.Tokens = ctxmeter.Estimate(out.Text)
	return out
}

// queryTimeout — сколько ждать вектор вопроса; 0 в настройках значит умолчание
// коллекции, а не «без предела»: под идущей сборкой графа карта занята, и ждать
// вектор бесконечно нельзя (то же правило, что у поиска).
func queryTimeout(s Settings) time.Duration {
	if s.QueryTimeout > 0 {
		return s.QueryTimeout
	}
	return kb.DefaultQueryTimeout
}

// serviceMap берёт карту понятий у службы (этап 121, А2 и А6).
//
// Служба не ответила (kb.ErrUnreachable) — карту считает свой граф из
// d.LocalGraph, и в заметке «служба недоступна — искал по своим файлам» (А4).
// Если книги уже ушли на свои файлы при открытии коллекции, служба этого
// вопроса не ответит и сейчас — ждать её второй раз незачем. Отказ службы
// по смыслу (нет графа, неизвестная коллекция) — карты нет, заметка — сам
// отказ: свой граф спрятал бы беду на службе за ответом.
//
// served — карта пришла от службы; local, release — свой граф и как его
// отпустить, когда карту считает он.
func serviceMap(question string, d Deps, s Settings) (res graph.SearchResult, chain []graph.PathStep,
	served bool, local *graph.Graph, release func(), note string) {

	err := kb.ErrUnreachable
	if !kb.Fell(d.Coll) {
		ctx, cancel := context.WithTimeout(context.Background(), mapTimeout)
		res, chain, err = d.Map.GraphSearch(ctx, d.Coll.Name(), question, s.Entities, s.Neighbors)
		cancel()
		if err == nil {
			return res, chain, true, nil, nil, ""
		}
	}
	if !errors.Is(err, kb.ErrUnreachable) {
		return graph.SearchResult{}, nil, false, nil, nil, "карта понятий: " + err.Error()
	}
	if d.LocalGraph == nil {
		return graph.SearchResult{}, nil, false, nil, nil, err.Error() + " — своего графа нет, карта не подмешана"
	}
	g, rel, lerr := d.LocalGraph()
	if lerr != nil || g == nil {
		if rel != nil {
			rel()
		}
		why := "своего графа нет"
		if lerr != nil {
			why = "свой граф не открылся: " + lerr.Error()
		}
		return graph.SearchResult{}, nil, false, nil, nil, kb.FallbackNote + "; " + why + ", карта не подмешана"
	}
	return graph.SearchResult{}, nil, false, g, rel, kb.FallbackNote
}

// CachedGraph — свой граф для запасного пути (Deps.LocalGraph): коллекция
// открывается только при переходе, граф берётся из общего кэша графов
// и отпускается после подмеса — кэш закроет его после простоя. Без кэша
// (graph.cache = false) граф открывается на подмес и закрывается после него.
func CachedGraph(cache *graph.Cache, rules graph.Rules, open func() (*kb.Collection, error)) func() (*graph.Graph, func(), error) {
	return func() (*graph.Graph, func(), error) {
		coll, err := open()
		if err != nil {
			return nil, nil, err
		}
		if cache != nil {
			return cache.Get(coll.Dir(), coll.ChunkCount())
		}
		g, err := graph.Open(coll.Dir(), coll.ChunkCount(), rules)
		if err != nil {
			return nil, nil, err
		}
		return g, func() { _ = g.Close() }, nil
	}
}

// aboutPair — о связи ли понятий вопрос: найдено не меньше двух понятий
// и между какими-то двумя из них есть прямая связь либо найдена цепочка.
// Вопрос с одним понятием («что такое горутина») — не о связи, и карта
// ему только мешает: замер Б1 показал, что на точных терминах граф вредит.
func aboutPair(res graph.SearchResult, chain []graph.PathStep) bool {
	if len(res.Entities) < 2 {
		return false
	}
	if len(chain) > 0 {
		return true
	}
	found := map[string]bool{}
	for _, e := range res.Entities {
		found[e.Name] = true
	}
	for _, r := range res.Relations {
		if found[r.Src] && found[r.Dst] {
			return true
		}
	}
	return false
}

// books собирает выдержки из книг — прежнее подмешивание kb.auto.
//
// Запросов два: `search` идёт в поиск (он может быть дополнен именами понятий
// графа), `question` — вопрос человека, и он уходит второй ступени. Кросс-энкодер
// читает вопрос вместе с куском, и приписанные синонимы для него шум.
func books(coll kb.Source, search, question string, topK int, d Deps, s Settings) (Result, find.Signals) {
	want := s.TopK
	if topK > 0 {
		want = topK
	}
	// Одно ядро с /search и kb_search (этап 91, R2.6): числа отбора те же,
	// вторая ступень та же, предел дополнения запроса тот же.
	o := find.Opts{
		Mode:           "mix",
		Collection:     s.Collection,
		TopK:           want,
		MaxPerBook:     s.MaxPerBook,
		TableBoost:     s.TableBoost,
		MinCosine:      s.MinCosine,
		SemanticWeight: s.SemanticWeight,
		QueryTimeout:   s.QueryTimeout,
		Semantic:       s.Semantic,
		Rerank:         true,
		RerankOpts:     s.RerankOpts,
		ExpandLimit:    s.ExpandLimit,
		DedupeCosine:   s.DedupeCosine,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hits, _, err := find.Books(ctx, find.Deps{Source: coll, Embedder: d.Embedder, Reranker: d.Reranker,
		Translator: d.Translator}, search, question, o)
	if d.probe != nil && !d.probe.done && err == nil {
		d.probe.done, d.probe.sig = true, find.SignalsOf(hits)
	}
	if err != nil || len(hits) == 0 {
		return Result{}, find.Signals{}
	}
	senseSnippets(hits, question, s, d.Reranker)
	return render(hits, s), find.SignalsOf(hits)
}

// logSearch пишет одну запись `search` за вопрос — те же поля, что у
// find.Search, их читает `stepstat.py --gaps`. Оценка первого места — только
// если поиск по книгам был и что-то нашёл: пустая выдача пишется `hits` 0 без
// `top1`, а не нулём, который выше порога и выглядел бы «в книгах есть».
// `mixed` — подмешано ли что-нибудь: false при выдаче ниже порога значит, что
// привратник сказал «в книгах этого нет».
func logSearch(question string, d Deps, s Settings, mixed Result, took time.Duration) {
	p := d.probe
	extra := map[string]any{"mode": "mix", "collection": s.Collection,
		"reranked": p.done && d.Reranker != nil, "hits": p.sig.Hits,
		"excerpts": mixed.Chunks, "entities": mixed.Entities, "ids": mixed.Refs,
		"mixed": !mixed.Empty()}
	if p.done && p.sig.Hits > 0 {
		extra["top1"], extra["top1_gap"] = p.sig.Top1, p.sig.Top1Gap
	}
	if d.Map != nil {
		// Подмес через службу общей библиотеки (этап 121): по этому полю
		// отчёт отделяет его от подмеса по своим файлам, а Note говорит,
		// если служба не ответила и искал свой граф.
		extra["service"] = true
	}
	d.Steps.Write(steplog.Step{Turn: d.Turn, Kind: steplog.KindSearch, Args: question,
		MS: took.Milliseconds(), Extra: extra, Note: mixed.Remote})
}

// pinned подаёт модели заданные куски вместо найденных (Settings.Pin).
//
// Выдержка режется так же, как у найденного поиском (kb.Snippet по вопросу),
// если не просили кусков целиком: иначе «потолок» мерил бы заодно и длину
// цитаты, и разница с обычным ответом смешала бы две причины. Ненайденная
// ссылка пропускается молча — прибор видит это по Result.Chunks.
func pinned(coll kb.Source, question string, s Settings, rr kb.Reranker) Result {
	var hits []kb.Result
	for _, id := range s.Pin {
		got, err := coll.Around(id, 0)
		if err != nil {
			continue
		}
		for _, h := range got {
			h.Snippet = kb.Snippet(h.Text, question, 0)
			hits = append(hits, h)
		}
	}
	senseSnippets(hits, question, s, rr)
	return render(hits, s)
}

// senseSnippets пересобирает выдержки по смыслу (Settings.SenseSnippet, этап 112,
// Ч21): предложения куска, лучшие по реранкеру, в пределах той же длины. Без
// реранкера и при его сбое остаётся выдержка по словам.
func senseSnippets(hits []kb.Result, question string, s Settings, rr kb.Reranker) {
	if !s.SenseSnippet || s.FullChunks || rr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := range hits {
		hits[i].Snippet = kb.SnippetBySense(ctx, hits[i].Text, question, 0, rr, hits[i].Snippet)
	}
}

// render — блок выдержек в том виде, в каком его читает модель.
func render(hits []kb.Result, s Settings) Result {
	if len(hits) == 0 {
		return Result{}
	}
	// Порядок частей: сперва сами выдержки, требование ссылаться — последним,
	// вплотную к вопросу. Замер 24.08.2026 на deepseek-r1:70b: с требованием
	// в начале блока модель отвечала по памяти и ни разу не сослалась на книгу.
	var b strings.Builder
	refs := make([]string, 0, len(hits))
	// Граница чужого текста стоит перед выдержками, требование ссылаться —
	// после них (замер 24.08.2026). Порядок: граница, выдержки, требование.
	b.WriteString(kb.QuotedDataNote + "\n\n")
	b.WriteString("Выдержки из книг пользователя, относящиеся к вопросу:\n\n")
	for i, h := range hits {
		refs = append(refs, h.ID)
		year := ""
		if h.Year > 0 {
			year = fmt.Sprintf(" · %d г.", h.Year)
		}
		text := h.Snippet
		if s.FullChunks {
			text = h.Text
		}
		fmt.Fprintf(&b, "[%d] %s%s · %s %d\n%s\n\n",
			i+1, h.Book, year, h.Unit, h.UnitFrom, strings.TrimSpace(text))
	}
	b.WriteString("Это опора для ответа, а не сам ответ. " +
		kb.AnswerStyle(s.AnswerStyle) + "\n")
	out := Result{Text: b.String(), Chunks: len(hits), Tokens: ctxmeter.Estimate(b.String()), Refs: refs}
	out.From, out.To, out.Note = kb.YearSpan(hits, time.Now())
	return out
}
