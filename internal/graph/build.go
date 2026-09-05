package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Сборка графа: обход кусков коллекции и разбор каждого моделью.
//
// Устроена по образцу kb.Embed, и по тем же причинам. Работа идёт часами
// и будет прервана — обрывом связи, занятой картой, перезагрузкой. Поэтому:
//
//   - отметка о разобранном куске ставится **после** записи его сущностей
//     и связей, а не до: прерывание стоит одного куска, а не всей работы;
//   - повторный запуск пропускает отмеченное и продолжает с того же места;
//   - кусок, на котором модель дала неразбираемый ответ, отмечается пропущенным
//     и больше не мешает: одна неудачная книга не имеет права остановить неделю
//     работы.
//
// Одновременных запросов несколько: на стенде время уходит не в видеокарту,
// а в дорогу до сервера и обратно, ровно как при счёте векторов.

// Source — то, откуда берутся куски. Коллекция базы знаний ему удовлетворяет.
//
// Отдельный интерфейс, а не *kb.Collection, ради проверяемости: сборку надо
// уметь прогонять на десятке выдуманных кусков в тесте, не заводя настоящую
// коллекцию с разбором PDF.
//
// Ссылки и тексты разделены намеренно. Список работы собирается по ссылкам —
// это дёшево, и его можно составить хоть на всю библиотеку; тексты читаются
// пачками по мере надобности. Держать в памяти тексты 249 тысяч кусков — это
// триста мегабайт ради ничего.
type Source interface {
	EachChunkRef(f kb.ChunkFilter, fn func(kb.ChunkRef) error) error
	ChunkTexts(indexes []int) (map[int]string, error)
	ChunkCount() int
}

// BuildOpts — как собирать.
type BuildOpts struct {
	// Folder — брать только книги, в пути которых есть эта строка: «/AI/».
	// Так граф собирается каталогами, а не всей библиотекой разом.
	Folder string

	// Books — брать только эти книги, если указаны.
	Books []uint32

	// Limit — сколько кусков разобрать за этот заход. Нужен калибровке:
	// «прогони двести и скажи, сколько это стоило».
	Limit int

	// Workers — сколько запросов к модели держать одновременно.
	Workers int

	// RedoEmpty — брать заново куски, помеченные пустыми.
	//
	// Пустая пометка означает «модель прочла и ничего не нашла», и причин
	// тому две. Одни куски пусты по-настоящему — титульные листы, оглавления,
	// страницы из одних формул. Другие модель просто пропустила: книги
	// советуют повторный проход именно поэтому — «additional extraction passes
	// on the same document, leads to more entity references being detected»
	// (Essential GraphRAG, 2025, стр. 111).
	//
	// Различить их можно только замером: перепройти пустые и посмотреть,
	// сколько из них выдаст понятия во второй раз. По нынешнему графу таких
	// кусков 743 из 43 465 — работа на минуты, а ответ решает, стоят ли
	// повторные проходы по всей библиотеке своих часов.
	RedoEmpty bool

	// Retry — повторять ли неразобранный ответ один раз.
	Retry bool

	// AllowModelChange разрешает досборку графа моделью, отличной от той,
	// которой он начат. По умолчанию это отказ: смешанный граф выглядит
	// исправным и не чинится ничем, кроме полной пересборки.
	AllowModelChange bool
	// AllowPromptChange — то же для версии промпта (graph.PromptID): без него
	// сборка промптом, отличным от записанного в паспорте, отказывается идти.
	AllowPromptChange bool

	// Link — связывать новые имена с существующими понятиями по вектору
	// и арбитру до того, как заведён узел (link.go). nil — не связывать.
	// Ключ сборки, по умолчанию выключен: упоминания, отданные чужому узлу,
	// назад не переносятся.
	Link *LinkOpts
}

func (o BuildOpts) norm() BuildOpts {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Workers > 16 {
		o.Workers = 16
	}
	return o
}

// BuildProgress — ход работы для показа пользователю.
type BuildProgress struct {
	Total    int // сколько кусков предстоит за этот заход
	Done     int // сколько разобрано
	Empty    int // в скольких не нашлось ничего
	Skipped  int // сколько пропущено из-за неразбираемого ответа
	Entities int // сколько сущностей в графе сейчас
	Edges    int
	Elapsed  time.Duration
	Book     string // над какой книгой идёт работа
}

// Rate — скорость в кусках в секунду.
func (p BuildProgress) Rate() float64 {
	if p.Elapsed <= 0 {
		return 0
	}
	return float64(p.Done) / p.Elapsed.Seconds()
}

