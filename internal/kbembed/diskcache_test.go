package kbembed

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// resetDisk забывает файл кэша — так выглядит новый запуск программы.
func resetDisk() {
	disk.mu.Lock()
	disk.path, disk.lines = "", 0
	disk.mu.Unlock()
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
	}
	return n
}

// Вектор вопроса переживает перезапуск: ради этого кэш и лежит на диске —
// повторный вопрос при занятой карте отвечает из файла, а не ждёт сервер.
func TestDiskCacheSurvivesRestart(t *testing.T) {
	reset()
	resetDisk()
	t.Cleanup(func() { reset(); resetDisk() })
	path := filepath.Join(t.TempDir(), "cache", "queries.cache")

	useDiskCache(path)
	cachePutAndPersist("bge-m3", "что такое горутина", []float32{0.25, -1.5, 3})
	cachePutAndPersist("bge-m3", "что такое горутина", []float32{9, 9, 9}) // повтор не пишется

	reset()
	resetDisk()
	useDiskCache(path)
	v, ok := cacheGet("bge-m3", "что такое горутина")
	if !ok || len(v) != 3 || v[0] != 0.25 || v[1] != -1.5 || v[2] != 3 {
		t.Fatalf("после перезапуска вектор не вернулся или искажён: %v, %v", v, ok)
	}
	if n := countLines(t, path); n != 1 {
		t.Errorf("в файле %d строк, ожидалась одна — повтор записан", n)
	}
	// Запросы — личное: файл только для владельца.
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("права файла кэша %v, ожидалось 0600 (%v)", info.Mode().Perm(), err)
	}
}

// Испорченная строка — не повод терять остальные: файл дописывают два
// процесса, и обрыв посреди строки возможен.
func TestDiskCacheSkipsBrokenLines(t *testing.T) {
	reset()
	resetDisk()
	t.Cleanup(func() { reset(); resetDisk() })
	path := filepath.Join(t.TempDir(), "queries.cache")
	good := `{"m":"bge-m3","q":"канал","v":"` + encodeVec([]float32{1, 2}) + `"}`
	body := "{обрыв строки\n" + `{"m":"bge-m3","q":"плохой вектор","v":"!!!"}` + "\n" + good + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	useDiskCache(path)
	if v, ok := cacheGet("bge-m3", "канал"); !ok || len(v) != 2 || v[1] != 2 {
		t.Fatalf("целая строка после испорченных не прочиталась: %v, %v", v, ok)
	}
	if _, ok := cacheGet("bge-m3", "плохой вектор"); ok {
		t.Error("строка с нечитаемым вектором попала в кэш")
	}
}

// Файл не растёт без предела: дойдя до двух пределов строк, он переписывается
// содержимым памяти — не больше одного предела.
func TestDiskCacheRewritesWhenOverfull(t *testing.T) {
	reset()
	resetDisk()
	t.Cleanup(func() { reset(); resetDisk() })
	path := filepath.Join(t.TempDir(), "queries.cache")
	useDiskCache(path)
	for i := 0; i < 2*queryCacheMax+5; i++ {
		cachePutAndPersist("bge-m3", "вопрос "+strconv.Itoa(i), []float32{float32(i)})
	}
	if n := countLines(t, path); n > 2*queryCacheMax {
		t.Fatalf("в файле %d строк при пределе переписывания %d", n, 2*queryCacheMax)
	}
	// Последний вопрос не потерян переписыванием.
	reset()
	resetDisk()
	useDiskCache(path)
	last := "вопрос " + strconv.Itoa(2*queryCacheMax+4)
	if _, ok := cacheGet("bge-m3", last); !ok {
		t.Fatalf("после переписывания файла потерян последний вопрос %q", last)
	}
}

// Вектор кодируется без потерь: float32 туда и обратно — те же биты.
func TestVecCodecRoundTrip(t *testing.T) {
	in := []float32{0, -0.000123, 1e-30, 3.4e38, -7}
	out := decodeVec(encodeVec(in))
	if len(out) != len(in) {
		t.Fatalf("длина %d вместо %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("[%d] = %v, было %v", i, out[i], in[i])
		}
	}
	if decodeVec("не base64") != nil || decodeVec(encodeVec(in)[:3]) != nil {
		t.Error("испорченная запись вектора принята")
	}
}
