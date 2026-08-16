package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Эмбеддинги — превращение текста в вектор.
//
// Используется /api/embed, а не устаревший /api/embeddings: первый принимает
// сразу пачку текстов и возвращает пачку векторов, второй — по одному тексту
// за запрос. На четверти миллиона кусков разница между ними — это разница
// между десятками минут и часами.
//
// keep_alive обязателен и конечен. Без него модель остаётся в видеопамяти
// сервера после работы; на общем стенде это значит вытеснить чужую модель
// и не вернуть память.

// EmbedRequest — запрос на векторизацию пачки текстов.
type EmbedRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	KeepAlive string   `json:"keep_alive,omitempty"`
	Truncate  *bool    `json:"truncate,omitempty"`
	Options   any      `json:"options,omitempty"`
}

// EmbedResponse — ответ сервера.
type EmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`

	// Счётчики приходят не от всех сборок, поэтому на них ничего не завязано.
	PromptEvalCount int   `json:"prompt_eval_count"`
	TotalDuration   int64 `json:"total_duration"`
}

// ErrNoEmbeddings возвращается, когда сервер ответил без единого вектора.
var ErrNoEmbeddings = errors.New("сервер не вернул ни одного вектора")

// Embed векторизует пачку текстов.
//
// Число векторов в ответе обязано совпасть с числом текстов: векторы кладутся
// в файл по порядковому номеру куска, и молчаливый сдвиг на один означал бы,
// что вся коллекция ищет не то. Поэтому расхождение — ошибка, а не повод
// подравнять длину.
func (c *Client) Embed(ctx context.Context, req EmbedRequest) ([]([]float32), error) {
	if len(req.Input) == 0 {
		return nil, nil
	}
	if req.Model == "" {
		return nil, errors.New("не указана модель эмбеддингов")
	}
	resp, err := c.embedOnce(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Embeddings) == 0 {
		return nil, ErrNoEmbeddings
	}
	if len(resp.Embeddings) != len(req.Input) {
		return nil, fmt.Errorf("сервер вернул %d векторов на %d текстов — порядок кусков нарушен",
			len(resp.Embeddings), len(req.Input))
	}
	dim := len(resp.Embeddings[0])
	if dim == 0 {
		return nil, ErrNoEmbeddings
	}
	for i, v := range resp.Embeddings {
		if len(v) != dim {
			return nil, fmt.Errorf("вектор %d длиной %d, а первый — %d", i, len(v), dim)
		}
	}
	return resp.Embeddings, nil
}

// EmbedDim выясняет размерность модели одним коротким запросом.
//
// Спрашивать сервер, а не брать из таблицы: размерность зависит от того, какой
// именно тег модели вытянут на этот сервер, и ошибка здесь испортила бы весь
// файл векторов.
func (c *Client) EmbedDim(ctx context.Context, model, keepAlive string) (int, error) {
	vecs, err := c.Embed(ctx, EmbedRequest{
		Model:     model,
		Input:     []string{"размерность"},
		KeepAlive: keepAlive,
	})
	if err != nil {
		return 0, err
	}
	return len(vecs[0]), nil
}

// embedOnce выполняет один запрос, отделяя сбои, которые есть смысл повторить.
//
// Разбор идёт здесь, а не через общий do(): многочасовая векторизация не должна
// срываться из-за одной оборванной связи, а для этого нужно знать код ответа.
// Правило то же, что у потокового чата: сеть и 5xx — повторить, 4xx — нет.
func (c *Client) embedOnce(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/api/embed", req)
	if err != nil {
		return nil, err
	}
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		return nil, retryable(fmt.Errorf("запрос к %s: %w", c.baseURL, err))
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		body := strings.TrimSpace(string(msg))
		srvErr := fmt.Errorf("сервер вернул %s: %s", httpResp.Status, body)
		if httpResp.StatusCode >= 500 || runnerDied(body) {
			return nil, retryable(srvErr)
		}
		return nil, srvErr
	}
	var resp EmbedResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, retryable(fmt.Errorf("разбор ответа /api/embed: %w", err))
	}
	return &resp, nil
}

// runnerDied распознаёт падение внутреннего обработчика модели.
//
// Живой случай: на 74% векторизации библиотеки сервер ответил
//
//	400 Bad Request: do embedding request:
//	Post "http://127.0.0.1:38271/v1/embeddings": EOF
//
// Код 400 обычно означает «запрос неверен, повторять бессмысленно», но здесь он
// врёт: запрос был тот же, что и предыдущие полторы тысячи, а оборвалась связь
// Ollama с её собственным обработчиком модели. Отличить это можно только по
// тексту — по признакам разрыва связи внутри сервера.
func runnerDied(body string) bool {
	low := strings.ToLower(body)
	if !strings.Contains(low, "127.0.0.1") && !strings.Contains(low, "localhost") {
		return false
	}
	for _, sign := range []string{"eof", "connection refused", "broken pipe", "connection reset"} {
		if strings.Contains(low, sign) {
			return true
		}
	}
	return false
}
