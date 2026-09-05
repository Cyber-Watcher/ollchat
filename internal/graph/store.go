package graph

import (
	"bufio"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Упоминания и связи.
//
// Оба журнала — двоичные, запись постоянной длины, только дозапись. Такой файл
// переживает обрыв: хвост короче записи просто отбрасывается при чтении,
// а всё, что записано до него, остаётся годным. Это то же решение, что
// в хранилище кусков (internal/kb/store.go), и по той же причине: сборка идёт
// часами, и обрыв посреди неё — обычное дело, а не исключительный случай.
//
// Читаются журналы целиком в память. Прикидка на всю библиотеку: 249 тысяч
// кусков дают порядка двух миллионов упоминаний по 12 байт и миллиона связей
// по 24 байта — это десятки мегабайт, столько же занимает индекс книг. Как
// только замер покажет, что это дорого, добавится сжатый вид на диске; пока
// заводить его значит усложнять формат без повода.

const (
	mentionsFile = "mentions.log"
	edgesFile    = "edges.log"

	mentionSize = 12 // сущность, книга, номер куска
	edgeSize    = 24 // src, dst, тип, вес, книга и номер куска-подтверждения
)

// Типы связей. Список закрыт по той же причине, что и типы сущностей:
// свободные названия у разных книг разъезжаются, и сравнивать граф становится
// нечем. «Связано» оставлено намеренно — это честное «связь есть, а какая,
// модель сказать не смогла», и оно лучше выдуманного точного типа.
const (
	RelIs      uint8 = iota + 1 // является, частный случай
	RelPart                     // часть целого
	RelUses                     // использует, опирается на
	RelAffects                  // влияет, ограничивает
	RelOpposed                  // противопоставлено, альтернатива
	RelExample                  // пример, реализация
	RelRelated                  // связано без уточнения
)

// RelName печатает тип связи по-русски.
func RelName(t uint8) string {
	switch t {
	case RelIs:
		return "является"
	case RelPart:
		return "часть"
	case RelUses:
		return "использует"
	case RelAffects:
		return "влияет"
	case RelOpposed:
		return "противопоставлено"
	case RelExample:
		return "пример"
	default:
		return "связано"
	}
}

// RelType разбирает название типа связи, пришедшее от модели.
func RelType(s string) uint8 {
	switch Normalize(s) {
	case "является", "is", "тип", "вид":
		return RelIs
	case "часть", "part", "часть целого", "входит":
		return RelPart
	case "использует", "uses", "опирается":
		return RelUses
	case "влияет", "affects", "ограничивает":
		return RelAffects
	case "противопоставлено", "альтернатива", "opposed", "против":
		return RelOpposed
	case "пример", "example", "реализация":
		return RelExample
	default:
		return RelRelated
	}
}

// ── Упоминания ───────────────────────────────────────────────────────────────

// Mentions — где какая сущность встречается.
type Mentions struct {
	// merges — склейки, надеваемые при чтении.
	merges *Merges

	mu sync.RWMutex
	f  *os.File
	w  *bufio.Writer

	byEntity map[uint32][]uint64 // сущность → упакованные номера кусков
	byChunk  map[uint64][]uint32 // кусок → сущности
	count    int
}

func openMentions(dir string) (*Mentions, error) {
	m := &Mentions{
		byEntity: map[uint32][]uint64{},
		byChunk:  map[uint64][]uint32{},
	}
	path := filepath.Join(dir, mentionsFile)
	if err := m.load(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	m.f, m.w = f, bufio.NewWriterSize(f, 64*1024)
	return m, nil
}

func (m *Mentions) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 256*1024)
	buf := make([]byte, mentionSize)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			// Хвост короче записи — это оборванная запись. Всё, что до неё,
			// годится; на этом и держится возобновление после обрыва.
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		ent := binary.LittleEndian.Uint32(buf[0:])
		key := ChunkKey{
			Doc: binary.LittleEndian.Uint32(buf[4:]),
			Ord: binary.LittleEndian.Uint32(buf[8:]),
		}.Pack()
		m.index(ent, key)
	}
}

func (m *Mentions) index(ent uint32, key uint64) {
	m.byEntity[ent] = append(m.byEntity[ent], key)
	m.byChunk[key] = append(m.byChunk[key], ent)
	m.count++
}

