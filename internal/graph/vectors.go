package graph

import (
	"hash/crc32"

	"encoding/json"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Векторы понятий: смысловой вход в граф.
//
// Зачем. До 26.08.2026 вопрос попадал в граф только по написанию: слова вопроса
// сравнивались с именами и синонимами понятий. Замер того дня на двенадцати
// живых вопросах дал два промаха, и оба — про дверь, а не про комнату. Знание
// в графе было, а войти не получалось:
//
//	«выявление сообществ в графе знаний» → мимо, а «community detection» → есть;
//	«чем переранжировать найденные куски» → мимо, «переранжирование» → есть.
//
// Первый промах закрыли основы слов (kb.StemPhrase), второй ими не закрывается:
// стеммер Портера сводит падежи, но не сводит глагол с существительным
// («переранжироват» против «переранжирован»), и заставлять его это делать
// значит начать склеивать разные слова.
//
// Оба случая закрывает вектор. Замер 26.08.2026 через bge-m3:
//
//	«выявление сообществ» ↔ Community detection — 0.860
//	«переранжировать найденные куски» ↔ reranking — 0.691
//	«выявление сообществ» ↔ Vector search (заведомо чужая пара) — 0.565
//
// Отсюда важное следствие для отбора: **порог абсолютным быть не может**.
// У чужой пары 0.565, а у осмысленной «как ускорить выдачу поиска» ↔ Vector
// search — 0.576, разница в сотую. Годится только относительный отбор: берём
// верхушку списка и требуем запас над серединой этой же верхушки.
//
// Стоимость. Имён и синонимов — десятки тысяч против сотен тысяч кусков
// коллекции, поэтому счёт занимает минуты, а не часы, и не требует того
// решения, которого требует сборка графа.

const entVecMagic = "OLLGRV1"

// entVecMeta — паспорт посчитанных векторов понятий.
type entVecMeta struct {
	Magic string `json:"magic"`
	Model string `json:"model"`
	Dim   int    `json:"dim"`
	Count int    `json:"count"` // сколько понятий покрыто, начиная с номера 1
	// CRC — контрольная сумма данных (CRC-32 IEEE). Ловит случай, когда
	// файл данных и паспорт остались от разных записей при совпавшем размере.
	// Ноль — паспорт старого образца, без суммы; принимается как есть.
	CRC uint32 `json:"crc,omitempty"`
}

const (
	entVecMetaFile = "entities.vecmeta"
	entVecDataFile = "entities.vec"
)

// EntityVectors — векторы имён понятий, по номеру: at(id) = data[(id-1)*dim:].
type EntityVectors struct {
	mu   sync.RWMutex
	dir  string
	meta entVecMeta
	data []int8
	// problem — почему векторы не приняты, когда файлы есть: доктор графа
	// покажет это вместо молчаливого «векторы не считались».
	problem string
}

// openEntityVectors читает векторы, если они посчитаны.
//
// Отсутствие файлов — не ошибка, а обычное состояние графа, которому векторы
// ещё не считали: поиск продолжает работать по написанию.
func openEntityVectors(dir string) *EntityVectors {
	v := &EntityVectors{dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, entVecMetaFile))
	if err != nil {
		return v
	}
	if json.Unmarshal(raw, &v.meta) != nil || v.meta.Magic != entVecMagic {
		v.meta = entVecMeta{}
		return v
	}
	data, err := os.ReadFile(filepath.Join(dir, entVecDataFile))
	if err != nil || len(data) != v.meta.Count*v.meta.Dim {
		if err == nil {
			v.problem = fmt.Sprintf("размер %s (%d байт) не совпадает с паспортом (%d понятий × %d); "+
				"пересчитать: --graph-embed", entVecDataFile, len(data), v.meta.Count, v.meta.Dim)
		}
		v.meta = entVecMeta{}
		return v
	}
	if v.meta.CRC != 0 && crc32.ChecksumIEEE(data) != v.meta.CRC {
		v.problem = "контрольная сумма " + entVecDataFile + " не сходится с паспортом: файлы от разных записей; " +
			"пересчитать: --graph-embed"
		v.meta = entVecMeta{}
		return v
	}
	v.data = unsafe.Slice((*int8)(unsafe.Pointer(&data[0])), len(data))
	return v
}

// Problem объясняет, почему векторы с диска не приняты; пусто — всё в порядке
// или файлов просто нет.
func (v *EntityVectors) Problem() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.problem
}

// Ready сообщает, есть ли чем искать по смыслу.
func (v *EntityVectors) Ready() bool {
	if v == nil {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.meta.Count > 0 && v.meta.Dim > 0 && len(v.data) > 0
}

// Model и Dim — чем и в каком пространстве посчитано.
func (v *EntityVectors) Model() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.meta.Model
}

