// Package graphex связывает граф понятий с сервером Ollama.
//
// Отдельный пакет по той же причине, по какой отдельно живёт kbembed: пакет
// graph не должен зависеть от клиента Ollama, иначе граф окажется привязан
// к одному способу разговаривать с моделью. Здесь же собрана вся настройка
// запроса, от которой зависит цена сборки: выключенные рассуждения, короткое
// окно, низкая температура и потолок одновременных запросов.
package graphex

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// maxInFlight — потолок одновременных запросов, сколько бы ни просили
// настройки. Стенд общий: очередь наших запросов стоит в одной очереди
// с чужими вопросами, и занимать её целиком нельзя.
const maxInFlight = 16

// maxRetries — сколько раз повторить запрос, сорванный по дороге.
// Сборка идёт часами, и одна оборванная связь не должна её ронять.
const maxRetries = 2

// Extractor извлекает сущности и связи через Ollama.
type Extractor struct {
	client    *ollama.Client
	url       string
	model     string
	keepAlive string
	numCtx    int
	maxTokens int
	temp      float64
	sem       chan struct{}

	// retryPause — шаг задержки между повторами сорванного запроса.
	// Поле, а не постоянная, ради тестов: проверка выключения узла в пуле
	// иначе ждала бы по шесть секунд на каждую неудачу.
	retryPause time.Duration
}

// New собирает извлекатель по настройкам.
//
// Возвращает nil, когда модель извлечения не задана: это не ошибка, а честное
// «граф собирать нечем», и вызывающий код скажет об этом словами.
// fallbackURL — адрес сервера, выбранного для чата: на него ссылается пустой
// graph.url.
// Options — что нужно извлекателю из настроек (этап 91, R3.7).
type Options struct {
	URL         string  // адрес сервера; пусто — fallbackURL
	Model       string  // модель извлечения; пусто — граф собирать нечем
	KeepAlive   string  // сколько держать модель загруженной
	Workers     int     // параллельных запросов; 0 — 4, потолок maxInFlight
	NumCtx      int     // окно контекста запроса
	MaxTokens   int     // предел ответа
	Temperature float64 // температура выборки

	// Stall — сколько поток ответа может молчать, прежде чем запрос считается
	// зависшим. 0 — без предела.
	//
	// Сборке он нужнее, чем чату: она идёт часами и через интернет, а предел
	// на ожидание заголовков молчащий поток не ловит. Оборванный канал без
	// сторожа держит слот на карте до конца chat_timeout — и, поскольку
	// у qwen35 слот один, стоит в очереди перед всеми остальными.
	Stall time.Duration

	// NodeWait — сколько пул ждёт возвращения узлов, когда живых не осталось.
	// 0 — не ждать: заход останавливается сразу. Одиночным извлекателем
	// не используется.
	NodeWait time.Duration
}

