package mixer

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
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

	// QueryTimeout — сколько ждать вектор вопроса; 0 — умолчание пакета kb.
	QueryTimeout time.Duration

	// QuotesWithoutTools — сколько выдержек добавлять модели без инструментов.
	QuotesWithoutTools int

	// RerankOpts — числа второй ступени: сколько кандидатов и что ей давать.
	RerankOpts kb.RerankOpts

	// Collection — имя коллекции, оно печатается в карте понятий.
	Collection string
}

// Deps — что нужно для сборки подмеса.
type Deps struct {
	Coll     kb.Source
	Graph    *graph.Graph // nil — графа нет, работает прежний порядок
	Embedder kb.Embedder
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
}

// Result — что подмешано к вопросу.
type Result struct {
	Text string // сообщение для модели; пусто — не подмешивать ничего

	Entities  int  // понятий графа в карте
	Relations int  // связей в карте
	Chunks    int  // выдержек из книг
	NoTools   bool // выдержки добавлены потому, что модель не умеет инструментов
	Tokens    int  // оценка цены в токенах

	// From, To — годы самой старой и самой новой из приведённых книг,
	// Note — оговорка о давности, когда она нужна. Знание из книги датировано,
	// а в ответе выглядит вечным: год ставится рядом со ссылкой, а оговорка —
	// одна на всю выдачу, чтобы не шуметь у каждой цитаты.
	From, To int
	Note     string
}

// Empty сообщает, что подмешивать нечего.
func (r Result) Empty() bool { return strings.TrimSpace(r.Text) == "" }

// autoMix собирает то, что уйдёт модели вместе с вопросом.
func Build(question string, d Deps, s Settings) Result {
	if strings.TrimSpace(question) == "" || d.Coll == nil {
		return Result{}
	}
	coll := d.Coll

	// Модель без инструментов ничего не дозапросит, поэтому ей нужна не только
	// карта понятий, но и сами выдержки — иначе ссылаться будет не на что.
	quotesNoTools := d.NoTools && s.QuotesWithoutTools > 0

	g := d.Graph
	if g == nil {
		// Графа нет — прежний порядок: решает подмешивание книг.
		if !d.BooksOn {
			return Result{}
		}
		return books(coll, question, question, 0, d, s)
	}

	if graph.WorkRequest(question) {
		// Вопрос про здешний код: книги ему не помогут, а карта понятий
		// займёт место в контексте.
		return Result{}
	}

	res := g.Search(question, graph.SearchOpts{
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

	if !d.GraphOn {
		// Граф выключен пользователем, но привратник уже сказал «вопрос по
		// делу» — выдержки подмешать можно, если их вообще просили.
		if want == 0 {
			return Result{}
		}
		out := books(coll, question, question, want, d, s)
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
	b.WriteString(head)
	b.WriteString(graph.Render(nil, res, graph.RenderOpts{ForModel: true, Collection: s.Collection}))

	if want > 0 {
		if q := books(coll, find.Expand(question, res.Entities, find.Opts{ExpandLimit: 3}), question, want, d, s); !q.Empty() {
			b.WriteString("\n")
			b.WriteString(q.Text)
			out.Chunks = q.Chunks
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

// books собирает выдержки из книг — прежнее подмешивание kb.auto.
//
// Запросов два: `search` идёт в поиск (он может быть дополнен именами понятий
// графа), `question` — вопрос человека, и он уходит второй ступени. Кросс-энкодер
// читает вопрос вместе с куском, и приписанные синонимы для него шум.
func books(coll kb.Source, search, question string, topK int, d Deps, s Settings) Result {
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
		ExpandLimit:    3,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hits, _, err := find.Books(ctx, find.Deps{Source: coll, Embedder: d.Embedder, Reranker: d.Reranker}, search, question, o)
	if err != nil || len(hits) == 0 {
		return Result{}
	}

	// Порядок частей: сперва сами выдержки, требование ссылаться — последним,
	// вплотную к вопросу. Замер 24.08.2026 на deepseek-r1:70b: с требованием
	// в начале блока модель отвечала по памяти и ни разу не сослалась на книгу.
	var b strings.Builder
	// Граница чужого текста стоит перед выдержками, требование ссылаться —
	// после них (замер 24.08.2026). Порядок: граница, выдержки, требование.
	b.WriteString(kb.QuotedDataNote + "\n\n")
	b.WriteString("Выдержки из книг пользователя, относящиеся к вопросу:\n\n")
	for i, h := range hits {
		year := ""
		if h.Year > 0 {
			year = fmt.Sprintf(" · %d г.", h.Year)
		}
		fmt.Fprintf(&b, "[%d] %s%s · %s %d\n%s\n\n",
			i+1, h.Book, year, h.Unit, h.UnitFrom, strings.TrimSpace(h.Snippet))
	}
	b.WriteString("Это опора для ответа, а не сам ответ. " +
		kb.AnswerStyle(s.AnswerStyle) + "\n")
	out := Result{Text: b.String(), Chunks: len(hits), Tokens: ctxmeter.Estimate(b.String())}
	out.From, out.To, out.Note = kb.YearSpan(hits, time.Now())
	return out
}
