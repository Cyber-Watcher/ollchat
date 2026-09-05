// Package steplog — журнал шагов: одна строка JSON на каждый шаг работы.
//
// Зачем. Журнал чата хранит вопросы и ответы, но не отвечает на вопросы
// «какой инструмент выбрала модель, с какими аргументами и чем это кончилось»,
// «сколько заняла каждая ступень поиска», «каким промптом собран граф,
// по которому подмешивалось». Книги называют это наблюдаемостью и просят
// писать шаг, а не только результат: «you usually want to track the user
// input, the prompt templates, the input variables, the generated response,
// the number of tokens, and the latency» («LLM Engineer's Handbook», 2024,
// стр. 1344). Форма строки на вызов инструмента — из «AI That Acts» (2026,
// стр. 121): `step=2 tool=find_orders args={…} outcome=rejected field=status`;
// по сотне таких строк ошибка выбора инструмента и ошибка аргументов
// разделяются глазами, без счёта.
//
// Что это не такое. Не Prometheus и не трассировка: приложение локальное
// и однопользовательское, строка JSONL в файл рядом с журналом чата даёт
// то же одному человеку. Заведён этапом 91 (R1.4).
//
// Устройство. Только дозапись; ошибка записи копится в LastError, как у
// журнала чата, и не роняет работу. Имя файла — тот же шаблон strftime, что у
// журнала чата (`log.steps_file_pattern`), поэтому у каждого запуска свой файл.
package steplog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
)

// Виды шагов.
const (
	KindChat    = "chat"    // один обмен с моделью
	KindTool    = "tool"    // один вызов инструмента
	KindSearch  = "search"  // один поиск по графу и книгам
	KindMix     = "mix"     // подмешивание к вопросу
	KindCompact = "compact" // сжатие истории сводкой
)

// Исходы вызова инструмента.
const (
	OutcomeOK        = "ok"
	OutcomeInvalid   = "invalid"   // аргументы не разобраны, план не собран
	OutcomeDenied    = "denied"    // запрещено правилами
	OutcomeRejected  = "rejected"  // отклонено человеком
	OutcomeCancelled = "cancelled" // ход прерван
	OutcomeFailed    = "failed"    // разрешён и запущен, но упал
)

// MaxArgs — сколько байт аргументов пишется в строку: остальное обрезается.
// Полный текст команды и так есть в журнале чата; здесь нужен вид, а не тело.
const MaxArgs = 512

// Step — один шаг.
type Step struct {
	TS        time.Time `json:"ts"`
	Src       string    `json:"src,omitempty"` // кто писал: ollchat, ollmcp
	Turn      string    `json:"turn,omitempty"`
	Step      int       `json:"step,omitempty"`
	Kind      string    `json:"kind"`
	Model     string    `json:"model,omitempty"`
	PromptID  string    `json:"prompt_id,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Args      string    `json:"args,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	TokensIn  int       `json:"tokens_in,omitempty"`
	TokensOut int       `json:"tokens_out,omitempty"`
	MS        int64     `json:"ms"`
	// Extra — что не укладывается в общие поля: у поиска ms_graph, ms_books,
	// ms_rerank, hits; у подмешивания — что реально ушло.
	Extra map[string]any `json:"extra,omitempty"`
	Note  string         `json:"note,omitempty"`
}

// Writer пишет шаги в файл. Нулевой указатель — журнал выключен: все методы
// безопасны на nil, чтобы вызывающим не проверять.
type Writer struct {
	mu      sync.Mutex
	dir     string
	pattern *chatlog.Pattern
	start   time.Time
	src     string
	path    string
	f       *os.File
	lastErr error
}

// New создаёт журнал шагов. Пустой шаблон или выключенный журнал — nil.
func New(dir string, pattern *chatlog.Pattern, start time.Time, src string, enabled bool) *Writer {
	if !enabled || pattern == nil || dir == "" {
		return nil
	}
	return &Writer{dir: dir, pattern: pattern, start: start, src: src}
}

// Path — имя файла, в который идут записи (после первой записи).
func (w *Writer) Path() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.path
}

// LastError — последняя ошибка записи; nil, если всё в порядке.
func (w *Writer) LastError() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}

// Write дописывает шаг. Пустое время заменяется текущим, аргументы
// обрезаются до MaxArgs байт по границе руны.
func (w *Writer) Write(s Step) {
	if w == nil {
		return
	}
	if s.TS.IsZero() {
		s.TS = time.Now()
	}
	if s.Src == "" {
		s.Src = w.src
	}
	s.Args = cut(s.Args, MaxArgs)

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.openLocked(); err != nil {
		w.lastErr = err
		return
	}
	line, err := json.Marshal(s)
	if err != nil {
		w.lastErr = fmt.Errorf("шаг не сериализуется: %w", err)
		return
	}
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		w.lastErr = fmt.Errorf("запись журнала шагов %s: %w", w.path, err)
		return
	}
	w.lastErr = nil
}

func (w *Writer) openLocked() error {
	if w.f != nil {
		return nil
	}
	path := w.pattern.Name(w.start)
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.dir, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("каталог журнала шагов: %w", err)
	}
	// O_APPEND без O_TRUNC: два экземпляра с одним именем файла допишут
	// каждый свои строки, и по полю turn они различимы.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("открытие журнала шагов %s: %w", path, err)
	}
	w.f, w.path = f, path
	return nil
}

// Close закрывает файл.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Parse разбирает одну строку журнала обратно в шаг.
func Parse(line []byte) (Step, error) {
	var s Step
	if err := json.Unmarshal(line, &s); err != nil {
		return s, err
	}
	if s.Kind == "" {
		return s, errors.New("в строке нет поля kind")
	}
	return s, nil
}

// cut обрезает строку до n байт по границе руны, добавляя многоточие.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && (s[i]&0xC0) == 0x80 {
		i--
	}
	return s[:i] + "…"
}
