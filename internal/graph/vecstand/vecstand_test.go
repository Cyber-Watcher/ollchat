// Тесты стенда векторов. Проверяется то, из-за чего стенд и вынесен в общую
// библиотеку: чтение файлов вместе с проверкой длины (её не хватало трём
// приборам из четырёх) и счёт близости (этап 114, пункт Г6).
package vecstand

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// stand — каталог с файлами векторов: паспорт, сами векторы и, по просьбе,
// отметки. Значения задаются вызывающим, чтобы ожидаемое считалось из данных.
func stand(t *testing.T, dim, count int, data []int8, stamps []uint64) string {
	t.Helper()
	dir := t.TempDir()
	meta := map[string]any{"model": "bge-m3", "digest": "sha256:тест", "dim": dim, "count": count}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metaFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, len(data))
	for i, v := range data {
		b[i] = byte(v)
	}
	if err := os.WriteFile(filepath.Join(dir, vecFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if stamps != nil {
		st := make([]byte, len(stamps)*stampBytes)
		for i, s := range stamps {
			binary.LittleEndian.PutUint64(st[i*stampBytes:], s)
		}
		if err := os.WriteFile(filepath.Join(dir, stampFile), st, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Паспорт, векторы и отметки читаются целиком и без искажения знака: значения
// хранятся как int8, а в файле лежат байтами, и −1 не должна стать 255.
func TestLoadReadsPassportVectorsAndStamps(t *testing.T) {
	data := []int8{1, -1, 127, -128, 0, 3}
	dir := stand(t, 3, 2, data, []uint64{7, 9})
	v, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Model != "bge-m3" || v.Dim != 3 || v.Count != 2 {
		t.Errorf("паспорт прочитан неверно: модель %q, dim %d, count %d", v.Model, v.Dim, v.Count)
	}
	if len(v.Data) != len(data) {
		t.Fatalf("значений %d, ожидалось %d", len(v.Data), len(data))
	}
	for i := range data {
		if v.Data[i] != data[i] {
			t.Errorf("значение %d: %d вместо %d (знак потерян?)", i, v.Data[i], data[i])
		}
	}
	if len(v.Stamps) != 2 || v.Stamps[0] != 7 || v.Stamps[1] != 9 {
		t.Errorf("отметки прочитаны неверно: %v", v.Stamps)
	}
}

// Укороченный файл векторов — отказ, а не молчаливый счёт по мусору. Ровно эта
// проверка была лишь у одного прибора из четырёх.
func TestLoadRefusesShortVectorFile(t *testing.T) {
	// Паспорт обещает 2×3 = 6 значений, в файле их 5.
	dir := stand(t, 3, 2, []int8{1, 2, 3, 4, 5}, nil)
	if _, err := Load(dir); err == nil {
		t.Fatal("на файле короче паспорта стенд обязан отказать")
	}
}

// Отметок может не быть у старого графа — это не ошибка, но и не выдумка:
// список остаётся пустым, чтобы прибор сам решил, годится ли ему такой стенд.
func TestLoadWithoutStampsIsNotAnError(t *testing.T) {
	dir := stand(t, 2, 2, []int8{1, 2, 3, 4}, nil)
	v, err := Load(dir)
	if err != nil {
		t.Fatalf("без файла отметок стенд отказал: %v", err)
	}
	if len(v.Stamps) != 0 {
		t.Errorf("отметки взялись из ниоткуда: %v", v.Stamps)
	}
}

// Вектор по номеру нарезается ровно по Dim, а за границами номера нет.
func TestVectorSlicesByDim(t *testing.T) {
	dir := stand(t, 3, 2, []int8{1, 2, 3, 4, 5, 6}, nil)
	v, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := v.Vector(1)
	if !ok {
		t.Fatal("второго вектора нет")
	}
	want := []int8{4, 5, 6}
	for i := range want {
		if second[i] != want[i] {
			t.Errorf("второй вектор: %v, ожидалось %v", second, want)
			break
		}
	}
	if _, ok := v.Vector(2); ok {
		t.Error("вектор за границей выдан как существующий")
	}
	if _, ok := v.Vector(-1); ok {
		t.Error("отрицательный номер выдан как существующий")
	}
}

// Близость считается как косинус: сама с собой — единица, противоположный —
// минус единица, перпендикулярный — ноль. Значения выведены из определения,
// а не подогнаны под код.
func TestCosineMatchesDefinition(t *testing.T) {
	a := []int8{3, 4}
	if got := CosineRaw(a, a); math.Abs(got-1) > 1e-9 {
		t.Errorf("сам с собой: %v, ожидалась 1", got)
	}
	if got := CosineRaw(a, []int8{-3, -4}); math.Abs(got+1) > 1e-9 {
		t.Errorf("с противоположным: %v, ожидалась −1", got)
	}
	if got := CosineRaw([]int8{1, 0}, []int8{0, 1}); got != 0 {
		t.Errorf("с перпендикулярным: %v, ожидался 0", got)
	}
	// Считанное по определению: (3·4 + 4·3) / (5 · 5).
	want := float64(3*4+4*3) / (math.Sqrt(9+16) * math.Sqrt(16+9))
	if got := CosineRaw(a, []int8{4, 3}); math.Abs(got-want) > 1e-9 {
		t.Errorf("на произвольной паре: %v, по определению %v", got, want)
	}
}

// Два косинуса проекта дают одно и то же на НОРМИРОВАННЫХ векторах и разное
// на ненормированных. Ради этого CosineRaw и держится отдельно от kb.Cosine:
// приборы сравнивают векторы из разных графов, где нормировка не обещана.
func TestCosineRawAgreesWithKBOnNormalizedVectors(t *testing.T) {
	// Нормированный int8-вектор: длина ≈ 127, как пишет kb при сохранении.
	a := []int8{127, 0, 0}
	b := []int8{0, 127, 0}
	for _, pair := range [][2][]int8{{a, a}, {a, b}} {
		raw, lib := CosineRaw(pair[0], pair[1]), kb.Cosine(pair[0], pair[1])
		if math.Abs(raw-lib) > 1e-6 {
			t.Errorf("на нормированных векторах разошлись: CosineRaw %v, kb.Cosine %v", raw, lib)
		}
	}
	// Ненормированные: kb.Cosine занижает, потому что делит на постоянную.
	short := []int8{1, 0, 0}
	raw, lib := CosineRaw(short, short), kb.Cosine(short, short)
	if math.Abs(raw-1) > 1e-9 {
		t.Errorf("полный косинус сам с собой: %v, ожидалась 1", raw)
	}
	if lib >= raw {
		t.Errorf("kb.Cosine на коротком векторе дал %v — он обязан быть меньше полного %v", lib, raw)
	}
}

// Нулевой вектор не даёт ни единицы, ни деления на ноль: у понятия без вектора
// близость неизвестна, и прибор не должен принимать её за полное совпадение.
func TestCosineWithZeroVectorIsZero(t *testing.T) {
	if got := CosineRaw([]int8{0, 0}, []int8{1, 2}); got != 0 {
		t.Errorf("с нулевым вектором: %v, ожидался 0", got)
	}
	if got := CosineRaw([]int8{0, 0}, []int8{0, 0}); got != 0 {
		t.Errorf("два нулевых: %v, ожидался 0", got)
	}
}

// Снимок «молодого» реестра берёт последнюю запись понятия в пределах окна
// от первой и не берёт позднейшие: на этом стоит сравнение вектора с тем
// состоянием записи, на котором он посчитан.
func TestYoungStatesTakesTheLastRecordInsideTheWindow(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"id":1,"name":"Go","at":100}`,
		`{"id":1,"name":"Go (язык)","at":105}`, // внутри окна 10 с — должно победить
		`{"id":1,"name":"Golang","at":400}`,    // позже окна — не берётся
		`{"id":2,"name":"RAG","at":200}`,
		`{"id":0,"name":"битая запись","at":1}`, // без номера — пропускается
		`не json`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "entities.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := YoungStates(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) != 2 {
		t.Errorf("понятий в снимке %d, ожидалось 2 (битая запись и не-json отброшены)", len(state))
	}
	if got := state[1].Name; got != "Go (язык)" {
		t.Errorf("у понятия 1 в снимке %q, ожидалось «Go (язык)» — последняя запись внутри окна", got)
	}
	if got := state[2].Name; got != "RAG" {
		t.Errorf("у понятия 2 в снимке %q, ожидалось «RAG»", got)
	}
}

// Архивы реестра читаются раньше самого реестра: первая запись понятия может
// лежать в архиве, и без него окно считалось бы от более поздней записи.
func TestYoungStatesReadsBackupsBeforeTheRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "entities.jsonl.bak-1"),
		[]byte(`{"id":1,"name":"первое имя","at":100}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entities.jsonl"),
		[]byte(`{"id":1,"name":"позднее имя","at":500}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := YoungStates(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := state[1].Name; got != "первое имя" {
		t.Errorf("в снимке %q: окно отсчитано не от записи из архива", got)
	}
}
