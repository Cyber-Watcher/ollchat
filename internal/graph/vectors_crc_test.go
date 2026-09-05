package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Паспорт с контрольной суммой отвергает данные от другой записи; паспорт
// старого образца без суммы принимается как есть.
func TestEntityVectorsCRC(t *testing.T) {
	dir := t.TempDir()
	v := &EntityVectors{dir: dir}
	data := []int8{1, 2, 3, 4, 5, 6}
	if err := v.save("m", 3, data); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := openEntityVectors(dir)
	if !got.Ready() || got.Problem() != "" {
		t.Fatalf("свежая запись должна читаться: ready=%v problem=%q", got.Ready(), got.Problem())
	}

	// Подменяем данные при том же размере: сумма расходится.
	if err := os.WriteFile(filepath.Join(dir, entVecDataFile), []byte{9, 9, 9, 9, 9, 9}, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := openEntityVectors(dir)
	if bad.Ready() || bad.Problem() == "" {
		t.Fatalf("подменённые данные должны отвергаться: ready=%v problem=%q", bad.Ready(), bad.Problem())
	}

	// Паспорт без суммы — старый образец — принимает те же данные.
	raw, _ := json.Marshal(entVecMeta{Magic: entVecMagic, Model: "m", Dim: 3, Count: 2})
	if err := os.WriteFile(filepath.Join(dir, entVecMetaFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	old := openEntityVectors(dir)
	if !old.Ready() || old.Problem() != "" {
		t.Fatalf("паспорт без суммы должен приниматься: ready=%v problem=%q", old.Ready(), old.Problem())
	}
}
