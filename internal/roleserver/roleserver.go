// Package roleserver выбирает сервер для роли — сборки графа, векторов,
// чата по умолчанию — и пишет выбор в файл конфига одной строкой (этап 123).
//
// Одна проверка на два входа: команды TUI (/graph-server, /embed-server)
// и ключи командной строки (--use-graph-server, --use-embed-server,
// --use-chat-server), чтобы обвязка и скрипты меняли сервер тем же путём,
// что человек, а не правкой конфига руками.
//
// До записи сервер проверяется: у графа — модель есть на всех его узлах
// и с одними весами (graphex.Pool.Check, тот же, что перед сборкой), у
// эмбеддера — веса совпадают с паспортом векторов каждой коллекции, у чата —
// сервер отвечает и его модель на нём есть. Не прошло — выбор не пишется,
// пока не сказано force: иначе ночная сборка упала бы, а векторы вопросов
// и кусков разошлись бы молча. Нет probe — только предупреждение (решение
// владельца 04.11.2026).
//
// Сервер графа и векторов не меняется под идущей работой с коллекцией (Busy):
// процесс сборки конфиг уже прочитал, а докатка и следующая книга очереди
// прочтут новый — и одна сборка пошла бы на двух серверах. Этот отказ force
// не снимает: граф стоит недель карты (слово владельца 04.11.2026: «граф это
// священная корова»).
package roleserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
)

// Kind — роль сервера, выбор которой пишется в конфиг.
type Kind int

const (
	Graph Kind = iota // сборка графа: [graph].server
	Embed             // векторы (эмбеддер): kb.embed_server
	Chat              // чат по умолчанию: general.default_server
)

// Command — имя команды TUI; ключ командной строки — оно же с приставкой use-.
func (k Kind) Command() string {
	switch k {
	case Embed:
		return "embed-server"
	case Chat:
		return "chat-server"
	}
	return "graph-server"
}

// Flag — ключ командной строки роли.
func (k Kind) Flag() string { return "--use-" + k.Command() }

func (k Kind) Title() string {
	switch k {
	case Embed:
		return "векторов (эмбеддер)"
	case Chat:
		return "чата по умолчанию"
	}
	return "сборки графа"
}

// Key — какой ключ конфига несёт выбор.
func (k Kind) Key() string {
	switch k {
	case Embed:
		return "kb.embed_server"
	case Chat:
		return "general.default_server"
	}
	return "[graph].server"
}

// edit — правка одной строки конфига: ключ встаёт после соседа по смыслу,
// комментарий пишется только у новой строки.
func (k Kind) edit(server, comment string) config.KeyEdit {
	switch k {
	case Embed:
		return config.KeyEdit{Section: "kb", Key: "embed_server", Value: server,
			After: []string{"embed_url", "embed_model"}, Comment: comment}
	case Chat:
		return config.KeyEdit{Section: "general", Key: "default_server", Value: server,
			After: []string{"role"}, Comment: comment}
	}
	return config.KeyEdit{Section: "graph", Key: "server", Value: server,
		After: []string{"url", "model"}, Comment: comment}
}

// Timeout — предел всей проверки: у каждого узла свои 10 с на версию
// и список моделей и 10 с на паспорт модели (graphex.Check и Stamp), узлы
// идут разом, а сверка с прежним сервером — вторым заходом.
const Timeout = 60 * time.Second

// Result — итог проверки сервера перед записью выбора.
type Result struct {
	Kind   Kind
	Server string
	Err    error    // проверка не прошла: без force выбор не пишется
	Notes  []string // что сказать, но писать можно
}

// Job — всё, что нужно проверке, скопировано из конфига при создании:
// Run идёт в фоне, а конфиг под ней меняет TUI.
type Job struct {
	Kind   Kind
	Server config.Server
	// graphs — графы, которые запись [graph].server переведёт на сервер:
	// рабочий ([graph]) первым и подразделы, идущие за ним. Не выбранный
	// в сеансе граф (/graph use): пишется общий ключ, и сверять надо тех,
	// кого он сдвинет (аудит этапа 123).
	graphs     []config.Graph
	old        []graphex.Node
	oldHeaders map[string]string // заголовки, с которыми сборка говорит с old
	embed      kbembed.Options
	collDirs   []string
}

