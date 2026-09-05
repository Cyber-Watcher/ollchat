package tools

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Поиск по личной библиотеке книг.
//
// Модель сама решает, когда заглянуть в книги, — так же, как решает прочитать
// файл. Инструмент не трогает файловую систему: он читает готовый индекс,
// собранный пользователем командой /kb add. Поэтому модель не может ни залезть
// в чужие каталоги, ни запустить многочасовую индексацию — инструмента для неё
// нет и не будет.
//
// Главное требование к выдаче — проверяемость. Каждый фрагмент идёт с книгой,
// страницей и номером куска, а в тексте результата стоит прямое указание
// Указание ссылаться на источники должно быть уравновешено требованием объяснять
// своими словами. Первая формулировка звучала «ОБЯЗАТЕЛЬНО ссылайся… не
// додумывай» — и модель поняла её буквально: на вопрос «как работают горутины»
// выдала подборку цитат без единой собственной мысли. Найдено пользователем
// на живом сеансе.
//
// ссылаться на источники. Ответ по книгам без ссылок ничем не отличается
// от выдумки.

// kbSearchTool ищет фрагменты книг.
type kbSearchTool struct{ opts Options }

func (t *kbSearchTool) Name() string { return NameKBSearch }

func (t *kbSearchTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameKBSearch,
		Description: "Ищет по личной библиотеке технических книг пользователя (PDF и EPUB, " +
			"русские и английские). Возвращает фрагменты с точной ссылкой на книгу и страницу. " +
			"Запрос можно писать на любом языке; для технических тем полезно добавлять английские " +
			"термины (goroutine, mutex, cgroup) — книги часто английские. " +
			"Чтобы прочитать больше вокруг фрагмента, вызови " + NameKBRead + " с его id. " +
			// Дальше идёт политика ответа — она задаётся настройкой kb.answer_style
			// и потому склеивается на ходу, а не записана здесь.
			kb.AnswerStyle(t.opts.AnswerStyle),
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"query":      {Type: "string", Description: "Что искать: своими словами или точный термин"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
				"top_k":      {Type: "integer", Description: "Сколько фрагментов вернуть, 1..20"},
				"book":       {Type: "string", Description: "Искать только в книгах, чьё название содержит эту строку"},
			},
			Required: []string{"query"},
		},
	}}
}

func (t *kbSearchTool) Plan(args map[string]any) (*Plan, error) {
	query, err := requireString(args, "query")
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("пустой запрос")
	}
	name := strings.TrimSpace(argStringOr(args, "collection", ""))
	topK := argInt(args, "top_k", 0)
	book := strings.TrimSpace(argStringOr(args, "book", ""))

	title := fmt.Sprintf("%s(%s)", NameKBSearch, textx.ShortenOneLine(query, 50))
	if name != "" {
		title = fmt.Sprintf("%s(%s, %s)", NameKBSearch, name, textx.ShortenOneLine(query, 40))
	}
	return &Plan{
		Tool: NameKBSearch,
		// Поиск читает только собственные данные приложения, поэтому целью
		// проверки прав служит каталог базы знаний, а не книги на диске.
		Req:   permissions.Request{Kind: permissions.KindRead, Target: t.opts.KBDir, Tool: NameKBSearch, Fixed: true},
		Title: title,
		Run: func(ctx context.Context) (string, error) {
			return t.run(ctx, name, query, book, topK)
		},
	}, nil
}

