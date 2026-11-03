package probes

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tsvHead = "кусок\tкнига\tтемпература\tпрогон\tпонятий\tсвязей\tвидимых\tимена\tсвязи\n"

func writeTSV(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Два сервера по одним кускам: при 0 A совпал сам с собой, B — сам с собой,
// а между ними на одном куске разошлось одно понятие. Прибор обязан это
// показать: «тот же состав» 2 из 2 внутри серверов и 1 из 2 между ними.
func TestCompareTwoServers(t *testing.T) {
	a := writeTSV(t, "a.tsv", tsvHead+
		"1#1\tк\t0.00\t1\t2\t1\t2\tgo | канал\tgo ~ канал\n"+
		"1#1\tк\t0.00\t2\t2\t1\t2\tgo | канал\tgo ~ канал\n"+
		"1#2\tк\t0.00\t1\t1\t0\t1\tmutex\t\n"+
		"1#2\tк\t0.00\t2\t1\t0\t1\tmutex\t\n")
	b := writeTSV(t, "b.tsv", tsvHead+
		"1#1\tк\t0.00\t1\t2\t1\t2\tgo | канал\tgo ~ канал\n"+
		"1#1\tк\t0.00\t2\t2\t1\t2\tgo | канал\tgo ~ канал\n"+
		"1#2\tк\t0.00\t1\t2\t0\t2\tmutex | замок\t\n"+
		"1#2\tк\t0.00\t2\t2\t0\t2\tmutex | замок\t\n"+
		"9#9\tк\t0.00\t1\t1\t0\t1\tлишний\t\n")
	var out bytes.Buffer
	if err := runCompare(&out, a, b); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"общих кусков: 2",
		"A: 1-й / 2-й                 1.000     2 из 2",
		"B: 1-й / 2-й                 1.000     2 из 2",
		"A против B, 1-й / 1-й        0.750     1 из 2",
		"нет строки (сбой прогона) A 0, B 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет строки %q в выводе:\n%s", want, got)
		}
	}
}

// Пропавший прогон (сбой запроса — stability строки не пишет) считается
// сбоем, а итог старого вида без колонки «связи» не выдумывает связей.
func TestCompareMissingRunAndOldTSV(t *testing.T) {
	old := "кусок\tкнига\tтемпература\tпрогон\tпонятий\tсвязей\tвидимых\tимена\n"
	a := writeTSV(t, "a.tsv", old+"1#1\tк\t0.00\t1\t1\t0\t1\tgo\n1#1\tк\t0.00\t2\t1\t0\t1\tgo\n")
	b := writeTSV(t, "b.tsv", old+"1#1\tк\t0.00\t1\t1\t0\t1\tgo\n")
	var out bytes.Buffer
	if err := runCompare(&out, a, b); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "нет строки (сбой прогона) A 0, B 1") {
		t.Errorf("пропавший прогон не посчитан сбоем:\n%s", got)
	}
	if !strings.Contains(got, "связей в итоге нет") {
		t.Errorf("итог без колонки связей показан со связями:\n%s", got)
	}
	if err := runCompare(&out, a, ""); err == nil {
		t.Error("без второго файла compare не отказал")
	}
}
