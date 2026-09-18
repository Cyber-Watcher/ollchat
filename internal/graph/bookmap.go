package graph

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Карта книг графа и перенос графа на новую нумерацию книг.
//
// **Зачем.** Граф ссылается на книги по НОМЕРАМ (`ChunkKey.Doc` в связях,
// упоминаниях, отметках, синонимах формата 2), а номера раздаёт индексация
// коллекции. Переиндексируй библиотеку, уплотни коллекцию, перенеси её
// на другую машину другим порядком — и граф, стоивший недель карты, указывает
// не на те книги. До 18.09.2026 от этого спасали только два правила: «архив —
// коллекция целиком» и «`--kb-rebase` при переезде» (решение 6 паспорта
// опытного графа: ключ книги должен быть от содержимого, а не от порядка).
//
// **Как.** Рядом с графом лежит `books.json` — «номер книги → sha256
// содержимого и имя файла». Пишется в конце каждого захода сборки. Когда
// нумерация коллекции сменилась, `RebaseBooks` находит каждую книгу графа
// по хешу среди нынешних книг и переписывает номера во всех журналах
// (с копиями `.bak-<время>` и сухим прогоном). Записи журналов остаются
// того же размера и формата: меняется только поле номера книги.
//
// Ключ книги — хеш файла, а не текста: у одного текста в PDF и EPUB разные
// хеши, но и куски у них разные, так что склеивать их графу и не следует.
//
// **Перечитанная книга — не переехавшая.** У книги, перечитанной новым разбором
// (`--kb-reindex`), тот же файл и тот же хеш, но новый номер и ДРУГАЯ нарезка:
// перенести записи прежнего номера на новый значило бы приписать старые
// отметки и связи чужим кускам (после чистки 17.09 у прежнего номера остались
// отметки «не разбирать» — они легли бы на новые куски). Поэтому в карте
// хранится и число кусков: хеш совпал, число кусков нет — перенос не делается,
// книга считается перечитанной, а её прежние записи — предметом чистки
// (`--graph-forget-chunks … N#*`), не переноса.

const booksFile = "books.json"

// BookKey — что граф помнит о книге.
type BookKey struct {
	Hash   string `json:"hash"`
	Name   string `json:"name,omitempty"` // имя файла, ради человека
	Chunks int    `json:"chunks,omitempty"`
}

// BookMap — карта книг графа.
type BookMap struct {
	Books map[uint32]BookKey `json:"books"`
	At    int64              `json:"at"`
}

func loadBookMap(dir string) (BookMap, error) {
	m := BookMap{Books: map[uint32]BookKey{}}
	raw, err := os.ReadFile(filepath.Join(dir, booksFile))
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("%s: %w", booksFile, err)
	}
	if m.Books == nil {
		m.Books = map[uint32]BookKey{}
	}
	return m, nil
}

func saveBookMap(dir string, m BookMap) error {
	m.At = time.Now().Unix()
	raw, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, booksFile), raw, 0o644)
}

// KnownBook — книга коллекции, как её видит граф: номер и хеш содержимого.
// Книги без хеша (проиндексированы до 18.09.2026, `--kb-hash` не прошёл)
// в карту не попадают: по ним переносить нечем.
type KnownBook struct {
	ID     uint32
	Hash   string
	Name   string
	Chunks int
}