func (v *EntityVectors) Dim() int {
	if v == nil {
		return 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.meta.Dim
}

func (v *EntityVectors) Count() int {
	if v == nil {
		return 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.meta.Count
}

// at возвращает вектор понятия или nil.
func (v *EntityVectors) at(id uint32) []int8 {
	if id == 0 || int(id) > v.meta.Count {
		return nil
	}
	from := (int(id) - 1) * v.meta.Dim
	return v.data[from : from+v.meta.Dim]
}

// save записывает векторы целиком.
//
// Не дозапись, а перезапись: понятия нумеруются подряд, и частичное обновление
// потребовало бы держать на диске дырки. Файл невелик — десятки мегабайт.
func (v *EntityVectors) save(model string, dim int, data []int8) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(&data[0])), len(data))
	meta := entVecMeta{Magic: entVecMagic, Model: model, Dim: dim, Count: len(data) / dim,
		CRC: crc32.ChecksumIEEE(bytes)}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	// Сначала данные, потом паспорт, оба атомарно: обрыв между ними оставит
	// старый паспорт при новых данных, и это поймает контрольная сумма.
	if err := fsx.WriteFileAtomic(filepath.Join(v.dir, entVecDataFile), bytes, 0o644); err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(filepath.Join(v.dir, entVecMetaFile), raw, 0o644); err != nil {
		return err
	}
	v.meta, v.data, v.problem = meta, data, ""
	return nil
}

// senseHit — понятие, близкое к вопросу по смыслу.
type senseHit struct {
	ID    uint32
	Score float64
}

// linkBySense находит понятия, близкие к вектору вопроса.
//
// Отбор относительный, по причине из шапки файла: берём верхушку и оставляем
// в ней только то, что заметно выше её же середины. Абсолютный порог тут
// не работает — у чужой пары близость бывает выше, чем у своей.
// margin — насколько выше середины верхушки должно быть понятие (Rules.SenseMargin).
func (v *EntityVectors) linkBySense(query []int8, limit int, margin float64) []senseHit {
	if !v.Ready() || len(query) != v.Dim() || limit <= 0 {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	// Верхушка вчетверо шире запрошенного: середина нужна для сравнения,
	// а по короткому списку её не посчитать.
	wide := limit * 4
	all := make([]senseHit, 0, v.meta.Count)
	for id := 1; id <= v.meta.Count; id++ {
		vec := v.at(uint32(id))
		if len(vec) == 0 {
			continue
		}
		all = append(all, senseHit{ID: uint32(id), Score: kb.Cosine(vec, query)})
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].Score != all[b].Score {
			return all[a].Score > all[b].Score
		}
		return all[a].ID < all[b].ID
	})
	if len(all) > wide {
		all = all[:wide]
	}
	if len(all) == 0 {
		return nil
	}

	median := all[len(all)/2].Score
	out := make([]senseHit, 0, limit)
	for _, h := range all {
		if len(out) >= limit {
			break
		}
		if h.Score < median+margin {
			break
		}
		out = append(out, h)
	}
	return out
}

// senseMargin — насколько понятие должно быть ближе середины верхушки, чтобы
// считаться связанным с вопросом.
//
// Подобран по замеру из шапки файла: у осмысленных пар отрыв от чужих начинался
// примерно с трёх сотых, у пары «выявление сообществ» ↔ Community detection он
// вдвое больше. Значение намеренно осторожное: лишнее понятие на входе тянет
// за собой чужие связи и цитаты, и это хуже, чем не найти ничего, — во втором
// случае поиск честно говорит «не нашёл», а в первом уверенно врёт.

// EntityVectorsInfo — что показать человеку о векторах понятий.
type EntityVectorsInfo struct {
	Ready bool
	Model string
	Dim   int
	Count int
}

// VectorsInfo сообщает состояние смыслового входа.
// VectorsProblem объясняет, почему векторы понятий с диска не приняты.
func (g *Graph) VectorsProblem() string {
	if g == nil || g.vecs == nil {
		return ""
	}
	return g.vecs.Problem()
}

func (g *Graph) VectorsInfo() EntityVectorsInfo {
	if g == nil || g.vecs == nil {
		return EntityVectorsInfo{}
	}
	return EntityVectorsInfo{Ready: g.vecs.Ready(), Model: g.vecs.Model(),
		Dim: g.vecs.Dim(), Count: g.vecs.Count()}
}

