package graph

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Забыть куски: убрать из графа всё, что извлечено из названных кусков (этап 99).
//
// **Зачем.** Оглавления и указатели помечены в индексе признаком FlagTOC, и
// новая сборка их не разбирает. Но уже извлечённое из них в графе осталось:
// 49 тысяч упоминаний и 24 тысячи подтверждений связей (перепись 07.09.2026),
// причём связи «всё со всем» — в оглавлении все понятия книги стоят рядом.
// Пересборка сняла бы это неделями карты; здесь то же делается по журналам
// за секунды.
//
// **Что делается.** Три журнала переписываются без записей, чьи куски велено
// забыть: mentions.log (упоминания), edges.log (подтверждения связей),
// progress.log (отметки разбора — кусок становится «пропущен», чтобы новая
// сборка его не взяла). Реестр понятий переписывается с пересчитанными
// счётчиками упоминаний и книг по оставшимся упоминаниям; номера понятий
// не меняются — на них держатся векторы и разбиение. Понятие, оставшееся
// без единого упоминания, из реестра не убирается: номер сдвигать нельзя,
// а доктор такие видит. Векторы понятий не трогаются: их текст — имя
// и синонимы, к кускам он не привязан. Разбиение тем после этого
// пересчитывается отдельно (--graph-communities).
//
// **Как пишется.** Каждый файл — во временный рядом, старый переименовывается
// в .bak-<время>, новый встаёт на место; каталог синхронизируется. Граф
// при этом обязан быть закрыт, а замок сборки — свободен: журналы читает
// и дописывает сама сборка.

// ForgetStats — что изменилось.
type ForgetStats struct {
	Chunks   int // кусков забыто (по ним нашлись записи)
	Mentions int // упоминаний убрано
	Edges    int // подтверждений связей убрано
	Marks    int // отметок разбора переведено в «пропущен»
	Entities int // понятий с изменившимися счётчиками
	Orphans  int // понятий, оставшихся без упоминаний
	Backups  []string
}

// ForgetChunks убирает из графа в каталоге dir всё, что извлечено из кусков,
// для которых drop возвращает true. dry — только посчитать.
func ForgetChunks(dir string, drop func(ChunkKey) bool, dry bool) (ForgetStats, error) {
	var st ForgetStats
	if _, err := os.Stat(filepath.Join(dir, lockFile)); err == nil {
		if owner := readLock(filepath.Join(dir, lockFile)); owner.alive() {
			return st, &LockedError{Path: filepath.Join(dir, lockFile), PID: owner.PID, Since: owner.Since}
		}
	}
	stamp := time.Now().Format("20060102-150405")
	touched := map[uint64]bool{}

	// Упоминания: считаем оставшиеся на понятие и книги на понятие.
	count := map[uint32]int{}
	keep := func(k ChunkKey) bool {
		if drop(k) {
			touched[k.Pack()] = true
			return false
		}
		return true
	}
	mentions, err := rewriteBinary(filepath.Join(dir, mentionsFile), 12, stamp, dry, func(b []byte) bool {
		id := binary.LittleEndian.Uint32(b[0:])
		k := ChunkKey{Doc: binary.LittleEndian.Uint32(b[4:]), Ord: binary.LittleEndian.Uint32(b[8:])}
		if !keep(k) {
			return false
		}
		count[id]++
		return true
	})
	if err != nil {
		return st, err
	}
	st.Mentions = mentions.dropped
	if mentions.backup != "" {
		st.Backups = append(st.Backups, mentions.backup)
	}

	edges, err := rewriteBinary(filepath.Join(dir, edgesFile), 24, stamp, dry, func(b []byte) bool {
		k := ChunkKey{Doc: binary.LittleEndian.Uint32(b[16:]), Ord: binary.LittleEndian.Uint32(b[20:])}
		return keep(k)
	})
	if err != nil {
		return st, err
	}
	st.Edges = edges.dropped
	if edges.backup != "" {
		st.Backups = append(st.Backups, edges.backup)
	}

	// Отметки: не убираем, а переводим в «служебный» — иначе новая сборка
	// разобрала бы кусок заново. А снимут с него признак — возьмёт снова.
	marks, err := rewriteBinaryMap(filepath.Join(dir, progressFile), 12, stamp, dry, func(b []byte) bool {
		k := ChunkKey{Doc: binary.LittleEndian.Uint32(b[0:]), Ord: binary.LittleEndian.Uint32(b[4:])}
		if !drop(k) || binary.LittleEndian.Uint32(b[8:]) == MarkService {
			return false
		}
		touched[k.Pack()] = true
		binary.LittleEndian.PutUint32(b[8:], MarkService)
		return true
	})
	if err != nil {
		return st, err
	}
	st.Marks = marks.dropped
	if marks.backup != "" {
		st.Backups = append(st.Backups, marks.backup)
	}
	st.Chunks = len(touched)

	// Реестр: счётчик упоминаний по оставшимся. Всегда, а не только когда
	// что-то убрано сейчас: так повторный запуск после сбоя доводит реестр
	// до согласия с журналом. Поле docs не трогается — сборка его не ведёт
	// (Touch зовётся без признака новой книги), и заводить его здесь значило
	// бы переписать все 207 тысяч записей ради поля, которое никто не читает.
	changed, orphans, backup, err := recountEntities(filepath.Join(dir, entitiesFile), count, stamp, dry)
	if err != nil {
		return st, err
	}
	st.Entities, st.Orphans = changed, orphans
	if backup != "" {
		st.Backups = append(st.Backups, backup)
	}
	if !dry {
		syncDir(dir)
	}
	return st, nil
}

