// Package live содержит замеры параметров генерации на настоящем сервере
// Ollama. Обычного кода в пакете нет — только живые тесты, как в
// internal/kb/live и internal/tools/live.
//
// Все тесты пропускаются, если не задан OLLCHAT_TEST_SERVER, поэтому обычный
// `go test ./...` сети не касается.
//
// Результаты каждого прогона складываются в JSON целиком, включая тексты
// ответов: замеры стоят десятков минут, а срезы по ним считаются мгновенно —
// пересчитывать таблицы для статьи без повторного обращения к серверу.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Значения по умолчанию для прогона.
const (
	defaultTimeout = 10 * time.Minute
	defaultRepeats = 5
	// Предел длины ответа: замеры не должны зависеть от того, что модель
	// разговорилась. Для длинных текстов задаётся отдельно.
	defaultNumPredict = 200

	// Окно контекста задаётся явно во всех замерах, и это обязательно.
	//
	// Не задать его — не значит «оставить по умолчанию»: при
	// OLLAMA_CONTEXT_LENGTH=0 сервер выбирает окно сам и берёт максимум модели.
	// Для qwen3.5:122b это 262144 при потолке карты около 133k — модель
	// загрузилась с 4.3 ГиБ в оперативной памяти, и все замеры скорости на ней
	// стали бессмысленными (вытеснение роняет генерацию больше чем на порядок).
	// Промпты здесь короткие, 8192 хватает с запасом.
	measureNumCtx = 8192
)

// client собирает клиента к серверу из переменных окружения.
func client(t *testing.T) *ollama.Client {
	t.Helper()
	url := os.Getenv("OLLCHAT_TEST_SERVER")
	if url == "" {
		t.Skip("не задан OLLCHAT_TEST_SERVER — живой замер пропущен")
	}
	return ollama.New(url, defaultTimeout, 0, nil)
}

// models возвращает список моделей для прогона.
func models(t *testing.T) []string {
	t.Helper()
	if s := os.Getenv("OLLCHAT_PARAM_MODELS"); s != "" {
		var out []string
		for _, m := range strings.Split(s, ",") {
			if m = strings.TrimSpace(m); m != "" {
				out = append(out, m)
			}
		}
		return out
	}
	return []string{"gemma4:12b"}
}

// run — один прогон: вопрос модели с заданными параметрами.
type run struct {
	Model    string         `json:"model"`
	Param    string         `json:"param"`    // какой параметр меняли
	Value    any            `json:"value"`    // его значение в этом прогоне
	Options  map[string]any `json:"options"`  // что реально ушло в запрос
	Probe    string         `json:"probe"`    // имя задачи
	Attempt  int            `json:"attempt"`  // номер повтора
	Answer   string         `json:"answer"`   // текст ответа
	Thinking int            `json:"thinking"` // символов рассуждений
	OK       bool           `json:"ok"`       // задача решена верно
	Err      string         `json:"err,omitempty"`

	PromptTokens int     `json:"prompt_tokens"`
	EvalTokens   int     `json:"eval_tokens"`
	TokPerSec    float64 `json:"tok_per_sec"`
	PromptTokSec float64 `json:"prompt_tok_per_sec"`
	DoneReason   string  `json:"done_reason"`
	WallMs       int64   `json:"wall_ms"`
}

// ask задаёт модели один вопрос и собирает ответ целиком.
//
// Рассуждения в текст ответа не попадают: они приходят отдельным полем, и
// смешивать их с ответом нельзя — метрики поедут.
func ask(ctx context.Context, c *ollama.Client, model, prompt string, opts map[string]any, think *bool) (answer string, thinkChars int, st ollama.Stats, err error) {
	var sb, tb strings.Builder
	req := ollama.ChatRequest{
		Model:     model,
		Messages:  []ollama.Message{{Role: ollama.RoleUser, Content: prompt}},
		KeepAlive: "10m",
		Think:     think,
		Options:   opts,
	}
	for ev := range c.Chat(ctx, req) {
		switch ev.Kind {
		case ollama.EventContent:
			sb.WriteString(ev.Text)
		case ollama.EventThinking:
			tb.WriteString(ev.Text)
		case ollama.EventDone:
			st = ev.Stats
		case ollama.EventError:
			return sb.String(), tb.Len(), st, ev.Err
		}
	}
	return sb.String(), tb.Len(), st, nil
}

