// Package chatlog ведёт журнал чата в текстовом файле.
//
// Файл всегда открывается на дозапись (O_APPEND) и никогда не затирается.
// Имя файла вычисляется из шаблона времени при каждой записи, поэтому смена
// суток автоматически создаёт новый файл, если в шаблоне есть дата.
package chatlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Формат отметки времени в заголовках записей.
const stampLayout = "2006.01.02 15:04"

// Заголовки записей.
const (
	KindQuestion = "Вопрос"
	KindAnswer   = "Ответ"
	KindThinking = "Рассуждения"
	KindTool     = "Инструмент"
	KindSystem   = "Система"
)

// Logger — потокобезопасный журнал чата.
type Logger struct {
	mu      sync.Mutex
	dir     string
	pattern string
	enabled bool

	file    *os.File
	curName string
	lastErr error
}

// New создаёт журнал. Каталог создаётся при первой записи, а не при старте,
// чтобы выключенный журнал не создавал лишних каталогов.
func New(dir, pattern string, enabled bool) *Logger {
	if pattern == "" {
		pattern = "chat-2006-01-02.md"
	}
	return &Logger{dir: dir, pattern: pattern, enabled: enabled}
}

// Enabled сообщает, включена ли запись.
func (l *Logger) Enabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enabled
}

// SetEnabled включает или выключает запись. При выключении файл закрывается.
func (l *Logger) SetEnabled(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = v
	if !v {
		l.closeLocked()
	}
}

// CurrentPath возвращает путь к файлу, в который идёт запись сейчас.
func (l *Logger) CurrentPath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return filepath.Join(l.dir, time.Now().Format(l.pattern))
}

// LastError возвращает последнюю ошибку записи (для показа в статус-баре).
func (l *Logger) LastError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}

// Close закрывает файл журнала.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeLocked()
}

func (l *Logger) closeLocked() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	l.curName = ""
	return err
}

// openLocked открывает нужный по времени файл, переоткрывая его при ротации.
func (l *Logger) openLocked(now time.Time) error {
	name := filepath.Join(l.dir, now.Format(l.pattern))
	if l.file != nil && l.curName == name {
		return nil
	}
	if err := l.closeLocked(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return fmt.Errorf("создание каталога журнала: %w", err)
	}
	// O_APPEND без O_TRUNC — содержимое существующего файла сохраняется.
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("открытие журнала %s: %w", name, err)
	}
	l.file = f
	l.curName = name
	return nil
}

// Write добавляет в журнал запись вида:
//
//	ГГГГ.ММ.ДД ЧЧ:ММ ----- Вопрос
//
//	<текст>
func (l *Logger) Write(kind, body string) error {
	return l.WriteAt(time.Now(), kind, body)
}

// WriteFrom добавляет запись, помеченную названием модели:
//
//	ГГГГ.ММ.ДД ЧЧ:ММ (qwen3.5:122b) ----- Ответ
//
//	<текст>
//
// Пустое имя модели даёт обычный заголовок без скобок.
func (l *Logger) WriteFrom(kind, model, body string) error {
	return l.writeEntry(time.Now(), kind, model, body)
}

// WriteAt — то же, что Write, но с явной отметкой времени.
func (l *Logger) WriteAt(ts time.Time, kind, body string) error {
	return l.writeEntry(ts, kind, "", body)
}

// WriteFromAt — запись с явной отметкой времени и названием модели.
func (l *Logger) WriteFromAt(ts time.Time, kind, model, body string) error {
	return l.writeEntry(ts, kind, model, body)
}

func (l *Logger) writeEntry(ts time.Time, kind, model, body string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.enabled {
		return nil
	}
	if err := l.openLocked(ts); err != nil {
		l.lastErr = err
		return err
	}

	var b strings.Builder
	b.WriteString(ts.Format(stampLayout))
	if model = strings.TrimSpace(model); model != "" {
		b.WriteString(" (")
		b.WriteString(model)
		b.WriteString(")")
	}
	b.WriteString(" ----- ")
	b.WriteString(kind)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n\n")

	if _, err := l.file.WriteString(b.String()); err != nil {
		l.lastErr = err
		return err
	}
	// Сбрасываем на диск сразу: TUI может быть закрыт нештатно.
	if err := l.file.Sync(); err != nil {
		l.lastErr = err
		return err
	}
	l.lastErr = nil
	return nil
}

// WriteSessionHeader отмечает в журнале начало сеанса работы.
func (l *Logger) WriteSessionHeader(server, serverURL, model string) error {
	return l.Write(KindSystem, fmt.Sprintf("Начало сеанса. Сервер: %s (%s). Модель: %s.",
		server, serverURL, model))
}

// WriteTool записывает вызов инструмента одной компактной записью.
func (l *Logger) WriteTool(name, args, result string, ok bool) error {
	status := "успешно"
	if !ok {
		status = "ошибка"
	}
	body := fmt.Sprintf("%s(%s) → %s\n\n%s", name, args, status, strings.TrimRight(result, "\n"))
	return l.Write(KindTool, body)
}
