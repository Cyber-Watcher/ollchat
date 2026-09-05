package kb

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Хранилище векторов — колонка, параллельная кускам.
//
// Вектор куска с номером i лежит в vectors.dat по смещению i×dim. Никакого
// отдельного отображения «кусок → вектор» нет, и поэтому нечему разъехаться:
// номер куска и есть адрес.
//
// Главный инвариант: покрытие векторами — всегда начальный отрезок [0, count).
// Он держится сам собой, потому что куски дописываются только в конец, а векторы
// считаются от count до конца. Из него следует всё остальное: доливка книг
// требует работы только на хвосте, а уплотнение сохраняет порядок кусков
// и потому сохраняет и сам отрезок.
//
// Числа хранятся в int8. Вектор сначала нормируется (длина 1), затем каждое
// число умножается на 127 и округляется. Тогда косинус — это просто скалярное
// произведение, делённое на 127², и множитель на каждый вектор не нужен.
// Цена — dim байт на кусок: при dim=1024 и четверти миллиона кусков это 255 МБ,
// вдвое больше всего словесного индекса.

const vecMagic = "OLLKBV1"

// VecMeta — сведения о посчитанных векторах.
type VecMeta struct {
	Magic string `json:"magic"`
	Model string `json:"model"` // какой моделью считали
	Dim   int    `json:"dim"`
	Count int    `json:"count"` // сколько кусков покрыто, начиная с нулевого
}

// Compatible сообщает, годятся ли векторы для работы с этой моделью.
//
// Векторы разных моделей несравнимы между собой: близость считается внутри
// одного пространства. Поэтому смена модели в настройках не «слегка ухудшает
// поиск», а делает его бессмысленным — и об этом надо говорить прямо.
func (m VecMeta) Compatible(model string, dim int) bool {
	if m.Magic != vecMagic || m.Count == 0 {
		return false
	}
	if model != "" && m.Model != model {
		return false
	}
	return dim <= 0 || m.Dim == dim
}

// Vectors — прочитанные в память векторы коллекции.
//
// После открытия только читаются, поэтому замок не нужен: файл сменяется
// целиком вместе с коллекцией.
type Vectors struct {
	dir  string
	meta VecMeta
	data []int8 // все векторы подряд; count×dim
}

// vecPaths — имена файлов внутри каталога коллекции.
func vecPaths(dir string) (dat, meta string) {
	return filepath.Join(dir, "vectors.dat"), filepath.Join(dir, "vectors.meta")
}

// OpenVectors читает векторы коллекции. Отсутствие файлов — не ошибка:
// коллекция без смыслов просто ищется по словам.
func OpenVectors(dir string) (*Vectors, error) {
	dat, metaPath := vecPaths(dir)
	var meta VecMeta
	if err := readJSON(metaPath, &meta); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if meta.Magic != vecMagic || meta.Dim <= 0 || meta.Count <= 0 {
		return nil, nil
	}

	f, err := os.Open(dat)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	want := int64(meta.Count) * int64(meta.Dim)
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Файл длиннее счётчика — работу прервали между дозаписью и сохранением
	// счётчика. Лишний хвост просто не читаем: он будет перезаписан при
	// следующем запуске. Файл короче — испорчен, и врать об этом нельзя.
	if info.Size() < want {
		return nil, fmt.Errorf("vectors.dat короче обещанного: %d байт вместо %d — посчитайте векторы(смыслы) заново",
			info.Size(), want)
	}

	buf := make([]byte, want)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	v := &Vectors{dir: dir, meta: meta, data: make([]int8, want)}
	for i, b := range buf {
		v.data[i] = int8(b)
	}
	return v, nil
}

// Meta возвращает сведения о векторах.
func (v *Vectors) Meta() VecMeta {
	if v == nil {
		return VecMeta{}
	}
	return v.meta
}

// Count — сколько кусков покрыто.
func (v *Vectors) Count() int {
	if v == nil {
		return 0
	}
	return v.meta.Count
}

// Dim — размерность.
func (v *Vectors) Dim() int {
	if v == nil {
		return 0
	}
	return v.meta.Dim
}

// At возвращает вектор куска.
func (v *Vectors) At(i int) []int8 {
	if v == nil || i < 0 || i >= v.meta.Count {
		return nil
	}
	return v.data[i*v.meta.Dim : (i+1)*v.meta.Dim]
}

// VecWriter дозаписывает векторы в конец файла.
//
// Порядок работы жёсткий: сначала данные на диск, потом счётчик. Обратный
// порядок означал бы, что после обрыва счётчик обещает векторы, которых нет,
// и коллекция ищет по мусору.
type VecWriter struct {
	dir   string
	meta  VecMeta
	f     *os.File
	added int
}