// Add записывает упоминание сущности в куске.
// useMerges надевает журнал склеек на упоминания.
func (m *Mentions) useMerges(mg *Merges) { m.merges = mg }

func (m *Mentions) Add(entity uint32, chunk ChunkKey) error {
	if entity == 0 {
		return nil
	}
	var buf [mentionSize]byte
	binary.LittleEndian.PutUint32(buf[0:], entity)
	binary.LittleEndian.PutUint32(buf[4:], chunk.Doc)
	binary.LittleEndian.PutUint32(buf[8:], chunk.Ord)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.w.Write(buf[:]); err != nil {
		return err
	}
	m.index(entity, chunk.Pack())
	return nil
}

// Of возвращает куски, где встречается сущность, по возрастанию и без повторов.
func (m *Mentions) Of(entity uint32) []ChunkKey {
	// Склейка: упоминания поглощённых достаются выжившему. Иначе понятие,
	// собранное из двух половин, выглядело бы вдвое реже встречающимся,
	// чем оно есть, и проигрывало бы в весе при поиске.
	entity = m.merges.Resolve(entity)
	m.mu.RLock()
	packed := append([]uint64(nil), m.byEntity[entity]...)
	for _, gone := range m.merges.Absorbed(entity) {
		packed = append(packed, m.byEntity[gone]...)
	}
	m.mu.RUnlock()
	if len(packed) == 0 {
		return nil
	}
	sort.Slice(packed, func(i, j int) bool { return packed[i] < packed[j] })
	out := make([]ChunkKey, 0, len(packed))
	var prev uint64
	for i, v := range packed {
		if i > 0 && v == prev {
			continue
		}
		prev = v
		out = append(out, UnpackChunk(v))
	}
	return out
}

// In возвращает сущности, упомянутые в куске, по возрастанию и без повторов.
//
// Повторы неизбежны: модель называет одно понятие дважды в одном куске,
// и оба раза записываются честно. Слипаться они должны при чтении, а не при
// записи — журнал остаётся простым дозаписываемым файлом.
// eachMention обходит все упоминания: сущность и упакованный кусок. Порядок
// не гарантирован. Под замком чтения — обход не должен ловить полузапись.
func (m *Mentions) eachMention(fn func(ent uint32, key uint64)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for ent, keys := range m.byEntity {
		for _, k := range keys {
			fn(ent, k)
		}
	}
}

func (m *Mentions) In(chunk ChunkKey) []uint32 {
	m.mu.RLock()
	ids := append([]uint32(nil), m.byChunk[chunk.Pack()]...)
	m.mu.RUnlock()
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := ids[:0]
	var prev uint32
	for i, v := range ids {
		if i > 0 && v == prev {
			continue
		}
		prev = v
		out = append(out, v)
	}
	return out
}

// Count возвращает число записанных упоминаний.
func (m *Mentions) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.count
}

// Sync сбрасывает буфер и просит диск записать его: Flush отдаёт данные
// системе, а при отказе питания они всё ещё в её кеше. Журнал упоминаний.
func (m *Mentions) Sync() error {
	if err := m.Flush(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.f == nil {
		return nil
	}
	return m.f.Sync()
}

// Flush дописывает буфер на диск.
func (m *Mentions) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil {
		return nil
	}
	return m.w.Flush()
}

// Close закрывает журнал упоминаний.
func (m *Mentions) Close() error {
	if err := m.Flush(); err != nil {
		return err
	}
	if m.f == nil {
		return nil
	}
	err := m.f.Close()
	m.f = nil
	return err
}

// ── Связи ────────────────────────────────────────────────────────────────────

// Edge — связь между двумя сущностями.
type Edge struct {
	Src, Dst uint32
	Type     uint8
	Weight   float32

	// Evidence — кусок, которым связь подтверждена. Связь без подтверждения
	// в граф не попадает вовсе: именно на выдуманных связях без опоры
	// разваливается доверие ко всей затее.
	Evidence ChunkKey
}

