package graph

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Векторы троек — вход в граф по связи, а не по понятию (этап 105, Б8).
//
// **Зачем.** Вектор понятия отвечает на «что такое X», а вопрос «как X влияет
// на Y» — о связи: у него свой смысл, и ближайшим к нему понятием часто
// оказывается общее слово («влияние»). Тройка «X —тип→ Y» с синонимами обоих
// концов — отдельный текст, и близость к нему ищет саму связь. Книги
// (переписи 18.09.2026, А6) этот вход называют самым дешёвым способом
// отвечать на «как связаны»: связей с двумя и более источниками у нас
// в четыре раза меньше, чем понятий, а текст тройки короток.
//
// **Что здесь и чего нет.** Индекс лежит рядом с графом (`edges.vec`,
// `edges.vecmeta`) и не трогает ни реестр, ни журнал связей: убрать оба
// файла — граф прежний. Считаются только связи с числом источников не меньше
// порога (Rules.TripleMinOrigins, умолчание 2): одиночные подтверждения —
// 83% связей и в основном шум разбора (этап 104, П1). Вход по тройкам
// включается настройкой (Rules.TripleLimit), по умолчанию выключен —
// без выигрыша на замере (этап 105, В1) индекс не включается.
//
// **Формат.** Запись фиксированной длины: ключ тройки (src, dst, тип),
// отметка текста (FNV-1a, как у векторов понятий) и вектор int8. Паспорт —
// точка фиксации, как у векторов понятий: данные дописываются первыми,
// паспорт вторым, лишний хвост читается как небывший. Обновлённая тройка
// (склеили конец — изменился текст) дописывается новой записью и перекрывает
// прежнюю по ключу; перекрытые записи убираются перепаковкой при следующем
// счёте, когда их набирается больше четверти.

const edgeVecMagic = "OLLGEV1"

const (
	edgeVecMetaFile = "edges.vecmeta"
	edgeVecDataFile = "edges.vec"
	// edgeVecHead — длина заголовка записи: src, dst (по 4 байта), тип (1),
	// три байта выравнивания и отметка текста (4).
	edgeVecHead = 16
)

// edgeVecMeta — паспорт индекса троек.
type edgeVecMeta struct {
	Magic string `json:"magic"`
	Model string `json:"model"`
	Dim   int    `json:"dim"`
	Count int    `json:"count"` // записей в файле, с перекрытыми
	CRC   uint32 `json:"crc,omitempty"`
	// Digest — отпечаток весов эмбеддера, по той же причине, что у entVecMeta.
	Digest string `json:"digest,omitempty"`
	// MinOrigins — порог источников, с которым отбирались тройки: другой порог
	// означает другой состав индекса, и досчёт начинается заново.
	MinOrigins int `json:"min_origins"`
}

// EdgeKey — ключ тройки: выжившие концы и вид связи.
type EdgeKey struct {
	Src, Dst uint32
	Type     uint8
}

// EdgeVectors — индекс векторов троек.
type EdgeVectors struct {
	mu    sync.RWMutex
	dir   string
	meta  edgeVecMeta
	keys  []EdgeKey
	stamp []uint32
	data  []int8
	// index — ключ → номер последней записи с ним.
	index map[EdgeKey]int
	// problem — почему файлы есть, а индекс не принят; печатает доктор.
	problem string
}

func (v *EdgeVectors) rowSize() int { return edgeVecHead + v.meta.Dim }

