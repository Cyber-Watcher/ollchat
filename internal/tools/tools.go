// Package tools реализует инструменты, доступные модели через function calling.
//
// Каждый инструмент сначала строит План (Plan): разбирает аргументы, приводит
// пути к абсолютным, проверяет их на выход за песочницу и готовит текст для
// показа пользователю. Только после проверки разрешений план выполняется.
// Такое разделение нужно, чтобы подтверждение показывало ровно то действие,
// которое затем будет выполнено.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Имена инструментов.
const (
	NameReadFile  = "read_file"
	NameListDir   = "list_dir"
	NameGrep      = "grep"
	NameWriteFile = "write_file"
	NameEditFile  = "edit_file"
	NameBash      = "bash"
	NameHTTPFetch = "http_fetch"
	NameKBSearch  = "kb_search"
	NameKBRead    = "kb_read"
	NameViewImage = "view_image"
	NameWebSearch = "web_search"
)

// AllNames возвращает имена всех поддерживаемых инструментов.
func AllNames() []string {
	return []string{NameReadFile, NameListDir, NameGrep, NameWriteFile, NameEditFile,
		NameBash, NameHTTPFetch, NameKBSearch, NameKBRead, NameViewImage, NameWebSearch}
}

// Plan — подготовленное к выполнению действие.
type Plan struct {
	Tool    string              // имя инструмента
	Req     permissions.Request // что проверять правилами
	Title   string              // краткая строка для интерфейса: read_file(go.mod)
	Preview string              // подробности для подтверждения: diff или текст команды
	Run     func(ctx context.Context) (string, error)

	// Images отдаёт картинки, добытые во время Run, в base64. Строкой картинку
	// не вернёшь, поэтому цикл агента подкладывает их в диалог отдельным
	// сообщением — только так их увидит модель с возможностью vision.
	// Заполняется лишь теми инструментами, которым это нужно.
	Images func() []string
}

// Tool — инструмент, предоставляемый модели.
type Tool interface {
	Name() string
	Spec() ollama.Tool
	Plan(args map[string]any) (*Plan, error)
}

// Options — настройки, общие для инструментов.
type Options struct {
	Sandbox     *permissions.Sandbox
	BashTimeout time.Duration
	MaxOutputKB int

	// База знаний по книгам. Инструменты поиска работают только с готовым
	// индексом и файловой системы не касаются, поэтому здесь не путь к книгам,
	// а сама база.
	KB           *kb.Base
	KBDir        string // каталог базы — цель проверки прав
	KBDefault    string // коллекция по умолчанию
	KBTopK       int
	KBMaxPerBook int

	// Смысловой поиск. Embedder необязателен: без него база ищется по словам,
	// как и до его появления.
	Semantic       bool
	MinCosine      float64
	SemanticWeight float64
	AnswerStyle    string // пусто — встроенная политика kb.DefaultAnswerStyle
	Embedder       kb.Embedder

	// Поиск в сети. Пустой адрес означает, что инструмент web_search
	// объяснит, чего не хватает, а не промолчит.
	SearxURL     string
	SearxTimeout time.Duration

	// CanViewImages — включён ли инструмент view_image. Заполняется NewRegistry,
	// в конфиге такой настройки нет. Нужен, чтобы подсказка к документу
	// не звала к инструменту, которого в этом сеансе нет.
	CanViewImages bool
}

// Registry — набор включённых инструментов.
type Registry struct {
	opts  Options
	tools map[string]Tool
	order []string
}

// NewRegistry собирает инструменты по списку имён из конфига.
// Неизвестное имя — ошибка запуска, а не молчаливый пропуск.
func NewRegistry(enabled []string, opts Options) (*Registry, error) {
	if opts.MaxOutputKB <= 0 {
		opts.MaxOutputKB = 64
	}
	if opts.BashTimeout <= 0 {
		opts.BashTimeout = 120 * time.Second
	}

	r := &Registry{opts: opts, tools: map[string]Tool{}}
	// Подсказка о картинках зависит от того, чем их показывать. Список известен
	// здесь целиком, поэтому решается один раз, до сборки инструментов.
	for _, name := range enabled {
		if name == NameViewImage {
			opts.CanViewImages = true
		}
	}

	for _, name := range enabled {
		var t Tool
		switch name {
		case NameReadFile:
			t = &readFileTool{opts: opts}
		case NameListDir:
			t = &listDirTool{opts: opts}
		case NameGrep:
			t = &grepTool{opts: opts}
		case NameWriteFile:
			t = &writeFileTool{opts: opts}
		case NameEditFile:
			t = &editFileTool{opts: opts}
		case NameBash:
			t = &bashTool{opts: opts}
		case NameHTTPFetch:
			t = &httpFetchTool{opts: opts}
		case NameKBSearch:
			t = &kbSearchTool{opts: opts}
		case NameKBRead:
			t = &kbReadTool{opts: opts}
		case NameViewImage:
			t = &viewImageTool{opts: opts}
		case NameWebSearch:
			t = &webSearchTool{opts: opts}
		default:
			return nil, fmt.Errorf("agent.tools: неизвестный инструмент %q (доступны: %s)",
				name, strings.Join(AllNames(), ", "))
		}
		if _, dup := r.tools[name]; dup {
			continue
		}
		r.tools[name] = t
		r.order = append(r.order, name)
	}
	return r, nil
}

