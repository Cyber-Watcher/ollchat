package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/vram"
)

// Обращения к Ollama и к видеокарте. Своего клиента здесь ровно столько,
// сколько нужно инструменту: тянуть ради этого зависимости в программу,
// которую предстоит копировать на сервер, ни к чему.

type client struct {
	base string
	http *http.Client
}

func newClient(base string, timeout time.Duration) *client {
	return &client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// ── Модели ───────────────────────────────────────────────────────────────────

type modelInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Details struct {
		QuantizationLevel string `json:"quantization_level"`
		ParameterSize     string `json:"parameter_size"`
	} `json:"details"`
}

func (c *client) tags(ctx context.Context) ([]modelInfo, error) {
	var out struct {
		Models []modelInfo `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/tags", nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

type showResponse struct {
	Capabilities []string       `json:"capabilities"`
	ModelInfo    map[string]any `json:"model_info"`
}

func (c *client) show(ctx context.Context, model string) (*showResponse, error) {
	var out showResponse
	err := c.do(ctx, http.MethodPost, "/api/show", map[string]any{"model": model}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// maxContext достаёт окно, на которое модель обучена. Ключ снабжён префиксом
// семейства (qwen35moe.context_length, gemma3.context_length), поэтому ищем
// по окончанию имени, а не по точному совпадению.
func (s *showResponse) maxContext() int {
	for k, v := range s.ModelInfo {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		if f, ok := v.(float64); ok && f > 0 {
			return int(f)
		}
	}
	return 0
}

func (s *showResponse) hasVision() bool {
	for _, c := range s.Capabilities {
		if c == "vision" {
			return true
		}
	}
	return false
}

// ── Загруженные модели ───────────────────────────────────────────────────────

type runningModel struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	ContextLength int    `json:"context_length"`
}

func (c *client) ps(ctx context.Context) ([]runningModel, error) {
	var out struct {
		Models []runningModel `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/ps", nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// load заставляет сервер загрузить модель с нужным окном.
//
// Отдельного способа «загрузи с таким-то num_ctx» в API нет: окно едет в
// options обычного запроса, и, увидев новое значение, Ollama перезагружает
// модель. Поэтому шлём самый дешёвый запрос из возможных.
func (c *client) load(ctx context.Context, model string, numCtx int, keepAlive string) error {
	body := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "1"}},
		"stream":     false,
		"keep_alive": keepAlive,
		"options": map[string]any{
			"num_ctx":     numCtx,
			"num_predict": 1, // ответ не нужен, нужна загрузка
		},
	}
	return c.do(ctx, http.MethodPost, "/api/chat", body, nil)
}

// unload выгружает модель из памяти: keep_alive = 0.
func (c *client) unload(ctx context.Context, model string) error {
	body := map[string]any{"model": model, "keep_alive": 0}
	return c.do(ctx, http.MethodPost, "/api/chat", body, nil)
}

// waitLoaded ждёт появления модели в /api/ps с ожидаемым окном.
func (c *client) waitLoaded(ctx context.Context, model string, numCtx int, timeout time.Duration) (runningModel, error) {
	deadline := time.Now().Add(timeout)
	var last runningModel
	for time.Now().Before(deadline) {
		running, err := c.ps(ctx)
		if err == nil {
			for _, r := range running {
				if r.Name != model && r.Model != model {
					continue
				}
				last = r
				if r.ContextLength == numCtx {
					return r, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if last.Name != "" {
		return last, fmt.Errorf("модель загружена с окном %d, а ожидалось %d", last.ContextLength, numCtx)
	}
	return last, fmt.Errorf("модель %s не появилась в /api/ps за %s", model, timeout)
}

// ── Видеокарта ───────────────────────────────────────────────────────────────

// Состояние карты описано типом vram.GPU: это не то же самое, что сумма по
// /api/ps — сюда входит и контекст CUDA, и всё, что заняли соседние процессы.

// readGPU опрашивает nvidia-smi. Отсутствие утилиты — не ошибка: расчёт
// возможен и без неё, просто бюджет придётся задать руками.
func readGPU(ctx context.Context) vram.GPU {
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=name,memory.total,memory.used", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return vram.GPU{}
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	parts := strings.Split(line, ",")
	if len(parts) < 3 {
		return vram.GPU{}
	}
	total, err1 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	used, err2 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err1 != nil || err2 != nil {
		return vram.GPU{}
	}
	return vram.GPU{
		Name:      strings.TrimSpace(parts[0]),
		TotalMiB:  total,
		UsedMiB:   used,
		Available: true,
	}
}

// waitUnloaded ждёт, пока модель исчезнет из /api/ps.
//
// Выгрузка не мгновенна: запрос с keep_alive=0 возвращается сразу, а память
// освобождается позже. Если сразу начать следующий замер, эта память попадёт
// в показания карты и запишется следующей модели в накладные расходы.
func waitUnloaded(ctx context.Context, c *client, model string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := c.ps(ctx)
		if err == nil {
			gone := true
			for _, r := range running {
				if r.Name == model || r.Model == model {
					gone = false
					break
				}
			}
			if gone {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