// Edges — связи графа.
type Edges struct {
	// merges — склейки, надеваемые при чтении.
	merges *Merges

	mu sync.RWMutex
	f  *os.File
	w  *bufio.Writer

	bySrc map[uint32][]Edge
	// byDst — тот же список, но по второму концу связи.
	//
	// **Зачем.** Обход шёл только по исходящим, и у понятия, на которое лишь
	// ссылаются, соседей в выдаче не было вовсе: «канал» встречается в сотне
	// связей вида «горутина —использует→ канал», а спроси про канал — пусто.
	// Память: тот же список ссылок второй раз, единицы мегабайт на нашем графе.
	byDst map[uint32][]Edge
	count int
}

func openEdges(dir string) (*Edges, error) {
	e := &Edges{bySrc: map[uint32][]Edge{}, byDst: map[uint32][]Edge{}}
	path := filepath.Join(dir, edgesFile)
	if err := e.load(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	e.f, e.w = f, bufio.NewWriterSize(f, 64*1024)
	return e, nil
}

func (e *Edges) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 256*1024)
	buf := make([]byte, edgeSize)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		e.index(decodeEdge(buf))
	}
}

func (e *Edges) index(ed Edge) {
	e.bySrc[ed.Src] = append(e.bySrc[ed.Src], ed)
	e.byDst[ed.Dst] = append(e.byDst[ed.Dst], ed)
	e.count++
}

func decodeEdge(b []byte) Edge {
	return Edge{
		Src:    binary.LittleEndian.Uint32(b[0:]),
		Dst:    binary.LittleEndian.Uint32(b[4:]),
		Type:   b[8],
		Weight: math.Float32frombits(binary.LittleEndian.Uint32(b[12:])),
		Evidence: ChunkKey{
			Doc: binary.LittleEndian.Uint32(b[16:]),
			Ord: binary.LittleEndian.Uint32(b[20:]),
		},
	}
}

func encodeEdge(b []byte, ed Edge) {
	binary.LittleEndian.PutUint32(b[0:], ed.Src)
	binary.LittleEndian.PutUint32(b[4:], ed.Dst)
	b[8], b[9], b[10], b[11] = ed.Type, 0, 0, 0
	binary.LittleEndian.PutUint32(b[12:], math.Float32bits(ed.Weight))
	binary.LittleEndian.PutUint32(b[16:], ed.Evidence.Doc)
	binary.LittleEndian.PutUint32(b[20:], ed.Evidence.Ord)
}

// Add записывает связь. Петли и связи без подтверждения отбрасываются молча:
// первое бессмысленно, второе не проверить.
func (e *Edges) Add(ed Edge) error {
	if ed.Src == 0 || ed.Dst == 0 || ed.Src == ed.Dst {
		return nil
	}
	if ed.Type == 0 {
		ed.Type = RelRelated
	}
	if ed.Weight == 0 {
		ed.Weight = 1
	}
	var buf [edgeSize]byte
	encodeEdge(buf[:], ed)

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(buf[:]); err != nil {
		return err
	}
	e.index(ed)
	return nil
}

// useMerges надевает журнал склеек на связи.
func (e *Edges) useMerges(m *Merges) { e.merges = m }

// outgoing собирает связи выжившего вместе со связями поглощённых им,
// переводя и второй конец каждой связи к его выжившему.
//
// Ради этого склейка и делается: у «сборщика мусора» и `Garbage collection`
// было по половине связей, и только сойдясь у одного узла они дают целое.
// Петли отбрасываются: связь понятия с самим собой после склейки бессмысленна.
func (e *Edges) outgoing(src uint32) []Edge {
	src = e.merges.Resolve(src)
	e.mu.RLock()
	list := append([]Edge(nil), e.bySrc[src]...)
	for _, gone := range e.merges.Absorbed(src) {
		list = append(list, e.bySrc[gone]...)
	}
	e.mu.RUnlock()

	if e.merges.Count() == 0 {
		return list
	}
	out := list[:0]
	for _, ed := range list {
		ed.Src, ed.Dst = src, e.merges.Resolve(ed.Dst)
		if ed.Src == ed.Dst {
			continue
		}
		out = append(out, ed)
	}
	return out
}

// Of возвращает связи, исходящие из сущности.
func (e *Edges) Of(src uint32) []Edge {
	return e.outgoing(src)
}

// ofRaw — связи без наложения склеек. Нужен там, где граф читается как есть.
func (e *Edges) ofRaw(src uint32) []Edge {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.bySrc[src]) == 0 {
		return nil
	}
	return append([]Edge(nil), e.bySrc[src]...)
}

