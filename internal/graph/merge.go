package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	gone int // сколько понятий поглощено: записи to, ведущие не в себя

	// size — сколько байт журнала прочитано: по нему снятие склеек узнаёт,
	// что журнал дописали, пока оно готовило подмену (см. undo).
	size int64
}

// openMerges читает журнал склеек. Отсутствие файла — обычное состояние.
func openMerges(dir string) (*Merges, error) {
	m := &Merges{
		path: filepath.Join(dir, mergesFile),
		to:   map[uint32]uint32{},
		from: map[uint32][]uint32{},
	}
	recs, size, err := readMergeRecs(m.path)
	if err != nil {
		return nil, err
	}
	m.recs, m.size = recs, size
	m.rebuild()
	return m, nil
}

// readMergeRecs читает записи журнала склеек и число прочитанных байт.
// Нет файла — пусто, и это не ошибка.
func readMergeRecs(path string) ([]MergeRec, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	var recs []MergeRec
	// Битая и слишком длинная строки пропускаются, а ошибка чтения — ошибка:
	// журнал, молча усечённый сбоем диска, снятие склеек переписало бы
	// по усечённому навсегда (eachLine).
	size, err := eachLine(f, 1024*1024, func(line []byte) {
		if len(line) == 0 {
			return
		}
		var r MergeRec
		if json.Unmarshal(line, &r) != nil || r.From == 0 || r.To == 0 || r.From == r.To {
			return // оборванная последняя строка — не беда, дозапись
		}
		recs = append(recs, r)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", mergesFile, err)
	}
	return recs, size, nil
}

// reload перечитывает журнал склеек с диска.
func (m *Merges) reload() error {
	recs, size, err := readMergeRecs(m.path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs, m.size = recs, size
	m.rebuild()
	return nil
}

// rebuild собирает разрешение номеров по журналу.
func (m *Merges) rebuild() {
	m.to = make(map[uint32]uint32, len(m.recs))
	m.from = make(map[uint32][]uint32, len(m.recs))
	for _, r := range m.recs {
		m.to[r.From] = r.To
	}
	// Обход по возрастанию номера, а не по карте.
	//
	// **Почему это важнее, чем кажется.** Порядок поглощённых становится
	// порядком синонимов выжившего (withAbsorbed в entities.go), а синонимы
	// идут в текст вектора понятия и тройки — и обрезаются по
	// `graph.vector_aliases`. Обход карты случаен в каждом процессе, поэтому
	// с ним у одного и того же графа от запуска к запуску менялись и порядок
	// синонимов, и то, КАКИЕ синонимы переживут обрезку, а значит и вектор.
	//
	// Замерено 24.09.2026 на рабочем графе: два запуска подряд на неизменных
	// файлах давали 183 005 и 183 003 тройки, расходилось 7 562 текста
	// из 183 тысяч (4%) — «GPU, графический, видеокарты, ГП» против
	// «GPU, видеокарты, ГП, графический», а у понятий с длинным хвостом
	// синонимов менялся и состав. Каждый счёт индекса считал заново эти
	// проценты «устаревших» троек, и никакая работа не сходилась.
	ids := make([]uint32, 0, len(m.to))
	for id := range m.to {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	// Сжатие цепочек: A→B→C превращается в A→C. Без этого поиск через A
	// вернул бы B, которого уже нет как отдельного понятия.
	for _, id := range ids {
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
	m.gone = 0
	for _, id := range ids {
		if dst := m.to[id]; id != dst {
			m.from[dst] = append(m.from[dst], id)
			m.gone++
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

// Выключенные склейки (Rules.MergesOff) выключены целиком: Resolve, Absorbed,
// Gone и Count отвечают так, будто журнала нет. До 07.10.2026 выключался
// только Resolve, и граф выходил гибридным: поглощённое понятие находилось
// само по себе, но его связи и упоминания доставались ещё и выжившему, а из
// Live оно пропадало — связь считалась дважды (аудит, 4.5). Записи журнала
// (Records, Add, снятие склеек) от выключателя не зависят: это работа
// с журналом, а не его действие на граф.

// Absorbed возвращает номера, поглощённые этим понятием.
func (m *Merges) Absorbed(id uint32) []uint32 {
	if m == nil || m.off {
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
	if m == nil || m.off {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	dst, ok := m.to[id]
	return ok && dst != id
}

// Count — сколько понятий поглощено.
//
// Именно поглощённых, а не записей журнала. В журнале бывают круги: пара,
// записанная дважды навстречу (A→B автоматом и B→A человеком), или тройка
// по кругу. Разрешение номеров оставляет в круге одного выжившего — он
// ссылается сам на себя и никуда не делся. До 03.10.2026 возвращалось
// `len(m.to)`, и выжившие кругов считались поглощёнными: на рабочем графе
// 38 909 вместо 38 883, а доктор занижал число живых понятий на 26 —
// расходясь с `Entities().Live()`, которым пользуются поиск и приборы.
//
// Число считается один раз в rebuild, а не здесь: Count зовут на каждое
// понятие как быструю проверку «склеек нет» (Edges.outgoing, Entities.Live),
// и проход по таблице на каждый вызов делал доктора квадратичным.
func (m *Merges) Count() int {
	if m == nil || m.off {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gone
}

// inJournal — участвует ли понятие в журнале склеек поглощённым или
// выжившим, включены склейки или нет. Нужен уплотнению: понятие из журнала
// выбрасывать нельзя, даже пока склейки выключены, — включат их снова,
// и склейка повиснет на пустом номере.
func (m *Merges) inJournal(id uint32) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	dst, ok := m.to[id]
	return (ok && dst != id) || len(m.from[id]) > 0
}

// leadsTo — приводит ли цепочка склеек от понятия id к понятию target.
// Вызывается под замком; идёт по цепочке сам, потому что записи текущей
// пачки в m.to ещё не сжаты.
func (m *Merges) leadsTo(id, target uint32) bool {
	seen := map[uint32]bool{}
	for !seen[id] {
		if id == target {
			return true
		}
		seen[id] = true
		next, ok := m.to[id]
		if !ok {
			return false
		}
		id = next
	}
	return false
}

// maxEntity — наибольший номер понятия в журнале склеек, не больше limit
// (см. Mentions.maxEntity).
func (m *Merges) maxEntity(limit uint32) uint32 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out uint32
	for _, r := range m.recs {
		for _, id := range []uint32{r.From, r.To} {
			if id > out && id <= limit {
				out = id
			}
		}
	}
	return out
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
		// Уже поглощено — значит, ведёт не в себя. Выживший круга (A→B и B→A
		// в журнале) ведёт в себя, и до 07.10.2026 его новая склейка
		// отбрасывалась молча, как «уже склеенное»: склеить такое понятие
		// с третьим было нельзя никогда (аудит, 4.5).
		if dst, done := m.to[r.From]; done && dst != r.From {
			continue
		}
		// Встречная склейка: To уже поглощён понятием From (прямо или через
		// цепочку). Запись замкнула бы круг — так 18.09.2026 ручной разбор
		// записал B→A по 25 парам, которые автомат тремя днями раньше склеил
		// как A→B. Пара и так склеена, новая запись ничего не добавляет.
		if m.leadsTo(r.To, r.From) {
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

	if err := appendJSONL(m.path, fresh, false); err != nil {
		return 0, err
	}
	if fi, err := os.Stat(m.path); err == nil {
		m.size = fi.Size()
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
