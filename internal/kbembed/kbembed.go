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
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
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
func New(cfg *config.Config, fallbackURL string, timeout time.Duration, headers map[string]string) *Embedder {
	if cfg == nil || cfg.KB.EmbedModel == "" {
		return nil
	}
	url := cfg.KB.EmbedURL
	if url == "" {
		url = fallbackURL
	}
	if url == "" {
		return nil
	}
	keep := cfg.KB.EmbedKeepAlive
	if keep == "" {
		keep = "5m"
	}
	return &Embedder{
		client:    ollama.New(url, timeout, headers),
		model:     cfg.KB.EmbedModel,
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