// NewJob готовит проверку сервера name для роли kind. collDirs — каталоги
// коллекций, с чьими графами и векторами сверяется выбор.
func NewJob(cfg *config.Config, kind Kind, name string, collDirs []string) (Job, error) {
	srv, ok := cfg.ServerByName(name)
	if !ok {
		return Job{}, fmt.Errorf("сервер %q не описан в конфиге (есть: %s)",
			name, strings.Join(cfg.ServerNames(), ", "))
	}
	j := Job{Kind: kind, Server: *srv, collDirs: append([]string(nil), collDirs...)}
	switch kind {
	case Graph:
		base := cfg.GraphFor("")
		j.graphs = append(j.graphs, base)
		for _, n := range cfg.GraphsFollowingServer() {
			j.graphs = append(j.graphs, cfg.GraphFor(n))
		}
		j.old = GraphNodesOf(cfg, base)
		j.oldHeaders = cfg.GraphHeadersFor(base)
	case Embed:
		j.embed = cfg.KB.EmbedOptions()
	}
	return j, nil
}

// Run проверяет сервер; вся проверка — не дольше Timeout.
func (j Job) Run() Result { return j.RunContext(context.Background()) }

// RunContext — Run, которую можно снять: Esc в TUI отменяет ctx, и проверка
// кончается сразу, а не через минуту. Текст отказа и замечаний — без
// управляющих знаков (textx.TermSafe): в нём имена моделей и ответы чужого
// сервера, а печатается он в ленту TUI и в терминал ключей --use-*-server.
func (j Job) RunContext(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var r Result
	switch j.Kind {
	case Embed:
		r = checkEmbed(ctx, j.embed, j.Server, j.collDirs)
	case Chat:
		r = checkChat(ctx, j.Server)
	default:
		r = checkGraph(ctx, j.graphs, j.Server, j.old, j.oldHeaders, j.collDirs)
	}
	if r.Err != nil {
		r.Err = errors.New(textx.TermSafe(r.Err.Error()))
	}
	for i, n := range r.Notes {
		r.Notes[i] = textx.TermSafe(n)
	}
	return r
}

// CollDirs — каталоги всех коллекций базы; базы нет — пусто.
func CollDirs(base *kb.Base) ([]string, error) {
	if base == nil {
		return nil, nil
	}
	names, err := base.Names()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, base.CollectionDir(n))
	}
	return out, nil
}

// Busy — кто сейчас пишет в коллекции collDirs или в их графы (graph.Busy:
// индексация, сборка, векторы, помеченная работа); пусто — никто или роль
// чата, которой работа с коллекцией не касается.
func Busy(kind Kind, collDirs []string) string {
	if kind == Chat {
		return ""
	}
	var parts []string
	for _, dir := range collDirs {
		if s := graph.Busy(dir); s != "" {
			parts = append(parts, filepath.Base(dir)+": "+s)
		}
	}
	return strings.Join(parts, "; ")
}

// CheckIdle — отказ выбрать сервер под идущей работой (Busy); nil — работы
// нет. Зовётся и до проверки, и перед самой записью: проверка идёт до минуты,
// и сборка могла начаться за это время.
func CheckIdle(kind Kind, server string, collDirs []string) error {
	busy := Busy(kind, collDirs)
	if busy == "" {
		return nil
	}
	return fmt.Errorf("сервер %s для %s не выбран: идёт работа с коллекцией — %s.\n"+
		"Сервер графа и векторов меняется после её конца: идущий процесс конфиг уже прочитал, "+
		"а докатка и следующая книга очереди прочли бы новый. force этот отказ не снимает", server, kind.Title(), busy)
}

// Write записывает выбор в файл конфига path одной строкой (config.SetString):
// остальной файл не меняется, а новый проходит ту же загрузку, что при запуске.
func Write(path string, kind Kind, server, comment string) error {
	return config.SetString(path, kind.edit(server, comment))
}

