package graph

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Склейка двойников: наложение поверх графа, а не правка графа.
//
// **Зачем отдельным файлом.** Склейка необратима по сути — два понятия
// становятся одним. Если её записывать прямо в реестр сущностей, отменить
// будет нечем, а первое же неверное решение испортит несколько суток работы
// модели. Поэтому решения лежат в своём журнале `merges.jsonl` и **надеваются
// на граф при чтении**: убрать файл — и граф прежний.
//
// Так же советует *Building Knowledge Graphs* (2023, стр. 145–149): хранить
// «persisted record of master entities» отдельно, потому что разрешение
// сущностей — не разовое дело, а повторяемое на растущих данных.
//
// **Что происходит при склейке.** Поглощённое понятие перестаёт находиться
// само по себе: его имя, синонимы, связи и упоминания достаются выжившему.
// Номера не перенумеровываются — векторы понятий лежат по номерам, и сдвиг
// испортил бы смысловой вход.
//
// **Цепочки.** Если A поглощено B, а B поглощено C, то A должно вести к C.
// Разбирается при чтении сжатием путей, иначе поиск через A вернул бы
// понятие, которого уже нет.

const mergesFile = "merges.jsonl"

// MergeRec — одно решение о склейке.
//
// Поля сверх пары нужны не программе, а человеку: когда через месяц окажется,
// что склейка неверна, по ним видно, на каком основании она принята.
type MergeRec struct {
	From uint32 `json:"from"` // поглощённое понятие
	To   uint32 `json:"to"`   // выживший

	Cos     float64 `json:"cos,omitempty"`
	Verdict string  `json:"verdict,omitempty"` // вердикт разбиравшей модели
	Alias   bool    `json:"alias,omitempty"`   // модель извлечения давала синоним
	Why     string  `json:"why,omitempty"`
	Level   string  `json:"level,omitempty"` // каким правилом отобрано
	At      int64   `json:"at"`
}

// Merges — журнал склеек и разрешение номеров по нему.
type Merges struct {
	mu   sync.RWMutex
	path string
	// off — склейки не действуют (Rules.MergesOff): для отката и сравнения
	// «граф со склейками» против «граф с группами».
	off bool

	to   map[uint32]uint32   // поглощённое → выживший, цепочки уже сжаты
	from map[uint32][]uint32 // выживший → все поглощённые им
	recs []MergeRec
}

// openMerges читает журнал склеек. Отсутствие файла — обычное состояние.
func openMerges(dir string) (*Merges, error) {
	m := &Merges{
		path: filepath.Join(dir, mergesFile),
		to:   map[uint32]uint32{},
		from: map[uint32][]uint32{},
	}
	f, err := os.Open(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r MergeRec
		if json.Unmarshal(line, &r) != nil || r.From == 0 || r.To == 0 || r.From == r.To {
			continue // оборванная последняя строка — не беда, дозапись
		}
		m.recs = append(m.recs, r)
	}
	m.rebuild()
	return m, nil
}

// rebuild собирает разрешение номеров по журналу.
func (m *Merges) rebuild() {
	m.to = make(map[uint32]uint32, len(m.recs))
	m.from = make(map[uint32][]uint32, len(m.recs))
	for _, r := range m.recs {
		m.to[r.From] = r.To
	}
	// Сжатие цепочек: A→B→C превращается в A→C. Без этого поиск через A
	// вернул бы B, которого уже нет как отдельного понятия.
	for id := range m.to {
		seen := map[uint32]bool{id: true}
		cur := m.to[id]
		for {
			next, ok := m.to[cur]
			if !ok || seen[cur] {
				break
			}
			seen[cur] = true
			cur = next
		}
		m.to[id] = cur
	}
	for id, dst := range m.to {
		if id != dst {
			m.from[dst] = append(m.from[dst], id)
		}
	}
}

// Resolve возвращает выжившего для номера. Для несклеенного — его же.
func (m *Merges) Resolve(id uint32) uint32 {
	if m == nil || m.off {
		return id
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if dst, ok := m.to[id]; ok {
		return dst
	}
	return id
}

// Absorbed возвращает номера, поглощённые этим понятием.
func (m *Merges) Absorbed(id uint32) []uint32 {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.from[id]) == 0 {
		return nil
	}
	return append([]uint32(nil), m.from[id]...)
}

// Gone сообщает, что понятие поглощено и само по себе больше не существует.
func (m *Merges) Gone(id uint32) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	dst, ok := m.to[id]
	return ok && dst != id
}

// Count — сколько понятий поглощено.
func (m *Merges) Count() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.to)
}

// Records отдаёт журнал целиком: он нужен, чтобы показать человеку, на каком
// основании принято каждое решение.
func (m *Merges) Records() []MergeRec {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]MergeRec(nil), m.recs...)
}

// Add дописывает решения в журнал.
//
// Дозапись, а не перезапись: журнал — след принятых решений, и терять прежние
// при добавлении новых нельзя. Уже склеенное и петли отбрасываются молча.
func (m *Merges) Add(recs []MergeRec) (int, error) {
	if m == nil || len(recs) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Unix()
	var fresh []MergeRec
	for _, r := range recs {
		if r.From == 0 || r.To == 0 || r.From == r.To {
			continue
		}
		if _, done := m.to[r.From]; done {
			continue
		}
		if r.At == 0 {
			r.At = now
		}
		fresh = append(fresh, r)
		m.to[r.From] = r.To // чтобы повтор внутри одной пачки не прошёл дважды
	}
	if len(fresh) == 0 {
		return 0, nil
	}

	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriter(f)
	for _, r := range fresh {
		b, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return 0, err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			f.Close()
			return 0, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	m.recs = append(m.recs, fresh...)
	m.rebuild()
	return len(fresh), nil
}

// Merges отдаёт журнал склеек.
func (g *Graph) Merges() *Merges { return g.merges }

// removeFile снимает файл из каталога графа. Нужен, чтобы отменить склейку.
func removeFile(dir, name string) error {
	err := os.Remove(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// DropMerges снимает все склейки: граф возвращается в прежний вид.
//
// Затем граф надо открыть заново — наложение читается при открытии.
func (g *Graph) DropMerges() error {
	if g == nil {
		return nil
	}
	return removeFile(g.dir, mergesFile)
}