// openEdgeVectors читает индекс троек, если он посчитан. Отсутствие файлов —
// обычное состояние графа.
func openEdgeVectors(dir string) *EdgeVectors {
	v := &EdgeVectors{dir: dir, index: map[EdgeKey]int{}}
	raw, err := os.ReadFile(filepath.Join(dir, edgeVecMetaFile))
	if err != nil {
		return v
	}
	if json.Unmarshal(raw, &v.meta) != nil || v.meta.Magic != edgeVecMagic || v.meta.Dim <= 0 {
		v.meta = edgeVecMeta{}
		return v
	}
	buf, err := os.ReadFile(filepath.Join(dir, edgeVecDataFile))
	want := v.meta.Count * v.rowSize()
	if err == nil && len(buf) > want {
		buf = buf[:want]
	}
	if err != nil || len(buf) != want {
		if err == nil {
			v.problem = fmt.Sprintf("размер %s (%d байт) не совпадает с паспортом (%d троек × %d); "+
				"пересчитать: --graph-embed-edges", edgeVecDataFile, len(buf), v.meta.Count, v.rowSize())
		}
		v.meta = edgeVecMeta{}
		return v
	}
	if v.meta.CRC != 0 && crc32.ChecksumIEEE(buf) != v.meta.CRC {
		v.problem = "контрольная сумма " + edgeVecDataFile + " не сходится с паспортом; пересчитать: --graph-embed-edges"
		v.meta = edgeVecMeta{}
		return v
	}
	v.load(buf)
	return v
}

// load раскладывает файл данных по записям.
func (v *EdgeVectors) load(buf []byte) {
	n := len(buf) / v.rowSize()
	v.keys = make([]EdgeKey, n)
	v.stamp = make([]uint32, n)
	v.data = make([]int8, n*v.meta.Dim)
	v.index = make(map[EdgeKey]int, n)
	for i := 0; i < n; i++ {
		row := buf[i*v.rowSize():]
		k := EdgeKey{
			Src:  binary.LittleEndian.Uint32(row[0:]),
			Dst:  binary.LittleEndian.Uint32(row[4:]),
			Type: row[8],
		}
		v.keys[i] = k
		v.stamp[i] = binary.LittleEndian.Uint32(row[12:])
		vec := v.data[i*v.meta.Dim : (i+1)*v.meta.Dim]
		for j := range vec {
			vec[j] = int8(row[edgeVecHead+j])
		}
		v.index[k] = i
	}
}