// Apply применяет записанный выбор к уже загруженному конфигу.
func Apply(cfg *config.Config, kind Kind, server string) error {
	switch kind {
	case Embed:
		return cfg.SetEmbedServer(server)
	case Chat:
		if _, ok := cfg.ServerByName(server); !ok {
			return fmt.Errorf("сервер %q не описан в конфиге", server)
		}
		cfg.General.DefaultServer = server
		return nil
	}
	return cfg.SetGraphServer(server)
}

// Overrides — замечания свежей загрузки файла о том, что перекрыл выбор
// ([[graph.nodes]], kb.embed_url): они должны быть видны сразу, а не при
// следующем запуске.
func Overrides(path string) []string {
	fresh, _, err := config.Load(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, w := range fresh.Warnings {
		if strings.Contains(w, "перекрывает") {
			out = append(out, w)
		}
	}
	return out
}

// ServerNodes — узлы сборки на сервере s: его [[servers.nodes]], а без них —
// он сам одним узлом (так же, как подставит его загрузка конфига).
func ServerNodes(s config.Server, workers int) []graphex.Node {
	gn := s.GraphNodes()
	if len(gn) == 0 {
		gn = []config.GraphNode{{Name: s.Name, URL: s.URL}}
	}
	out := make([]graphex.Node, 0, len(gn))
	for _, n := range gn {
		w := n.Workers
		if w <= 0 {
			w = workers
		}
		out = append(out, graphex.Node{Name: n.Name, URL: n.URL, Workers: w, Probe: n.Probe, ProbeToken: n.ProbeToken})
	}
	return out
}

// CurrentGraphNodes — на чём сборка идёт сейчас: пул узлов или один адрес.
func CurrentGraphNodes(cfg *config.Config) []graphex.Node { return GraphNodesOf(cfg, cfg.Graph) }

// GraphNodesOf — на чём идёт сборка графа с настройками g: пул узлов или
// один адрес (пустой url — запасной адрес, так же берёт сборка, graphrun.go).
func GraphNodesOf(cfg *config.Config, g config.Graph) []graphex.Node {
	if n := g.ExtractNodes(); len(n) > 0 {
		return n
	}
	url := g.URL
	if url == "" {
		url = cfg.EmbedFallback()
	}
	if url == "" {
		return nil
	}
	return []graphex.Node{{Name: "прежний", URL: url, Workers: g.Workers}}
}

// graphTitle — как назвать граф в отказе: раздел конфига, где его модель.
func graphTitle(g config.Graph) string {
	if g.Name == "" {
		return "рабочий граф, [graph]"
	}
	return "граф " + g.Name + ", [graph." + g.Name + "]"
}

// checkGraph проверяет сервер s для сборки графов graphs (рабочий первым):
// модель каждого — на всех узлах сервера и с одними весами. Графы с одной
// моделью сверяются одним заходом. old — на чём рабочий граф собирается
// сейчас, oldHeaders — заголовки, с которыми с ним говорит сборка.
func checkGraph(ctx context.Context, graphs []config.Graph, s config.Server, old []graphex.Node,
	oldHeaders map[string]string, collDirs []string) Result {
	r := Result{Kind: Graph, Server: s.Name}
	if len(graphs) == 0 || graphs[0].Model == "" {
		r.Err = errors.New("graph.model пуст — сборка графа выключена, проверять нечем")
		return r
	}
	nodes := ServerNodes(s, graphs[0].Workers)
	checked := map[string]bool{}
	for _, g := range graphs {
		if g.Model == "" || checked[g.Model] {
			continue
		}
		checked[g.Model] = true
		var names []string
		for _, o := range graphs {
			if o.Model == g.Model {
				names = append(names, graphTitle(o))
			}
		}
		opts := g.ExtractOptions()
		p, err := graphex.NewPool(nodes, opts, s.TimeoutDuration(), s.Headers)
		if err == nil {
			err = p.Check(ctx)
		}
		if err != nil {
			r.Err = fmt.Errorf("модель %s (%s) на сервере %s: %w", g.Model, strings.Join(names, "; "), s.Name, err)
			return r
		}
		r.Notes = append(r.Notes, p.Notes()...)
		if note := compareOld(ctx, opts, old, oldHeaders, nodes, s); note != "" {
			r.Notes = append(r.Notes, note)
		}
	}
	probe := false
	for _, n := range nodes {
		probe = probe || n.Probe != ""
	}
	if !probe {
		r.Notes = append(r.Notes, "у сервера "+s.Name+" нет probe (наблюдателя ollnode): "+
			"занятость карты перед сборкой проверяться не будет")
	}
	for _, g := range graphs {
		for _, dir := range collDirs {
			if h, ok := graph.QuickHealth(dir, g.Name, 0); ok && h.Model != "" && h.Model != g.Model {
				r.Notes = append(r.Notes, fmt.Sprintf("%s коллекции %s собран моделью %s, а в настройках %s — "+
					"сборка откажет без --graph-allow-model-change", graphTitle(g), filepath.Base(dir), h.Model, g.Model))
			}
		}
	}
	return r
}

// compareOld сверяет веса модели на новом сервере с прежним: штамп графа
// хранит одно имя модели, и смену файла весов под тем же именем сборка не
// заметит. Пусто — расхождения нет или сверять не с чем.
//
// С одними заголовками оба сервера идут одним пулом — как два узла одного
// захода (Pool.Check сверяет файлы, рецепт и квантование). С разными один
// пул нельзя: у него одни заголовки на все узлы, и прежний сервер за прокси
// «расходился» сам с собой (аудит этапа 123). Тогда отпечатки снимаются
// с каждого своими заголовками и сравниваются по весам и квантованию;
// прежний не ответил — замечание «сверить не удалось», не отказ.
func compareOld(ctx context.Context, opts graphex.Options, old []graphex.Node, oldHeaders map[string]string,
	nodes []graphex.Node, s config.Server) string {
	if len(old) == 0 || sameURLs(old, nodes) {
		return ""
	}
	if maps.Equal(oldHeaders, s.Headers) {
		both := make([]graphex.Node, 0, len(old)+len(nodes))
		for _, n := range old {
			n.Name = "было-" + n.Name
			both = append(both, n)
		}
		for _, n := range nodes {
			n.Name = "стало-" + n.Name
			both = append(both, n)
		}
		if bp, err := graphex.NewPool(both, opts, s.TimeoutDuration(), s.Headers); err == nil {
			if err := bp.Check(ctx); err != nil {
				return "сверка с прежним сервером сборки: " + err.Error()
			}
		}
		return ""
	}
	stamp := func(n graphex.Node, headers map[string]string) (graphex.ModelStamp, error) {
		o := opts
		o.URL = n.URL
		return graphex.New(o, n.URL, s.TimeoutDuration(), headers).Stamp(ctx)
	}
	was, err := stamp(old[0], oldHeaders)
	if err != nil {
		return "прежний сервер сборки сверить не удалось (" + old[0].URL + "): " + err.Error()
	}
	now, err := stamp(nodes[0], s.Headers)
	if err != nil {
		return "сверка с прежним сервером сборки: " + err.Error()
	}
	switch {
	case len(was.Files) > 0 && len(now.Files) > 0 && strings.Join(was.Files, " ") != strings.Join(now.Files, " "):
		return fmt.Sprintf("сверка с прежним сервером сборки: модель %s — разные веса (файлы %s против %s)",
			opts.Model, strings.Join(was.Files, " "), strings.Join(now.Files, " "))
	case (len(was.Files) == 0 || len(now.Files) == 0) && was.Digest != "" && now.Digest != "" && was.Digest != now.Digest:
		return fmt.Sprintf("сверка с прежним сервером сборки: модель %s — разные веса (digest %s против %s)",
			opts.Model, shortDigest(was.Digest), shortDigest(now.Digest))
	case was.Quant != now.Quant:
		return fmt.Sprintf("сверка с прежним сервером сборки: модель %s — разное квантование (%s против %s)",
			opts.Model, was.Quant, now.Quant)
	}
	return ""
}

// checkEmbed проверяет сервер s для векторов: модель есть, и её веса — те же,
// что в паспорте векторов каждой коллекции (kb.embed_server у всех один).
func checkEmbed(ctx context.Context, opts kbembed.Options, s config.Server, collDirs []string) Result {
	r := Result{Kind: Embed, Server: s.Name}
	if opts.Model == "" {
		r.Err = errors.New("kb.embed_model пуст — векторы выключены, проверять нечем")
		return r
	}
	opts.URL = s.URL
	digest, err := kbembed.New(opts, s.URL, s.TimeoutDuration(), s.Headers).Stamp(ctx)
	if err != nil {
		r.Err = fmt.Errorf("модель %s на сервере %s: %w", opts.Model, s.Name, err)
		return r
	}
	if len(collDirs) == 0 {
		r.Notes = append(r.Notes, "коллекций нет — веса с паспортом векторов не сверены")
		return r
	}
	var other []string
	for _, dir := range collDirs {
		name := filepath.Base(dir)
		meta, ok, err := kb.ReadVecMeta(dir)
		switch {
		case err != nil:
			r.Notes = append(r.Notes, "коллекция "+name+": паспорт векторов не прочитан, веса не сверены: "+err.Error())
		case !ok:
			// Векторов ещё нет — сверять не с чем, и это не беда.
		case !meta.SameWeights(digest):
			other = append(other, name+" (digest "+shortDigest(meta.Digest)+")")
		case meta.Digest == "" || digest == "":
			r.Notes = append(r.Notes, "коллекция "+name+": веса не сверены — digest нет в паспорте векторов или у сервера")
		}
	}
	if len(other) > 0 {
		r.Err = fmt.Errorf("векторы коллекций %s посчитаны другим файлом модели %s, у сервера %s — digest %s; "+
			"вопросы и куски разошлись бы молча", strings.Join(other, ", "), opts.Model, s.Name, shortDigest(digest))
	}
	return r
}

// checkChat проверяет сервер s для чата по умолчанию: отвечает, и модель
// из его настройки на нём есть.
func checkChat(ctx context.Context, s config.Server) Result {
	r := Result{Kind: Chat, Server: s.Name}
	models, err := ollama.New(s.URL, s.TimeoutDuration(), s.TimeoutDuration(), s.Headers).Tags(ctx)
	if err != nil {
		r.Err = fmt.Errorf("сервер %s (%s) не ответил на список моделей: %w", s.Name, redacted(s.URL), err)
		return r
	}
	if s.Model == "" {
		r.Notes = append(r.Notes, "у сервера "+s.Name+" в конфиге не задана model — модель на нём не сверена")
		return r
	}
	if !hasModel(models, s.Model) {
		r.Err = fmt.Errorf("модели %s (model сервера %s) на нём нет — моделей на сервере %d", s.Model, s.Name, len(models))
	}
	return r
}

// hasModel — есть ли модель name в списке /api/tags: имя без тега Ollama
// читает как «:latest» (kbembed.FullModelName — то же правило у сверки
// весов эмбеддера).
func hasModel(models []ollama.ModelInfo, name string) bool {
	full := kbembed.FullModelName(name)
	for _, m := range models {
		if kbembed.FullModelName(m.Name) == full || kbembed.FullModelName(m.Model) == full {
			return true
		}
	}
	return false
}

// redacted — адрес для текста отказа без пароля из user:pass@. Загрузка
// конфига такие адреса не принимает; это вторая преграда — на случай
// сервера, собранного мимо неё.
func redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(адрес не разобран)"
	}
	return u.Redacted()
}

func sameURLs(a, b []graphex.Node) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].URL != b[i].URL {
			return false
		}
	}
	return true
}

// shortDigest — начало отпечатка весов для сообщения; пусто — «нет».
func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	switch {
	case d == "":
		return "нет"
	case len(d) > 12:
		return d[:12]
	}
	return d
}
