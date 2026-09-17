package graph

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Незаконченный полный пересчёт векторов понятий.
//
// **Зачем.** Полный пересчёт (`--graph-embed-recount`) нашего графа — около
// пятидесяти минут карты, и до 17.09.2026 он держал всё посчитанное в памяти
// до самого конца: обрыв на сорок пятой минуте стоил сорока пяти минут
// (10.09.2026 так и вышло — 115 тысяч понятий из 244, туннель лёг на семь секунд).
//
// **Почему не дозаписью в основной файл**, как досчёт хвоста. Пересчёт заменяет
// векторы, а не добавляет: начни он писать в `entities.vec`, смысловой вход
// в граф пропал бы на всё время счёта — и навсегда, если счёт оборвётся.
// Поэтому посчитанное копится рядом, в файлах `.part`, прежние векторы работают
// до самой подмены, а подмена — прежняя атомарная запись целиком.
//
// **Точка фиксации — паспорт части.** Данные дописываются и сбрасываются
// на диск первыми, отпечатки текстов вторыми, паспорт последним; хвост данных
// сверх паспорта — оборвавшаяся запись и срезается при продолжении.
//
// **Отпечатки лежат рядом с частью не случайно.** Между обрывом и продолжением
// граф живёт: у понятий из посчитанной головы прибавляются синонимы. Отпечаток
// обязан отвечать тексту, от которого вектор посчитан НА ДЕЛЕ, иначе такой
// вектор никогда не найдётся как устаревший (см. vecstale.go).
//
// Имена кончаются на `.part`: такие файлы отпечаток каталога графа не считает
// (cache.go, stampIgnored), и служба не переоткрывает граф на каждой фиксации.

const (
	entVecPartData  = entVecDataFile + ".part"
	entVecPartMeta  = entVecMetaFile + ".part"
	entVecPartStamp = entVecStampFile + ".part"
)

// vecPartMeta — паспорт незаконченного пересчёта.
type vecPartMeta struct {
	Model  string `json:"model"`
	Digest string `json:"digest,omitempty"`
	Dim    int    `json:"dim"`
	Count  int    `json:"count"`
}

// vecPart — незаконченный пересчёт в памяти и на диске.
type vecPart struct {
	dir    string
	meta   vecPartMeta
	data   []int8
	stamps []uint64
}

// loadVecPart поднимает незаконченный пересчёт, если он годится для продолжения:
// та же модель, те же веса, понятий в графе не меньше, чем посчитано. Всё
// прочее — пустая часть: пересчёт начнётся с начала, а негодные файлы будут
// переписаны первой же фиксацией.
func loadVecPart(dir, model, digest string, entities int) *vecPart {
	p := &vecPart{dir: dir, meta: vecPartMeta{Model: model, Digest: digest}}
	raw, err := os.ReadFile(filepath.Join(dir, entVecPartMeta))
	if err != nil {
		return p
	}
	var m vecPartMeta
	if json.Unmarshal(raw, &m) != nil || m.Model != model || m.Dim <= 0 || m.Count <= 0 || m.Count > entities {
		return p
	}
	if checkDigest(m.Digest, digest, model) != nil {
		return p
	}
	data, err := os.ReadFile(filepath.Join(dir, entVecPartData))
	if err != nil || len(data) < m.Count*m.Dim {
		return p
	}
	stamps := loadStampsFrom(filepath.Join(dir, entVecPartStamp))
	if len(stamps) < m.Count {
		return p
	}
	data = data[:m.Count*m.Dim]
	p.meta = m
	if p.meta.Digest == "" {
		p.meta.Digest = digest
	}
	p.data = append([]int8(nil), unsafe.Slice((*int8)(unsafe.Pointer(&data[0])), len(data))...)
	p.stamps = stamps[:m.Count]
	return p
}

// add фиксирует очередную порцию: векторы понятий Count+1… и тексты, от которых
// они посчитаны.
func (p *vecPart) add(dim int, data []int8, texts []string) error {
	if p.meta.Dim != 0 && p.meta.Dim != dim {
		return fmt.Errorf("размерность векторов разъехалась посреди пересчёта: %d против %d", dim, p.meta.Dim)
	}
	if len(data) != len(texts)*dim {
		return fmt.Errorf("пересчёт: %d байт на %d понятий размерности %d", len(data), len(texts), dim)
	}
	path := filepath.Join(p.dir, entVecPartData)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	at := int64(p.meta.Count) * int64(dim)
	if err := f.Truncate(at); err != nil {
		return err
	}
	if _, err := f.Seek(at, 0); err != nil {
		return err
	}
	if _, err := f.Write(unsafe.Slice((*byte)(unsafe.Pointer(&data[0])), len(data))); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	for _, t := range texts {
		p.stamps = append(p.stamps, textStamp(t))
	}
	if err := saveStampsTo(filepath.Join(p.dir, entVecPartStamp), p.stamps); err != nil {
		return err
	}
	meta := p.meta
	meta.Dim, meta.Count = dim, p.meta.Count+len(texts)
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(filepath.Join(p.dir, entVecPartMeta), raw, 0o644); err != nil {
		return err
	}
	p.meta = meta
	p.data = append(p.data, data...)
	return nil
}

// drop убирает файлы части: пересчёт закончен и подменил основной файл.
func (p *vecPart) drop() {
	for _, n := range []string{entVecPartMeta, entVecPartData, entVecPartStamp} {
		_ = os.Remove(filepath.Join(p.dir, n))
	}
}

// saveStampsTo и loadStampsFrom — отпечатки по произвольному пути: у части
// пересчёта свой файл, у готовых векторов свой (saveStamps, loadStamps).
func saveStampsTo(path string, stamps []uint64) error {
	raw := make([]byte, 8*len(stamps))
	for i, st := range stamps {
		binary.LittleEndian.PutUint64(raw[i*8:], st)
	}
	return fsx.WriteFileAtomic(path, raw, 0o644)
}

func loadStampsFrom(path string) []uint64 {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 8 {
		return nil
	}
	out := make([]uint64, len(raw)/8)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(raw[i*8:])
	}
	return out
}