func (t *kbSearchTool) run(ctx context.Context, name, query, book string, topK int) (string, error) {
	coll, err := t.opts.collection(name)
	if err != nil {
		return "", err
	}
	kbTopK, maxPerDoc, minCos, semWeight := t.opts.kbNumbers()
	if topK > 0 {
		kbTopK = min(topK, 20)
	}
	o := find.Opts{
		Mode:           "tool",
		Collection:     coll.Name(),
		TopK:           kbTopK,
		MaxPerBook:     maxPerDoc,
		TableBoost:     t.opts.KBTableBoost,
		Semantic:       t.opts.Semantic,
		MinCosine:      minCos,
		SemanticWeight: semWeight,
		QueryTimeout:   t.opts.QueryTimeout,
		Rerank:         true,
		RerankOpts:     t.opts.RerankOpts,
		// Перевод запроса на язык библиотеки — тот же, что у подмешивания
		// и у `/search`: три понятия графа и их синонимы. Замер 30.08.2026:
		// на слово «горутина» приходили десять русских книг из десяти.
		ExpandLimit: 3,
	}
	if book != "" {
		o.Docs = booksMatching(coll, book)
		if len(o.Docs) == 0 {
			return fmt.Sprintf("В коллекции %q нет книг, название которых содержит %q.", coll.Name(), book), nil
		}
	}
	search := find.Expand(query, graphEntities(t.opts, name, query, 3), o)

	// Одно ядро с /search (этап 91, R2.4): модель и человек видят одну выдачу.
	// Ищется дополненным запросом, а переранжируется исходным: кросс-энкодер
	// читает вопрос вместе с куском, и приписанные синонимы для него шум.
	deps := find.Deps{Source: coll, Embedder: t.opts.embedder(), Reranker: t.opts.reranker()}
	hits, _, err := find.Books(ctx, deps, search, query, o)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		st := coll.Stats()
		return fmt.Sprintf("По запросу %q в коллекции %q (книг: %d) ничего не нашлось. "+
			"Попробуйте другие слова — например, английские термины, если книги английские.",
			query, coll.Name(), st.Indexed), nil
	}

	st := coll.Stats()
	var b strings.Builder
	fmt.Fprintf(&b, "Найдено фрагментов: %d (коллекция %s, книг %d).\n", len(hits), coll.Name(), st.Indexed)
	// Та же граница, что и у подмешивания: выдача инструмента попадает
	// в разговор ровно так же, как выдержки, и отличается лишь тем, что
	// её запросила модель, а не привратник.
	// Воздержание (этап 91, R2.11): порог снят замером на 457 вопросах.
	sig := find.SignalsOf(hits)
	if note := find.AbstainScoreNote(sig, t.opts.KBAbstainScore, deps.Reranker != nil); note != "" {
		b.WriteString(note + "\n")
	} else if note := find.AbstainNote(sig, t.opts.KBAbstainGap); note != "" {
		b.WriteString(note + "\n")
	}
	b.WriteString(kb.QuotedDataNote + "\n\n")
	// Бюджет вывода считается заранее: обрезать выдачу посреди фрагмента нельзя,
	// иначе модель получит оборванную цитату и сошлётся на неё как на целую.
	budget := t.opts.MaxOutputKB * 1024 / 2
	shown := 0
	for i, h := range hits {
		entry := formatHit(i+1, h)
		if b.Len()+len(entry) > budget && shown > 0 {
			break
		}
		b.WriteString(entry)
		shown++
	}
	if shown < len(hits) {
		fmt.Fprintf(&b, "\n[показано %d фрагментов из %d]\n", shown, len(hits))
	}
	if _, _, note := kb.YearSpan(hits, time.Now()); note != "" {
		fmt.Fprintf(&b, "\nСамой старой из приведённых книг %s — сверься со временем: "+
			"версии, имена инструментов и «как принято» с тех пор могли измениться.\n",
			strings.TrimPrefix(note, "книге "))
	}
	b.WriteString("\n" + kb.AnswerStyle(t.opts.AnswerStyle) +
		"\nПродолжение фрагмента: " + NameKBRead + " с его id.")
	return t.opts.truncate(b.String()), nil
}

// formatHit оформляет один фрагмент.
func formatHit(n int, h kb.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] %s", n, h.Book)
	if h.Author != "" {
		fmt.Fprintf(&b, " · %s", h.Author)
	}
	// Год издания рядом со ссылкой, а не отдельной сноской: модель ссылается
	// построчно, и год обязан быть там же, где книга и страница.
	if h.Year > 0 {
		fmt.Fprintf(&b, " · %d г.", h.Year)
	}
	if h.UnitFrom > 0 {
		if h.UnitTo > h.UnitFrom {
			fmt.Fprintf(&b, " · %s %d–%d", h.Unit, h.UnitFrom, h.UnitTo)
		} else {
			fmt.Fprintf(&b, " · %s %d", h.Unit, h.UnitFrom)
		}
	}
	fmt.Fprintf(&b, " · id=%s\n", h.ID)
	text := strings.TrimSpace(h.Snippet)
	if h.Code {
		// Код отдаём в ограждённом блоке, иначе модель принимает его за прозу
		// и пересказывает своими словами.
		fmt.Fprintf(&b, "```\n%s\n```\n\n", text)
	} else {
		fmt.Fprintf(&b, "%s\n\n", text)
	}
	return b.String()
}

// booksMatching отбирает книги, название или автор которых содержит строку.
func booksMatching(coll kb.Source, want string) []uint32 {
	want = strings.ToLower(want)
	var out []uint32
	for _, b := range coll.Books() {
		if b.Kind != kb.BookOK {
			continue
		}
		if strings.Contains(strings.ToLower(b.Title), want) ||
			strings.Contains(strings.ToLower(b.Author), want) ||
			strings.Contains(strings.ToLower(b.Path), want) {
			out = append(out, b.ID)
		}
	}
	return out
}

