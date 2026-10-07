package maint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// holdLock ставит признак сборки от живого процесса — этого же: так выглядит
// каталог графа, пока идёт --graph-build.
func holdLock(t *testing.T, dir string) {
	t.Helper()
	body := fmt.Sprintf("pid %d, начато %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, "LOCK"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Сухой прогон снятия склеек ничего не трогает (аудит 07.10.2026, №8):
// до правки --graph-merge-drop удалял журнал раньше проверки сухого прогона,
// и «посмотреть, что снимется» стоило журнала вердиктов арбитра.
func TestMergeDropDryLeavesGraphIntact(t *testing.T) {
	f := newMaintFixture(t)
	before := dirHash(t, f.dir)
	var out bytes.Buffer
	if err := Merge(&out, f.cfg, f.name, "", "strict", 0, true, true, false); err != nil {
		t.Fatalf("сухой прогон снятия: %v", err)
	}
	if dirHash(t, f.dir) != before {
		t.Fatalf("сухой прогон изменил каталог графа; файлы теперь: %v", dirFiles(t, f.dir))
	}
	for _, want := range []string{"поглощено понятий 1", "сухой прогон"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("в выводе нет %q:\n%s", want, out.String())
		}
	}
}

// Снятие не удаляет журнал, а откладывает его в копию с тем же содержимым,
// и граф после этого открывается без склеек.
func TestMergeDropKeepsBackup(t *testing.T) {
	f := newMaintFixture(t)
	journal := filepath.Join(f.dir, "merges.jsonl")
	orig, err := os.ReadFile(journal)
	if err != nil || len(orig) == 0 {
		t.Fatalf("в фикстуре нет журнала склеек: %v", err)
	}
	var out bytes.Buffer
	if err := Merge(&out, f.cfg, f.name, "", "strict", 0, true, false, true); err != nil {
		t.Fatalf("снятие с --kb-yes: %v", err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("журнал склеек остался на месте: %v", err)
	}
	baks, _ := filepath.Glob(journal + ".bak-*")
	if len(baks) != 1 {
		t.Fatalf("копий журнала %d (%v), ожидалась одна", len(baks), baks)
	}
	got, err := os.ReadFile(baks[0])
	if err != nil || !bytes.Equal(got, orig) {
		t.Fatalf("копия журнала не совпала с прежним журналом (%v)", err)
	}
	if !strings.Contains(out.String(), filepath.Base(baks[0])) {
		t.Errorf("в выводе не названа копия %s:\n%s", baks[0], out.String())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "LOCK")); !os.IsNotExist(err) {
		t.Errorf("замок сборки не снят после снятия склеек: %v", err)
	}
	g, err := graph.Open(f.coll, 0, f.cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if n := g.Merges().Count(); n != 0 {
		t.Errorf("после снятия поглощено понятий %d, ожидался 0", n)
	}
}

// Без --kb-yes и без терминала снятие не начинается: спросить некого.
func TestMergeDropRefusesWithoutConfirmation(t *testing.T) {
	f := newMaintFixture(t)
	before := dirHash(t, f.dir)
	err := Merge(io.Discard, f.cfg, f.name, "", "strict", 0, true, false, false)
	if err == nil {
		t.Fatal("склейки сняты без подтверждения и без терминала")
	}
	if !strings.Contains(err.Error(), "--kb-yes") {
		t.Errorf("в отказе не сказано, как быть в скрипте: %v", err)
	}
	if dirHash(t, f.dir) != before {
		t.Fatalf("отказ изменил каталог графа; файлы теперь: %v", dirFiles(t, f.dir))
	}
}

// Под идущей сборкой ни снятие, ни склейка не идут — даже с --kb-yes:
// сборка со связыванием сама дописывает журнал склеек.
func TestMergeRefusesUnderBuild(t *testing.T) {
	f := newMaintFixture(t)
	verdicts := filepath.Join(t.TempDir(), "verdicts.tsv")
	body := "вердикт\tcos\tсиноним\tцифры\tязык\tid_a\tid_b\tкниг_a\tкниг_b\n" +
		"ДА\t0.97\ttrue\tfalse\tfalse\t1\t4\t1\t1\n"
	if err := os.WriteFile(verdicts, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	holdLock(t, f.dir)
	before := dirHash(t, f.dir)

	err := Merge(io.Discard, f.cfg, f.name, "", "strict", 0, true, false, true)
	if !errors.Is(err, graph.ErrLocked) {
		t.Errorf("снятие под сборкой: %v, ожидался отказ по замку", err)
	}
	err = Merge(io.Discard, f.cfg, f.name, verdicts, "strict", 0, false, false, false)
	if !errors.Is(err, graph.ErrLocked) {
		t.Errorf("склейка под сборкой: %v, ожидался отказ по замку", err)
	}
	if dirHash(t, f.dir) != before {
		t.Fatalf("отказ под сборкой изменил каталог графа; файлы теперь: %v", dirFiles(t, f.dir))
	}
}

// Занятое имя копии не затирается: две правки в одну секунду дают две копии.
func TestAsideNameKeepsExistingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "merges.jsonl")
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	first, err := asideName(path, at)
	if err != nil || first != path+".bak-20261007-120000" {
		t.Fatalf("имя копии %q, %v", first, err)
	}
	if err := os.WriteFile(first, []byte("прежняя копия"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := asideName(path, at)
	if err != nil || second == first || !strings.HasPrefix(second, first) {
		t.Fatalf("второе имя копии %q (первое %q), %v", second, first, err)
	}
}