// Existing возвращает уже посчитанные векторы, если они годны для этой модели
// и размерности.
//
// Нужна досчёту: понятия нумеруются подряд, и вектор понятия с номером N лежит
// на месте N-1. Значит уже посчитанное можно взять как есть, а считать только
// хвост — понятия, появившиеся после прошлого счёта.
//
// Годность проверяется по модели: векторы разных моделей живут в разных
// пространствах, и склеивать их — то же, что складывать метры с килограммами.
func (v *EntityVectors) Existing(model string, dim int) ([]int8, int) {
	if v == nil {
		return nil, 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	// Ноль в dim означает «любая»: вызывающий узнаёт размерность только
	// от эмбеддера, а тот отвечает уже после первого запроса.
	if v.meta.Count == 0 || v.meta.Model != model || (dim > 0 && v.meta.Dim != dim) {
		return nil, 0
	}
	out := make([]int8, len(v.data))
	copy(out, v.data)
	return out, v.meta.Count
}

// SaveEntityVectors записывает посчитанные векторы понятий.
func (g *Graph) SaveEntityVectors(model string, dim int, data []int8) error {
	if g == nil || g.vecs == nil {
		return fmt.Errorf("граф не открыт")
	}
	if dim <= 0 || len(data) == 0 || len(data)%dim != 0 {
		return fmt.Errorf("векторы понятий: длина %d не делится на размерность %d", len(data), dim)
	}
	return g.vecs.save(model, dim, data)
}

// ── Поиск двойников среди понятий ────────────────────────────────────────────

// cosine — близость векторов двух понятий.
//
// Второе значение говорит, было ли чем считать: вектор посчитан не всем
// понятиям, а граф всё время растёт, и хвост без векторов — обычное дело.
func (v *EntityVectors) cosine(a, b uint32) (float64, bool) {
	if !v.Ready() {
		return 0, false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	va, vb := v.at(a), v.at(b)
	if len(va) == 0 || len(vb) == 0 {
		return 0, false
	}
	return kb.Cosine(va, vb), true
}

// vecPair — пара понятий, близких по вектору.
type vecPair struct {
	A, B uint32
	Cos  float64
}

// pairsAbove перебирает ВСЕ пары понятий и возвращает те, что ближе порога.
//
// Дорого и намеренно: 62 805 понятий дают около двух миллиардов пар, а каждая
// пара — 1024 умножения, то есть порядка двух триллионов операций. Поэтому
// работа разложена по ядрам, а вызывается только по явному ключу.
//
// Приблизительных приёмов (хеши по знакам, деление на корзины) здесь нет
// сознательно: команда запускается редко, а её выдачу человек сверяет глазами,
// и пропуск пары из-за неудачного хеша объяснить было бы нечем.
func (v *EntityVectors) pairsAbove(min float64, workers int) []vecPair {
	if !v.Ready() {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	n, dim := v.meta.Count, v.meta.Dim
	if n < 2 {
		return nil
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > n {
		workers = n
	}
	// Порог переводится в целое один раз: сравнивать целые скалярные
	// произведения дешевле, чем делить каждое из двух миллиардов.
	minDot := int64(min * 127 * 127)

	var next int64 // общий счётчик строк: строки разной длины, и делить поровну нельзя
	parts := make([][]vecPair, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var out []vecPair
			for {
				i := int(atomic.AddInt64(&next, 1)) - 1
				if i >= n-1 {
					break
				}
				from := i * dim
				a := v.data[from : from+dim]
				for j := i + 1; j < n; j++ {
					off := j * dim
					if d := dotInt8(a, v.data[off:off+dim]); d >= minDot {
						out = append(out, vecPair{
							A: uint32(i + 1), B: uint32(j + 1),
							Cos: float64(d) / (127 * 127),
						})
					}
				}
			}
			parts[w] = out
		}(w)
	}
	wg.Wait()

	var total int
	for _, p := range parts {
		total += len(p)
	}
	out := make([]vecPair, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// dotInt8 — скалярное произведение с раскруткой по четыре.
//
// Раскрутка не украшение: внутренний цикл выполняется два триллиона раз,
// и четыре независимых накопителя дают процессору складывать их параллельно.
// int32 хватает с запасом: слагаемое не больше 127×127, а на накопитель
// приходится четверть из 1024 членов.
func dotInt8(a, b []int8) int64 {
	var s0, s1, s2, s3 int32
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for ; i+4 <= n; i += 4 {
		s0 += int32(a[i]) * int32(b[i])
		s1 += int32(a[i+1]) * int32(b[i+1])
		s2 += int32(a[i+2]) * int32(b[i+2])
		s3 += int32(a[i+3]) * int32(b[i+3])
	}
	for ; i < n; i++ {
		s0 += int32(a[i]) * int32(b[i])
	}
	return int64(s0) + int64(s1) + int64(s2) + int64(s3)
}

// vectorOf отдаёт вектор понятия. Второе значение — было ли что отдавать.
//
// Нужен ранжированию соседей: у понятия десятки связей, а показывается пять,
// и выбирать эти пять надо по вопросу, а не только по числу подтверждений.
func (v *EntityVectors) vectorOf(id uint32) ([]int8, bool) {
	if !v.Ready() {
		return nil, false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	vec := v.at(id)
	if len(vec) == 0 {
		return nil, false
	}
	return vec, true
}

// SetVectorsForTest записывает паспорт векторов с одним нулевым вектором:
// тестам нужен граф, у которого «векторы посчитаны», а считать их нечем.
func (g *Graph) SetVectorsForTest(model string, dim int) error {
	if g.vecs == nil {
		g.vecs = &EntityVectors{dir: g.dir}
	}
	return g.vecs.save(model, dim, make([]int8, dim))
}
