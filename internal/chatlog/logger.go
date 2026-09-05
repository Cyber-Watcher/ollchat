// Package chatlog ведёт журнал чата в текстовом файле.
//
// Файл всегда открывается на дозапись (O_APPEND) и никогда не затирается.
// Имя файла строится по шаблону (см. Pattern). Шаблон с часами, минутами
// или секундами даёт свой файл на каждый запуск программы: на одной машине
// одновременно работает несколько экземпляров ollchat — в разных сеансах ssh
// или окнах tmux, — и общий файл они перемешали бы. Шаблон только с датой
// сохраняет прежнее поведение: имя пересчитывается на каждой записи, смена
// суток создаёт новый файл.
package chatlog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Сколько запасных имён перебирать, если файл с таким именем уже занят.
const maxNameAttempts = 99

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
	pattern *Pattern
	start   time.Time
	enabled bool

	// sessionID и turn метят записи идентификатором обмена: UI объявляет
	// границу обмена через BeginTurn/EndTurn, а журнал сам метит всё, что
	// между ними. Записи одного обмена идут из четырёх разных мест кода,
	// и протаскивать идентификатор через каждую сигнатуру значит однажды
	// где-то разойтись.
	sessionID string
	turn      int
	// lastTurn — номер последнего начатого обмена. Отдельно от turn, потому что
	// turn обнуляется на EndTurn, а «сколько обменов было» нужно и между ними.
	lastTurn int

	file *os.File
	// curName — файл, открытый прямо сейчас; пусто, когда файл закрыт.
	curName string
	// sessionName — имя, занятое этим экземпляром при шаблоне «файл на запуск».
	// Хранится отдельно от curName, чтобы после /log off → /log on вернуться
	// в тот же файл, а не занять новое имя с суффиксом.
	sessionName string
	lastErr     error
}

// New создаёт журнал по устаревшему шаблону — раскладке времени Go
// вида "chat-2006-01-02.md". Каталог создаётся при первой записи, а не при
// старте, чтобы выключенный журнал не создавал лишних каталогов.
func New(dir, pattern string, enabled bool) *Logger {
	return NewFromPattern(dir, LegacyPattern(pattern), time.Now(), enabled)
}

// NewFromPattern создаёт журнал по разобранному шаблону имени. Момент start —
// время запуска программы: по нему строится имя файла, если шаблон требует
// свой файл на каждый запуск.
func NewFromPattern(dir string, pattern *Pattern, start time.Time, enabled bool) *Logger {
	if pattern == nil {
		pattern = LegacyPattern("")
	}
	return &Logger{dir: dir, pattern: pattern, start: start, enabled: enabled,
		sessionID: NewSessionID()}
}

// SessionID возвращает идентификатор этого запуска — общую часть всех
// идентификаторов обмена в нём.
func (l *Logger) SessionID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessionID
}

// BeginTurn открывает новый обмен и возвращает его идентификатор. Всё
// записанное до EndTurn помечается этим идентификатором.
//
// Счётчик работает и при выключенном журнале: идентификатор показывается
// в интерфейсе, и он не должен зависеть от того, ведётся ли запись.
func (l *Logger) BeginTurn() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Считает lastTurn, а не turn: turn обнуляется в EndTurn, и увеличивать
	// надо именно сквозной номер, иначе второй обмен снова стал бы первым.
	l.lastTurn++
	l.turn = l.lastTurn
	return FormatTurnID(l.sessionID, l.turn)
}

// EndTurn закрывает обмен: дальнейшие записи снова считаются сеансовыми.
func (l *Logger) EndTurn() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turn = 0
}

// TurnID возвращает идентификатор текущего обмена, а вне обмена — сеансовый
// идентификатор с номером 00.
func (l *Logger) TurnID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return FormatTurnID(l.sessionID, l.turn)
}