// CreateVecWriter открывает файл векторов на дозапись.
//
// from — с какого куска продолжать. Обычно это Count() из прежних сведений;
// ноль означает пересчёт с нуля, и тогда прежний файл усекается.
func CreateVecWriter(dir, model string, dim, from int) (*VecWriter, error) {
	if dim <= 0 {
		return nil, errors.New("размерность вектора не может быть нулевой")
	}
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	dat, _ := vecPaths(dir)
	f, err := os.OpenFile(dat, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	// Обрезаем ровно по границе продолжения: и хвост прерванной работы,
	// и всё лишнее при пересчёте уходят одинаково.
	if err := f.Truncate(int64(from) * int64(dim)); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(int64(from)*int64(dim), 0); err != nil {
		f.Close()
		return nil, err
	}
	return &VecWriter{
		dir:  dir,
		meta: VecMeta{Magic: vecMagic, Model: model, Dim: dim, Count: from},
		f:    f,
	}, nil
}

// Append дописывает векторы очередной пачки кусков.
func (w *VecWriter) Append(vecs [][]float32) error {
	if len(vecs) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(vecs)*w.meta.Dim)
	for _, v := range vecs {
		if len(v) != w.meta.Dim {
			return fmt.Errorf("вектор длиной %d при размерности %d", len(v), w.meta.Dim)
		}
		for _, q := range Quantize(v) {
			buf = append(buf, byte(q))
		}
	}
	if _, err := w.f.Write(buf); err != nil {
		return err
	}
	w.meta.Count += len(vecs)
	w.added += len(vecs)
	return nil
}

// Commit доводит данные до диска и только потом сохраняет счётчик.
func (w *VecWriter) Commit() (VecMeta, error) {
	if err := w.f.Sync(); err != nil {
		return w.meta, err
	}
	_, metaPath := vecPaths(w.dir)
	if err := writeJSON(metaPath, w.meta); err != nil {
		return w.meta, err
	}
	return w.meta, nil
}

// Added — сколько векторов дописано за эту работу.
func (w *VecWriter) Added() int { return w.added }

// Close закрывает файл. Незафиксированный хвост останется на диске
// и будет усечён при следующем открытии на дозапись.
func (w *VecWriter) Close() error { return w.f.Close() }

// DropVectors убирает векторы коллекции целиком.
func DropVectors(dir string) error {
	dat, meta := vecPaths(dir)
	if err := os.Remove(dat); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(meta); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Quantize нормирует вектор и переводит его в int8.
//
// Нормировка обязательна: без неё скалярное произведение зависит от длины
// вектора, а нужна только его направленность.
func Quantize(v []float32) []int8 {
	out := make([]int8, len(v))
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return out
	}
	for i, x := range v {
		q := math.Round(float64(x) / norm * 127)
		// Значения за пределами int8 брать неоткуда — длина вектора равна
		// единице, — но обрезка стоит дёшево и снимает вопрос.
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		out[i] = int8(q)
	}
	return out
}

// Cosine считает близость двух квантованных векторов.
func Cosine(a, b []int8) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot int64
	for i := range a {
		dot += int64(a[i]) * int64(b[i])
	}
	return float64(dot) / (127 * 127)
}

// vecBytes — сколько места занимают векторы на диске.
func vecBytes(dir string) int64 {
	dat, _ := vecPaths(dir)
	info, err := os.Stat(dat)
	if err != nil {
		return 0
	}
	return info.Size()
}

// copyVectors переносит векторы выживших кусков при уплотнении.
//
// Без этого уплотнение стало бы порчей данных: оно меняет сквозные номера
// кусков, а номер куска — это и есть адрес вектора.
func copyVectors(src *Vectors, dstDir string, keep []int) (VecMeta, error) {
	if src == nil || len(keep) == 0 {
		return VecMeta{}, nil
	}
	w, err := CreateVecWriter(dstDir, src.meta.Model, src.meta.Dim, 0)
	if err != nil {
		return VecMeta{}, err
	}
	defer w.Close()

	buf := make([]byte, 0, 4096*src.meta.Dim)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if _, err := w.f.Write(buf); err != nil {
			return err
		}
		buf = buf[:0]
		return nil
	}
	for _, id := range keep {
		vec := src.At(id)
		if vec == nil {
			// Покрытие — начальный отрезок, поэтому первый непокрытый кусок
			// означает конец: дальше векторов нет ни у кого.
			break
		}
		for _, q := range vec {
			buf = append(buf, byte(q))
		}
		w.meta.Count++
		if len(buf) >= 4096*src.meta.Dim {
			if err := flush(); err != nil {
				return VecMeta{}, err
			}
		}
	}
	if err := flush(); err != nil {
		return VecMeta{}, err
	}
	return w.Commit()
}
