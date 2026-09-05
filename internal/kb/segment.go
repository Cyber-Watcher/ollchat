package kb

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Инвертированный индекс: терм → список кусков, где он встречается.
//
// Сегмент неизменяем. Доливка книг создаёт новый сегмент, а не переписывает
// старый — отсюда бесплатная дозапись и возможность прервать работу в любой
// момент. Поиск идёт по всем сегментам сразу, результаты складываются.
//
// Формат:
//
//	terms.dic  термы по алфавиту, с общим префиксом от предыдущего (front-coding)
//	post.dat   списки кусков: разница номеров и частота, оба varint
//	seg.meta   сведения о сегменте в JSON
//
// Front-coding даёт заметную экономию: в словаре книги подряд идут «канал»,
// «канала», «каналы», и повторять общее начало каждый раз незачем.

const segMagic = "OLLKBS1"

// SegMeta — сведения о сегменте.
type SegMeta struct {
	Magic    string `json:"magic"`
	Analyzer string `json:"analyzer"` // версия правил разбора
	Terms    int    `json:"terms"`
	Chunks   int    `json:"chunks"`   // сколько кусков покрывает сегмент
	FirstID  int    `json:"first_id"` // номер первого куска в хранилище
	Tokens   int64  `json:"tokens"`   // сумма длин, нужна средней длине
	Postings int64  `json:"postings"`
}

// posting — вхождение терма в кусок.
type posting struct {
	chunk uint32
	tf    uint32
}