// LastTurnID — идентификатор последнего обмена: текущего, пока он идёт, и
// последнего завершённого между обменами. Это то значение, на которое сошлётся
// пользователь, поэтому его и показывает строка состояния. До первого вопроса —
// сеансовый номер 00.
func (l *Logger) LastTurnID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return FormatTurnID(l.sessionID, l.lastTurn)
}

// Turns возвращает, сколько обменов было начато за этот запуск.
func (l *Logger) Turns() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastTurn
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
// Пока не сделано ни одной записи, возвращается имя, которое будет занято.
func (l *Logger) CurrentPath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.curName != "":
		return l.curName
	case l.sessionName != "":
		return l.sessionName
	case l.pattern.PerSession():
		return filepath.Join(l.dir, l.pattern.Name(l.start))
	default:
		return filepath.Join(l.dir, l.pattern.Name(time.Now()))
	}
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

// openLocked открывает нужный файл, переоткрывая его при ротации.
func (l *Logger) openLocked(now time.Time) error {
	if l.pattern.PerSession() {
		return l.openSessionLocked()
	}
	name := filepath.Join(l.dir, l.pattern.Name(now))
	if l.file != nil && l.curName == name {
		return nil
	}
	if err := l.closeLocked(); err != nil {
		return err
	}
	return l.openAppendLocked(name)
}

// openSessionLocked открывает файл этого запуска, занимая имя один раз.
//
// Имя занимается флагом O_EXCL: два экземпляра, запущенные в одну и ту же
// секунду, не получат один файл — второй возьмёт имя с суффиксом «-2».
// Проверить существование и потом открыть было бы недостаточно: между двумя
// вызовами успевает вклиниться соседний процесс.
func (l *Logger) openSessionLocked() error {
	if l.file != nil {
		return nil
	}
	if l.sessionName != "" {
		// Имя уже занято этим экземпляром — просто возвращаемся в свой файл.
		return l.openAppendLocked(l.sessionName)
	}

	base := filepath.Join(l.dir, l.pattern.Name(l.start))
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return fmt.Errorf("создание каталога журнала: %w", err)
	}
	for i := 1; i <= maxNameAttempts; i++ {
		name := base
		if i > 1 {
			name = nameWithSuffix(base, i)
		}
		f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("открытие журнала %s: %w", name, err)
		}
		l.file = f
		l.curName = name
		l.sessionName = name
		return nil
	}
	return fmt.Errorf("журнал %s: имя и %d запасных имён уже заняты", base, maxNameAttempts-1)
}

// openAppendLocked открывает файл на дозапись, создавая каталог при нужде.
func (l *Logger) openAppendLocked(name string) error {
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

// nameWithSuffix вставляет номер перед расширением: chat-....md → chat-...-2.md.
func nameWithSuffix(name string, n int) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), n, ext)
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

// FormatEntry собирает запись журнала ровно в том виде, в каком она ложится
// в файл: идентификатор обмена в квадратных скобках, отметка времени и, если
// оно задано, название модели, пустая строка, тело и две пустые
// строки-разделителя после него.
//
// Идентификатор стоит перед датой, чтобы одинаковые значения выстраивались
// в колонку у левого края: обмен с десятком вызовов инструментов иначе никак
// не отделить от следующего. Пустой идентификатор даёт прежнюю шапку без
// скобок — так выглядят все журналы, написанные до его появления.
//
// Вынесено из writeEntry, чтобы тот же формат можно было собрать снаружи:
// копирование ответа в буфер обмена (Shift+F5) отдаёт пользователю текст,
// неотличимый от журнала. Второй реализации этого формата быть не должно —
// они разъедутся при первой же правке.
func FormatEntry(id string, ts time.Time, kind, model, body string) string {
	var b strings.Builder
	if id = strings.TrimSpace(id); id != "" {
		b.WriteString("[")
		b.WriteString(id)
		b.WriteString("] ")
	}
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
	return b.String()
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

	if _, err := l.file.WriteString(
		FormatEntry(FormatTurnID(l.sessionID, l.turn), ts, kind, model, body)); err != nil {
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
