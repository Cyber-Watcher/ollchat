package kb

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Хранилище кусков.
//
// Тексты кусков лежат в самой базе, а не берутся из книги при выдаче. Причин
// три: перечитывать 30-мегабайтный PDF ради одной цитаты дорого (секунда против
// полумиллисекунды), книгу могли переместить или удалить, а разбор PDF —
// операция, которая на повреждённом файле может и не завершиться.
//
// Формат — два файла и только дозапись:
//
//	chunks.dat  блоки по 64 куска, каждый сжат compress/flate (примерно втрое)
//	chunks.idx  записи по 32 байта: где кусок лежит и к какой странице относится
//
// Дозапись без переписывания — главное свойство: доливка книг в коллекцию
// стоит ровно столько, сколько новых книг, а прерывание отбрасывает одну книгу.

const (
	chunksPerBlock = 64
	chunkRecSize   = 32
	storeMagic     = "OLLKBC1"
)

// ChunkRec — запись указателя на кусок. Ровно 32 байта, порядок байтов малый.
type ChunkRec struct {
	Doc      uint32 // номер книги в коллекции
	Ord      uint32 // порядковый номер куска внутри книги
	UnitFrom uint16 // страница или раздел, где кусок начинается
	UnitTo   uint16 // где заканчивается
	Flags    uint16 // ChunkFlags
	Tokens   uint16 // длина в термах — нужна BM25, поэтому лежит рядом
	BlockOff uint64 // смещение блока в chunks.dat
	InBlock  uint32 // смещение внутри разжатого блока
	Length   uint32 // длина текста в байтах
}

func (r ChunkRec) encode(b []byte) {
	binary.LittleEndian.PutUint32(b[0:], r.Doc)
	binary.LittleEndian.PutUint32(b[4:], r.Ord)
	binary.LittleEndian.PutUint16(b[8:], r.UnitFrom)
	binary.LittleEndian.PutUint16(b[10:], r.UnitTo)
	binary.LittleEndian.PutUint16(b[12:], r.Flags)
	binary.LittleEndian.PutUint16(b[14:], r.Tokens)
	binary.LittleEndian.PutUint64(b[16:], r.BlockOff)
	binary.LittleEndian.PutUint32(b[24:], r.InBlock)
	binary.LittleEndian.PutUint32(b[28:], r.Length)
}

func decodeRec(b []byte) ChunkRec {
	return ChunkRec{
		Doc:      binary.LittleEndian.Uint32(b[0:]),
		Ord:      binary.LittleEndian.Uint32(b[4:]),
		UnitFrom: binary.LittleEndian.Uint16(b[8:]),
		UnitTo:   binary.LittleEndian.Uint16(b[10:]),
		Flags:    binary.LittleEndian.Uint16(b[12:]),
		Tokens:   binary.LittleEndian.Uint16(b[14:]),
		BlockOff: binary.LittleEndian.Uint64(b[16:]),
		InBlock:  binary.LittleEndian.Uint32(b[24:]),
		Length:   binary.LittleEndian.Uint32(b[28:]),
	}
}

// StoreState — длины обоих файлов. По ним восстанавливается состояние после
// прерывания: файлы обрезаются до последней записи журнала.
type StoreState struct {
	Dat   int64 `json:"dat"`
	Idx   int64 `json:"idx"`
	Count int   `json:"count"`
}

// Writer дописывает куски в хранилище.
type Writer struct {
	dir   string
	dat   *os.File
	idx   *os.File
	count int

	// Незаполненный блок копится в памяти и уходит на диск целиком: сжимать
	// по одному куску невыгодно, словарь flate не успевает разогреться.
	pend     []Chunk
	pendDocs []uint32
	pendOrds []uint32
	buf      []byte
}