// Neighbor — сосед сущности с суммарным весом связей к нему.
type Neighbor struct {
	ID     uint32
	Weight float32
	Types  []uint8
	Count  int
	// In — связь идёт ОТ соседа К нам, а не наоборот.
	//
	// Направление приходится помнить: без него «горутина —использует→ канал»
	// в карточке канала напечаталось бы как «канал —использует→ горутина»,
	// то есть ровно наоборот.
	In bool
}

// incoming — связи, ведущие К сущности. Зеркало outgoing, включая склейки:
// поглощённые узлы отдают свои входящие связи выжившему.
func (e *Edges) incoming(dst uint32) []Edge {
	dst = e.merges.Resolve(dst)
	e.mu.RLock()
	list := append([]Edge(nil), e.byDst[dst]...)
	for _, gone := range e.merges.Absorbed(dst) {
		list = append(list, e.byDst[gone]...)
	}
	e.mu.RUnlock()

	if e.merges.Count() == 0 {
		return list
	}
	out := list[:0]
	for _, ed := range list {
		ed.Src = e.merges.Resolve(ed.Src)
		ed.Dst = e.merges.Resolve(ed.Dst)
		if ed.Src == ed.Dst {
			continue // петля после склейки
		}
		out = append(out, ed)
	}
	return out
}

// Neighbors собирает соседей сущности, сливая повторяющиеся связи.
//
// Одна и та же связь встречается в десятках кусков — это не десять разных
// связей, а одна, подтверждённая десять раз. Вес складывается: чем чаще
// книги говорят о связи, тем она крепче.
func (e *Edges) Neighbors(src uint32) []Neighbor {
	type key struct {
		id uint32
		in bool
	}
	agg := map[key]*Neighbor{}
	add := func(other uint32, typ uint8, w float32, in bool) {
		k := key{other, in}
		n, ok := agg[k]
		if !ok {
			n = &Neighbor{ID: other, In: in}
			agg[k] = n
		}
		n.Weight += w
		n.Count++
		if !hasType(n.Types, typ) {
			n.Types = append(n.Types, typ)
		}
	}
	for _, ed := range e.outgoing(src) {
		add(ed.Dst, ed.Type, ed.Weight, false)
	}
	// Входящие идут тем же списком: для вопроса «с чем это связано» разницы
	// нет, а для печати направление сохранено в поле In.
	for _, ed := range e.incoming(src) {
		add(ed.Src, ed.Type, ed.Weight, true)
	}
	out := make([]Neighbor, 0, len(agg))
	for _, n := range agg {
		out = append(out, *n)
	}
	// Порядок устойчивый: по весу, при равенстве по номеру, затем по
	// направлению. Иначе выдача пляшет от запуска к запуску вслед за порядком
	// обхода отображения.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return !out[i].In && out[j].In
	})
	return out
}

func hasType(list []uint8, t uint8) bool {
	for _, v := range list {
		if v == t {
			return true
		}
	}
	return false
}

// Between возвращает связи между двумя сущностями в обе стороны.
func (e *Edges) Between(a, b uint32) []Edge {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Edge
	for _, ed := range e.bySrc[a] {
		if ed.Dst == b {
			out = append(out, ed)
		}
	}
	for _, ed := range e.bySrc[b] {
		if ed.Dst == a {
			out = append(out, ed)
		}
	}
	return out
}

// Count возвращает число записанных связей.
// eachEdge обходит все связи по исходящим спискам. Порядок не гарантирован.
func (e *Edges) eachEdge(fn func(Edge)) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, list := range e.bySrc {
		for _, ed := range list {
			fn(ed)
		}
	}
}

func (e *Edges) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.count
}

// Sync сбрасывает буфер и просит диск записать его: Flush отдаёт данные
// системе, а при отказе питания они всё ещё в её кеше. Журнал связей.
func (e *Edges) Sync() error {
	if err := e.Flush(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return nil
	}
	return e.f.Sync()
}

// Flush дописывает буфер на диск.
func (e *Edges) Flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.w == nil {
		return nil
	}
	return e.w.Flush()
}

// Close закрывает журнал связей.
func (e *Edges) Close() error {
	if err := e.Flush(); err != nil {
		return err
	}
	if e.f == nil {
		return nil
	}
	err := e.f.Close()
	e.f = nil
	return err
}
