package chatlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFormat(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat.md", true)
	defer l.Close()

	ts := time.Date(2026, 8, 11, 11, 42, 0, 0, time.Local)
	if err := l.WriteAt(ts, KindQuestion, "как работает num_ctx?"); err != nil {
		t.Fatalf("запись вопроса: %v", err)
	}
	if err := l.WriteAt(ts, KindAnswer, "num_ctx задаёт размер окна."); err != nil {
		t.Fatalf("запись ответа: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "chat.md"))
	if err != nil {
		t.Fatalf("чтение журнала: %v", err)
	}
	got := string(data)

	want := "2026.08.11 11:42 ----- Вопрос\n\nкак работает num_ctx?\n\n\n" +
		"2026.08.11 11:42 ----- Ответ\n\nnum_ctx задаёт размер окна.\n\n\n"
	if got != want {
		t.Errorf("формат журнала не совпадает.\nполучено:\n%q\nожидалось:\n%q", got, want)
	}
}

func TestWriteFromAddsModelName(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat.md", true)
	defer l.Close()

	ts := time.Date(2026, 8, 11, 14, 5, 0, 0, time.Local)
	if err := l.WriteAt(ts, KindQuestion, "как дела?"); err != nil {
		t.Fatalf("запись вопроса: %v", err)
	}
	if err := l.WriteFromAt(ts, KindAnswer, "qwen3.5:122b", "хорошо"); err != nil {
		t.Fatalf("запись ответа: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "chat.md"))
	if err != nil {
		t.Fatalf("чтение журнала: %v", err)
	}
	got := string(data)

	// Порядок строго задан: дата, время, пробел, модель в скобках, затем вид записи.
	want := "2026.08.11 14:05 ----- Вопрос\n\nкак дела?\n\n\n" +
		"2026.08.11 14:05 (qwen3.5:122b) ----- Ответ\n\nхорошо\n\n\n"
	if got != want {
		t.Errorf("журнал не совпадает.\nполучено:\n%q\nожидалось:\n%q", got, want)
	}
}

func TestWriteFromWithoutModelKeepsPlainHeader(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat.md", true)
	defer l.Close()

	ts := time.Date(2026, 8, 11, 14, 5, 0, 0, time.Local)
	// Пустое имя и имя из пробелов не должны давать пустых скобок.
	if err := l.WriteFromAt(ts, KindAnswer, "", "ответ без модели"); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if err := l.WriteFromAt(ts, KindAnswer, "   ", "и ещё один"); err != nil {
		t.Fatalf("запись: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "chat.md"))
	if strings.Contains(string(data), "()") || strings.Contains(string(data), "(   )") {
		t.Errorf("пустое имя модели не должно давать скобок:\n%s", string(data))
	}
	if n := strings.Count(string(data), "14:05 ----- Ответ"); n != 2 {
		t.Errorf("ожидалось 2 обычных заголовка, найдено %d:\n%s", n, string(data))
	}
}

func TestAppendNeverTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")

	existing := "2026.08.10 10:00 ----- Вопрос\n\nстарый вопрос\n\n\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("подготовка файла: %v", err)
	}

	// Два независимых запуска приложения подряд.
	for i := 0; i < 2; i++ {
		l := New(dir, "chat.md", true)
		if err := l.Write(KindQuestion, "новый вопрос"); err != nil {
			t.Fatalf("запись: %v", err)
		}
		l.Close()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	got := string(data)
	if !strings.HasPrefix(got, existing) {
		t.Error("прежнее содержимое журнала должно сохраняться")
	}
	if n := strings.Count(got, "новый вопрос"); n != 2 {
		t.Errorf("ожидалось 2 новые записи, найдено %d", n)
	}
}

func TestRotationByDate(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat-2006-01-02.md", true)
	defer l.Close()

	day1 := time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)

	if err := l.WriteAt(day1, KindQuestion, "первый день"); err != nil {
		t.Fatalf("запись дня 1: %v", err)
	}
	if err := l.WriteAt(day2, KindQuestion, "второй день"); err != nil {
		t.Fatalf("запись дня 2: %v", err)
	}

	for _, name := range []string{"chat-2026-08-11.md", "chat-2026-08-12.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("ожидался файл %s: %v", name, err)
		}
	}
}

func TestDisabledLoggerWritesNothing(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat.md", false)
	defer l.Close()

	if err := l.Write(KindQuestion, "вопрос"); err != nil {
		t.Fatalf("запись при выключенном журнале не должна возвращать ошибку: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chat.md")); !os.IsNotExist(err) {
		t.Error("выключенный журнал не должен создавать файл")
	}
}

// TestFormatEntryMatchesFile закрепляет, что вынесенное наружу форматирование
// даёт ровно то же, что ложится в файл: по этой функции собирается текст для
// буфера обмена, и разойтись с журналом она не имеет права.
func TestFormatEntryMatchesFile(t *testing.T) {
	dir := t.TempDir()
	l := New(dir, "chat.md", true)
	defer l.Close()

	ts := time.Date(2026, 8, 16, 12, 35, 0, 0, time.Local)
	if err := l.WriteFromAt(ts, KindAnswer, "qwen3.5:122b", "Горутина — это лёгкий поток."); err != nil {
		t.Fatalf("запись ответа: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "chat.md"))
	if err != nil {
		t.Fatalf("чтение журнала: %v", err)
	}
	want := FormatEntry(ts, KindAnswer, "qwen3.5:122b", "Горутина — это лёгкий поток.")
	if string(data) != want {
		t.Errorf("FormatEntry разошёлся с файлом:\nфайл: %q\nфункция: %q", string(data), want)
	}
	if !strings.HasPrefix(want, "2026.08.16 12:35 (qwen3.5:122b) ----- Ответ\n\n") {
		t.Errorf("заголовок записи не тот: %q", want)
	}
}

func TestFormatEntryOmitsEmptyModel(t *testing.T) {
	ts := time.Date(2026, 8, 16, 12, 30, 0, 0, time.Local)
	got := FormatEntry(ts, KindQuestion, "  ", "вопрос")
	if !strings.HasPrefix(got, "2026.08.16 12:30 ----- Вопрос\n\n") {
		t.Errorf("пустое имя модели не должно давать скобок: %q", got)
	}
	if !strings.HasSuffix(got, "вопрос\n\n\n") {
		t.Errorf("тело записи должно кончаться двумя пустыми строками: %q", got)
	}
}
