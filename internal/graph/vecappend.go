package graph

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Дозапись векторов понятий в хвост файла.
//
// **Зачем.** `save` пишет файл целиком — 178 МБ на 174 тысячи понятий (замер
// 06.09.2026). Раз в неделю это ничто. Но если считать векторы **по мере
// появления понятий**, прямо по ходу недельной сборки графа, пачками, то
// переписывание одного и того же становится основной работой на диске.
// Дозапись превращает её в запись только нового.
//
// **Почему это вообще возможно.** Номера понятий выдаются подряд
// (`entities.go`: `ID: uint32(len(e.list) + 1)`), вектор понятия N лежит
// на месте N-1, а уплотнение реестра номера не меняет (`compact.go`:
// последняя запись на каждый номер, порядок первого появления). Значит файл
// векторов — это не произвольная таблица, а **префикс**: понятия с 1 по Count.
// Дописать хвост в такой файл — обычное дело; отдельный индекс «номер → строка»
// (как IDMap у FAISS) нужен только при разрежённых номерах, а их здесь нет.
//
// **Единственное настоящее ограничение — порядок.** Векторы обязаны появляться
// по возрастанию номера. Пропущенное понятие останавливает дозапись всего, что
// за ним: дырка в этом формате неотличима от посчитанного нуля, а нулевой
// вектор даёт близость 0 со всем подряд, то есть уверенное «совсем не похоже»
// вместо честного «не считали».
//
// **Обрыв посреди дозаписи не портит файл.** Данные пишутся первыми, паспорт
// вторым, и точка фиксации — паспорт. Файл, оказавшийся длиннее паспорта, —
// это оборвавшаяся дозапись: лишний хвост читается как небывший
// (см. openEntityVectors) и срезается следующей дозаписью. Без этого один
// обрыв питания стоил бы недель счёта.

// AppendEntityVectors дописывает векторы понятий, идущих сразу за посчитанными.
//
// data — векторы понятий с номерами от Count+1 подряд, уже квантованные.
// Возвращает ошибку, если модель, её веса или размерность разошлись
// с паспортом: склеивать векторы разных пространств нельзя.
func (g *Graph) AppendEntityVectors(model, digest string, dim int, data []int8) error {
	if g == nil || g.vecs == nil {
		return fmt.Errorf("граф не открыт")
	}
	if dim <= 0 || len(data) == 0 || len(data)%dim != 0 {
		return fmt.Errorf("векторы понятий: длина %d не делится на размерность %d", len(data), dim)
	}
	return g.vecs.appendVectors(model, digest, dim, data)
}

// appendVectors дописывает хвост и обновляет паспорт.
func (v *EntityVectors) appendVectors(model, digest string, dim int, data []int8) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.meta.Count == 0 {
		// Дописывать не к чему — это обычная первая запись.
		return v.saveLocked(model, digest, dim, data)
	}
	if v.meta.Model != model {
		return fmt.Errorf("векторы посчитаны моделью %q, а дописываются моделью %q: "+
			"это разные пространства — нужен полный пересчёт (--graph-embed --graph-embed-recount)",
			v.meta.Model, model)
	}
	if v.meta.Dim != dim {
		return fmt.Errorf("размерность векторов %d, а дописывается %d", v.meta.Dim, dim)
	}
	if err := checkDigest(v.meta.Digest, digest, model); err != nil {
		return err
	}

	path := filepath.Join(v.dir, entVecDataFile)
	want := int64(v.meta.Count) * int64(dim)

	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// Срезаем хвост от прежней оборвавшейся дозаписи: паспорт — точка фиксации,
	// и всё сверх него не данные, а мусор.
	if err := f.Truncate(want); err != nil {
		return err
	}
	if _, err := f.Seek(want, 0); err != nil {
		return err
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(&data[0])), len(data))
	if _, err := f.Write(bytes); err != nil {
		return err
	}
	// fsync до паспорта: паспорт, попавший на диск раньше данных, объявил бы
	// посчитанным то, чего в файле нет, — а это уже не терпимый хвост, а дырка.
	if err := f.Sync(); err != nil {
		return err
	}

	meta := v.meta
	meta.Count += len(data) / dim
	meta.CRC = crc32.Update(v.meta.CRC, crc32.IEEETable, bytes)
	if meta.Digest == "" {
		meta.Digest = digest
	}
	// Старый паспорт без контрольной суммы продолжать нечем: единственный
	// честный ответ — посчитать её по всему файлу заново.
	if v.meta.CRC == 0 {
		whole := unsafe.Slice((*byte)(unsafe.Pointer(&v.data[0])), len(v.data))
		meta.CRC = crc32.Update(crc32.ChecksumIEEE(whole), crc32.IEEETable, bytes)
	}
	raw, err := metaBytes(meta)
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(filepath.Join(v.dir, entVecMetaFile), raw, 0o644); err != nil {
		return err
	}
	v.meta = meta
	v.data = append(v.data, data...)
	v.problem = ""
	return nil
}

// checkDigest сверяет веса эмбеддера.
//
// **Это не про «другую модель».** Модель одна и та же — `bge-m3`; сверяется
// то, что одно её имя на двух машинах может указывать на **разные файлы**:
// тег `:latest`, загруженный в разные дни, приносит разные веса. Риск
// появляется ровно тогда, когда счёт векторов уезжает на вторую машину,
// то есть в том самом случае, ради которого заводится догонщик.
//
// Пустой digest с любой стороны сверку пропускает, а не заваливает: его нет
// у паспортов старого образца и у серверов, которые его не отдают. Ровно так
// же поступает пул узлов сборки (graphex/pool.go) — и по той же причине:
// отказ работать из-за отсутствующего поля хуже, чем несделанная проверка.
func checkDigest(have, got, model string) error {
	if have == "" || got == "" || have == got {
		return nil
	}
	return fmt.Errorf("векторы понятий посчитаны другим файлом модели %s: "+
		"в паспорте digest %s, у сервера %s.\n"+
		"Модель та же, но веса разные: тег вроде :latest на двух машинах, "+
		"загруженных в разные дни, указывает на разные файлы. Векторы, посчитанные "+
		"вперемешку, лежат в разных углах пространства, и по выдаче этого не увидеть — "+
		"поиск продолжит отвечать, просто хуже.\n"+
		"Дешёвое лечение: взять на этом сервере те же веса, что в паспорте "+
		"(ollama rm %s && ollama pull %s@%s).\n"+
		"Дорогое, если тех весов уже не достать: полный пересчёт "+
		"(--graph-embed --graph-embed-recount).",
		model, shortDigest(have), shortDigest(got), model, model, have)
}

// shortDigest укорачивает отпечаток до читаемого вида.
func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
