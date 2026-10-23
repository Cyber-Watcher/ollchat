package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/qtranslate"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Спросить модель из командной строки — без интерфейса.
//
// **Зачем.** Числа отбора (вес уместности связи, ширина пула, сколько
// фрагментов брать, порог косинуса) подбираются только замером, а замер
// до сих пор упирался в то, что ollchat умел спрашивать модель лишь из TUI.
// Каждый прогон приходилось обкладывать одноразовыми скриптами, и сравнивались
// в итоге не ответы, а выдача поиска.
//
//	ollchat --ask "как связаны RAG и дообучение" --graph-sense 1.5 --temperature 0
//	ollchat --questions вопросы.txt --json --mix graph > out.jsonl
//
// **Повторяемость — условие, а не удобство.** Ответ модели меняется сам по себе,
// и без `--temperature 0` (а лучше и `--seed`) сравнение двух настроек измеряет
// сэмплирование, а не настройку. Поэтому в этом режиме температура ставится
// в ноль по умолчанию, в отличие от диалога.
//
// **Инструменты по умолчанию выключены.** Агент без человека не может ответить
// на запрос подтверждения, а `bash` и `write_file` в цикле замера — способ
// однажды снести чужую работу. Включаются явным `--tools`, и тогда все
// действия, требующие подтверждения, **отклоняются**: спросить некого.

// askOpts — что задают ключи командной строки.
type askOpts struct {
	Question  string
	Questions string // файл с вопросами, по одному в строке
	Stdin     bool
	Repeat    int
	JSON      bool
	ShowMix   bool
	// MixOnly — собрать подмес и не спрашивать модель: замер выдачи ровно
	// тем путём, каким она уходит модели, без минуты карты на ответ
	// (этап 112, О7).
	MixOnly bool
	// Logprobs — просить у сервера логарифмы вероятностей токенов и вывести
	// сводку по ответу (logprob_*): замер уверенности модели (этап 112, Ч15).
	Logprobs bool

	Temperature float64
	HasTemp     bool
	Seed        int
	HasSeed     bool
	NumCtx      int
	Think       string // on, off, пусто — как в конфиге
	Tools       bool

	// Подмешивание и числа отбора.
	Mix        string // off, graph, books, all; пусто — как в конфиге
	Collection string
	Entities   int
	Neighbors  int

	SenseWeight float64
	HasSense    bool
	Pool        int

	TopK           int
	MaxPerBook     int
	MinCosine      float64
	HasMinCos      bool
	SemanticWeight float64
	HasSemWeight   bool

	// Прибор «потолка поиска» (этап 112, О7): куски вместо найденных
	// и куски целиком вместо выдержек.
	Pin        []string
	FullChunks bool
	// SenseSnippet — выдержка из предложений, лучших по реранкеру (этап 112, Ч21).
	SenseSnippet bool

	// Инструменты, если их разрешили ключом: реестр и правила собирает main —
	// там же, где они собираются для диалога, чтобы песочница и правила deny
	// были теми же самыми.
	registry *tools.Registry
	guard    *permissions.Guard

	// library — общая библиотека (kb.server_url): подмес идёт через службу,
	// свой граф не открывается (этап 121, А2); nil — по своим файлам.
	// graphCache — общий кэш графов для запасного пути, когда служба не
	// отвечает (А4): граф открывается при переходе и отпускается после вопроса.
	library    kb.Library
	graphCache *graph.Cache
	base       *kb.Base // свои файлы для запасного пути; nil — их нет
}