// unload выгружает модель из видеопамяти и ждёт, пока она действительно уйдёт.
//
// Ждать обязательно: запрос с keep_alive=0 возвращается сразу, а память
// освобождается позже. Без ожидания следующий замер получит чужую занятую
// карту — а на занятой карте Ollama размещает модель иначе и считает медленнее.
func unload(t *testing.T, c *ollama.Client, model string) {
	t.Helper()
	body := map[string]any{"model": model, "keep_alive": 0, "messages": []any{}}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, c.BaseURL()+"/api/chat", strings.NewReader(string(data)))
	if err != nil {
		t.Logf("выгрузка %s: %v", model, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("выгрузка %s: %v", model, err)
		return
	}
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		running, err := c.PS(ctx)
		cancel()
		if err == nil {
			busy := false
			for _, r := range running {
				if r.Name == model || r.Model == model {
					busy = true
				}
			}
			if !busy {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("модель %s не выгрузилась за отведённое время", model)
}

// results накапливает прогоны и пишет их на диск.
type results struct {
	Format      int    `json:"format"`
	GeneratedAt string `json:"generated_at"`
	Server      string `json:"server"`
	Runs        []run  `json:"runs"`
}

func newResults(c *ollama.Client) *results {
	return &results{Format: 1, GeneratedAt: time.Now().Format(time.RFC3339), Server: c.BaseURL()}
}

func (r *results) add(x run) { r.Runs = append(r.Runs, x) }

// save пишет результаты в файл, имя которого задано OLLCHAT_PARAM_OUT.
func (r *results) save(t *testing.T, name string) {
	t.Helper()
	dir := os.Getenv("OLLCHAT_PARAM_OUT")
	if dir == "" {
		t.Logf("OLLCHAT_PARAM_OUT не задан — результаты не сохранены (прогонов: %d)", len(r.Runs))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("создание каталога результатов: %v", err)
		return
	}
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Logf("сериализация результатов: %v", err)
		return
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Logf("запись результатов: %v", err)
		return
	}
	t.Logf("результаты записаны: %s (прогонов: %d)", path, len(r.Runs))
}

// ── Задачи и проверки ────────────────────────────────────────────────────────

// probe — задача с проверкой ответа.
//
// Правильность фиксируется заранее, до замера: оценка «на глаз» после прогона
// показывает то, что хочется увидеть, а не то, что есть.
type probe struct {
	name    string
	prompt  string
	check   func(answer string) bool // nil — правильность не проверяется
	predict int                      // предел длины ответа, 0 — общий
}

func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("*", "", "`", "", "\"", "", "'", "", ".", "", ",", "").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// probes — набор задач для сеток параметров. У каждой своя проверяемая сторона:
// точность, формат, разнообразие, склонность к повторам.
var probes = []probe{
	{
		name:   "факт",
		prompt: "Столица Австралии? Ответь одним словом, без пояснений.",
		check:  func(a string) bool { return strings.Contains(norm(a), "канберра") },
	},
	{
		name:   "арифметика",
		prompt: "Сколько будет 17 умножить на 24? Ответь только числом.",
		check:  func(a string) bool { return strings.Contains(norm(a), "408") },
	},
	{
		name: "json",
		prompt: "Верни JSON-объект с полями name (строка) и age (число) " +
			"для человека по имени Иван, которому 30 лет. Только JSON, без пояснений и без markdown.",
		check: checkJSON,
	},
	{
		name:   "формат",
		prompt: "Назови три планеты Солнечной системы. Каждую с новой строки, без нумерации и без пояснений.",
		check:  checkThreeLines,
	},
	{
		name:   "выдумка",
		prompt: "Придумай одно название для кофейни. Ответь только названием.",
		// Правильного ответа нет: эта задача меряет разнообразие.
	},
	{
		name:    "рассказ",
		prompt:  "Объясни, что такое канал в языке Go. Ровно пять предложений, без списков.",
		predict: 400,
		// Меряет склонность к повторам.
	},
	{
		name: "перечень",
		prompt: "Перечисли 30 разных областей, где применяют язык Go. " +
			"По одной в строке, без нумерации и без пояснений.",
		predict: 800,
		// Длинный однородный список — там, где штрафы за повтор вообще имеют
		// смысл: на ответе в пять предложений модель повторяться не успевает,
		// и разницы между значениями не видно (замерено на gemma4:12b).
	},
}

// checkJSON проверяет, что ответ — разбираемый JSON с нужными полями.
func checkJSON(a string) bool {
	s := strings.TrimSpace(a)
	// Модели любят обернуть JSON в ограждение markdown, хотя их просили не делать
	// этого. Для проверки самого JSON ограждение снимаем, но в статью этот факт
	// идёт отдельной цифрой.
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	var v struct {
		Name string `json:"name"`
		Age  *int   `json:"age"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err != nil {
		return false
	}
	return v.Name != "" && v.Age != nil
}

// checkThreeLines проверяет ровно три строки без нумерации.
func checkThreeLines(a string) bool {
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(a), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		return false
	}
	for _, l := range lines {
		if l == "" || strings.HasPrefix(l, "1") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, "*") {
			return false
		}
	}
	return true
}

// repeatRatio — доля повторяющихся четвёрок слов в тексте.
//
// Так ловится зацикливание: модель, ушедшая в повтор, выдаёт одни и те же
// обороты подряд, и доля повторов резко растёт.
func repeatRatio(s string) float64 {
	w := strings.Fields(norm(s))
	if len(w) < 8 {
		return 0
	}
	seen := map[string]int{}
	total := 0
	repeats := 0
	for i := 0; i+4 <= len(w); i++ {
		g := strings.Join(w[i:i+4], " ")
		total++
		if seen[g] > 0 {
			repeats++
		}
		seen[g]++
	}
	if total == 0 {
		return 0
	}
	return float64(repeats) / float64(total)
}

// uniqueRatio — доля различных ответов среди повторов, от 0 до 1.
func uniqueRatio(answers []string) float64 {
	if len(answers) == 0 {
		return 0
	}
	seen := map[string]bool{}
	for _, a := range answers {
		seen[norm(a)] = true
	}
	return float64(len(seen)) / float64(len(answers))
}

// options собирает карту параметров запроса поверх базовых.
//
// Окно контекста подставляется всегда, если вызывающий не задал своё:
// без него сервер выберет окно сам и может вытеснить модель в оперативную
// память — см. комментарий к measureNumCtx.
func options(base map[string]any, kv ...any) map[string]any {
	out := map[string]any{"num_ctx": measureNumCtx}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return out
}
