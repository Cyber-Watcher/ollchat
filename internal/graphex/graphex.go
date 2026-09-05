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
		client:    ollama.New(url, 60*time.Second, timeout, headers),
		url:       url,
		model:     o.Model,
		keepAlive: o.KeepAlive,
		numCtx:    o.NumCtx,
		maxTokens: o.MaxTokens,
		temp:      o.Temperature,
		sem:       make(chan struct{}, workers),
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
		if m.Name == e.model || strings.HasPrefix(m.Name, e.model+":") {
			return nil
		}
	}
	var names []string
	for _, m := range tags {
		names = append(names, m.Name)
	}
	return fmt.Errorf("на сервере %s нет модели %q; есть: %s",
		e.url, e.model, strings.Join(names, ", "))
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
	req := ollama.ChatRequest{
		Model: e.model,
		Messages: []ollama.Message{
			{Role: ollama.RoleSystem, Content: system},
			{Role: ollama.RoleUser, Content: user},
		},
		Think:     &think,
		KeepAlive: e.keepAlive,
		Options:   opts,
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
		case <-time.After(time.Duration(try+1) * 2 * time.Second):
		}
	}
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
