package graph

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// ErrBooksMoved — нумерация книг коллекции сменилась после того, как граф
// записал свою карту книг: граф ещё ссылается на прежние номера, и сперва его
// надо перенести (--graph-rebase-books).
var ErrBooksMoved = errors.New("нумерация книг коллекции сменилась")

// RecordBooks дописывает в карту нынешние книги коллекции. Прежние записи
// о номерах, которых в списке нет (книга удалена из коллекции), остаются:
// граф на них ещё ссылается.
//
// **Сменившуюся нумерацию карта не принимает.** Карту пишет конец каждого
// захода сборки. Переиндексировали коллекцию, а сборку запустили раньше
// --graph-rebase-books, — и до 07.10.2026 карта молча переписывалась новыми
// номерами: прежний номер книги, на который ссылаются журналы графа,
// забывался, и перенести граф становилось нечем (аудит, 4.5). Теперь
// в таком случае карта не трогается вовсе, а ошибка говорит, что делать.
func RecordBooks(dir string, books []KnownBook) (int, error) {
	m, err := loadBookMap(dir)
	if err != nil {
		return 0, err
	}
	if len(m.Books) > 0 {
		if st := planRebase(m, books); st.Moved > 0 {
			mv := st.SortedMoves()[0]
			return 0, fmt.Errorf("%w после прошлой записи карты (переехало книг: %d, например %d → %d): "+
				"карта книг графа не перезаписана, иначе граф забыл бы прежние номера; "+
				"перенесите граф: ollchat --graph-rebase-books <коллекция>", ErrBooksMoved, st.Moved, mv[0], mv[1])
		}
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
	// Resumed — доведён перенос, прерванный прежде посреди подмены журналов
	// (см. rebasePlanFile): Moves — из его плана.
	Resumed bool
}

// planRebase сопоставляет карту книг графа с нынешними книгами коллекции:
// какие номера на месте, какие переехали, каких книг больше нет. Ничего
// не пишет.
func planRebase(m BookMap, books []KnownBook) RebaseStats {
	st := RebaseStats{Moves: map[uint32]uint32{}, Files: map[string]int{}, Mapped: len(m.Books)}
	byHash := map[string]KnownBook{}
	byID := map[uint32]KnownBook{}
	for _, b := range books {
		if b.Hash != "" {
			byHash[b.Hash] = b
			byID[b.ID] = b
		}
	}
	for id, k := range m.Books {
		// Книга на своём номере — на месте, даже если такой же файл лежит
		// в коллекции ещё раз под другим номером: копия одного файла в двух
		// каталогах иначе выглядела бы переездом.
		if cur, ok := byID[id]; ok && cur.Hash == k.Hash {
			st.Same++
			continue
		}
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
	// Два прежних номера не могут вести в один новый; новый номер не может
	// совпасть с прежним номером книги, которая никуда не переезжает.
	target := map[uint32]uint32{}
	for _, mv := range st.SortedMoves() {
		from, to := mv[0], mv[1]
		if other, dup := target[to]; dup {
			st.Collision = fmt.Sprintf("книги %d и %d обе ведут в номер %d", other, from, to)
			return st
		}
		target[to] = from
		if _, stays := m.Books[to]; stays {
			if _, moves := st.Moves[to]; !moves {
				st.Collision = fmt.Sprintf("новый номер %d книги %d занят книгой, которая остаётся на месте", to, from)
				return st
			}
		}
	}
	return st
}

// RebaseBooks переносит граф на нумерацию books.
func RebaseBooks(dir string, books []KnownBook, dry bool) (RebaseStats, error) {
	m, err := loadBookMap(dir)
	if err != nil {
		return RebaseStats{Moves: map[uint32]uint32{}, Files: map[string]int{}}, err
	}
	if len(m.Books) == 0 {
		return RebaseStats{Moves: map[uint32]uint32{}, Files: map[string]int{}},
			fmt.Errorf("у графа нет карты книг (%s): она пишется в конце захода сборки — "+
				"пока нумерация не менялась, снимите её командой --graph-record-books", booksFile)
	}
	st := planRebase(m, books)
	// Прерванный перенос доводится, даже если по карте переносить уже нечего:
	// его план лежит рядом, пока перенос не доведён (rebasePlanFile).
	if dry || (!rebasePending(dir) && (st.Moved == 0 || st.Collision != "")) {
		return st, nil
	}

	release, err := holdBuildLock(dir)
	if err != nil {
		return st, err
	}
	defer release()

	plan, err := loadRebasePlan(dir)
	if err != nil {
		return st, err
	}
	if plan != nil {
		st.Resumed = true
		st.Moves, st.Moved, st.Collision = plan.Moves, len(plan.Moves), ""
	} else {
		if st.Moved == 0 || st.Collision != "" {
			return st, nil
		}
		if plan, err = prepareRebase(dir, time.Now().Format("20060102-150405"), st.Moves, m); err != nil {
			return st, err
		}
	}
	if err := finishRebase(dir, plan, &st); err != nil {
		return st, err
	}
	st.Applied = true
	return st, nil
}

// План переноса номеров книг.
//
// **Зачем.** Перенос переписывает до шести журналов по одному. До 07.10.2026
// обрыв посреди него оставлял часть журналов под новыми номерами, а карту
// книг — под прежними, и повтор команды переносил уже перенесённое ещё раз:
// для сдвига на свободные номера это безвредно, а при обмене номерами
// (перестановке) записи возвращались к прежнему номеру — граф приписывал одну
// книгу другой (аудит, 4.5).
//
// **Как.** Сперва все журналы переписываются в заготовки рядом с собой, затем
// на диск ложится план: какие переносы и какие журналы ждут подмены. Подмена
// журнала — одно переименование заготовки на его место, и заготовка при этом
// исчезает: её наличие и есть признак «ещё не подменён». Карта книг пишется
// последней, план убирается после неё. Повтор команды после обрыва находит
// план и доводит подмену по нему, не трогая уже подменённого; сборка, пока
// план лежит, не идёт (Build) — дописанное ею под новыми номерами в ещё не
// подменённый журнал сделало бы перенос недоводимым.
const rebasePlanFile = "books.rebase.json"

type rebasePlan struct {
	Stamp string            `json:"stamp"`
	Moves map[uint32]uint32 `json:"moves"`
	Books BookMap           `json:"books"` // карта книг под новыми номерами
	Files []rebaseFile      `json:"files"` // журналы с заготовками
}

type rebaseFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`    // сколько байт было в журнале при подготовке
	Changed int    `json:"changed"` // записей переписано
}

// rebasePending — лежит ли план недоведённого переноса номеров книг.
func rebasePending(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, rebasePlanFile))
	return err == nil
}

// errRebasePending — сборка по графу с недоведённым переносом не идёт.
func errRebasePending(dir string) error {
	return fmt.Errorf("перенос номеров книг прерван посреди подмены журналов (план: %s) — "+
		"часть журналов уже под новыми номерами; сперва доведите его: ollchat --graph-rebase-books <коллекция>",
		filepath.Join(dir, rebasePlanFile))
}

func loadRebasePlan(dir string) (*rebasePlan, error) {
	raw, err := os.ReadFile(filepath.Join(dir, rebasePlanFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p rebasePlan
	if err := json.Unmarshal(raw, &p); err != nil || p.Stamp == "" || p.Books.Books == nil {
		return nil, fmt.Errorf("план прерванного переноса номеров книг %s не читается (%v): "+
			"журналы и их копии .bak-… придётся разобрать руками", rebasePlanFile, err)
	}
	return &p, nil
}

// rebaseTmp — имя заготовки журнала для переноса с отметкой stamp.
func rebaseTmp(path, stamp string) string { return path + ".rebase-" + stamp }

// prepareRebase переписывает журналы в заготовки и кладёт на диск план.
// Ничего из живых журналов при этом не меняется.
func prepareRebase(dir, stamp string, moves map[uint32]uint32, m BookMap) (*rebasePlan, error) {
	// Заготовки без плана — от подготовки, оборванной до его записи: ничьи.
	removeRebaseLeftovers(dir)
	remap := func(doc uint32) (uint32, bool) {
		to, ok := moves[doc]
		return to, ok
	}
	plan := &rebasePlan{Stamp: stamp, Moves: moves, Books: BookMap{Books: map[uint32]BookKey{}}}
	for id, k := range m.Books {
		if to, ok := moves[id]; ok {
			id = to
		}
		plan.Books.Books[id] = k
	}
	fail := func(err error) (*rebasePlan, error) {
		removeRebaseLeftovers(dir)
		return nil, err
	}
	add := func(name string, size int64, changed int) {
		if changed > 0 {
			plan.Files = append(plan.Files, rebaseFile{Name: name, Size: size, Changed: changed})
		}
	}

	// Двоичные журналы фиксированной длины: номер книги на известном месте.
	for _, f := range []struct {
		name     string
		size, at int
	}{{mentionsFile, mentionSize, 4}, {edgesFile, edgeSize, 16}, {progressFile, progressSize, 0}} {
		path := filepath.Join(dir, f.name)
		res, size, err := writeBinaryRewrite(path, rebaseTmp(path, stamp), f.size, func(b []byte) bool {
			to, ok := remap(binary.LittleEndian.Uint32(b[f.at:]))
			if !ok {
				return false
			}
			binary.LittleEndian.PutUint32(b[f.at:], to)
			return true
		}, false)
		if err != nil {
			return fail(err)
		}
		add(f.name, size, res.dropped)
	}

	// Синонимы формата 2 и журналы JSON (отброшенные книги, решения
	// о связывании) переписываются в памяти — они невелики.
	type rewrite struct {
		name string
		run  func(raw []byte) ([]byte, int, error)
	}
	for _, f := range []rewrite{
		{aliasesFile, func(raw []byte) ([]byte, int, error) { return remapAliases(raw, remap) }},
		{droppedBooksFile, func(raw []byte) ([]byte, int, error) {
			return remapJSONL(raw, func(rec map[string]any) bool { return remapField(rec, "book", remap) })
		}},
		{linksFile, func(raw []byte) ([]byte, int, error) {
			return remapJSONL(raw, func(rec map[string]any) bool {
				ch, ok := rec["chunk"].(map[string]any)
				if !ok {
					return false
				}
				return remapField(ch, "Doc", remap)
			})
		}},
	} {
		path := filepath.Join(dir, f.name)
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		out, changed, err := f.run(raw)
		if err != nil {
			return fail(err)
		}
		if changed == 0 {
			continue
		}
		if err := writeSynced(rebaseTmp(path, stamp), out); err != nil {
			return fail(err)
		}
		add(f.name, int64(len(raw)), changed)
	}

	raw, err := json.MarshalIndent(plan, "", " ")
	if err != nil {
		return fail(err)
	}
	syncDir(dir)
	if err := fsx.WriteFileAtomic(filepath.Join(dir, rebasePlanFile), raw, 0o644); err != nil {
		return fail(err)
	}
	syncDir(dir)
	return plan, nil
}

// finishRebase подменяет журналы заготовками по плану, пишет карту книг
// под новыми номерами и убирает план. Повторный вызов после обрыва
// доводит то, что не успело.
func finishRebase(dir string, plan *rebasePlan, st *RebaseStats) error {
	for _, f := range plan.Files {
		path := filepath.Join(dir, f.Name)
		tmp := rebaseTmp(path, plan.Stamp)
		if _, err := os.Stat(tmp); os.IsNotExist(err) {
			continue // подменён до обрыва
		}
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		// Журнал менялся после подготовки (его переписала чистка) — заготовка
		// устарела, и подмена ею потеряла бы те правки.
		if fi.Size() != f.Size {
			return fmt.Errorf("перенос номеров книг не доведён: %s изменился после подготовки переноса "+
				"(было %d байт, стало %d), и заготовка %s устарела; уже подменённые журналы лежат "+
				"рядом с копиями .bak-%s, план — %s", f.Name, f.Size, fi.Size(),
				filepath.Base(tmp), plan.Stamp, rebasePlanFile)
		}
		backup, err := swapIn(path, tmp, path+".bak-"+plan.Stamp)
		if err != nil {
			return err
		}
		st.Files[f.Name] = f.Changed
		st.Backups = append(st.Backups, backup)
	}
	syncDir(dir)
	if err := saveBookMap(dir, plan.Books); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, rebasePlanFile)); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// removeRebaseLeftovers убирает заготовки переноса, у которых нет плана.
func removeRebaseLeftovers(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), ".rebase-") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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

// remapJSONL переписывает журнал строк JSON: change правит запись и говорит,
// менялась ли она. Возвращает новое содержимое и число изменённых записей.
func remapJSONL(raw []byte, change func(map[string]any) bool) ([]byte, int, error) {
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
			return nil, 0, err
		}
		out.Write(b)
		out.WriteByte('\n')
		changed++
	}
	return out.Bytes(), changed, nil
}

// remapAliases переписывает номера книг в журнале синонимов формата 2.
// Оборванный хвост отбрасывается, как при чтении.
func remapAliases(raw []byte, remap func(uint32) (uint32, bool)) ([]byte, int, error) {
	var out bytes.Buffer
	changed := 0
	for len(raw) >= aliasHeaderSize {
		head := append([]byte(nil), raw[:aliasHeaderSize]...)
		n := int(binary.LittleEndian.Uint16(head[12:]))
		if len(raw) < aliasHeaderSize+n {
			break
		}
		if to, ok := remap(binary.LittleEndian.Uint32(head[4:])); ok {
			binary.LittleEndian.PutUint32(head[4:], to)
			changed++
		}
		out.Write(head)
		out.Write(raw[aliasHeaderSize : aliasHeaderSize+n])
		raw = raw[aliasHeaderSize+n:]
	}
	return out.Bytes(), changed, nil
}

// BookMapReport — карта книг графа против нынешней коллекции, для доктора:
// сколько книг графа найдено на тех же номерах, сколько переехало, сколько
// в коллекции больше нет.
func BookMapReport(dir string, books []KnownBook) (mapped, same, moved, unknown, reread int, err error) {
	st, err := RebaseBooks(dir, books, true)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	// Reread возвращается с 27.09.2026 (этап 110, А0.3): без него строка
	// доктора не сходилась — 523 на своих номерах + 0 переехавших + 7
	// исчезнувших давали 530 при 532 книгах в карте, и два перечитанных
	// файла пропадали неизвестно куда.
	return st.Mapped, st.Same, st.Moved, st.Unknown, st.Reread, nil
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