func New(o Options, fallbackURL string, timeout time.Duration, headers map[string]string) *Extractor {
	if o.Model == "" {
		return nil
	}
	url := o.URL
	if url == "" {
		url = fallbackURL
	}
	if url == "" {
		return nil
	}
	workers := o.Workers
	if workers <= 0 {
		workers = 4
	}
	if workers > maxInFlight {
		workers = maxInFlight
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Extractor{
		client:     ollama.NewWithStall(url, 60*time.Second, timeout, o.Stall, headers),
		url:        url,
		model:      o.Model,
		keepAlive:  o.KeepAlive,
		numCtx:     o.NumCtx,
		maxTokens:  o.MaxTokens,
		temp:       o.Temperature,
		sem:        make(chan struct{}, workers),
		retryPause: 2 * time.Second,
	}
}

// WithModel возвращает копию извлекателя, работающую другой моделью.
//
// Нужна описанию тем. Понятия извлекает модель, выбранная за русские синонимы
// (`qwen3.8`), а Ollama отказывает её архитектуре в параллельных запросах:
// в журнале службы на каждый запрос пишется «model architecture does not
// currently support parallel requests». Замер 26.08.2026: четыре запроса разом
// идут вчетверо дольше одного, тогда как glm на тех же четырёх слотах даёт
// ускорение 2.7x. Резюме темы — обычный русский абзац, синонимы для него
// не нужны, поэтому его можно писать быстрой моделью.
//
// Пустое имя или совпадение с прежним возвращает извлекатель как есть.
func (e *Extractor) WithModel(model string, workers int) *Extractor {
	if e == nil || model == "" || model == e.model {
		return e
	}
	c := *e // клиент общий: адрес сервера тот же
	c.model = model
	if workers > 0 {
		if workers > maxInFlight {
			workers = maxInFlight
		}
		c.sem = make(chan struct{}, workers)
	}
	return &c
}

// Model возвращает имя модели извлечения.
func (e *Extractor) Model() string { return e.model }

// URL возвращает адрес сервера извлечения.
func (e *Extractor) URL() string { return e.url }

// Check проверяет, что сервер отвечает и модель на нём есть.
//
// Нужна до начала сборки, а не по ходу. Без неё запуск при недоступном сервере
// выглядит как зависшая строка хода: первый запрос ждёт своего таймаута
// в несколько минут, и человек всё это время смотрит на «0/22156 · 0.0/с».
// А недоступен он обычно по понятной причине — на время ночных прогонов
// Ollama на стенде слушает только localhost.
func (e *Extractor) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := e.client.Version(ctx); err != nil {
		return fmt.Errorf("сервер %s не отвечает: %w", e.url, err)
	}
	tags, err := e.client.Tags(ctx)
	if err != nil {
		return fmt.Errorf("сервер %s: не удалось получить список моделей: %w", e.url, err)
	}
	for _, m := range tags {
		if sameTag(m.Name, e.model) {
			return nil
		}
	}
	return fmt.Errorf("на сервере %s нет модели %q; есть: %s", e.url, e.model, tagNames(tags))
}

// maxListedTags — сколько имён моделей показывать в отказе «модели нет».
const maxListedTags = 20

// tagNames — имена моделей сервера для отказа: не больше maxListedTags,
// каждое в кавычках с экранированием. Имена приходят с чужого сервера,
// а отказ печатается в ленту и журнал: ни управляющих знаков, ни списка
// в мегабайты в нём быть не должно.
func tagNames(tags []ollama.ModelInfo) string {
	if len(tags) == 0 {
		return "ни одной модели"
	}
	var names []string
	for i, m := range tags {
		if i == maxListedTags {
			names = append(names, fmt.Sprintf("и ещё %d", len(tags)-maxListedTags))
			break
		}
		names = append(names, fmt.Sprintf("%q", m.Name))
	}
	return strings.Join(names, ", ")
}

// sameTag — имя из /api/tags и есть та модель, которую чат получит по имени
// model. Имя без тега Ollama читает как «:latest»; «проба:8b» — другой тег
// с другими весами, и чат по «проба» его не возьмёт, а ответит 404. Прежнее
// сравнение по началу имени принимало любой тег и брало отпечаток у первого
// из них — какого, решал порядок строк в /api/tags.
func sameTag(name, model string) bool {
	return withTag(name) == withTag(model)
}

// withTag — имя модели с тегом: без тега — «:latest». Тег — то, что после
// двоеточия в последней части пути: в «registry.example:5000/m» двоеточие
// принадлежит адресу, а не тегу.
func withTag(name string) string {
	if strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
		return name
	}
	return name + ":latest"
}

// ModelStamp — отпечаток модели на сервере: чем она отличается от одноимённой
// модели на соседнем сервере.
//
// Digest из /api/tags — sha256 МАНИФЕСТА, а не весов: он меняется от любого
// слоя, даже от лицензии и записи о том, из чего создан тег. 03.11.2026 у
// `qwen3.8:latest` на двух серверах digest разошёлся (лицензия есть только
// на одном), а файл весов, шаблон, RENDERER/PARSER и параметры совпали.
// Поэтому пул сверяет Files и рецепт, а digest — только когда Files пуст.
type ModelStamp struct {
	Model   string   // имя модели, как оно уходит в чат
	Digest  string   // digest манифеста из /api/tags
	Quant   string   // уровень квантования, «Q4_K_M»
	Size    int64    // размер всех слоёв в байтах
	Files   []string // FROM и ADAPTER из Modelfile: имена блобов весов и проектора
	Recipe  []Part   // всё прочее, от чего зависит ответ модели
	Version string   // версия Ollama на сервере
}

