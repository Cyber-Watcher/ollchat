// Package session хранит историю диалога и умеет сохранять её на диск.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Conversation — история диалога с моделью.
type Conversation struct {
	mu       sync.Mutex
	system   string
	messages []ollama.Message
	// today — подставлять ли сегодняшнюю дату в системное сообщение.
	// Устройство и доводы — в today.go.
	today bool
}

// New создаёт диалог с указанным системным промптом.
func New(systemPrompt string) *Conversation {
	return &Conversation{system: systemPrompt}
}

// SetSystem задаёт системный промпт. Он хранится отдельно и всегда идёт первым
// сообщением запроса, поэтому очистка истории его не затрагивает.
func (c *Conversation) SetSystem(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.system = s
}

// System возвращает текущий системный промпт.
func (c *Conversation) System() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.system
}

// Append добавляет сообщение в историю.
func (c *Conversation) Append(m ollama.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, m)
}

// Messages возвращает копию истории без системного промпта.
func (c *Conversation) Messages() []ollama.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ollama.Message(nil), c.messages...)
}

// Request собирает сообщения для отправки: системный промпт плюс история.
func (c *Conversation) Request() []ollama.Message {
	return c.RequestAt(time.Now())
}

// RequestAt — то же, но с датой, заданной снаружи. Агент берёт дату один раз
// на ход: цепочка вызовов инструментов, пересёкшая полночь, иначе получала бы
// разные системные сообщения в соседних итерациях одного хода.
func (c *Conversation) RequestAt(now time.Time) []ollama.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ollama.Message, 0, len(c.messages)+1)
	if sys := c.systemFor(now); sys != "" {
		out = append(out, ollama.Message{Role: ollama.RoleSystem, Content: sys})
	}
	return append(out, c.messages...)
}

// Len возвращает число сообщений в истории.
func (c *Conversation) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

// Clear очищает историю, сохраняя системный промпт.
func (c *Conversation) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = nil
}

// Compact оставляет последние keep сообщений, отбрасывая более старые.
// Возвращает число отброшенных сообщений.
func (c *Conversation) Compact(keep int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	cut := c.cutIndex(keep)
	c.messages = append([]ollama.Message(nil), c.messages[cut:]...)
	return cut
}

// EstimatedChars возвращает суммарную длину истории в байтах —
// основа для оценки заполнения контекста до первого ответа сервера.
func (c *Conversation) EstimatedChars() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.system)
	for _, m := range c.messages {
		n += len(m.Content) + len(m.Thinking)
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name) + len(tc.Function.ArgumentsJSON())
		}
	}
	return n
}

// ── Сохранение на диск ───────────────────────────────────────────────────────

// Saved — формат файла сохранённой сессии.
type Saved struct {
	ID       string           `json:"id"`
	SavedAt  time.Time        `json:"saved_at"`
	Server   string           `json:"server"`
	Model    string           `json:"model"`
	System   string           `json:"system"`
	Messages []ollama.Message `json:"messages"`
}

// Store — каталог с сохранёнными сессиями.
type Store struct{ dir string }

// NewStore создаёт хранилище сессий в указанном каталоге.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir возвращает каталог хранилища.
func (s *Store) Dir() string { return s.dir }

// Save записывает диалог в файл <id>.json. Идентификатор — время сохранения
// и случайный хвост, см. newID.
//
// Запись атомарная: сохранение, оборванное посреди файла, раньше оставляло
// обрезанный JSON, который List молча пропускал, — диалог пропадал без следа.
func (s *Store) Save(c *Conversation, server, model string) (string, error) {
	if err := privateDir(s.dir); err != nil {
		return "", err
	}
	now := time.Now()
	id, err := s.newID(now)
	if err != nil {
		return "", err
	}
	rec := Saved{
		ID:       id,
		SavedAt:  now,
		Server:   server,
		Model:    model,
		System:   c.System(),
		Messages: c.Messages(),
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, id+".json")
	if err := fsx.WriteFileAtomic(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// newID — идентификатор нового сохранения: время до секунды, как раньше,
// и случайный хвост.
//
// Одного времени мало: два /save за одну секунду — из двух окон tmux или
// подряд из одного — получали одно имя, и второе молча затирало первое.
// Хвост случайный, а не счётчик, потому что сохраняют и разные процессы,
// а проверка «имя свободно» между ними не договорится.
func (s *Store) newID(now time.Time) (string, error) {
	stamp := now.Format("2006-01-02_150405")
	for range 100 {
		var b [3]byte
		_, _ = rand.Read(b[:]) // crypto/rand не возвращает ошибок с Go 1.24
		id := stamp + "-" + hex.EncodeToString(b[:])
		if _, err := os.Lstat(filepath.Join(s.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			return id, nil
		}
	}
	return "", fmt.Errorf("не нашлось свободного имени сессии в %s", s.dir)
}

// privateDir создаёт каталог сессий закрытым от чужих (0700) и закрывает
// уже существующий.
//
// В сессии лежит весь диалог: вопросы, ответы, прочитанные моделью файлы,
// вывод команд. Каталог создавался 0755, и имена сессий видели все; а там,
// где он не наш, закрыть его не выйдет — и это повод отказаться, а не писать
// диалог туда, где его может подменить хозяин каталога.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("каталог сессий %s открыт другим (%o), а закрыть его не удалось: %w", dir, perm, err)
		}
	}
	return nil
}

// List возвращает сохранённые сессии, начиная с самых свежих.
func (s *Store) List() ([]Saved, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Saved
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, err := s.load(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // повреждённый файл не должен ломать список
		}
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out, nil
}

func (s *Store) load(path string) (*Saved, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec Saved
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("разбор сессии %s: %w", path, err)
	}
	return &rec, nil
}

// Load восстанавливает сессию по идентификатору. Пустой id — самая свежая сессия.
func (s *Store) Load(id string) (*Saved, error) {
	if id == "" {
		list, err := s.List()
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("сохранённых сессий нет")
		}
		return &list[0], nil
	}
	return s.load(filepath.Join(s.dir, id+".json"))
}

// Restore заполняет диалог сохранёнными сообщениями.
func (c *Conversation) Restore(rec *Saved) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.system = rec.System
	c.messages = append([]ollama.Message(nil), rec.Messages...)
}