// BuildResult — что вышло за заход.
type BuildResult struct {
	BuildProgress
	NewEntities int
	NewEdges    int
	// Linked — сколько новых имён связано с существующими понятиями вместо
	// заведения узла; Queued — сколько пар отложено человеку (вердикт «?»).
	Linked, Queued int

	// Pending — сколько кусков под этим отбором оставалось неразобранными
	// на начало захода. По нему считается остаток: «ещё столько-то часов».
	Pending  int
	Canceled bool
}

// Build разбирает куски коллекции и наполняет граф.
func Build(ctx context.Context, coll Source, g *Graph, ex Extractor,
	opt BuildOpts, report func(BuildProgress)) (BuildResult, error) {

	opt = opt.norm()
	var res BuildResult

	if err := g.Lock(); err != nil {
		return res, err
	}
	defer g.Unlock()

	// Модель извлечения записана в паспорте графа, и менять её на полпути
	// нельзя: половина графа окажется собрана одной моделью, половина другой,
	// а видно этого не будет ниоткуда — в graph.meta модель одна. Замер
	// 24.08.2026 показал, насколько это разные графы: у qwen3.8 синонимы есть
	// у 63% понятий, у glm-4.7-flash — у 3–19%, и по ним связывается русский
	// вопрос с английской книгой. Молчаливое продолжение чужой моделью
	// испортило бы работу, которую уже нельзя переделать иначе как целиком.
	switch have := g.Meta().Model; {
	case ex.Model() == "":
	case have == "":
		if err := g.SetModel(ex.Model()); err != nil {
			return res, err
		}
	case have != ex.Model() && !opt.AllowModelChange:
		return res, fmt.Errorf(
			"граф собран моделью %s, а в настройках сейчас %s.\n"+
				"Продолжать другой моделью нельзя: граф окажется собран двумя, "+
				"и различить их потом будет невозможно.\n"+
				"Либо верните %s в graph.model, либо соберите граф заново — "+
				"отложите каталог %s и запустите сборку сначала",
			have, ex.Model(), have, g.Dir())
	}
	// Промпт — по формату графа: у формата 2 свой текст и свой PromptID,
	// чтобы его ужесточение не трогало рабочий граф формата 1.
	system := SystemPromptFor(g.Meta().Version)
	if err := g.stampPrompt(PromptIDFor(g.Meta().Version), opt.AllowPromptChange); err != nil {
		return res, err
	}

	filter := kb.ChunkFilter{PathContains: opt.Folder, Docs: opt.Books}

	// Сначала собираем список неразобранного — по ссылкам, без текстов.
	// Так известно общее число заранее: без него полоса хода врёт, а по ней
	// человек решает, ждать ему или идти спать.
	var jobs []kb.ChunkRef
	var left int
	err := coll.EachChunkRef(filter, func(c kb.ChunkRef) error {
		key := ChunkKey{Doc: c.Doc, Ord: c.Ord}
		if mark, ok := g.Progress().MarkOf(key); ok {
			// Пустые берутся заново только по явной просьбе: иначе каждый
			// заход перечитывал бы одни и те же титульные листы.
			if !opt.RedoEmpty || mark != MarkEmpty {
				return nil
			}
		}
		left++
		if opt.Limit > 0 && len(jobs) >= opt.Limit {
			return nil // считаем остаток дальше, но в работу не берём
		}
		jobs = append(jobs, c)
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return res, err
	}
	res.Pending = left

	res.Total = len(jobs)
	if res.Total == 0 {
		res.Entities, res.Edges = g.Entities().Count(), g.Edges().Count()
		return res, nil
	}

	type job struct {
		key  ChunkKey
		book string
		unit string
		from int
		to   int
		text string
	}

	started := time.Now()
	entsBefore, edgesBefore := g.Entities().Count(), g.Edges().Count()

	// Свой контекст поверх пришедшего. Когда рабочий упирается в мёртвый
	// сервер, он уходит, и раздатчик заданий остался бы висеть на канале,
	// который больше некому читать. Отмена внутреннего контекста снимает
	// раздатчика; внешний при этом не трогается — по нему отличают
	// «остановлено пользователем» от «сервер отвалился».
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	var (
		mu       sync.Mutex
		done     int
		empty    int
		skipped  int
		lastBook string
		firstErr error
		linked   int
	)
	queuedBefore := 0
	if g.links != nil {
		queuedBefore = g.links.Queued()
	}

	queue := make(chan job)
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for j := range queue {
			if runCtx.Err() != nil {
				return
			}
			facts, err, badAnswer := askModel(runCtx, ex, system, j.book, j.unit, j.from, j.to, j.text, opt.Retry)

			mu.Lock()
			switch {
			case err != nil && !badAnswer && ctx.Err() == nil:
				// Сервер отвалился совсем — работать дальше бессмысленно,
				// и лучше сказать об этом, чем пометить полкниги пропущенной.
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				stop()
				return
			case err != nil:
				skipped++
				_ = g.Progress().Mark(j.key, MarkSkipped)
			case len(facts.Entities) == 0:
				empty++
				_ = g.Progress().Mark(j.key, MarkEmpty)
			default:
				n, werr := writeFacts(ctx, g, j.key, facts, opt.Link)
				linked += n
				if werr != nil {
					if firstErr == nil {
						firstErr = werr
					}
					mu.Unlock()
					stop()
					return
				}
				_ = g.Progress().Mark(j.key, MarkDone)
			}
			done++
			lastBook = j.book
			// Дозапись на диск не после каждого куска, а раз в полсотни:
			// иначе половина времени уходит на мелкие записи. Потеря
			// при обрыве — эти полсотни, и они просто разберутся заново.
			if done%50 == 0 {
				flushAll(g)
			}
			if report != nil {
				report(BuildProgress{
					Total: res.Total, Done: done, Empty: empty, Skipped: skipped,
					Entities: g.Entities().Count(), Edges: g.Edges().Count(),
					Elapsed: time.Since(started), Book: lastBook,
				})
			}
			mu.Unlock()
		}
	}

	wg.Add(opt.Workers)
	for i := 0; i < opt.Workers; i++ {
		go worker()
	}

	// Тексты читаются пачками по ходу работы: держать их все в памяти незачем,
	// а читать по одному — значит разжимать один блок хранилища десятки раз.
	const textBatch = 256
send:
	for from := 0; from < len(jobs); from += textBatch {
		to := from + textBatch
		if to > len(jobs) {
			to = len(jobs)
		}
		part := jobs[from:to]
		idx := make([]int, 0, len(part))
		for _, r := range part {
			idx = append(idx, r.Index)
		}
		texts, terr := coll.ChunkTexts(idx)
		if terr != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = terr
			}
			mu.Unlock()
			break send
		}
		for _, r := range part {
			j := job{
				key: ChunkKey{Doc: r.Doc, Ord: r.Ord}, book: bookTitle(r.Book),
				unit: r.Unit, from: r.UnitFrom, to: r.UnitTo, text: texts[r.Index],
			}
			select {
			case <-runCtx.Done():
				break send
			case queue <- j:
			}
		}
	}
	close(queue)
	wg.Wait()

	flushAll(g)
	if err := g.Entities().SaveCounters(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := g.SetChunks(coll.ChunkCount()); err != nil && firstErr == nil {
		firstErr = err
	}

	res.BuildProgress = BuildProgress{
		Total: res.Total, Done: done, Empty: empty, Skipped: skipped,
		Entities: g.Entities().Count(), Edges: g.Edges().Count(),
		Elapsed: time.Since(started), Book: lastBook,
	}
	res.NewEntities = res.Entities - entsBefore
	res.Linked = linked
	if g.links != nil {
		res.Queued = g.links.Queued() - queuedBefore
	}
	res.NewEdges = res.Edges - edgesBefore
	if res.Pending >= done {
		res.Pending -= done
	} else {
		res.Pending = 0
	}
	res.Canceled = ctx.Err() != nil
	return res, firstErr
}