// askAnswer — одна строка вывода --json.
type askAnswer struct {
	Question string  `json:"question"`
	Answer   string  `json:"answer"`
	Thinking string  `json:"thinking,omitempty"`
	Model    string  `json:"model"`
	Server   string  `json:"server"`
	Seconds  float64 `json:"seconds"`

	PromptTokens int `json:"prompt_tokens"`
	EvalTokens   int `json:"eval_tokens"`

	// Сводка по логарифмам вероятностей токенов ответа (только с --logprobs):
	// сколько токенов, средний и наименьший логарифм, токенов с вероятностью
	// ниже 0,5. Сигнал «насколько модель уверена» (этап 112, Ч15).
	LogprobTokens int     `json:"logprob_tokens,omitempty"`
	LogprobMean   float64 `json:"logprob_mean,omitempty"`
	LogprobMin    float64 `json:"logprob_min,omitempty"`
	LogprobLow    int     `json:"logprob_low,omitempty"`

	MixText      string   `json:"mix_text,omitempty"`
	MixEntities  int      `json:"mix_entities,omitempty"`
	MixRelations int      `json:"mix_relations,omitempty"`
	MixChunks    int      `json:"mix_chunks,omitempty"`
	MixTokens    int      `json:"mix_tokens,omitempty"`
	MixRefs      []string `json:"mix_refs,omitempty"` // поданные куски в порядке подачи
	// MixRemote — служба библиотеки не ответила (искал по своим файлам) или
	// отказала; пусто — подмес шёл обычным путём (этап 121, А4).
	MixRemote string `json:"mix_remote,omitempty"`

	Tools []string `json:"tools,omitempty"`
	Error string   `json:"error,omitempty"`

	// Settings — чем считали. Без этого jsonl через неделю нечитаем:
	// строки одинаковые, а какими ключами получены — уже не вспомнить.
	Settings map[string]any `json:"settings"`
}

// runAsk выполняет режим --ask.
func runAsk(cfg *config.Config, srv *config.Server, model string, o askOpts) error {
	questions, err := askQuestions(o)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		return fmt.Errorf("нечего спрашивать: задайте --ask \"вопрос\", --ask-stdin или --questions файл")
	}

	client := ollama.NewWithStall(srv.URL, srv.TimeoutDuration(), srv.ChatTimeoutDuration(),
		srv.StallTimeoutDuration(), srv.Headers)

	// Числа отбора: конфиг, поверх — ключи.
	set := askSettings(cfg, o)
	// Закреплённый кусок без коллекции молча не подмешался бы, и замер потолка
	// сравнил бы ответ «по эталону» с ответом без книг вовсе.
	if len(o.Pin) > 0 && !askMixBooks(cfg, o) {
		return fmt.Errorf("--mix-pin подаёт куски книг: нужен --mix books или --mix all")
	}
	// Без подмеса --mix-only не напечатал бы ничего, и пустые строки
	// выглядели бы как «поиск ничего не нашёл».
	if o.MixOnly && !askMixWanted(cfg, o) {
		return fmt.Errorf("--mix-only печатает подмес: нужен --mix books, graph или all")
	}

	// Коллекция и граф открываются один раз на весь прогон: открытие графа
	// стоит секунды, а вопросов в файле бывают сотни.
	//
	// С общей библиотекой своего не открывается ничего: книги и карта — у службы
	// на каждый вопрос (askOne), свои файлы — только при её недоступности.
	var coll *kb.Collection
	var g *graph.Graph
	if askMixWanted(cfg, o) && o.library == nil {
		coll, g, err = askOpenKB(cfg, o)
		if err != nil {
			return err
		}
		if g != nil {
			defer g.Close()
		}
	}

	repeat := o.Repeat
	if repeat <= 0 {
		repeat = 1
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for _, q := range questions {
		for i := 0; i < repeat; i++ {
			ans := askOne(client, cfg, srv, model, q, o, set, coll, g)
			if o.JSON {
				line, err := json.Marshal(ans)
				if err != nil {
					return err
				}
				out.Write(line)
				out.WriteByte('\n')
			} else {
				if len(questions) > 1 || repeat > 1 {
					fmt.Fprintf(out, "── %s%s ──\n", q, askRunSuffix(i, repeat))
				}
				if o.MixOnly {
					// Ответа не было: печатается то, что ушло бы модели.
					if ans.MixText == "" {
						fmt.Fprintln(out, "подмес пуст")
					} else {
						fmt.Fprintf(out, "%s\nкуски по порядку: %s\n", ans.MixText, strings.Join(ans.MixRefs, ", "))
					}
					out.Flush()
					continue
				}
				if o.ShowMix && ans.MixText != "" {
					fmt.Fprintf(out, "%s\n── ответ ──\n", ans.MixText)
				}
				if ans.Error != "" {
					fmt.Fprintf(out, "ошибка: %s\n", ans.Error)
				} else {
					fmt.Fprintln(out, ans.Answer)
				}
			}
			out.Flush()
		}
	}
	return nil
}

