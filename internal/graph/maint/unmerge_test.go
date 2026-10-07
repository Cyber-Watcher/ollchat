package maint

import (
	"os"
	"path/filepath"
	"testing"
)

// Список снятия склеек: столбцы from и to ищутся по заголовку, без
// заголовка — первый и второй; выживший может быть нулём («куда бы ни вело»);
// опечатка в номере — ошибка, а не пропуск строки.
func TestReadUnmergeList(t *testing.T) {
	dir := t.TempDir()
	read := func(body string) ([][2]uint32, error) {
		path := filepath.Join(dir, "list.tsv")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return readUnmergeList(path)
	}
	got, err := read("// перепись цепочек\n# и так тоже комментарий\n\n12\t7\tпричина\n13\n")
	if err != nil || len(got) != 2 || got[0] != [2]uint32{12, 7} || got[1] != [2]uint32{13, 0} {
		t.Fatalf("без заголовка: %v, %v", got, err)
	}
	// Файл переписи chaincensus.py: столбцы по именам, в другом порядке.
	got, err = read("cos\tto\tfrom\tname\n0.71\t7\t12\tAPI secret\n0.64\t9\t15\tSecrets API\n")
	if err != nil || len(got) != 2 || got[0] != [2]uint32{12, 7} || got[1] != [2]uint32{15, 9} {
		t.Fatalf("по заголовку: %v, %v", got, err)
	}
	for _, bad := range []string{
		"12\t7\n1x\t7\n",  // опечатка в номере поглощённого
		"12\tseven\n",     // выживший не номер
		"0\t7\n",          // нулевой номер поглощённого
		"from\tto\n\t7\n", // пустой номер поглощённого
	} {
		if got, err := read(bad); err == nil {
			t.Errorf("список %q принят: %v", bad, got)
		}
	}
}