// Part — одна часть рецепта модели: шаблон, RENDERER/PARSER, системный
// промпт, параметры. Имя — для отказа, который говорит, что именно разошлось.
type Part struct {
	Name  string
	Value string
}

// Stamp снимает отпечаток модели извлечения на этом сервере.
//
// Нужен пулу узлов (pool.go): одно имя `qwen3.8:latest` на двух серверах может
// указывать на разные файлы — другое квантование, другая дата загрузки. Граф,
// собранный наполовину одними весами, наполовину другими, выглядит исправным
// и не чинится ничем, кроме полной пересборки.
//
// Digest и размер — из строки /api/tags ровно этого тега (имя без тега —
// «:latest», см. sameTag); квантование, файлы весов и рецепт — из /api/show
// по тому же имени, которое уйдёт в чат: так сверяется ровно та модель,
// которую сервер возьмёт, а не первая с похожим тегом.
//
// Параметры в рецепте — без тех, что запрос извлечения задаёт сам (options):
// их значение из Modelfile запрос перекрывает, на ответ оно не влияет,
// и расхождение в нём между узлами — не повод отказывать.
func (e *Extractor) Stamp(ctx context.Context) (ModelStamp, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st := ModelStamp{Model: e.model}
	v, err := e.client.Version(ctx)
	if err != nil {
		return st, fmt.Errorf("сервер не отвечает: %w", err)
	}
	st.Version = v
	tags, err := e.client.Tags(ctx)
	if err != nil {
		return st, fmt.Errorf("не удалось получить список моделей: %w", err)
	}
	found := false
	for _, m := range tags {
		if sameTag(m.Name, e.model) {
			st.Digest, st.Quant, st.Size = m.Digest, m.Details.QuantizationLevel, m.Size
			found = true
			break
		}
	}
	if !found {
		return st, fmt.Errorf("нет модели %q", e.model)
	}
	show, err := e.client.Show(ctx, e.model)
	if err != nil {
		return st, fmt.Errorf("модель %q: не удалось прочитать /api/show: %w", e.model, err)
	}
	// Квантование — того же ответа, что и файлы: старые версии Ollama его
	// в /api/show не отдают, тогда остаётся взятое из строки тега.
	if q := show.Details.QuantizationLevel; q != "" {
		st.Quant = q
	}
	files, directives := modelfileParts(show.Modelfile)
	st.Files = files
	st.Recipe = []Part{
		{"шаблон", show.Template},
		{"RENDERER/PARSER", strings.Join(directives, "\n")},
		{"системный промпт", show.System},
		{"параметры", sortedLines(ownParams(show.Parameters, e.options()))},
	}
	return st, nil
}