func askRunSuffix(i, repeat int) string {
	if repeat <= 1 {
		return ""
	}
	return fmt.Sprintf(" (прогон %d из %d)", i+1, repeat)
}

// askOne — один вопрос: подмес, запрос, сбор ответа.
func askOne(client *ollama.Client, cfg *config.Config, srv *config.Server, model, question string,
	o askOpts, set mixer.Settings, coll *kb.Collection, g *graph.Graph) askAnswer {

	ans := askAnswer{
		Question: question, Model: model, Server: srv.Name,
		Settings: askSettingsMap(o, set, cfg),
	}

	conv := session.New(srv.SystemPrompt)
	conv.SetToday(cfg.General.Today)

	if coll != nil || (o.library != nil && askMixWanted(cfg, o)) {
		deps := mixer.Deps{
			Embedder:   kbembed.New(cfg.KB.EmbedOptions(), srv.URL, srv.TimeoutDuration(), srv.Headers),
			Translator: qtranslate.New(cfg, srv.URL, srv.Headers),
			Reranker:   kbrerank.New(cfg.KB.RerankOptions()),
			BooksOn:    askMixBooks(cfg, o),
			GraphOn:    askMixGraph(cfg, o),
		}
		var mix mixer.Result
		if coll != nil {
			deps.Coll, deps.Graph = coll, g
			mix = mixer.Build(question, deps, set)
		} else {
			mix = askRemoteMix(cfg, o, question, deps, set)
		}
		if mix.Remote != "" {
			ans.MixRemote = mix.Remote
			fmt.Fprintln(os.Stderr, "подмес: "+mix.Remote)
		}
		if !mix.Empty() {
			conv.Append(ollama.Message{Role: ollama.RoleUser, Content: mix.Text})
			ans.MixText = mix.Text
			ans.MixEntities, ans.MixRelations = mix.Entities, mix.Relations
			ans.MixChunks, ans.MixTokens = mix.Chunks, mix.Tokens
			ans.MixRefs = mix.Refs
		}
	}
	if o.MixOnly {
		return ans
	}
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: question})

	opts := askOptions(srv, o)
	// Шаблон проверен при загрузке настроек (config.finalize), здесь ошибки нет.
	stepsPattern, _ := cfg.Log.StepsPattern()
	steps := steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollchat-ask", cfg.Log.Enabled)
	defer steps.Close()
	pruneSteps(steps, cfg)
	var sreg *tools.Registry
	if o.Tools {
		sreg = o.registry
	}
	steps.Write(sessionStep(cfg, srv, model, sreg))
	runner := &agent.Runner{
		Client:        client,
		Model:         model,
		KeepAlive:     srv.KeepAlive,
		Options:       opts,
		Think:         askThink(srv, o),
		MaxIterations: cfg.Agent.MaxIterations,
		MaxRetries:    cfg.Agent.MaxRetries,
		Steps:         steps,
		// Свой идентификатор хода, как у диалога (этап 112, В1): с константой «ask»
		// два запуска в одну секунду писали в один файл журнала, и записи двух ходов
		// сливались в один ход для stepstat.py, который группирует по (файл, turn).
		Turn:     chatlog.FormatTurnID(chatlog.NewSessionID(), 1),
		Logprobs: o.Logprobs,
		// Защита и пределы хода (этап 112) — те же, что в диалоге.
		JournalFailClosed:  cfg.Agent.JournalFailClosed,
		UntrustedGate:      cfg.Agent.UntrustedGate,
		PrivateEgressCheck: cfg.Agent.PrivateEgressCheck,
		MaxWritesNoask:     cfg.Agent.MaxWritesNoask,
		MaxTurnTokens:      cfg.Agent.MaxTurnTokens,
		OutHead:            cfg.Log.StepsOutHead,
	}
	if o.Tools && o.registry != nil {
		runner.Tools = o.registry
		runner.Guard = o.guard
		runner.ToolsSupported = true
	}

	started := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var answer, thinking strings.Builder
	for ev := range runner.Run(ctx, conv) {
		switch ev.Kind {
		case agent.EventContent:
			answer.WriteString(ev.Text)
		case agent.EventThinking:
			thinking.WriteString(ev.Text)
		case agent.EventToolResult:
			if ev.Tool != nil {
				ans.Tools = append(ans.Tools, ev.Tool.Name)
			}
		case agent.EventToolConfirm:
			// Спросить некого: подтверждать действие в безголовом режиме
			// нельзя, и молча выполнять его тем более нельзя.
			if ev.Confirm != nil {
				ev.Confirm.Reply <- agent.AnswerNo
			}
		case agent.EventStats:
			ans.PromptTokens, ans.EvalTokens = ev.Stats.PromptEvalCount, ev.Stats.EvalCount
			if ev.Stats.LogprobN > 0 {
				ans.LogprobTokens, ans.LogprobMean = ev.Stats.LogprobN, ev.Stats.MeanLogprob()
				ans.LogprobMin, ans.LogprobLow = ev.Stats.LogprobMin, ev.Stats.LogprobLow
			}
		case agent.EventError:
			if ev.Err != nil {
				ans.Error = ev.Err.Error()
			}
		}
	}
	ans.Answer = strings.TrimSpace(answer.String())
	ans.Thinking = strings.TrimSpace(thinking.String())
	ans.Seconds = time.Since(started).Seconds()
	return ans
}

