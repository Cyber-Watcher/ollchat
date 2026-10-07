package maint

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Сверка доктора вторым путём (ollscripts/graphdoctorcheck.py) сходится
// с доктором в ВЕРНЫХ числах (аудит 07.10.2026, раздел 4.6). Прежде прибор
// считал «размечено кусков» по всему журналу вместе с отметками удалённых
// книг и сходился с доктором потому, что доктор ошибался так же. И на графе
// без тем и без векторов понятий он падал трассировкой, хотя у доктора это
// обычные строки отчёта.
//
// Нет python3 — сверка не проверяется: прибор вспомогательный, а тесты
// не должны требовать ничего сверх Go.
func TestGraphDoctorCheckScriptAgrees(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 нет — сверка вторым путём здесь не проверяется")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "ollscripts", "graphdoctorcheck.py"))
	if err != nil {
		t.Fatal(err)
	}
	f := newMaintFixture(t)
	f.markMixed(t)
	var out bytes.Buffer
	if err := DoctorTo(&out, io.Discard, f.cfg, f.name); err != nil {
		t.Fatal(err)
	}
	doctor := out.String()
	got, err := exec.Command(py, "-I", script, f.name, "--kb-dir", f.cfg.KB.Dir).CombinedOutput()
	if err != nil {
		t.Fatalf("сверка упала: %v\n%s", err, got)
	}
	check := string(got)
	if strings.Contains(check, "Traceback") {
		t.Fatalf("сверка упала трассировкой:\n%s", check)
	}
	want := fmt.Sprintf("разобрано кусков 3 из %d (%d%%), осталось %d", f.keepChunks, 300/f.keepChunks, f.keepChunks-2)
	if got := lineWith(check, "разобрано кусков"); got != want {
		t.Errorf("сверка: %q, ожидалось %q", got, want)
	}
	for _, prefix := range []string{"разобрано кусков", "с понятиями", "отметок книг, которых в коллекции НЕТ"} {
		d, _, _ := strings.Cut(lineWith(doctor, prefix), " — ")
		if s := lineWith(check, prefix); d == "" || d != s {
			t.Errorf("доктор и сверка разошлись:\n  доктор  %q\n  сверка  %q", d, s)
		}
	}
	for _, want := range []string{"communities.json нет", "entities.vecmeta нет"} {
		if !strings.Contains(check, want) {
			t.Errorf("в сверке нет строки про %q:\n%s", want, check)
		}
	}
	if t.Failed() {
		t.Logf("доктор:\n%s\nсверка:\n%s", doctor, check)
	}
}