type rewriteResult struct {
	dropped int
	backup  string
}

// rewriteBinary переписывает журнал записей постоянной длины, оставляя те,
// для которых keep вернул true. Оборванный хвост отбрасывается, как при чтении.
func rewriteBinary(path string, size int, stamp string, dry bool, keep func([]byte) bool) (rewriteResult, error) {
	return rewriteBinaryMap(path, size, stamp, dry, func(b []byte) bool { return !keep(b) })
}

// rewriteBinaryMap — то же, но change правит запись на месте и возвращает,
// изменилась ли она; записи не удаляются. Для rewriteBinary «изменилась» —
// значит «удалить».
func rewriteBinaryMap(path string, size int, stamp string, dry bool, change func([]byte) bool) (rewriteResult, error) {
	var res rewriteResult
	src, err := os.Open(path)
	if os.IsNotExist(err) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	defer src.Close()
	deleting := filepath.Base(path) != progressFile

	tmp := path + ".tmp-" + stamp
	var dst *os.File
	var w *bufio.Writer
	if !dry {
		if dst, err = os.Create(tmp); err != nil {
			return res, err
		}
		w = bufio.NewWriterSize(dst, 1<<20)
	}
	r := bufio.NewReaderSize(src, 1<<20)
	buf := make([]byte, size)
	for {
		if _, err := readFull(r, buf); err != nil {
			break // обрыв хвоста — как при чтении
		}
		changed := change(buf)
		if changed {
			res.dropped++
			if deleting {
				continue
			}
		}
		if w != nil {
			if _, err := w.Write(buf); err != nil {
				dst.Close()
				os.Remove(tmp)
				return res, err
			}
		}
	}
	if dry || res.dropped == 0 {
		if dst != nil {
			dst.Close()
			os.Remove(tmp)
		}
		return res, nil
	}
	if err := w.Flush(); err != nil {
		dst.Close()
		os.Remove(tmp)
		return res, err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		os.Remove(tmp)
		return res, err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return res, err
	}
	res.backup = path + ".bak-" + stamp
	if err := os.Rename(path, res.backup); err != nil {
		os.Remove(tmp)
		return res, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(res.backup, path)
		return res, err
	}
	return res, nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			if n == len(buf) {
				return n, nil
			}
			return n, err
		}
	}
	return n, nil
}

// recountEntities переписывает реестр со счётчиком упоминаний по журналу.
// Записи — как при уплотнении: последняя на каждый номер, порядок появления.
func recountEntities(path string, count map[uint32]int, stamp string, dry bool) (changed, orphans int, backup string, err error) {

	last, order, _, err := lastPerID(path)
	if err != nil {
		return 0, 0, "", err
	}
	for _, id := range order {
		var rec map[string]any
		if json.Unmarshal(last[id], &rec) != nil {
			continue
		}
		n := count[id]
		oldN, _ := rec["count"].(float64)
		if n == 0 {
			orphans++
		}
		if int(oldN) == n {
			continue
		}
		changed++
		if n > 0 {
			rec["count"] = n
		} else {
			delete(rec, "count")
		}
		b, err := json.Marshal(rec)
		if err != nil {
			return 0, 0, "", err
		}
		last[id] = b
	}
	if dry || changed == 0 {
		return changed, orphans, "", nil
	}
	tmp := path + ".tmp-" + stamp
	if err := writeRecords(tmp, last, order); err != nil {
		os.Remove(tmp)
		return 0, 0, "", err
	}
	backup = path + ".bak-" + stamp
	if err := os.Rename(path, backup); err != nil {
		os.Remove(tmp)
		return 0, 0, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(backup, path)
		return 0, 0, "", err
	}
	return changed, orphans, backup, nil
}

// String — сводка для человека.
func (s ForgetStats) String() string {
	return fmt.Sprintf("кусков %d: упоминаний убрано %d, подтверждений связей %d, отметок переведено в «пропущен» %d; "+
		"понятий с новыми счётчиками %d, без упоминаний осталось %d",
		s.Chunks, s.Mentions, s.Edges, s.Marks, s.Entities, s.Orphans)
}