// askOptions собирает options запроса: конфиг, поверх — ключи.
//
// Температура ноль по умолчанию: в этом режиме сравнивают ответы, а с чужой
// температурой сравнение измеряет сэмплирование.
func askOptions(srv *config.Server, o askOpts) map[string]any {
	opts := map[string]any{}
	for k, v := range srv.Options {
		opts[k] = v
	}
	if o.HasTemp {
		opts["temperature"] = o.Temperature
	} else if _, ok := opts["temperature"]; !ok {
		opts["temperature"] = 0.0
	}
	if o.HasSeed {
		opts["seed"] = o.Seed
	}
	if o.NumCtx > 0 {
		opts["num_ctx"] = o.NumCtx
	}
	return opts
}

func askThink(srv *config.Server, o askOpts) *bool {
	switch strings.ToLower(strings.TrimSpace(o.Think)) {
	case "on", "true", "1":
		v := true
		return &v
	case "off", "false", "0":
		v := false
		return &v
	}
	return srv.Think
}

// askSettings — числа отбора: конфиг, поверх — ключи.
func askSettings(cfg *config.Config, o askOpts) mixer.Settings {
	s := mixer.Settings{
		Chain:         cfg.Mix.Chain,
		RelationYears: cfg.Mix.RelationYears,
		MapPlain:      cfg.Mix.MapStyle == "plain",
		MapPairOnly:   cfg.Mix.MapWhen == "pair",
		Abstain:       cfg.Mix.Abstain,
		SenseEntry:    cfg.Mix.SenseEntry,
		AbstainScore:  cfg.KB.AbstainScore,
		AbstainGap:    cfg.KB.AbstainGap,
		TableBoost:    cfg.KB.TableBoost,
		ExpandLimit:   cfg.KB.ExpandLimitOr(),
		DedupeCosine:  cfg.KB.DedupeCosine,
		Entities:      cfg.Mix.Entities,
		Neighbors:     cfg.Mix.Neighbors,
		Rank: graph.NeighborRank{
			SenseWeight: cfg.Graph.NeighborSenseWeight,
			Pool:        cfg.Graph.NeighborPool,
		},
		TopK:               cfg.KB.TopK,
		MaxPerBook:         cfg.KB.MaxPerBook,
		MinCosine:          cfg.KB.MinCosine,
		SemanticWeight:     cfg.KB.SemanticWeight,
		Semantic:           cfg.KB.Semantic,
		QueryTimeout:       cfg.KB.QueryTimeoutDuration(),
		ServiceTimeout:     cfg.KB.ServerTimeoutDuration(), // срок карты и книг от службы (находка 12 аудита 23.10)
		AnswerStyle:        cfg.KB.AnswerStyle,
		QuotesWithoutTools: cfg.Mix.QuotesWithoutTools,
		RerankOpts: kb.RerankOpts{
			Candidates: cfg.KB.RerankCandidates,
			Snippet:    cfg.KB.RerankSnippet,
		},
		Collection: askCollection(cfg, o),
	}
	if o.Entities > 0 {
		s.Entities = o.Entities
	}
	if o.Neighbors > 0 {
		s.Neighbors = o.Neighbors
	}
	if o.HasSense {
		s.Rank.SenseWeight = o.SenseWeight
	}
	if o.Pool > 0 {
		s.Rank.Pool = o.Pool
	}
	if o.TopK > 0 {
		s.TopK = o.TopK
	}
	if o.MaxPerBook > 0 {
		s.MaxPerBook = o.MaxPerBook
	}
	if o.HasMinCos {
		s.MinCosine = o.MinCosine
	}
	if o.HasSemWeight {
		s.SemanticWeight = o.SemanticWeight
	}
	s.Pin, s.FullChunks, s.SenseSnippet = o.Pin, o.FullChunks, o.SenseSnippet
	return s
}

