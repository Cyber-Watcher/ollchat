// Пакет vecstand — стенд для замеров свежести векторов понятий: чтение файлов
// векторов графа с их паспортом и отметками, близость двух векторов и снимок
// реестра «каким он был в первые секунды жизни понятия». Только читает; ни
// граф целиком, ни карта не нужны.
//
// **Зачем отдельной библиотекой.** Четыре прибора этапа 95 (`vecage`,
// `vecyoung`, `vecaliasfind`, `veccompare`) носили этот стенд каждый свой
// копией, и копии разошлись: у `vecaliasfind` в `loadVecs` не было проверки
// «файл векторов короче паспорта» и не читались отметки — то есть на битом
// файле он молча считал мусор, а `veccompare` на том же файле отказывался.
// Пункт Г6 этапа 114, слово владельца 30.09.2026.
//
// **Что такое «свежесть вектора».** Вектор понятия считается по его имени
// и описанию; и то и другое меняется по ходу сборки, а вектор остаётся от
// прежнего состояния. Отметка (`entities.vecstamp`) говорит, на какую версию
// записи вектор посчитан, паспорт (`entities.vecmeta`) — какой моделью.
package vecstand

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Имена файлов стенда в каталоге графа.
const (
	metaFile  = "entities.vecmeta"
	vecFile   = "entities.vec"
	stampFile = "entities.vecstamp"
)

// stampBytes — размер одной отметки в `entities.vecstamp`.
const stampBytes = 8

// Vectors — векторы понятий с паспортом и отметками, как они лежат на диске.
// Значения — int8: векторы хранятся сжатыми до байта на измерение.
type Vectors struct {
	Model  string   // какой моделью посчитаны (из паспорта)
	Digest string   // отпечаток модели
	Dim    int      // измерений в одном векторе
	Count  int      // сколько векторов
	Data   []int8   // Count × Dim подряд
	Stamps []uint64 // на какую версию записи понятия посчитан вектор
}

// Load — прочитать векторы понятий из каталога графа. Отметок может не быть
// (старый граф) — тогда Stamps пуст, и это не ошибка: приборы, которым отметки
// нужны, проверяют длину сами.
func Load(dir string) (*Vectors, error) {
	var meta struct {
		Model  string `json:"model"`
		Digest string `json:"digest"`
		Dim    int    `json:"dim"`
		Count  int    `json:"count"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", metaFile, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, vecFile))
	if err != nil {
		return nil, err
	}
	// Проверка была только у одного прибора из четырёх, и именно её
	// не хватало остальным: на укороченном файле счёт идёт по мусору.
	if len(b) < meta.Count*meta.Dim {
		return nil, fmt.Errorf("%s: файл векторов короче паспорта (%d байт против %d×%d)",
			dir, len(b), meta.Count, meta.Dim)
	}
	d := make([]int8, meta.Count*meta.Dim)
	for i := range d {
		d[i] = int8(b[i])
	}
	v := &Vectors{Model: meta.Model, Digest: meta.Digest, Dim: meta.Dim, Count: meta.Count, Data: d}
	if st, err := os.ReadFile(filepath.Join(dir, stampFile)); err == nil {
		v.Stamps = make([]uint64, len(st)/stampBytes)
		for i := range v.Stamps {
			v.Stamps[i] = binary.LittleEndian.Uint64(st[i*stampBytes:])
		}
	}
	return v, nil
}

// Vector — вектор под номером i. Второе значение ложно, если номера нет.
// Назван не `At`, чтобы не путать с `graph.Entity.At` — там это время записи.
func (v *Vectors) Vector(i int) ([]int8, bool) {
	if i < 0 || i >= v.Count {
		return nil, false
	}
	return v.Data[i*v.Dim : (i+1)*v.Dim], true
}

// CosineRaw — полный косинус с настоящими нормами обоих векторов.
//
// **Не путать с `kb.Cosine`**: тот делит скалярное произведение на постоянную
// 127² и потому верен только для векторов, уже нормированных при записи.
// Здесь нормы считаются по самим данным, поэтому счёт верен и для векторов
// из разных графов, где нормировка не обещана. На нормированных векторах
// оба дают одно и то же — это проверяет тест.
//
// Какой из двух вариантов правильнее для сравнения графов — вопрос к замеру;
// здесь сохранено поведение перенесённого прибора (`veccompare`), чтобы
// перенос не менял чисел.
func CosineRaw(a, b []int8) float64 {
	var dot, na, nb int64
	for i := range a {
		x, y := int64(a[i]), int64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float64(dot) / math.Sqrt(float64(na)*float64(nb))
}

// YoungStates — снимок реестра понятий «каким он был в первые lagSec секунд
// жизни каждого понятия»: по всем архивам реестра и самому реестру берётся
// последняя запись понятия, сделанная не позже, чем через lagSec после первой.
// Этим сравнивают вектор с тем состоянием записи, на котором он посчитан.
func YoungStates(dir string, lagSec int64) (map[uint32]graph.Entity, error) {
	baks, _ := filepath.Glob(filepath.Join(dir, "entities.jsonl.bak-*"))
	sort.Strings(baks)
	files := append(baks, filepath.Join(dir, "entities.jsonl"))
	first := map[uint32]int64{}
	state := map[uint32]graph.Entity{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var e graph.Entity
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.ID == 0 {
				continue
			}
			if _, ok := first[e.ID]; !ok {
				first[e.ID] = e.At
			}
			if e.At <= first[e.ID]+lagSec {
				state[e.ID] = e
			}
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		f.Close()
	}
	return state, nil
}
