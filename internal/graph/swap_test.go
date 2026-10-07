package graph

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Подмена ставит новый файл на место, а прежний оставляет копией — тем же
// файлом на диске, без копирования байт.
func TestSwapInKeepsOldAsBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "журнал")
	must(t, os.WriteFile(path, []byte("прежний"), 0o644))
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	must(t, os.WriteFile(tmp, []byte("новый"), 0o644))

	bak, err := swapIn(path, tmp, path+".bak-1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "новый" {
		t.Fatalf("на месте журнала %q", b)
	}
	if b, _ := os.ReadFile(bak); string(b) != "прежний" {
		t.Fatalf("в копии %q", b)
	}
	if fi, err := os.Stat(bak); err != nil || !os.SameFile(fi, old) {
		t.Error("копия — не прежний файл, а новый с тем же содержимым")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("заготовка осталась после подмены")
	}
}

// Не вышло поставить новый файл — прежний на месте, лишней копии нет.
func TestSwapInKeepsPathOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "журнал")
	must(t, os.WriteFile(path, []byte("прежний"), 0o644))
	if _, err := swapIn(path, path+".нет-такой-заготовки", path+".bak-1"); err == nil {
		t.Fatal("подмена несуществующей заготовкой прошла")
	}
	if b, _ := os.ReadFile(path); string(b) != "прежний" {
		t.Fatalf("журнал после неудачной подмены: %q", b)
	}
	if _, err := os.Stat(path + ".bak-1"); !os.IsNotExist(err) {
		t.Error("после неудачной подмены осталась копия")
	}
}

// Копия с тем же именем (две чистки в одну секунду) не затирается: прежде
// второе переименование молча заменяло её, и терялся исходный журнал.
func TestForgetKeepsEarlierBackup(t *testing.T) {
	coll, chunk := buildFixture(t)
	dir := filepath.Join(coll, DirName)
	now := time.Now()
	var sentinels []string
	for _, at := range []time.Time{now, now.Add(time.Second)} {
		name := filepath.Join(dir, mentionsFile+".bak-"+at.Format("20060102-150405"))
		must(t, os.WriteFile(name, []byte("исходный журнал"), 0o644))
		sentinels = append(sentinels, name)
	}
	st, err := ForgetChunks(dir, func(k ChunkKey) bool { return k == chunk }, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sentinels {
		if b, err := os.ReadFile(name); err != nil || string(b) != "исходный журнал" {
			t.Fatalf("прежняя копия %s затёрта: %q, %v", filepath.Base(name), b, err)
		}
	}
	if len(st.Backups) == 0 {
		t.Fatal("копии чистки не названы")
	}
	for _, b := range st.Backups {
		if _, err := os.Stat(b); err != nil {
			t.Errorf("названная копия %s: %v", b, err)
		}
	}
}