// askSettingsMap — чем считали, для строки json.
func askSettingsMap(o askOpts, s mixer.Settings, cfg *config.Config) map[string]any {
	m := map[string]any{
		"mix":             askMixName(cfg, o),
		"collection":      s.Collection,
		"graph_sense":     s.Rank.SenseWeight,
		"graph_pool":      s.Rank.Pool,
		"mix_entities":    s.Entities,
		"mix_neighbors":   s.Neighbors,
		"kb_topk":         s.TopK,
		"kb_max_per_book": s.MaxPerBook,
		"kb_min_cosine":   s.MinCosine,
		"kb_semantic_w":   s.SemanticWeight,
		"tools":           o.Tools,
	}
	if o.HasTemp {
		m["temperature"] = o.Temperature
	} else {
		m["temperature"] = 0.0
	}
	if o.HasSeed {
		m["seed"] = o.Seed
	}
	if o.NumCtx > 0 {
		m["num_ctx"] = o.NumCtx
	}
	if len(s.Pin) > 0 {
		m["mix_pin"] = s.Pin
	}
	if s.FullChunks {
		m["mix_full"] = true
	}
	if s.SenseSnippet {
		m["mix_sense_snippet"] = true
	}
	if o.MixOnly {
		m["mix_only"] = true
	}
	if o.Logprobs {
		m["logprobs"] = true
	}
	return m
}

func askCollection(cfg *config.Config, o askOpts) string {
	if strings.TrimSpace(o.Collection) != "" {
		return strings.TrimSpace(o.Collection)
	}
	return cfg.KB.Default
}

// splitList — значение ключа через запятую в список; пустые элементы
// (лишние запятые, пробелы по краям) пропускаются.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// askMixName — что просили подмешивать.
func askMixName(cfg *config.Config, o askOpts) string {
	if v := strings.ToLower(strings.TrimSpace(o.Mix)); v != "" {
		return v
	}
	switch {
	case cfg.Mix.Graph && cfg.Mix.Books:
		return "all"
	case cfg.Mix.Graph:
		return "graph"
	case cfg.Mix.Books:
		return "books"
	}
	return "off"
}

