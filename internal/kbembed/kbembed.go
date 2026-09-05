// Package kbembed связывает базу знаний с сервером Ollama.
//
// Отдельный пакет нужен, чтобы kb не зависел от клиента Ollama: база знаний
// должна уметь работать с любым способом считать смыслы, а не только с этим.
// Здесь же живут ограничения вежливости к общему серверу — не больше двух
// запросов в полёте и конечный keep_alive.
package kbembed

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// inFlight — потолок одновременных запросов к серверу эмбеддингов.
//
// Сколько слать на самом деле, решает kb.embed_workers; здесь только защита
// от явно чрезмерного потока. Замер на стенде: выше четырёх пачек скорость
// почти не растёт — упирается в саму модель, — а очередь из наших запросов
// стоит в одной очереди с чужими вопросами.
const inFlight = 8

// maxRetries — сколько раз повторить пачку при сбое, который того стоит.
// Многочасовая работа не должна срываться из-за одной оборванной связи.
const maxRetries = 3

// Embedder считает векторы через Ollama.
type Embedder struct {
	client    *ollama.Client
	model     string
	keepAlive string
	sem       chan struct{}
}

// New собирает эмбеддер по настройкам.
//
// Возвращает nil, когда модель не задана: это не ошибка, а обычное состояние
// «смысловой поиск не настроен», и всё продолжает работать по словам.
// fallbackURL — адрес сервера, выбранного для чата: на него ссылается пустой
// kb.embed_url.
// Options — что нужно эмбеддеру из настроек. Узкая структура вместо всего
// конфига: пакет не должен знать формат файла настроек (этап 91, R3.7).
type Options struct {
	URL       string        // адрес сервера эмбеддингов; пусто — fallbackURL
	Model     string        // модель; пусто — смысловой поиск не настроен
	KeepAlive string        // сколько держать модель загруженной; пусто — 5m
	Timeout   time.Duration // предел ожидания ответа
	// CacheDir — где держать кэш векторов одиночных запросов между запусками
	// (файл queries.cache); пусто — только в памяти. Этап 91, R2.12.
	CacheDir string
}

func New(o Options, fallbackURL string, timeout time.Duration, headers map[string]string) *Embedder {
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
	keep := o.KeepAlive
	if keep == "" {
		keep = "5m"
	}
	if o.CacheDir != "" {
		useDiskCache(filepath.Join(o.CacheDir, "queries.cache"))
	}
	// Предел ожидания у эмбеддингов свой: первый запрос ждёт, пока сервер
	// выгрузит чужую модель и загрузит эту. Переданный вызывающим timeout
	// используется только когда он больше — так настройка сервера может
	// сделать ожидание длиннее, но не короче разумного.
	wait := o.Timeout
	if timeout > wait {
		wait = timeout
	}
	return &Embedder{
		client:    ollama.New(url, wait, wait, headers),
		model:     o.Model,
		keepAlive: keep,
		sem:       make(chan struct{}, inFlight),
	}
}

// Model — имя модели. По нему сверяется пригодность посчитанных векторов.
func (e *Embedder) Model() string {
	if e == nil {
		return ""
	}
	return e.model
}

// URL — адрес сервера эмбеддингов, для отчётов и подсказок.
func (e *Embedder) URL() string {
	if e == nil {
		return ""
	}
	return e.client.BaseURL()
}

// Embed векторизует пачку текстов.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e == nil || len(texts) == 0 {
		return nil, nil
	}
	// Одиночный запрос — это вопрос человека или модели, и он повторяется.
	// Пачка — векторизация библиотеки, там повторов нет.
	if len(texts) == 1 {
		if v, ok := cacheGet(e.model, texts[0]); ok {
			return [][]float32{v}, nil
		}
	}

	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-e.sem }()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		vecs, err := e.client.Embed(ctx, ollama.EmbedRequest{
			Model:     e.model,
			Input:     texts,
			KeepAlive: e.keepAlive,
		})
		if err == nil {
			if len(texts) == 1 && len(vecs) == 1 {
				cachePutAndPersist(e.model, texts[0], vecs[0])
			}
			return vecs, nil
		}
		lastErr = err
		if ctx.Err() != nil || !ollama.Retryable(err) {
			return nil, err
		}
		// Пауза растёт: если сервер занят чужой работой, частые повторы
		// только добавляют ему нагрузки.
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// Placement — где сейчас лежит модель эмбеддингов.
type Placement struct {
	Loaded  bool
	VRAMPct int // сколько процентов веса модели в видеопамяти
}

// Slow сообщает, что модель считает в основном на процессоре.
func (p Placement) Slow() bool { return p.Loaded && p.VRAMPct < 90 }

// Warning — что сказать пользователю, или пусто.
//
// Случай живой и неочевидный: Ollama размещает модель по свободному месту
// в момент загрузки и **не переселяет её обратно**, когда место освободилось.
// Достаточно один раз загрузить рядом большую модель — и эмбеддинги останутся
// считаться в оперативной памяти, втрое медленнее, ничем этого не показывая.
// Замер на стенде: 21.5 кусков/с на карте против 7.8 в оперативной.
//
// Смотреть надо size_vram, а не size: общий размер выглядит одинаково в обоих
// случаях.
func (p Placement) Warning(model string) string {
	if !p.Slow() {
		return ""
	}
	return fmt.Sprintf("модель %s лежит в видеопамяти лишь на %d%% — считать будет втрое дольше. "+
		"Ollama размещает модель по свободному месту при загрузке и обратно не переселяет; "+
		"освободите карту и выгрузите модель, чтобы она встала заново", model, p.VRAMPct)
}

// Placement спрашивает сервер, где лежит модель.
func (e *Embedder) Placement(ctx context.Context) Placement {
	if e == nil {
		return Placement{}
	}
	running, err := e.client.PS(ctx)
	if err != nil {
		return Placement{}
	}
	for _, m := range running {
		if m.Name != e.model && m.Model != e.model &&
			!strings.HasPrefix(m.Name, e.model+":") {
			continue
		}
		if m.Size <= 0 {
			return Placement{Loaded: true, VRAMPct: 100}
		}
		return Placement{Loaded: true, VRAMPct: int(m.SizeVRAM * 100 / m.Size)}
	}
	return Placement{}
}