// ownParams — строки PARAMETER из /api/show без тех, чьё имя запрос задаёт
// сам в options. Список имён берётся из options() — того же места, где
// собирается запрос, — чтобы сверка и запрос не разошлись.
func ownParams(params string, sent map[string]any) string {
	var keep []string
	for _, l := range strings.Split(params, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			if _, ok := sent[f[0]]; ok {
				continue
			}
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// modelfileParts — файлы весов (FROM, ADAPTER) и строки RENDERER/PARSER из
// Modelfile, который отдаёт /api/show.
//
// От пути FROM остаётся имя блоба «sha256-…»: каталог моделей у каждого
// сервера свой. Текст в тройных кавычках (LICENSE, многострочные TEMPLATE
// и SYSTEM) пропускается — в лицензии бывают строки, начинающиеся с тех же
// слов. RENDERER/PARSER старые версии Ollama в отдельные поля /api/show
// не выводят, а в Modelfile пишут все.
func modelfileParts(mf string) (files, directives []string) {
	inQuote := false
	for _, line := range strings.Split(mf, "\n") {
		odd := strings.Count(line, `"""`)%2 == 1
		if inQuote {
			if odd {
				inQuote = false
			}
			continue
		}
		if odd {
			inQuote = true
		}
		kw, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		rest = strings.TrimSpace(rest)
		switch kw {
		case "FROM", "ADAPTER":
			files = append(files, blobName(rest))
		case "RENDERER", "PARSER":
			directives = append(directives, kw+" "+rest)
		}
	}
	sort.Strings(directives)
	return files, directives
}

// blobName — «sha256-…» из пути к блобу; не блоб (имя модели) — как есть.
func blobName(from string) string {
	base := filepath.Base(from)
	if strings.HasPrefix(base, "sha256-") {
		return base
	}
	return from
}

// sortedLines — строки по алфавиту, пробелы внутри строки сжаты: порядок
// PARAMETER у разных версий Ollama разный, а смысл тот же.
func sortedLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// Extract спрашивает модель и возвращает её ответ как есть — разбирает его
// уже пакет graph.
//
// Три настройки запроса стоят отдельного слова.
//
// **Рассуждения выключены.** На извлечении они бесполезны, а стоят дорого:
// замерено на прогонах моделей, что `deepseek-r1:70b` тратил на рассуждения
// десятки тысяч знаков и не выдавал ответа вовсе. Здесь нужен только JSON.
//
// **Окно короткое.** Кусок книги — это тысяча знаков; окно в тридцать две
// тысячи заняло бы видеопамять зря и замедлило бы загрузку модели.
//
// **Температура низкая.** Замерено в ModelsParams.md: чем она выше, тем
// чаще модель отвечает не тем, что просили, — а здесь просили строгий JSON.
func (e *Extractor) Extract(ctx context.Context, system, user string) (string, error) {
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	think := false
	req := ollama.ChatRequest{
		Model: e.model,
		Messages: []ollama.Message{
			{Role: ollama.RoleSystem, Content: system},
			{Role: ollama.RoleUser, Content: user},
		},
		Think:     &think,
		KeepAlive: e.keepAlive,
		Options:   e.options(),
	}

	var last error
	for try := 0; ; try++ {
		text, err := e.once(ctx, req)
		if err == nil {
			return text, nil
		}
		last = err
		if ctx.Err() != nil || try >= maxRetries || !retryable(err) {
			return "", last
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(try+1) * e.retryPause):
		}
	}
}

// options — параметры модели, которые запрос извлечения задаёт сам, поверх
// PARAMETER из Modelfile. Одно место на запрос (Extract) и на сверку узлов
// (Stamp): что перекрыто здесь, то сверке не нужно.
func (e *Extractor) options() map[string]any {
	opts := map[string]any{"temperature": e.temp}
	if e.numCtx > 0 {
		opts["num_ctx"] = e.numCtx
	}
	// Потолок на длину ответа. Замерено 23.08.2026: на отдельных кусках модель
	// выдавала 3 648 токенов вместо трёхсот — тридцать пять секунд на один
	// кусок вместо трёх. Разбор такой простыни всё равно обрезается нашими же
	// пределами, так что платить за неё незачем.
	if e.maxTokens > 0 {
		opts["num_predict"] = e.maxTokens
	}
	return opts
}

func (e *Extractor) once(ctx context.Context, req ollama.ChatRequest) (string, error) {
	var b strings.Builder
	for ev := range e.client.Chat(ctx, req) {
		switch ev.Kind {
		case ollama.EventContent:
			b.WriteString(ev.Text)
		case ollama.EventError:
			return "", ev.Err
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", graph.ErrEmptyAnswer
	}
	return b.String(), nil
}

// retryable — сбой дороги или сервера, помеченный клиентом Ollama.
// Свой список подстрок («connection refused», «timeout», «502»…) отсюда убран
// этапом 91 (R8.6): клиент метит такие ошибки сам, а два предиката об одном
// расходились — именно так 01.09.2026 отказ в соединении остановил заход.
func retryable(err error) bool { return ollama.Retryable(err) }