// kbReadTool читает окрестности найденного фрагмента.
type kbReadTool struct{ opts Options }

func (t *kbReadTool) Name() string { return NameKBRead }

func (t *kbReadTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameKBRead,
		Description: "Читает найденный фрагмент книги целиком вместе с соседними: " +
			"нужен, когда во фрагменте из " + NameKBSearch + " мысль оборвана. " +
			"Принимает id из выдачи поиска, например «go/12#37».",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"id":     {Type: "string", Description: "Номер фрагмента из выдачи поиска, например go/12#37"},
				"around": {Type: "integer", Description: "Сколько соседних фрагментов добавить с каждой стороны, 0..5"},
			},
			Required: []string{"id"},
		},
	}}
}

func (t *kbReadTool) Plan(args map[string]any) (*Plan, error) {
	id, err := requireString(args, "id")
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	around := argInt(args, "around", 1)

	return &Plan{
		Tool:  NameKBRead,
		Req:   permissions.Request{Kind: permissions.KindRead, Target: t.opts.KBDir, Tool: NameKBRead, Fixed: true},
		Title: fmt.Sprintf("%s(%s)", NameKBRead, id),
		Run: func(ctx context.Context) (string, error) {
			return t.run(id, around)
		},
	}, nil
}

func (t *kbReadTool) run(id string, around int) (string, error) {
	name := ""
	if i := strings.Index(id, "/"); i > 0 {
		name = id[:i]
	}
	coll, err := t.opts.collection(name)
	if err != nil {
		return "", err
	}
	parts, err := coll.Around(id, around)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	head := parts[0]
	fmt.Fprintf(&b, "%s", head.Book)
	if head.Author != "" {
		fmt.Fprintf(&b, " · %s", head.Author)
	}
	fmt.Fprintf(&b, " · фрагменты %d\n\n", len(parts))
	for _, p := range parts {
		fmt.Fprintf(&b, "── %s %d · id=%s ──\n%s\n\n", p.Unit, p.UnitFrom, p.ID, strings.TrimSpace(p.Text))
	}
	return t.opts.truncate(b.String()), nil
}

// collection выбирает коллекцию: названную явно, иначе выбранную пользователем.
//
// Отдаёт **интерфейс `kb.Source`, а не конкретную коллекцию**: инструментам
// поиска по книгам всё равно, лежат файлы рядом или знание приходит из общего
// хранилища организации. Инструменты графа так не умеют и берут коллекцию
// напрямую — графу нужны файлы (`graphOpen` в graph.go).
func (o Options) collection(name string) (kb.Source, error) {
	if o.Library != nil {
		return o.library(name)
	}
	if o.KB == nil {
		return nil, fmt.Errorf("база знаний не настроена: укажите kb.dir в файле настроек")
	}
	if name == "" {
		name = o.KBDefault
	}
	if name == "" {
		names, err := o.KB.Names()
		if err != nil {
			return nil, err
		}
		switch len(names) {
		case 0:
			return nil, fmt.Errorf("книги ещё не проиндексированы: пользователь собирает базу командой /kb add")
		case 1:
			name = names[0]
		default:
			return nil, fmt.Errorf("коллекция не выбрана; доступные: %s (укажите её в параметре collection)",
				strings.Join(names, ", "))
		}
	}
	return o.KB.Source(name)
}

// library выбирает коллекцию в общей библиотеке организации.
//
// Отдельно от локального пути, потому что различается способ узнать список:
// у файлов он читается с диска мгновенно, у службы это запрос по сети, и
// делать его дважды (сначала за именами, потом за коллекцией) незачем —
// драйвер сам разбирается с умолчанием и единственной коллекцией.
func (o Options) library(name string) (kb.Source, error) {
	if name == "" {
		name = o.KBDefault
	}
	return o.Library.Source(name)
}

// argStringOr возвращает строковый аргумент или значение по умолчанию.
func argStringOr(args map[string]any, key, def string) string {
	if s, ok := argString(args, key); ok {
		return s
	}
	return def
}

// graphEntities — понятия графа, которыми дополняется запрос к книгам.
// Нет графа (или библиотека по сети) — дополнять нечем, запрос идёт как есть.
func graphEntities(opts Options, collection, query string, top int) []graph.FoundEntity {
	coll, g, _, release, err := graphOpen(opts, collection)
	if err != nil {
		return nil
	}
	defer release()
	return g.Search(query, graph.SearchOpts{TopEntities: top, Rank: graph.RankWith(coll)}).Entities
}