// errEnough — внутренний признак «набрали сколько просили», а не ошибка.
var errEnough = errors.New("хватит")

// ErrEmptyAnswer — модель ответила на кусок пустотой.
//
// Живёт здесь, а не в мосте к Ollama: это часть договора с разборщиком кусков.
// Пустой ответ — **отказ модели на этом куске**, а не отвалившийся сервер;
// разница дорогая, потому что первое пропускает один кусок, а второе
// останавливает весь заход.
//
// Замер 30.08.2026: на одном куске книги «Agentic GraphRAG» модель отдавала
// пустоту. Пустой ответ считался бедой дороги и валил заход целиком, цикл
// начинал новый, доходил до того же куска и падал снова — три куска в минуту
// вместо пятнадцати, и каждый круг заново грузил на карту 18 ГБ.
var ErrEmptyAnswer = errors.New("модель вернула пустой ответ")

// askModel спрашивает модель и разбирает ответ.
//
// Третьим значением возвращается «это дурной ответ модели», а не беда дороги.
// Различать их по тексту ошибки — гиблое дело: у разных клиентов он разный,
// и первая же непредусмотренная формулировка приводит к тому, что двадцать
// тысяч кусков помечаются пропущенными из-за выключенного сервера. Источник
// ошибки известен здесь точно: пришла она от Extract или от разбора JSON.
//
// Повтор ровно один и только при неразобранном ответе: модель, не сумевшая
// дважды выдать JSON, не выдаст его и на третий раз, а куски кончатся нескоро.
func askModel(ctx context.Context, ex Extractor, system, book, unit string, from, to int,
	text string, retry bool) (Facts, error, bool) {

	user := UserPrompt(book, unit, from, to, text)
	answer, err := ex.Extract(ctx, system, user)
	if err != nil {
		// Пустота — отказ на этом куске: пропускаем кусок, заход продолжается.
		// Всё остальное — беда дороги, и она обязана остановить заход.
		if errors.Is(err, ErrEmptyAnswer) {
			if !retry || ctx.Err() != nil {
				return Facts{}, err, true
			}
			answer, err = ex.Extract(ctx, system,
				user+"\n\nОтветь строго объектом JSON, без пояснений.")
			if err != nil {
				return Facts{}, err, errors.Is(err, ErrEmptyAnswer)
			}
			facts, perr := ParseFacts(answer, text)
			return facts, perr, perr != nil
		}
		return Facts{}, err, false
	}
	facts, perr := ParseFacts(answer, text)
	if perr == nil {
		return facts, nil, false
	}
	if !retry || ctx.Err() != nil {
		return Facts{}, perr, true
	}
	answer, err = ex.Extract(ctx, system,
		user+"\n\nОтветь строго объектом JSON, без пояснений.")
	if err != nil {
		return Facts{}, err, false
	}
	facts, perr = ParseFacts(answer, text)
	return facts, perr, perr != nil
}