// embedder возвращает эмбеддер или nil. Интерфейс в поле может хранить
// «типизированный nil», а kb проверяет именно на nil — поэтому проверка здесь.
func (o Options) embedder() kb.Embedder {
	if o.Embedder == nil {
		return nil
	}
	if v, ok := o.Embedder.(interface{ Model() string }); ok && v.Model() == "" {
		return nil
	}
	return o.Embedder
}

// Names возвращает имена включённых инструментов в порядке конфига.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// Has сообщает, включён ли инструмент в этом сеансе.
func (r *Registry) Has(name string) bool { _, ok := r.tools[name]; return ok }

// Specs возвращает описания инструментов для передачи модели.
func (r *Registry) Specs() []ollama.Tool {
	specs := make([]ollama.Tool, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.tools[name].Spec())
	}
	return specs
}

// ErrUnknownTool возвращается, когда модель запросила неизвестный инструмент.
var ErrUnknownTool = errors.New("инструмент недоступен")

// Plan подготавливает вызов инструмента.
//
// К ошибке разбора аргументов приписывается список параметров инструмента:
// это сообщение уходит модели, и без подсказки она нередко пробует наугад
// поля от другого инструмента вместо того, чтобы исправить вызов.
func (r *Registry) Plan(name string, args map[string]any) (*Plan, error) {
	t, ok := r.tools[name]
	if !ok {
		available := r.Names()
		sort.Strings(available)
		return nil, fmt.Errorf("%w: %s (доступны: %s)", ErrUnknownTool, name, strings.Join(available, ", "))
	}
	plan, err := t.Plan(args)
	if err != nil {
		return nil, fmt.Errorf("%w. Инструмент %s принимает параметры: %s", err, name, describeParams(t.Spec()))
	}
	return plan, nil
}

// describeParams перечисляет параметры инструмента для сообщения об ошибке.
func describeParams(spec ollama.Tool) string {
	props := spec.Function.Parameters.Properties
	required := make(map[string]bool, len(spec.Function.Parameters.Required))
	for _, r := range spec.Function.Parameters.Required {
		required[r] = true
	}

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	// Обязательные параметры идут первыми, дальше — по алфавиту.
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})

	parts := make([]string, 0, len(names))
	for _, name := range names {
		kind := props[name].Type
		if required[name] {
			parts = append(parts, fmt.Sprintf("%s (%s, обязателен)", name, kind))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s, необязателен)", name, kind))
	}
	if len(parts) == 0 {
		return "без параметров"
	}
	return strings.Join(parts, ", ")
}

// Sandbox возвращает песочницу, с которой работают инструменты.
func (r *Registry) Sandbox() *permissions.Sandbox { return r.opts.Sandbox }

// ── Разбор аргументов ────────────────────────────────────────────────────────

func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", false
	}
	switch s := v.(type) {
	case string:
		return s, true
	case fmt.Stringer:
		return s.String(), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

func requireString(args map[string]any, key string) (string, error) {
	s, ok := argString(args, key)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("не указан обязательный параметр %q", key)
	}
	return s, nil
}

func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		var out int
		if _, err := fmt.Sscanf(n, "%d", &out); err == nil {
			return out
		}
	}
	return def
}

func argBool(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(b) {
		case "true", "yes", "1":
			return true
		case "false", "no", "0":
			return false
		}
	}
	return def
}

// truncate обрезает вывод инструмента до предела, заданного настройками,
// и честно сообщает модели, что часть данных отброшена.
func (o Options) truncate(s string) string {
	limit := o.MaxOutputKB * 1024
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := s[:limit]
	// Не режем посреди символа UTF-8. Отбросить одни лишь «хвостовые» байты
	// мало: обрыв может прийтись сразу после ведущего байта, и тот останется
	// висеть без продолжения — модель получит испорченный символ в конце.
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf("\n\n[вывод обрезан: показано %d из %d байт]", len(cut), len(s))
}
