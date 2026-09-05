package graph

// Журнал синонимов с источником — формат 2 (GraphSchemaV2.md, п. 1).
//
// **Зачем.** В формате 1 синоним живёт только в реестре понятий, и откуда он
// взялся, узнать нельзя. Оттого проверка синонимов задним числом давала шум
// (50.7% несовпадений, замер 03.09.2026), а арбитр косил переводы. Здесь на
// каждое ВХОЖДЕНИЕ синонима пишется запись: понятие, книга и кусок, где он
// встречен, и его нормализованное написание. Это даёт «покажи, где так
// называют», перепроверку ровно по тому куску, откуда синоним взят, и опору
// для поздней доливки выдержек (evidence.log) без пересборки.
//
// Запись имеет стабильный номер — её порядковое место в журнале, начиная
// с единицы. Журнал только дозаписывается, поэтому номер не меняется никогда,
// и на него можно ссылаться из других файлов.
//
// Файл: подряд записи вида
//
//	entity uint32 · doc uint32 · ord uint32 · nlen uint16 · norm [nlen]byte
//
// Оборванный хвост (обрыв сборки посреди записи) отбрасывается при чтении,
// как и у mentions.log: всё до него годится.
//
// Формат 1 этого файла не имеет и не читает: у графа формата 1 Aliases()
// возвращает nil, и вызывающий код обязан это учитывать.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	aliasesFile     = "aliases.log"
	aliasHeaderSize = 14 // entity, doc, ord, nlen
	aliasMaxNorm    = 512
)

// AliasRec — одно вхождение синонима: где и к какому понятию.
type AliasRec struct {
	ID     uint32 // стабильный номер записи, с единицы
	Entity uint32
	Chunk  ChunkKey
	Norm   string // нормализованное написание, см. Normalize
}

// Aliases — журнал синонимов графа формата 2.
type Aliases struct {
	merges *Merges

	mu sync.RWMutex
	f  *os.File
	w  *bufio.Writer

	list     []AliasRec          // по номеру записи (ID = индекс + 1)
	byEntity map[uint32][]uint32 // понятие → номера записей
	byNorm   map[string][]uint32 // написание → номера записей
}

func openAliases(dir string) (*Aliases, error) {
	a := &Aliases{byEntity: map[uint32][]uint32{}, byNorm: map[string][]uint32{}}
	path := filepath.Join(dir, aliasesFile)
	if err := a.load(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	a.f, a.w = f, bufio.NewWriterSize(f, 64*1024)
	return a, nil
}

func (a *Aliases) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 256*1024)
	head := make([]byte, aliasHeaderSize)
	body := make([]byte, aliasMaxNorm)
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // оборванная запись: всё до неё годится
			}
			return err
		}
		n := int(binary.LittleEndian.Uint16(head[12:]))
		if n > aliasMaxNorm {
			return fmt.Errorf("%s: запись %d длиннее предела (%d байт) — файл повреждён", aliasesFile, len(a.list)+1, n)
		}
		if _, err := io.ReadFull(r, body[:n]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		a.index(AliasRec{
			Entity: binary.LittleEndian.Uint32(head[0:]),
			Chunk: ChunkKey{
				Doc: binary.LittleEndian.Uint32(head[4:]),
				Ord: binary.LittleEndian.Uint32(head[8:]),
			},
			Norm: string(body[:n]),
		})
	}
}

func (a *Aliases) index(rec AliasRec) uint32 {
	rec.ID = uint32(len(a.list) + 1)
	a.list = append(a.list, rec)
	a.byEntity[rec.Entity] = append(a.byEntity[rec.Entity], rec.ID)
	a.byNorm[rec.Norm] = append(a.byNorm[rec.Norm], rec.ID)
	return rec.ID
}

// useMerges надевает журнал склеек: вхождения поглощённых достаются выжившему.
func (a *Aliases) useMerges(mg *Merges) {
	if a != nil {
		a.merges = mg
	}
}

// Add записывает вхождение синонима alias у понятия entity в куске chunk.
// Написание нормализуется здесь же; пустое после нормализации не пишется.
func (a *Aliases) Add(entity uint32, chunk ChunkKey, alias string) (uint32, error) {
	norm := Normalize(alias)
	if entity == 0 || norm == "" {
		return 0, nil
	}
	if len(norm) > aliasMaxNorm {
		norm = norm[:aliasMaxNorm]
	}
	var head [aliasHeaderSize]byte
	binary.LittleEndian.PutUint32(head[0:], entity)
	binary.LittleEndian.PutUint32(head[4:], chunk.Doc)
	binary.LittleEndian.PutUint32(head[8:], chunk.Ord)
	binary.LittleEndian.PutUint16(head[12:], uint16(len(norm)))

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.w.Write(head[:]); err != nil {
		return 0, err
	}
	if _, err := a.w.WriteString(norm); err != nil {
		return 0, err
	}
	return a.index(AliasRec{Entity: entity, Chunk: chunk, Norm: norm}), nil
}

// Of — вхождения синонимов понятия, в порядке записи.
func (a *Aliases) Of(entity uint32) []AliasRec {
	if a == nil {
		return nil
	}
	entity = a.merges.Resolve(entity)
	a.mu.RLock()
	defer a.mu.RUnlock()
	ids := append([]uint32(nil), a.byEntity[entity]...)
	for _, gone := range a.merges.Absorbed(entity) {
		ids = append(ids, a.byEntity[gone]...)
	}
	return a.pick(ids)
}

// Where — вхождения данного написания (уже нормализованного или нет):
// у каких понятий и в каких кусках оно встречено как синоним.
func (a *Aliases) Where(alias string) []AliasRec {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pick(a.byNorm[Normalize(alias)])
}

// Get — запись по стабильному номеру; ok=false, если такой нет.
func (a *Aliases) Get(id uint32) (AliasRec, bool) {
	if a == nil {
		return AliasRec{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if id == 0 || int(id) > len(a.list) {
		return AliasRec{}, false
	}
	return a.list[id-1], true
}

func (a *Aliases) pick(ids []uint32) []AliasRec {
	if len(ids) == 0 {
		return nil
	}
	out := make([]AliasRec, 0, len(ids))
	for _, id := range ids {
		out = append(out, a.list[id-1])
	}
	return out
}

// Count — сколько вхождений записано.
// All — копия всех записей журнала по порядку номеров. Для отчётов; журнал
// на живом графе — сотни тысяч записей, копия дешевле удержания замка
// на время чужого разбора.
func (a *Aliases) All() []AliasRec {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AliasRec, len(a.list))
	copy(out, a.list)
	return out
}

func (a *Aliases) Count() int {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.list)
}

// Sync сбрасывает буфер и просит диск записать его.
func (a *Aliases) Sync() error {
	if a == nil {
		return nil
	}
	if err := a.Flush(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	return a.f.Sync()
}

// Flush дописывает буфер на диск.
func (a *Aliases) Flush() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.w == nil {
		return nil
	}
	return a.w.Flush()
}

// Close закрывает журнал.
func (a *Aliases) Close() error {
	if a == nil {
		return nil
	}
	if err := a.Flush(); err != nil {
		return err
	}
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}
