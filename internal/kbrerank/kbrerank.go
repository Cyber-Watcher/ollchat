// Package kbrerank — клиент переранжирования к llama-server.
//
// Почему отдельная служба, а не Ollama. Ollama кросс-энкодеры не обслуживает:
// у неё нет ручки переранжирования, а эмбеддинги она отдаёт только при заданном
// типе пулинга, которого у реранкеров особый (RANK). Проверено 26.08.2026 на
// двух сборках `bge-reranker-v2-m3` — обе отвечают одинаково:
//
//	/api/embed    → "This server does not support embeddings"
//	/api/generate → "the current context does not logits computation"
//	/api/rerank   → 404
//
// Поэтому рядом поднимается `llama-server` из llama.cpp с ключом `--reranking`,
// у которого ручка `/rerank` есть. Обе службы уживаются на одной карте:
// реранкер держит 1.1 ГБ постоянно и отвечает короткими всплесками.
package kbrerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Reranker оценивает пары «вопрос — кусок» через llama-server.
type Reranker struct {
	url    string
	model  string
	client *http.Client
}

// New собирает клиент по настройкам.
//
// Возвращает nil, когда адрес не задан: это не ошибка, а обычное состояние
// «переранжирование не настроено», и поиск продолжает работать одной ступенью.
// Options — что нужно клиенту переранжирования из настроек (этап 91, R3.7).
type Options struct {
	URL     string        // адрес llama-server с /rerank; пусто — не настроено
	Model   string        // имя модели, если сервер его требует
	Timeout time.Duration // предел ожидания ответа
}

func New(o Options) *Reranker {
	url := strings.TrimRight(strings.TrimSpace(o.URL), "/")
	if url == "" {
		return nil
	}
	timeout := o.Timeout
	return &Reranker{
		url:    url,
		model:  strings.TrimSpace(o.Model),
		client: &http.Client{Timeout: timeout},
	}
}

// Model возвращает имя модели переранжирования — для отчётов и для проверок.
func (r *Reranker) Model() string {
	if r == nil {
		return ""
	}
	if r.model == "" {
		return "llama-server"
	}
	return r.model
}

type rerankRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type rerankResponse struct {
	Results []struct {
		Index int     `json:"index"`
		Score float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank возвращает оценки в порядке переданных документов.
//
// Служба отвечает списком, отсортированным по оценке, поэтому ответ
// раскладывается обратно по номерам: вызывающий рассчитывает на исходный
// порядок и сам решает, как переставлять.
func (r *Reranker) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if r == nil || len(docs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(rerankRequest{
		Model: r.model, Query: query, Documents: docs, TopN: len(docs),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("служба переранжирования %s недоступна: %w", r.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("переранжирование вернуло %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	var out rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ответ переранжирования не разобран: %w", err)
	}
	scores := make([]float64, len(docs))
	seen := 0
	for _, res := range out.Results {
		if res.Index < 0 || res.Index >= len(docs) {
			continue // чужой номер: молча пропускаем, но в счёт не берём
		}
		scores[res.Index] = res.Score
		seen++
	}
	if seen != len(docs) {
		return nil, fmt.Errorf("переранжирование вернуло %d оценок на %d кусков", seen, len(docs))
	}
	return scores, nil
}

// Check проверяет, что служба отвечает и понимает переранжирование.
//
// Нужна до начала работы, а не по ходу: без неё первый же поиск ждёт своего
// предела ожидания и выглядит зависшим.
func (r *Reranker) Check(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("переранжирование не настроено")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	scores, err := r.Rerank(ctx, "проверка связи", []string{"первый документ", "второй документ"})
	if err != nil {
		return err
	}
	if len(scores) != 2 {
		return fmt.Errorf("переранжирование ответило %d оценками на два куска", len(scores))
	}
	return nil
}