// CreateWriter открывает хранилище на дозапись, создавая его при надобности.
func CreateWriter(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dat, err := os.OpenFile(filepath.Join(dir, "chunks.dat"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	idx, err := os.OpenFile(filepath.Join(dir, "chunks.idx"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		dat.Close()
		return nil, err
	}
	st, err := idx.Stat()
	if err != nil {
		dat.Close()
		idx.Close()
		return nil, err
	}
	if st.Size()%chunkRecSize != 0 {
		dat.Close()
		idx.Close()
		return nil, fmt.Errorf("chunks.idx повреждён: %d байт не делится на %d", st.Size(), chunkRecSize)
	}
	// Заголовок пишется один раз, при создании пустого файла.
	if datStat, err := dat.Stat(); err == nil && datStat.Size() == 0 {
		if _, err := dat.WriteString(storeMagic + "\n"); err != nil {
			dat.Close()
			idx.Close()
			return nil, err
		}
	}
	return &Writer{dir: dir, dat: dat, idx: idx, count: int(st.Size() / chunkRecSize)}, nil
}

// Append дописывает куски одной книги. Куски одной книги идут подряд, поэтому
// соседние страницы попадают в один блок и читаются одним разжатием.
func (w *Writer) Append(doc uint32, chunks []Chunk) error {
	for i, c := range chunks {
		w.pend = append(w.pend, c)
		w.pendDocs = append(w.pendDocs, doc)
		w.pendOrds = append(w.pendOrds, uint32(i))
		if len(w.pend) >= chunksPerBlock {
			if err := w.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

// flush сжимает накопленный блок и дописывает его вместе с указателями.
func (w *Writer) flush() error {
	if len(w.pend) == 0 {
		return nil
	}
	off, err := w.dat.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	// Тексты кусков склеиваются, смещения внутри блока запоминаются.
	var raw bytes.Buffer
	offsets := make([]uint32, len(w.pend))
	lengths := make([]uint32, len(w.pend))
	for i, c := range w.pend {
		offsets[i] = uint32(raw.Len())
		lengths[i] = uint32(len(c.Text))
		raw.WriteString(c.Text)
	}

	var packed bytes.Buffer
	zw, err := flate.NewWriter(&packed, flate.DefaultCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:], uint32(raw.Len()))
	binary.LittleEndian.PutUint32(head[4:], uint32(packed.Len()))
	if _, err := w.dat.Write(head[:]); err != nil {
		return err
	}
	if _, err := w.dat.Write(packed.Bytes()); err != nil {
		return err
	}

	if cap(w.buf) < len(w.pend)*chunkRecSize {
		w.buf = make([]byte, len(w.pend)*chunkRecSize)
	}
	buf := w.buf[:len(w.pend)*chunkRecSize]
	for i, c := range w.pend {
		rec := ChunkRec{
			Doc:      w.pendDocs[i],
			Ord:      w.pendOrds[i],
			UnitFrom: clampUint16(c.UnitFrom),
			UnitTo:   clampUint16(c.UnitTo),
			Flags:    uint16(c.Flags),
			Tokens:   clampUint16(countTokens(c.Text)),
			BlockOff: uint64(off),
			InBlock:  offsets[i],
			Length:   lengths[i],
		}
		rec.encode(buf[i*chunkRecSize:])
	}
	if _, err := w.idx.Write(buf); err != nil {
		return err
	}
	w.count += len(w.pend)
	w.pend = w.pend[:0]
	w.pendDocs = w.pendDocs[:0]
	w.pendOrds = w.pendOrds[:0]
	return nil
}

// Commit сбрасывает всё на диск и возвращает состояние для журнала.
func (w *Writer) Commit() (StoreState, error) {
	if err := w.flush(); err != nil {
		return StoreState{}, err
	}
	if err := w.dat.Sync(); err != nil {
		return StoreState{}, err
	}
	if err := w.idx.Sync(); err != nil {
		return StoreState{}, err
	}
	datSt, err := w.dat.Stat()
	if err != nil {
		return StoreState{}, err
	}
	idxSt, err := w.idx.Stat()
	if err != nil {
		return StoreState{}, err
	}
	return StoreState{Dat: datSt.Size(), Idx: idxSt.Size(), Count: w.count}, nil
}

// Rollback обрезает файлы до состояния последнего коммита: так отбрасывается
// книга, на которой работу прервали.
func (w *Writer) Rollback(st StoreState) error {
	w.pend = w.pend[:0]
	w.pendDocs = w.pendDocs[:0]
	w.pendOrds = w.pendOrds[:0]
	if err := w.dat.Truncate(st.Dat); err != nil {
		return err
	}
	if err := w.idx.Truncate(st.Idx); err != nil {
		return err
	}
	if _, err := w.dat.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	w.count = st.Count
	return nil
}

// Count — сколько кусков записано (включая ещё не сброшенные).
func (w *Writer) Count() int { return w.count + len(w.pend) }

func (w *Writer) Close() error {
	err := w.flush()
	if cerr := w.dat.Close(); err == nil {
		err = cerr
	}
	if cerr := w.idx.Close(); err == nil {
		err = cerr
	}
	return err
}

// Store — хранилище, открытое на чтение.
type Store struct {
	dir  string
	recs []ChunkRec
	dat  *os.File

	mu     sync.Mutex
	cached uint64 // смещение блока в кэше
	block  []byte
	filled bool
}

// OpenStore открывает хранилище на чтение. Указатели читаются в память целиком:
// на сто книг это около четырёх мегабайт, зато поиск не ходит на диск.
func OpenStore(dir string) (*Store, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "chunks.idx"))
	if err != nil {
		return nil, err
	}
	if len(raw)%chunkRecSize != 0 {
		return nil, fmt.Errorf("chunks.idx повреждён: %d байт", len(raw))
	}
	recs := make([]ChunkRec, len(raw)/chunkRecSize)
	for i := range recs {
		recs[i] = decodeRec(raw[i*chunkRecSize:])
	}
	dat, err := os.Open(filepath.Join(dir, "chunks.dat"))
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir, recs: recs, dat: dat}, nil
}

func (s *Store) Count() int         { return len(s.recs) }
func (s *Store) Rec(i int) ChunkRec { return s.recs[i] }
func (s *Store) Recs() []ChunkRec   { return s.recs }
func (s *Store) Close() error       { return s.dat.Close() }

// Text достаёт текст куска.
func (s *Store) Text(i int) (string, error) {
	if i < 0 || i >= len(s.recs) {
		return "", fmt.Errorf("кусок %d вне границ (всего %d)", i, len(s.recs))
	}
	rec := s.recs[i]
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.filled || s.cached != rec.BlockOff {
		block, err := s.readBlock(rec.BlockOff)
		if err != nil {
			return "", err
		}
		s.block, s.cached, s.filled = block, rec.BlockOff, true
	}
	end := int(rec.InBlock) + int(rec.Length)
	if end > len(s.block) {
		return "", fmt.Errorf("кусок %d выходит за границы блока", i)
	}
	return string(s.block[rec.InBlock:end]), nil
}

// Texts достаёт несколько кусков, читая каждый блок один раз.
func (s *Store) Texts(ids []int) (map[int]string, error) {
	sorted := append([]int(nil), ids...)
	sort.Slice(sorted, func(a, b int) bool {
		return s.recs[sorted[a]].BlockOff < s.recs[sorted[b]].BlockOff
	})
	out := make(map[int]string, len(ids))
	for _, id := range sorted {
		text, err := s.Text(id)
		if err != nil {
			return out, err
		}
		out[id] = text
	}
	return out, nil
}

func (s *Store) readBlock(off uint64) ([]byte, error) {
	var head [8]byte
	if _, err := s.dat.ReadAt(head[:], int64(off)); err != nil {
		return nil, err
	}
	rawLen := binary.LittleEndian.Uint32(head[0:])
	compLen := binary.LittleEndian.Uint32(head[4:])
	if rawLen > 64<<20 || compLen > 64<<20 {
		return nil, fmt.Errorf("неправдоподобный размер блока: %d/%d", rawLen, compLen)
	}
	packed := make([]byte, compLen)
	if _, err := s.dat.ReadAt(packed, int64(off)+8); err != nil {
		return nil, err
	}
	zr := flate.NewReader(bytes.NewReader(packed))
	defer zr.Close()
	out := make([]byte, 0, rawLen)
	buf := bytes.NewBuffer(out)
	if _, err := io.Copy(buf, io.LimitReader(zr, int64(rawLen))); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clampUint16(v int) uint16 {
	if v < 0 {
		return 0
	}
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

// countTokens считает длину куска в термах: она нужна ранжированию, а второй
// раз разбирать текст при поиске накладно.
func countTokens(text string) int {
	return len(Tokens(text, nil))
}