// BuildSegment строит сегмент по кускам хранилища с номерами [first, first+n).
//
// Постинги копятся в памяти: на сто книг это около 130 тысяч кусков и примерно
// 300 мегабайт — терпимо. Для библиотеки целиком индекс строится окнами, каждое
// своим сегментом, поэтому потолок памяти задаётся размером окна, а не корпуса.
func BuildSegment(dir string, store *Store, first, n int, progress func(done int) error) (*SegMeta, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	index := map[string][]posting{}
	var tokens int64
	var buf []Token

	for i := first; i < first+n && i < store.Count(); i++ {
		text, err := store.Text(i)
		if err != nil {
			return nil, fmt.Errorf("кусок %d: %w", i, err)
		}
		buf = Tokens(text, buf)
		tokens += int64(len(buf))

		// Частоты внутри куска считаем разом, чтобы не искать в срезе.
		tf := map[string]uint32{}
		for _, t := range buf {
			tf[t.Term]++
		}
		for term, count := range tf {
			index[term] = append(index[term], posting{chunk: uint32(i), tf: count})
		}
		if progress != nil && (i-first)%500 == 0 {
			// Возврат ошибки прерывает построение: пользователь нажал Esc,
			// и досчитывать сегмент до конца незачем.
			if err := progress(i - first); err != nil {
				return nil, err
			}
		}
	}

	terms := make([]string, 0, len(index))
	for t := range index {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	post, err := os.Create(filepath.Join(dir, "post.dat"))
	if err != nil {
		return nil, err
	}
	defer post.Close()
	dic, err := os.Create(filepath.Join(dir, "terms.dic"))
	if err != nil {
		return nil, err
	}
	defer dic.Close()

	pw := bufio.NewWriterSize(post, 1<<20)
	dw := bufio.NewWriterSize(dic, 1<<20)

	var (
		postOff  int64
		prev     string
		vbuf     [binary.MaxVarintLen64]byte
		postings int64
	)
	putUvarint := func(w *bufio.Writer, v uint64) error {
		n := binary.PutUvarint(vbuf[:], v)
		_, err := w.Write(vbuf[:n])
		return err
	}

	for _, term := range terms {
		list := index[term]
		sort.Slice(list, func(a, b int) bool { return list[a].chunk < list[b].chunk })

		start := postOff
		var last uint32
		for _, p := range list {
			// Номера кусков идут по возрастанию, поэтому храним разницу:
			// она укладывается в один-два байта вместо четырёх.
			if err := putUvarint(pw, uint64(p.chunk-last)); err != nil {
				return nil, err
			}
			if err := putUvarint(pw, uint64(p.tf)); err != nil {
				return nil, err
			}
			n := binary.PutUvarint(vbuf[:], uint64(p.chunk-last))
			postOff += int64(n)
			n = binary.PutUvarint(vbuf[:], uint64(p.tf))
			postOff += int64(n)
			last = p.chunk
		}
		postings += int64(len(list))

		common := commonPrefix(prev, term)
		if err := putUvarint(dw, uint64(common)); err != nil {
			return nil, err
		}
		suffix := term[common:]
		if err := putUvarint(dw, uint64(len(suffix))); err != nil {
			return nil, err
		}
		if _, err := dw.WriteString(suffix); err != nil {
			return nil, err
		}
		if err := putUvarint(dw, uint64(len(list))); err != nil {
			return nil, err
		}
		if err := putUvarint(dw, uint64(start)); err != nil {
			return nil, err
		}
		if err := putUvarint(dw, uint64(postOff-start)); err != nil {
			return nil, err
		}
		prev = term
	}
	if err := pw.Flush(); err != nil {
		return nil, err
	}
	if err := dw.Flush(); err != nil {
		return nil, err
	}
	if err := post.Sync(); err != nil {
		return nil, err
	}
	if err := dic.Sync(); err != nil {
		return nil, err
	}

	meta := &SegMeta{
		Magic: segMagic, Analyzer: AnalyzerVersion,
		Terms: len(terms), Chunks: n, FirstID: first,
		Tokens: tokens, Postings: postings,
	}
	// seg.meta пишется последним: сегмент без него считается недостроенным
	// и при открытии пропускается.
	if err := writeJSON(filepath.Join(dir, "seg.meta"), meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func commonPrefix(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	// Общий префикс не должен разрезать символ: термы бывают на кириллице.
	for i > 0 && !utf8Start(b[i]) {
		i--
	}
	return i
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// termRef — где лежат постинги терма.
type termRef struct {
	df     uint32
	off    int64
	length int64
}

// Segment — сегмент, открытый на чтение.
type Segment struct {
	dir   string
	meta  SegMeta
	terms map[string]termRef
	post  *os.File
}

// OpenSegment читает словарь сегмента в память.
//
// Словарь держится целиком: у коллекции из ста книг это около трёхсот тысяч
// термов и десяток мегабайт. Разрежённый указатель с чтением словаря с диска
// понадобится, только когда коллекции вырастут до миллионов кусков.
func OpenSegment(dir string) (*Segment, error) {
	var meta SegMeta
	if err := readJSON(filepath.Join(dir, "seg.meta"), &meta); err != nil {
		return nil, err
	}
	if meta.Magic != segMagic {
		return nil, fmt.Errorf("чужой формат сегмента: %q", meta.Magic)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "terms.dic"))
	if err != nil {
		return nil, err
	}
	terms := make(map[string]termRef, meta.Terms)
	var prev string
	for pos := 0; pos < len(raw); {
		common, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("словарь повреждён на смещении %d", pos)
		}
		pos += n
		suffixLen, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("словарь повреждён на смещении %d", pos)
		}
		pos += n
		if int(common) > len(prev) || pos+int(suffixLen) > len(raw) {
			return nil, fmt.Errorf("словарь повреждён: терм на смещении %d", pos)
		}
		term := prev[:common] + string(raw[pos:pos+int(suffixLen)])
		pos += int(suffixLen)

		df, n := binary.Uvarint(raw[pos:])
		pos += n
		off, n := binary.Uvarint(raw[pos:])
		pos += n
		length, n := binary.Uvarint(raw[pos:])
		pos += n

		terms[term] = termRef{df: uint32(df), off: int64(off), length: int64(length)}
		prev = term
	}
	post, err := os.Open(filepath.Join(dir, "post.dat"))
	if err != nil {
		return nil, err
	}
	return &Segment{dir: dir, meta: meta, terms: terms, post: post}, nil
}

func (s *Segment) Meta() SegMeta { return s.meta }
func (s *Segment) Close() error  { return s.post.Close() }

// DF — в скольких кусках сегмента встречается терм.
func (s *Segment) DF(term string) int { return int(s.terms[term].df) }

// Postings читает список кусков для терма.
func (s *Segment) Postings(term string) ([]posting, error) {
	ref, ok := s.terms[term]
	if !ok {
		return nil, nil
	}
	buf := make([]byte, ref.length)
	if _, err := s.post.ReadAt(buf, ref.off); err != nil {
		return nil, err
	}
	out := make([]posting, 0, ref.df)
	var last uint32
	for pos := 0; pos < len(buf); {
		delta, n := binary.Uvarint(buf[pos:])
		if n <= 0 {
			break
		}
		pos += n
		tf, n := binary.Uvarint(buf[pos:])
		if n <= 0 {
			break
		}
		pos += n
		last += uint32(delta)
		out = append(out, posting{chunk: last, tf: uint32(tf)})
	}
	return out, nil
}

// Terms возвращает число термов — для отчёта о состоянии коллекции.
func (s *Segment) Terms() int { return len(s.terms) }

// segmentDirs перечисляет готовые сегменты коллекции по порядку. Недостроенный
// сегмент (без seg.meta) пропускается: он остался от прерванной работы.
func segmentDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "seg-") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "seg.meta")); err != nil {
			continue
		}
		out = append(out, dir)
	}
	sort.Strings(out)
	return out, nil
}

// ensureDir создаёт каталог со всеми родителями.
func ensureDir(path string) error { return os.MkdirAll(path, 0o755) }

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