func askMixWanted(cfg *config.Config, o askOpts) bool { return askMixName(cfg, o) != "off" }
func askMixGraph(cfg *config.Config, o askOpts) bool {
	n := askMixName(cfg, o)
	return n == "graph" || n == "all"
}
func askMixBooks(cfg *config.Config, o askOpts) bool {
	n := askMixName(cfg, o)
	return n == "books" || n == "all"
}

// askOpenKB открывает коллекцию и её граф — по разу на весь прогон.
func askOpenKB(cfg *config.Config, o askOpts) (*kb.Collection, *graph.Graph, error) {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("база знаний: %w", err)
	}
	name, err := askLocalName(base, askCollection(cfg, o))
	if err != nil {
		return nil, nil, err
	}
	coll, err := base.Open(name)
	if err != nil {
		return nil, nil, fmt.Errorf("коллекция %s: %w", name, err)
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		g = nil // графа нет — подмешаются одни выдержки, это законное состояние
	}
	return coll, g, nil
}

// askRemoteMix — подмес через общую библиотеку (этап 121, А2): книги и карта
// понятий — у службы, свой граф не открывается. Служба не отвечает — свои
// файлы (А4): коллекция открывается при переходе, граф берётся из общего кэша
// графов на один вопрос.
//
// Коллекция службы открывается на каждый вопрос, как у инструментов модели:
// это два запроса по сети, а не гигабайт графа в памяти.
func askRemoteMix(cfg *config.Config, o askOpts, question string, deps mixer.Deps, set mixer.Settings) mixer.Result {
	name := askCollection(cfg, o)
	lib := kb.Fallback{Remote: o.library}
	if o.base != nil {
		// Своя коллекция выбирается так же, как в askOpenKB: названная или
		// единственная. Несколько без kb.default — отказ с перечнем, как в TUI
		// (localCollection), а не молча первая (аудит 23.10.2026): ответ по чужой
		// коллекции выглядел бы ответом по нужной.
		local, lerr := askLocalName(o.base, name)
		lib.Local, lib.Default = o.base, local
		base := o.base
		deps.LocalGraph = mixer.CachedGraph(o.graphCache, cfg.Graph.Rules(), func() (*kb.Collection, error) {
			if lerr != nil {
				return nil, lerr
			}
			return base.Open(local)
		})
	}
	src, err := lib.Source(name)
	if err != nil {
		return mixer.Result{Remote: err.Error()}
	}
	deps.Coll = src
	if ms, ok := o.library.(mixer.MapSource); ok {
		deps.Map = ms
	}
	if set.Collection == "" {
		set.Collection = src.Name()
	}
	out := mixer.Build(question, deps, set)
	if kb.Fell(src) {
		// По какой своей коллекции искал — в заметке: в прогоне --ask её видно
		// только отсюда.
		out.Remote += "; своя коллекция " + src.Name()
	}
	return out
}

// askLocalName — своя коллекция для --ask: названная (--kb-use, kb.default)
// или единственная. Несколько без названной — отказ с перечнем, как в TUI.
func askLocalName(base *kb.Base, name string) (string, error) {
	if name = strings.TrimSpace(name); name != "" {
		return name, nil
	}
	names, err := base.Names()
	if err != nil {
		return "", fmt.Errorf("база знаний: %w", err)
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("коллекция не задана и своих коллекций нет: укажите --kb-use или kb.default")
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("коллекция не задана: укажите --kb-use или kb.default (своих коллекций %d: %s)",
		len(names), strings.Join(names, ", "))
}

// askQuestions — откуда брать вопросы: ключ, файл или стандартный ввод.
func askQuestions(o askOpts) ([]string, error) {
	if s := strings.TrimSpace(o.Questions); s != "" {
		data, err := os.ReadFile(s)
		if err != nil {
			return nil, fmt.Errorf("файл вопросов: %w", err)
		}
		var out []string
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out, nil
	}
	if o.Stdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("стандартный ввод: %w", err)
		}
		if q := strings.TrimSpace(string(data)); q != "" {
			return []string{q}, nil
		}
		return nil, nil
	}
	if q := strings.TrimSpace(o.Question); q != "" {
		return []string{q}, nil
	}
	return nil, nil
}
