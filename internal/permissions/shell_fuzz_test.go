package permissions

import (
	"strings"
	"testing"
)

// FuzzScanCommands — разбор строки модели не падает и не зацикливается ни на
// каком входе: паника здесь роняла бы интерфейс посреди хода, а вечный цикл
// вешал бы его на проверке разрешения. Затравка — записи из таблицы обходов.
func FuzzScanCommands(f *testing.F) {
	for _, cmd := range denyBypasses {
		f.Add(cmd)
	}
	f.Add("case x in (a|b) c;; esac")
	f.Add("$((")
	f.Add("${")
	f.Add("$'\\")
	f.Add("<<")
	f.Fuzz(func(t *testing.T, cmd string) {
		scan := scanCommands(cmd)
		for _, c := range scan.found {
			if strings.Contains(c.name, "/") {
				t.Fatalf("имя программы с каталогом: %q", c.name)
			}
		}
		_, _ = SplitWords(cmd)
	})
}
