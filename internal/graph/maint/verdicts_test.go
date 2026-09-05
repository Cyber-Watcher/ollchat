package maint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Разбор TSV вердиктов: уровень отбирает, «цифры» не склеиваются никогда,
// главным остаётся понятие из большего числа книг.
func TestReadVerdicts(t *testing.T) {
	head := strings.Join([]string{"cos", "синоним", "взаимный", "цифры", "язык", "id_a", "id_b", "книг_a", "книг_b", "вердикт", "причина"}, "\t")
	rows := []string{
		head,
		// ДА, синоним, высокая близость: проходит на любом уровне; b из большего числа книг — главный.
		"0.97\ttrue\ttrue\tfalse\tfalse\t10\t20\t2\t5\tДА\tодно и то же",
		// ДА, но «цифры»: не проходит никогда.
		"0.99\ttrue\ttrue\ttrue\tfalse\t30\t40\t3\t3\tДА\tверсии",
		// НЕТ: не проходит.
		"0.98\ttrue\ttrue\tfalse\tfalse\t50\t60\t1\t1\tНЕТ\tразное",
		// ДА без синонима и с низкой близостью: проходит только на all-yes.
		"0.60\tfalse\tfalse\tfalse\tfalse\t70\t80\t4\t1\tДА\tсомнительно",
	}
	path := filepath.Join(t.TempDir(), "v.tsv")
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	strict, err := readVerdicts(path, "strict", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(strict) != 1 || strict[0].keep != 20 || strict[0].drop != 10 || !strict[0].alias {
		t.Fatalf("strict: %+v", strict)
	}
	all, err := readVerdicts(path, "all-yes", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].keep != 70 || all[1].drop != 80 {
		t.Fatalf("all-yes: %+v", all)
	}
	// Порог одинаковости языка: пара 70/80 (не межъязыковая, cos 0.60) отсекается.
	same, err := readVerdicts(path, "all-yes", 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(same) != 1 {
		t.Fatalf("min-cos-same: %+v", same)
	}
	if _, err := readVerdicts(path, "нет-такого", 0); err == nil {
		t.Fatal("неизвестный уровень должен отклоняться")
	}
	bad := filepath.Join(t.TempDir(), "bad.tsv")
	_ = os.WriteFile(bad, []byte("cos\tid_a\n0.9\t1\n"), 0o644)
	if _, err := readVerdicts(bad, "strict", 0); err == nil {
		t.Fatal("файл без обязательных столбцов должен отклоняться")
	}
}