// writeFacts кладёт разобранное в граф.
//
// Порядок важен: сперва сущности (у связей должны быть оба конца), потом
// упоминания, потом связи. Отметку о разборе ставит вызывающий код — после
// всего этого.
func writeFacts(ctx context.Context, g *Graph, key ChunkKey, f Facts, link *LinkOpts) (linked int, err error) {
	ids := make(map[string]uint32, len(f.Entities))
	for _, e := range f.Entities {
		var id uint32
		if link != nil {
			to, ok, lerr := g.linkNew(ctx, e.Name, e.Type, key, *link)
			if lerr != nil {
				return linked, lerr
			}
			if ok {
				id = to
				linked++
				// Синонимы нового имени достаются выжившему узлу, само имя —
				// как синоним тоже: карточка покажет «он же», а ключом поиска
				// оно станет по обычному правилу пригодности.
				if err := g.Entities().AddAliases(id, append([]string{e.Name}, e.Aliases...)...); err != nil {
					return linked, err
				}
			}
		}
		if id == 0 {
			var aerr error
			id, _, aerr = g.Entities().Add(e.Name, e.Type, e.Aliases...)
			if aerr != nil {
				return linked, aerr
			}
		}
		if id == 0 {
			continue
		}
		ids[Normalize(e.Name)] = id
		g.Entities().Touch(id, false)
		if err := g.Mentions().Add(id, key); err != nil {
			return linked, err
		}
		// Формат 2: каждое вхождение синонима — с источником. Сюда доходят
		// только синонимы, найденные в тексте куска (clean), поэтому запись
		// и есть подтверждение: «в этом куске понятие названо и так».
		if al := g.Aliases(); al != nil {
			for _, a := range e.Aliases {
				if _, err := al.Add(id, key, a); err != nil {
					return linked, err
				}
			}
		}
	}
	for _, r := range f.Relations {
		src, okSrc := ids[Normalize(r.Src)]
		dst, okDst := ids[Normalize(r.Dst)]
		if !okSrc || !okDst {
			continue
		}
		if err := g.Edges().Add(Edge{
			Src: src, Dst: dst, Type: RelType(r.Type), Weight: 1, Evidence: key,
		}); err != nil {
			return linked, err
		}
	}
	return linked, nil
}

// flushAll сбрасывает журналы на диск и просит диск их записать.
//
// Sync, а не только Flush: при отказе питания буферы системы пропадают, и
// граф, стоящий недель видеокарты, откатывался бы дальше, чем на 50 кусков.
// Цена — четыре fsync раз в 50 кусков при 0.3 куска/с, то есть незаметная.
func flushAll(g *Graph) {
	_ = g.Entities().Sync()
	_ = g.Mentions().Sync()
	_ = g.Edges().Sync()
	_ = g.Aliases().Sync()
	_ = g.Progress().Sync()
}

// bookTitle — как называть книгу в вопросе к модели.
func bookTitle(b kb.BookRec) string {
	if b.Title != "" {
		if b.Author != "" {
			return b.Title + " — " + b.Author
		}
		return b.Title
	}
	return b.Path
}