// RecordBooks дописывает в карту нынешние книги коллекции. Прежние записи
// о номерах, которых в списке нет (книга удалена из коллекции), остаются:
// граф на них ещё ссылается.
func RecordBooks(dir string, books []KnownBook) (int, error) {
	m, err := loadBookMap(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range books {
		if b.ID == 0 || b.Hash == "" {
			continue
		}
		if cur, ok := m.Books[b.ID]; ok && cur.Hash == b.Hash && cur.Name == b.Name && cur.Chunks == b.Chunks {
			continue
		}
		m.Books[b.ID] = BookKey{Hash: b.Hash, Name: b.Name, Chunks: b.Chunks}
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, saveBookMap(dir, m)
}

// RebaseStats — что сделал (или сделал бы) перенос.
type RebaseStats struct {
	Mapped    int // книг в карте графа
	Same      int // номер не изменился
	Moved     int // номер сменился: перенос
	Unknown   int // книги с таким хешем в коллекции больше нет — номер оставлен
	Reread    int // тот же файл, но другая нарезка: перечитана, не переносится
	Moves     map[uint32]uint32
	Files     map[string]int // файл → записей переписано
	Backups   []string
	Applied   bool
	Collision string // почему переносить нельзя
}

// RebaseBooks переносит граф на нумерацию books.
func RebaseBooks(dir string, books []KnownBook, dry bool) (RebaseStats, error) {
	st := RebaseStats{Moves: map[uint32]uint32{}, Files: map[string]int{}}
	m, err := loadBookMap(dir)
	if err != nil {
		return st, err
	}
	st.Mapped = len(m.Books)
	if st.Mapped == 0 {
		return st, fmt.Errorf("у графа нет карты книг (%s): она пишется в конце захода сборки — "+
			"пока нумерация не менялась, снимите её командой --graph-record-books", booksFile)
	}
	byHash := map[string]KnownBook{}
	for _, b := range books {
		if b.Hash != "" {
			byHash[b.Hash] = b
		}
	}
	for id, k := range m.Books {
		now, ok := byHash[k.Hash]
		switch {
		case !ok:
			st.Unknown++
		case now.ID == id:
			st.Same++
		case k.Chunks > 0 && now.Chunks > 0 && k.Chunks != now.Chunks:
			st.Reread++
		default:
			st.Moved++
			st.Moves[id] = now.ID
		}
	}
	if st.Moved == 0 {
		return st, nil
	}
	// Два прежних номера не могут вести в один новый; новый номер не может
	// совпасть с прежним номером книги, которая никуда не переезжает.
	target := map[uint32]uint32{}
	for from, to := range st.Moves {
		if other, dup := target[to]; dup {
			st.Collision = fmt.Sprintf("книги %d и %d обе ведут в номер %d", from, other, to)
			return st, nil
		}
		target[to] = from
		if _, stays := m.Books[to]; stays {
			if _, moves := st.Moves[to]; !moves {
				st.Collision = fmt.Sprintf("новый номер %d книги %d занят книгой, которая остаётся на месте", to, from)
				return st, nil
			}
		}
	}
	if dry {
		return st, nil
	}

	release, err := holdBuildLock(dir)
	if err != nil {
		return st, err
	}
	defer release()
	stamp := time.Now().Format("20060102-150405")
	remap := func(doc uint32) (uint32, bool) {
		to, ok := st.Moves[doc]
		return to, ok
	}

	// Двоичные журналы фиксированной длины: номер книги на известном месте.
	for _, f := range []struct {
		name string
		size int
		at   int
	}{{mentionsFile, 12, 4}, {edgesFile, 24, 16}, {progressFile, 12, 0}} {
		res, err := rewriteBinaryWith(filepath.Join(dir, f.name), f.size, stamp, false, func(b []byte) bool {
			to, ok := remap(binary.LittleEndian.Uint32(b[f.at:]))
			if !ok {
				return false
			}
			binary.LittleEndian.PutUint32(b[f.at:], to)
			return true
		}, false)
		if err != nil {
			return st, err
		}
		st.Files[f.name] = res.dropped
		if res.backup != "" {
			st.Backups = append(st.Backups, res.backup)
		}
	}
	// Синонимы формата 2: заголовок 14 байт (понятие, книга, кусок, длина) и текст.
	if n, backup, err := rewriteAliases(filepath.Join(dir, aliasesFile), stamp, remap); err != nil {
		return st, err
	} else if backup != "" {
		st.Files[aliasesFile] = n
		st.Backups = append(st.Backups, backup)
	}
	// Журналы JSON: отброшенные книги и решения о связывании.
	if n, backup, err := rewriteJSONL(filepath.Join(dir, droppedBooksFile), stamp, func(rec map[string]any) bool {
		return remapField(rec, "book", remap)
	}); err != nil {
		return st, err
	} else if backup != "" {
		st.Files[droppedBooksFile] = n
		st.Backups = append(st.Backups, backup)
	}
	if n, backup, err := rewriteJSONL(filepath.Join(dir, linksFile), stamp, func(rec map[string]any) bool {
		ch, ok := rec["chunk"].(map[string]any)
		if !ok {
			return false
		}
		return remapField(ch, "Doc", remap)
	}); err != nil {
		return st, err
	} else if backup != "" {
		st.Files[linksFile] = n
		st.Backups = append(st.Backups, backup)
	}

	// Карта — под новыми номерами.
	fresh := BookMap{Books: map[uint32]BookKey{}}
	for id, k := range m.Books {
		if to, ok := st.Moves[id]; ok {
			id = to
		}
		fresh.Books[id] = k
	}
	if err := saveBookMap(dir, fresh); err != nil {
		return st, err
	}
	st.Applied = true
	return st, nil
}

// remapField переписывает целочисленное поле записи JSON по карте номеров.
func remapField(rec map[string]any, field string, remap func(uint32) (uint32, bool)) bool {
	v, ok := rec[field].(float64)
	if !ok {
		return false
	}
	to, ok := remap(uint32(v))
	if !ok {
		return false
	}
	rec[field] = to
	return true
}

// rewriteJSONL переписывает журнал строк JSON: change правит запись и говорит,
// менялась ли она. Файла нет или менять нечего — копии не делается.
func rewriteJSONL(path, stamp string, change func(map[string]any) bool) (int, string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	var out bytes.Buffer
	changed := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil || !change(rec) {
			out.Write(line)
			out.WriteByte('\n')
			continue
		}
		b, err := json.Marshal(rec)
		if err != nil {
			return 0, "", err
		}
		out.Write(b)
		out.WriteByte('\n')
		changed++
	}
	if changed == 0 {
		return 0, "", nil
	}
	backup := path + ".bak-" + stamp
	if err := os.Rename(path, backup); err != nil {
		return 0, "", err
	}
	if err := fsx.WriteFileAtomic(path, out.Bytes(), 0o644); err != nil {
		_ = os.Rename(backup, path)
		return 0, "", err
	}
	return changed, backup, nil
}

// rewriteAliases переписывает номера книг в журнале синонимов формата 2.
func rewriteAliases(path, stamp string, remap func(uint32) (uint32, bool)) (int, string, error) {
	src, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	defer src.Close()
	var out bytes.Buffer
	r := bufio.NewReaderSize(src, 1<<20)
	head := make([]byte, aliasHeaderSize)
	changed := 0
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			break // обрыв хвоста — как при чтении
		}
		n := int(binary.LittleEndian.Uint16(head[12:]))
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			break
		}
		if to, ok := remap(binary.LittleEndian.Uint32(head[4:])); ok {
			binary.LittleEndian.PutUint32(head[4:], to)
			changed++
		}
		out.Write(head)
		out.Write(body)
	}
	if changed == 0 {
		return 0, "", nil
	}
	backup := path + ".bak-" + stamp
	if err := os.Rename(path, backup); err != nil {
		return 0, "", err
	}
	if err := fsx.WriteFileAtomic(path, out.Bytes(), 0o644); err != nil {
		_ = os.Rename(backup, path)
		return 0, "", err
	}
	return changed, backup, nil
}

// BookMapReport — карта книг графа против нынешней коллекции, для доктора:
// сколько книг графа найдено на тех же номерах, сколько переехало, сколько
// в коллекции больше нет.
func BookMapReport(dir string, books []KnownBook) (mapped, same, moved, unknown int, err error) {
	st, err := RebaseBooks(dir, books, true)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return st.Mapped, st.Same, st.Moved, st.Unknown, nil
}

// SortedMoves — переносы в устойчивом порядке, для печати.
func (st RebaseStats) SortedMoves() [][2]uint32 {
	out := make([][2]uint32, 0, len(st.Moves))
	for from, to := range st.Moves {
		out = append(out, [2]uint32{from, to})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
