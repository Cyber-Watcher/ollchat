// Package backup — копии файлов перед правкой агентом (этап 112, Н5 и Т9).
//
// «High: Explicit human approval is required before execution … a rollback
// path must be defined» («Agentic AI for Platform Engineering», 2026,
// стр. 215–216). До 16.10.2026 у правки файла пути отката не было (С1): то,
// что модель перезаписала, возвращалось только через git, и то — если файл
// под ним. Теперь перед каждой правкой write_file и edit_file копия ложится
// в каталог запуска (решение владельца 16.10.2026):
//
//	.ollchat/backups/2026-10-16/<путь от каталога запуска>.<ЧЧММСС>
//	.ollchat/backups/2026-10-16/index.jsonl   строка на каждую правку
//
// Файл, которого до правки не было, копии не получает — в журнале дня он
// записан как созданный (откат — удалить). Дни старше keep_days удаляются
// при запуске. Каталог `.ollchat/` заводится вместе с `.gitignore`, где
// стоит `backups/`: копии не должны уйти в git, а конфиг проекта — может.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DirName — каталог ollchat в каталоге запуска (конфиг проекта, копии; позже —
// скиллы и хуки).
const DirName = ".ollchat"

// MaxBytes — больше этого файл не копируется: правка такого файла необратима
// и спрашивается даже в noask. Инструменты правки читают файлы до
// sandbox.max_file_kb, так что предел здесь — защита от случайного, а не норма.
const MaxBytes = 64 << 20

// Store — копии одного каталога запуска.
type Store struct {
	root     string // каталог запуска (корень песочницы)
	dir      string // root/.ollchat/backups
	keepDays int    // 0 и меньше — не удалять
	now      func() time.Time
}

// Entry — строка журнала дня.
type Entry struct {
	Time    time.Time `json:"ts"`
	Path    string    `json:"path"`             // правленый файл, абсолютный путь
	Backup  string    `json:"backup,omitempty"` // копия, путь от каталога копий; пусто — файла не было
	Created bool      `json:"created,omitempty"`
	SHA256  string    `json:"sha256,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
}

// New — копии каталога запуска root; keepDays — сколько дней хранить.
func New(root string, keepDays int) *Store {
	return &Store{root: root, dir: filepath.Join(root, DirName, "backups"), keepDays: keepDays, now: time.Now}
}

// Dir — каталог копий.
func (s *Store) Dir() string { return s.dir }

// Irreversible — почему правку path не откатить (permissions.Guard.WriteIrreversible);
// пусто — копия будет.
func (s *Store) Irreversible(path string) string {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "" // нового файла копировать незачем: откат — удалить
	case err != nil:
		return "копию сделать нельзя: " + err.Error()
	case !fi.Mode().IsRegular():
		return "копию сделать нельзя: это не обычный файл"
	case fi.Size() > MaxBytes:
		return fmt.Sprintf("копию сделать нельзя: файл %d байт больше предела %d", fi.Size(), MaxBytes)
	}
	if err := s.ensureDir(); err != nil {
		return "копию сделать нельзя: " + err.Error()
	}
	return ""
}

// Save копирует path перед правкой и пишет строку в журнал дня. Возвращает
// путь копии; пусто — файла не было, записано как созданный.
func (s *Store) Save(path string) (string, error) {
	if err := s.ensureDir(); err != nil {
		return "", err
	}
	now := s.now()
	day := filepath.Join(s.dir, now.Format("2006-01-02"))
	e := Entry{Time: now, Path: path}
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		e.Created = true
	case err != nil:
		return "", err
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%s — не обычный файл, копию не сделать", path)
	case fi.Size() > MaxBytes:
		return "", fmt.Errorf("%s: %d байт больше предела копии %d", path, fi.Size(), MaxBytes)
	default:
		rel := s.relative(path)
		dst := filepath.Join(day, rel+"."+now.Format("150405"))
		for i := 2; exists(dst); i++ { // две правки в одну секунду
			dst = filepath.Join(day, fmt.Sprintf("%s.%s.%d", rel, now.Format("150405"), i))
		}
		sum, n, err := copyFile(path, dst)
		if err != nil {
			return "", fmt.Errorf("копия %s: %w", path, err)
		}
		e.SHA256, e.Bytes = sum, n
		e.Backup, _ = filepath.Rel(s.dir, dst)
	}
	if err := os.MkdirAll(day, 0o700); err != nil {
		return "", err
	}
	line, _ := json.Marshal(e)
	f, err := os.OpenFile(filepath.Join(day, "index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return "", err
	}
	if e.Backup == "" {
		return "", nil
	}
	return filepath.Join(s.dir, e.Backup), nil
}

// Prune удаляет дни старше keepDays; возвращает, сколько дней удалено.
func (s *Store) Prune() (int, error) {
	if s.keepDays <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cut := s.now().AddDate(0, 0, -s.keepDays).Format("2006-01-02")
	var names []string
	for _, e := range entries {
		if _, perr := time.Parse("2006-01-02", e.Name()); e.IsDir() && perr == nil && e.Name() < cut {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.RemoveAll(filepath.Join(s.dir, n)); err != nil {
			return 0, err
		}
	}
	return len(names), nil
}

// relative — путь копии внутри дня: от каталога запуска, а файл вне его —
// под `_outside/` с полным путём.
func (s *Store) relative(path string) string {
	if rel, err := filepath.Rel(s.root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return rel
	}
	return filepath.Join("_outside", strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)))
}

// ensureDir заводит .ollchat/backups и .ollchat/.gitignore со строкой backups/.
func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	ign := filepath.Join(filepath.Dir(s.dir), ".gitignore")
	if _, err := os.Stat(ign); errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(ign, []byte("# копии файлов перед правкой агентом — не для git\nbackups/\n"), 0o644)
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func copyFile(src, dst string) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
