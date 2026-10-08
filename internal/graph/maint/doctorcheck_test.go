package maint

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
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
	f := newMaintFixture(t)
	f.markMixed(t)
	doctor, check := doctorAndCheck(t, f)
	want := fmt.Sprintf("разобрано кусков 3 из %d (%d%%), осталось %d", f.keepChunks, 300/f.keepChunks, f.keepChunks-2)
	if got := lineWith(check, "разобрано кусков"); got != want {
		t.Errorf("сверка: %q, ожидалось %q", got, want)
	}
	// Журналы фикстуры целы: сдвига нет, а 40 отметок книги 999, которой
	// нет даже в реестре, — третий счёт, не сдвиг.
	if got := lineWith(doctor, "журналы: записей с недопустимыми полями"); got != "журналы: записей с недопустимыми полями 0 (mentions.log 0, edges.log 0, progress.log 0)" {
		t.Errorf("доктор на целых журналах: %q", got)
	}
	agree(t, doctor, check, "разобрано кусков", "с понятиями", "отметок книг, которых в коллекции НЕТ",
		"журналы: записей с недопустимыми полями", "журналы: записей из кусков без отметки разбора",
		"журналы: записей с книгой не из реестра")
	if lineWith(check, "журналы: записей с книгой не из реестра") == "" {
		t.Error("сверка не назвала 40 отметок книги не из реестра")
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

// Журналы, дописанные после обрывка (так писал код до 07.10.2026), оба
// прибора разбирают каждый своим кодом — и называют одни и те же записи
// с недопустимыми полями, на тех же смещениях. Доктор при этом не говорит
// «всё в порядке»: первым шагом — снять архив и граф не править.
func TestGraphDoctorCheckScriptAgreesOnShift(t *testing.T) {
	f := newMaintFixture(t)
	f.markMixed(t)
	// Понятия фикстуры — 1..4, книга keep, куски keepOrds. Обрывки:
	// у упоминаний 3 байта (в старший байт понятия каждой следующей записи
	// попадает младший байт номера понятия — номер за границей), у связей
	// 16 (на место конца встаёт вес), у отметок 8 (признак берётся из номера
	// книги; у фикстуры он мал и признак выходит допустимым — такой сдвиг
	// виден только чужими книгами, и приборы обязаны сойтись и на этом).
	// Перед обрывком — одно целое упоминание куска, которого нет среди
	// отметок: счёт «без отметки» тоже сверяется не на нуле.
	var ment, edges, marks []byte
	m := func(ent uint32, ord uint32) []byte {
		b := make([]byte, 12)
		binary.LittleEndian.PutUint32(b[0:], ent)
		binary.LittleEndian.PutUint32(b[4:], f.keep)
		binary.LittleEndian.PutUint32(b[8:], ord)
		return b
	}
	e := func(src, dst uint32, ord uint32) []byte {
		b := make([]byte, 24)
		binary.LittleEndian.PutUint32(b[0:], src)
		binary.LittleEndian.PutUint32(b[4:], dst)
		b[8] = graph.RelUses
		binary.LittleEndian.PutUint32(b[12:], math.Float32bits(1))
		binary.LittleEndian.PutUint32(b[16:], f.keep)
		binary.LittleEndian.PutUint32(b[20:], ord)
		return b
	}
	p := func(ord uint32) []byte {
		b := make([]byte, 12)
		binary.LittleEndian.PutUint32(b[0:], f.keep)
		binary.LittleEndian.PutUint32(b[4:], ord)
		binary.LittleEndian.PutUint32(b[8:], graph.MarkDone)
		return b
	}
	ment = append(ment, m(4, 1_000_000)...)
	ment = append(ment, m(2, f.keepOrds[0])[:3]...)
	edges = append(edges, e(1, 2, f.keepOrds[0])[:16]...)
	marks = append(marks, p(f.keepOrds[3])[:8]...)
	for i := uint32(0); i < 5; i++ {
		ord := f.keepOrds[i%2]
		ment = append(ment, m(1+i%4, ord)...)
		edges = append(edges, e(1+i%4, 1+(i+1)%4, ord)...)
		marks = append(marks, p(f.keepOrds[3])...)
	}
	for name, b := range map[string][]byte{"mentions.log": ment, "edges.log": edges, "progress.log": marks} {
		fh, err := os.OpenFile(filepath.Join(f.dir, name), os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fh.Write(b); err != nil {
			t.Fatal(err)
		}
		if err := fh.Close(); err != nil {
			t.Fatal(err)
		}
	}
	doctor, check := doctorAndCheck(t, f)
	line := lineWith(doctor, "журналы: записей с недопустимыми полями")
	if !strings.Contains(line, "(mentions.log 5,") || strings.HasPrefix(line, "журналы: записей с недопустимыми полями 0 ") {
		t.Errorf("доктор не увидел сдвига: %q — после трёхбайтового обрывка недопустимы все 5 упоминаний", line)
	}
	if line := lineWith(check, "журналы: записей из кусков без отметки разбора"); !strings.Contains(line, "(mentions.log 1,") {
		t.Errorf("сверка не увидела упоминания куска без отметки: %q", line)
	}
	agree(t, doctor, check, "журналы: записей с недопустимыми полями",
		"ВНИМАНИЕ: mentions.log", "ВНИМАНИЕ: edges.log", "ВНИМАНИЕ: progress.log",
		"журналы: записей из кусков без отметки разбора", "mentions.log: первая", "edges.log: первая",
		"журналы: записей с книгой не из реестра",
		"журналы: нецелый хвост mentions.log", "журналы: нецелый хвост edges.log", "журналы: нецелый хвост progress.log")
	if !strings.Contains(doctor, "ollchat --graph-archive "+f.name+" — и больше ничего: граф не править") {
		t.Errorf("в «что сделать» нет шага о сдвиге:\n%s", doctor)
	}
	if t.Failed() {
		t.Logf("доктор:\n%s\nсверка:\n%s", doctor, check)
	}
}

// doctorAndCheck — вывод доктора и второго прибора на одной фикстуре.
// Нет python3 — тест пропускается: прибор вспомогательный, а тесты
// не должны требовать ничего сверх Go.
func doctorAndCheck(t *testing.T, f *maintFixture) (doctor, check string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 нет — сверка вторым путём здесь не проверяется")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "ollscripts", "graphdoctorcheck.py"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := DoctorTo(&out, io.Discard, f.cfg, f.name); err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command(py, "-I", script, f.name, "--kb-dir", f.cfg.KB.Dir).CombinedOutput()
	if err != nil {
		t.Fatalf("сверка упала: %v\n%s", err, got)
	}
	if strings.Contains(string(got), "Traceback") {
		t.Fatalf("сверка упала трассировкой:\n%s", got)
	}
	return out.String(), string(got)
}

// agree — строки доктора и сверки с этими началами совпадают до « — »;
// строки, которой нет ни у одного, нет и у другого.
func agree(t *testing.T, doctor, check string, prefixes ...string) {
	t.Helper()
	for _, prefix := range prefixes {
		d, _, _ := strings.Cut(lineWith(doctor, prefix), " — ")
		s, _, _ := strings.Cut(lineWith(check, prefix), " — ")
		if d != s {
			t.Errorf("доктор и сверка разошлись по «%s»:\n  доктор  %q\n  сверка  %q", prefix, d, s)
		}
	}
}
