package maint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Ход сборки пишется в указанный файл полными строками.
//
// Проверка нужна потому, что ради этого ключ и заведён: вывод в терминал
// копится буферами, когда его пропускают через tail, sed или awk, и ход
// сборки не появляется в журнале до конца работы — за сутки это случилось
// трижды. Программа обязана писать туда, куда велено, а не туда, куда её
// перенаправили.
func TestGraphProgressWritesLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ход.log")
	report := graphProgress(path)

	report(graph.BuildProgress{
		Total: 100, Done: 7, Entities: 42, Edges: 99,
		Elapsed: 3 * time.Second, Book: "Essential GraphRAG",
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("журнал хода не создан: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		t.Fatal("журнал хода пуст")
	}
	for _, want := range []string{"7/100", "понятий 42", "связей 99", "Essential GraphRAG"} {
		if !strings.Contains(line, want) {
			t.Errorf("в строке нет %q: %s", want, line)
		}
	}
	// Отметка времени вида 15:04:05 — по ней в журнале видно скорость,
	// а без неё строки неотличимы одна от другой.
	if len(line) < 8 || strings.Count(line[:8], ":") != 2 {
		t.Errorf("строка начинается не с отметки времени: %q", line[:min(12, len(line))])
	}
	// Возврата каретки в файле быть не должно: он хорош в терминале
	// и бесполезен там, где нужна история.
	if strings.ContainsRune(line, '\r') {
		t.Error("в журнал попал возврат каретки")
	}
}

// Недоступный путь не роняет сборку: журнал хода — удобство, а не условие
// работы. Сборка идёт часами, и терять её из-за опечатки в пути нельзя.
func TestGraphProgressBadPathSurvives(t *testing.T) {
	report := graphProgress(filepath.Join(t.TempDir(), "нет-такого-каталога", "ход.log"))
	report(graph.BuildProgress{Total: 10, Done: 1, Book: "книга"})
}

// Без пути ключ выключен и файлов не появляется.
func TestGraphProgressNoPath(t *testing.T) {
	dir := t.TempDir()
	report := graphProgress("")
	report(graph.BuildProgress{Total: 10, Done: 1, Book: "книга"})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("без пути появились файлы: %v", entries)
	}
}
