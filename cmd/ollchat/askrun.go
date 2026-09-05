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
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
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
// однажды снести чужую работу. Включаются явным `--tools on`, и тогда все
// действия, требующие подтверждения, **отклоняются**: спросить некого.

// askOpts — что задают ключи командной строки.
type askOpts struct {
	Question  string
	Questions string // файл с вопросами, по одному в строке
	Stdin     bool
	Repeat    int
	JSON      bool
	ShowMix   bool

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

	// Инструменты, если их разрешили ключом: реестр и правила собирает main —
	// там же, где они собираются для диалога, чтобы песочница и правила deny
	// были теми же самыми.
	registry *tools.Registry
	guard    *permissions.Guard
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

	MixText      string `json:"mix_text,omitempty"`
	MixEntities  int    `json:"mix_entities,omitempty"`
	MixRelations int    `json:"mix_relations,omitempty"`
	MixChunks    int    `json:"mix_chunks,omitempty"`
	MixTokens    int    `json:"mix_tokens,omitempty"`

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

	// Коллекция и граф открываются один раз на весь прогон: открытие графа
	// стоит секунды, а вопросов в файле бывают сотни.
	var coll *kb.Collection
	var g *graph.Graph
	if askMixWanted(cfg, o) {
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

	if coll != nil {
		mix := mixer.Build(question, mixer.Deps{
			Coll:     coll,
			Graph:    g,
			Embedder: kbembed.New(cfg.KB.EmbedOptions(), srv.URL, srv.TimeoutDuration(), srv.Headers),
			Reranker: kbrerank.New(cfg.KB.RerankOptions()),
			BooksOn:  askMixBooks(cfg, o),
			GraphOn:  askMixGraph(cfg, o),
		}, set)
		if !mix.Empty() {
			conv.Append(ollama.Message{Role: ollama.RoleUser, Content: mix.Text})
			ans.MixText = mix.Text
			ans.MixEntities, ans.MixRelations = mix.Entities, mix.Relations
			ans.MixChunks, ans.MixTokens = mix.Chunks, mix.Tokens
		}
	}
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: question})

	opts := askOptions(srv, o)
	// Шаблон проверен при загрузке настроек (config.finalize), здесь ошибки нет.
	stepsPattern, _ := cfg.Log.StepsPattern()
	steps := steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollchat-ask", cfg.Log.Enabled)
	defer steps.Close()
	runner := &agent.Runner{
		Client:        client,
		Model:         model,
		KeepAlive:     srv.KeepAlive,
		Options:       opts,
		Think:         askThink(srv, o),
		MaxIterations: cfg.Agent.MaxIterations,
		MaxRetries:    cfg.Agent.MaxRetries,
		Steps:         steps,
		Turn:          "ask",
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
		TableBoost: cfg.KB.TableBoost,
		Entities:   cfg.Mix.Entities,
		Neighbors:  cfg.Mix.Neighbors,
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
	return m
}

func askCollection(cfg *config.Config, o askOpts) string {
	if strings.TrimSpace(o.Collection) != "" {
		return strings.TrimSpace(o.Collection)
	}
	return cfg.KB.Default
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
	name := askCollection(cfg, o)
	if name == "" {
		names, err := base.Names()
		if err != nil || len(names) == 0 {
			return nil, nil, fmt.Errorf("коллекция не задана: укажите --kb-use или kb.default")
		}
		name = names[0]
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