// Ready — индекс есть и годен.
func (v *EdgeVectors) Ready() bool {
	if v == nil {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.meta.Dim > 0 && len(v.index) > 0
}

func (v *EdgeVectors) Model() string { v.mu.RLock(); defer v.mu.RUnlock(); return v.meta.Model }
func (v *EdgeVectors) Dim() int      { v.mu.RLock(); defer v.mu.RUnlock(); return v.meta.Dim }

// Count — сколько троек в индексе (без перекрытых записей).
func (v *EdgeVectors) Count() int { v.mu.RLock(); defer v.mu.RUnlock(); return len(v.index) }

// Problem объясняет, почему индекс с диска не принят; пусто — всё в порядке
// или файлов просто нет.
func (v *EdgeVectors) Problem() string {
	if v == nil {
		return ""
	}
	return v.problem
}

// edgeHit — тройка, близкая к вектору вопроса.
type edgeHit struct {
	Key   EdgeKey
	Score float64
}

// nearest — k ближайших троек к вектору. Перебор линейный, как у векторов
// понятий: индекс вчетверо меньше реестра, и цена — десятки миллисекунд.
func (v *EdgeVectors) nearest(query []int8, k int) []edgeHit {
	if v == nil || !v.Ready() || len(query) != v.Dim() || k <= 0 {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	all := make([]edgeHit, 0, len(v.index))
	for key, i := range v.index {
		vec := v.data[i*v.meta.Dim : (i+1)*v.meta.Dim]
		all = append(all, edgeHit{Key: key, Score: kb.Cosine(query, vec)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return keyLess(all[i].Key, all[j].Key)
	})
	if len(all) > k {
		all = all[:k]
	}
	return all
}

// linkBySense — относительный отбор, тот же, что у понятий (vectors.go):
// верхушка вчетверо шире, остаются тройки заметно выше её середины.
func (v *EdgeVectors) linkBySense(query []int8, limit int, margin float64) []edgeHit {
	if limit <= 0 {
		return nil
	}
	all := v.nearest(query, limit*4)
	if len(all) == 0 {
		return nil
	}
	// Верхушка короче четырёх — середина не о чем не говорит (крошечный
	// индекс в тестах и у только что заведённого графа): берём как есть.
	if len(all) < 4 {
		return all[:min(limit, len(all))]
	}
	median := all[len(all)/2].Score
	out := make([]edgeHit, 0, limit)
	for _, h := range all {
		if len(out) >= limit || h.Score < median+margin {
			break
		}
		out = append(out, h)
	}
	return out
}

func keyLess(a, b EdgeKey) bool {
	if a.Src != b.Src {
		return a.Src < b.Src
	}
	if a.Dst != b.Dst {
		return a.Dst < b.Dst
	}
	return a.Type < b.Type
}

// encodeRow собирает запись индекса.
func encodeRow(k EdgeKey, stamp uint32, vec []int8) []byte {
	row := make([]byte, edgeVecHead+len(vec))
	binary.LittleEndian.PutUint32(row[0:], k.Src)
	binary.LittleEndian.PutUint32(row[4:], k.Dst)
	row[8] = k.Type
	binary.LittleEndian.PutUint32(row[12:], stamp)
	for j, x := range vec {
		row[edgeVecHead+j] = byte(x)
	}
	return row
}

// tripleText — текст тройки для эмбеддера: оба конца с синонимами (как
// у вектора понятия) и вид связи между ними словом.
func tripleText(src string, dst string, rel uint8) string {
	return src + " —" + RelName(rel) + "→ " + dst
}

// Triple — тройка, отобранная в индекс: ключ, текст и число источников.
type Triple struct {
	Key     EdgeKey
	Text    string
	Origins int
}

// Triples отбирает связи для индекса: концы переведены к выжившим, петли
// отброшены, источники (соседние куски одной книги — один) посчитаны,
// остаются тройки с числом источников не меньше minOrigins. Порядок
// устойчив: по ключу.
func (g *Graph) Triples(minOrigins int) []Triple {
	if minOrigins <= 0 {
		minOrigins = DefaultTripleMinOrigins
	}
	type acc struct {
		byDoc map[uint32][]uint32
	}
	sum := map[EdgeKey]*acc{}
	for _, ent := range g.ents.Live() {
		for _, ed := range g.edge.Of(ent.ID) {
			if g.dropped.Dropped(ed.Evidence.Doc) {
				continue
			}
			k := EdgeKey{Src: ed.Src, Dst: ed.Dst, Type: ed.Type}
			a := sum[k]
			if a == nil {
				a = &acc{byDoc: map[uint32][]uint32{}}
				sum[k] = a
			}
			a.byDoc[ed.Evidence.Doc] = append(a.byDoc[ed.Evidence.Doc], ed.Evidence.Ord)
		}
	}
	names := map[uint32]string{}
	nameOf := func(id uint32) (string, bool) {
		if s, ok := names[id]; ok {
			return s, s != ""
		}
		ent, ok := g.ents.Get(id)
		if !ok {
			names[id] = ""
			return "", false
		}
		s := embedText(ent, g.ents.SafeAliases(ent), g.rules.VectorAliases, "")
		names[id] = s
		return s, true
	}
	out := make([]Triple, 0, len(sum)/4)
	for k, a := range sum {
		n := 0
		for _, ords := range a.byDoc {
			n += originsOf(ords)
		}
		if n < minOrigins {
			continue
		}
		src, ok1 := nameOf(k.Src)
		dst, ok2 := nameOf(k.Dst)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, Triple{Key: k, Text: tripleText(src, dst, k.Type), Origins: n})
	}
	sort.Slice(out, func(i, j int) bool { return keyLess(out[i].Key, out[j].Key) })
	return out
}

// TripleHit — тройка, близкая к вектору вопроса; наружу — ради замеров.
type TripleHit struct {
	Key   EdgeKey
	Score float64
}

// NearestTriples — k ближайших к вектору троек из индекса; nil — индекса нет.
func (g *Graph) NearestTriples(query []int8, k int) []TripleHit {
	if g == nil || g.evecs == nil || !g.evecs.Ready() {
		return nil
	}
	hits := g.evecs.nearest(query, k)
	out := make([]TripleHit, len(hits))
	for i, h := range hits {
		out[i] = TripleHit{Key: h.Key, Score: h.Score}
	}
	return out
}

// EdgeVectorsInfo — состояние индекса троек для доктора и замеров.
type EdgeVectorsInfo struct {
	Ready   bool
	Model   string
	Dim     int
	Count   int // троек в индексе
	Rows    int // записей в файле, с перекрытыми
	Problem string
}

// EdgeVectorsInfo — состояние индекса троек.
func (g *Graph) EdgeVectorsInfo() EdgeVectorsInfo {
	if g == nil || g.evecs == nil {
		return EdgeVectorsInfo{}
	}
	v := g.evecs
	v.mu.RLock()
	defer v.mu.RUnlock()
	return EdgeVectorsInfo{Ready: v.meta.Dim > 0 && len(v.index) > 0, Model: v.meta.Model,
		Dim: v.meta.Dim, Count: len(v.index), Rows: len(v.keys), Problem: v.problem}
}

// StaleTriples — сколько троек индекс должен содержать, сколько из них
// в нём нет и у скольких изменился текст (склеили конец, добавили синоним).
// Считается без карты; поиск это не зовёт.
func (g *Graph) StaleTriples(minOrigins int) (want, missing, stale int) {
	tr := g.Triples(minOrigins)
	want = len(tr)
	v := g.evecs
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, t := range tr {
		i, ok := v.index[t.Key]
		switch {
		case !ok:
			missing++
		case v.stamp[i] != uint32(textStamp(t.Text)):
			stale++
		}
	}
	return want, missing, stale
}

// EmbedTriples считает векторы троек: досчитывает недостающие и устаревшие,
// готовое не трогает. Паспорт другой модели, других весов или другого порога
// означает другой индекс — файлы начинаются заново.
//
// Фиксация — порциями (EmbedOpts.Checkpoint): дописать данные, обновить
// паспорт; обрыв стоит одной порции, повтор команды продолжает с места.
func (g *Graph) EmbedTriples(ctx context.Context, emb kb.Embedder, minOrigins int, o EmbedOpts,
	onProgress func(EmbedProgress)) error {
	if emb == nil {
		return errors.New("эмбеддер не задан")
	}
	if minOrigins <= 0 {
		minOrigins = DefaultTripleMinOrigins
	}
	o = o.norm()
	release, err := lockVectors(g.dir)
	if err != nil {
		return err
	}
	defer release()

	digest := embedderDigest(ctx, emb)
	v := g.evecs
	v.mu.Lock()
	fresh := v.meta.Dim == 0 || v.meta.Model != emb.Model() || v.meta.MinOrigins != minOrigins ||
		(digest != "" && v.meta.Digest != "" && v.meta.Digest != digest)
	if fresh {
		v.meta = edgeVecMeta{}
		v.keys, v.stamp, v.data = nil, nil, nil
		v.index = map[EdgeKey]int{}
		_ = os.Remove(filepath.Join(v.dir, edgeVecDataFile))
		_ = os.Remove(filepath.Join(v.dir, edgeVecMetaFile))
	}
	v.mu.Unlock()

	all := g.Triples(minOrigins)
	var todo []Triple
	v.mu.RLock()
	for _, t := range all {
		if i, ok := v.index[t.Key]; ok && v.stamp[i] == uint32(textStamp(t.Text)) {
			continue
		}
		todo = append(todo, t)
	}
	v.mu.RUnlock()
	total := len(todo)
	if onProgress != nil {
		onProgress(EmbedProgress{Done: 0, Total: total})
	}

	done := 0
	for from := 0; from < len(todo); from += o.Checkpoint {
		to := min(from+o.Checkpoint, len(todo))
		part := todo[from:to]
		texts := make([]string, len(part))
		for i, t := range part {
			texts[i] = t.Text
		}
		dim, vecs, err := embedBatches(ctx, emb, texts, o, nil)
		if err != nil {
			return err
		}
		if err := v.appendRows(part, dim, vecs, emb.Model(), digest, minOrigins); err != nil {
			return err
		}
		done = to
		if onProgress != nil {
			onProgress(EmbedProgress{Done: done, Total: total})
		}
	}

	// Перекрытых записей больше четверти — перепаковать: убрать старые
	// вектора склеенных и переименованных концов и троек, выпавших из порога.
	v.mu.RLock()
	live := make(map[EdgeKey]bool, len(all))
	for _, t := range all {
		live[t.Key] = true
	}
	dead := len(v.keys) - len(v.index)
	for k := range v.index {
		if !live[k] {
			dead++
		}
	}
	rows := len(v.keys)
	v.mu.RUnlock()
	if rows > 0 && dead*4 > rows {
		return v.repack(live)
	}
	return nil
}

// appendRows дописывает записи в конец файла и обновляет паспорт.
func (v *EdgeVectors) appendRows(part []Triple, dim int, vecs []int8, model, digest string, minOrigins int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.meta.Dim == 0 {
		v.meta = edgeVecMeta{Magic: edgeVecMagic, Model: model, Dim: dim, Digest: digest, MinOrigins: minOrigins}
	} else if v.meta.Dim != dim {
		return fmt.Errorf("размерность эмбеддера %d, в индексе %d", dim, v.meta.Dim)
	}
	buf := make([]byte, 0, len(part)*v.rowSize())
	for i, t := range part {
		vec := vecs[i*dim : (i+1)*dim]
		buf = append(buf, encodeRow(t.Key, uint32(textStamp(t.Text)), vec)...)
	}
	path := filepath.Join(v.dir, edgeVecDataFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// Хвост длиннее паспорта — обрывок прошлой дозаписи, срезается.
	want := int64(v.meta.Count * v.rowSize())
	if err := f.Truncate(want); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteAt(buf, want); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()

	for i, t := range part {
		vec := vecs[i*dim : (i+1)*dim]
		v.keys = append(v.keys, t.Key)
		v.stamp = append(v.stamp, uint32(textStamp(t.Text)))
		v.data = append(v.data, vec...)
		v.index[t.Key] = len(v.keys) - 1
	}
	v.meta.Count = len(v.keys)
	v.meta.CRC = crc32.Update(v.meta.CRC, crc32.IEEETable, buf)
	return v.writeMeta()
}

func (v *EdgeVectors) writeMeta() error {
	raw, err := json.Marshal(v.meta)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(v.dir, edgeVecMetaFile), raw, 0o644)
}

// repack переписывает файл без перекрытых записей и без троек, которых
// в графе больше нет (live). Данные первыми, паспорт вторым.
func (v *EdgeVectors) repack(live map[EdgeKey]bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	keep := make([]int, 0, len(v.index))
	for k, i := range v.index {
		if live == nil || live[k] {
			keep = append(keep, i)
		}
	}
	sort.Ints(keep)
	buf := make([]byte, 0, len(keep)*v.rowSize())
	keys := make([]EdgeKey, 0, len(keep))
	stamp := make([]uint32, 0, len(keep))
	data := make([]int8, 0, len(keep)*v.meta.Dim)
	index := make(map[EdgeKey]int, len(keep))
	for _, i := range keep {
		vec := v.data[i*v.meta.Dim : (i+1)*v.meta.Dim]
		buf = append(buf, encodeRow(v.keys[i], v.stamp[i], vec)...)
		index[v.keys[i]] = len(keys)
		keys = append(keys, v.keys[i])
		stamp = append(stamp, v.stamp[i])
		data = append(data, vec...)
	}
	if err := fsx.WriteFileAtomic(filepath.Join(v.dir, edgeVecDataFile), buf, 0o644); err != nil {
		return err
	}
	v.keys, v.stamp, v.data, v.index = keys, stamp, data, index
	v.meta.Count = len(keys)
	v.meta.CRC = crc32.ChecksumIEEE(buf)
	return v.writeMeta()
}
