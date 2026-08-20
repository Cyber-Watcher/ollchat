package chatlog

import (
	"fmt"
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

	// Идентификатор обмена стоит перед датой. Запись вне обмена — номер 00.
	id := "[" + l.SessionID() + "-00] "
	want := id + "2026.08.11 11:42 ----- Вопрос\n\nкак работает num_ctx?\n\n\n" +
		id + "2026.08.11 11:42 ----- Ответ\n\nnum_ctx задаёт размер окна.\n\n\n"
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

	// Порядок строго задан: идентификатор обмена в скобках, дата, время,
	// пробел, модель в скобках, затем вид записи.
	id := "[" + l.SessionID() + "-00] "
	want := id + "2026.08.11 14:05 ----- Вопрос\n\nкак дела?\n\n\n" +
		id + "2026.08.11 14:05 (qwen3.5:122b) ----- Ответ\n\nхорошо\n\n\n"
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
	want := FormatEntry(l.TurnID(), ts, KindAnswer, "qwen3.5:122b", "Горутина — это лёгкий поток.")
	if string(data) != want {
		t.Errorf("FormatEntry разошёлся с файлом:\nфайл: %q\nфункция: %q", string(data), want)
	}
	head := "[" + l.SessionID() + "-00] 2026.08.16 12:35 (qwen3.5:122b) ----- Ответ\n\n"
	if !strings.HasPrefix(want, head) {
		t.Errorf("заголовок записи не тот:\nполучено: %q\nожидалось начало: %q", want, head)
	}
}

func TestFormatEntryOmitsEmptyModel(t *testing.T) {
	ts := time.Date(2026, 8, 16, 12, 30, 0, 0, time.Local)
	got := FormatEntry("", ts, KindQuestion, "  ", "вопрос")
	if !strings.HasPrefix(got, "2026.08.16 12:30 ----- Вопрос\n\n") {
		t.Errorf("пустое имя модели не должно давать скобок: %q", got)
	}
	if !strings.HasSuffix(got, "вопрос\n\n\n") {
		t.Errorf("тело записи должно кончаться двумя пустыми строками: %q", got)
	}
}

// sessionLogger — журнал с шаблоном «файл на запуск» и заданным временем старта.
func sessionLogger(t *testing.T, dir string, start time.Time) *Logger {
	t.Helper()
	p, err := ParsePattern("chat-%Y-%m-%d_%H-%M-%S.md")
	if err != nil {
		t.Fatalf("разбор шаблона: %v", err)
	}
	return NewFromPattern(dir, p, start, true)
}

func TestSessionPatternNamesFileByStart(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 20, 14, 30, 5, 0, time.Local)
	l := sessionLogger(t, dir, start)
	defer l.Close()

	want := filepath.Join(dir, "chat-2026-08-20_14-30-05.md")
	if got := l.CurrentPath(); got != want {
		t.Fatalf("до записи CurrentPath = %q, ожидалось %q", got, want)
	}
	if err := l.WriteAt(start, KindQuestion, "вопрос"); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if got := l.CurrentPath(); got != want {
		t.Fatalf("после записи CurrentPath = %q, ожидалось %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("ожидался файл %s: %v", want, err)
	}
}

// TestSessionPatternDoesNotRotate: экземпляр, проработавший через полночь,
// продолжает писать в файл своего запуска — имя фиксировано на старте.
func TestSessionPatternDoesNotRotate(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 20, 23, 59, 0, 0, time.Local)
	l := sessionLogger(t, dir, start)
	defer l.Close()

	if err := l.WriteAt(start, KindQuestion, "до полуночи"); err != nil {
		t.Fatalf("запись до полуночи: %v", err)
	}
	if err := l.WriteAt(start.Add(2*time.Hour), KindAnswer, "после полуночи"); err != nil {
		t.Fatalf("запись после полуночи: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("ожидался один файл, получено %v", names)
	}
	body, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"до полуночи", "после полуночи"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в файле нет записи %q", want)
		}
	}
}

// TestSessionPatternAvoidsCollision: два экземпляра, запущенные в одну и ту же
// секунду (разные окна tmux, разные сеансы ssh), не должны делить один файл.
func TestSessionPatternAvoidsCollision(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 20, 14, 30, 5, 0, time.Local)

	first := sessionLogger(t, dir, start)
	defer first.Close()
	second := sessionLogger(t, dir, start)
	defer second.Close()
	third := sessionLogger(t, dir, start)
	defer third.Close()

	for i, l := range []*Logger{first, second, third} {
		if err := l.WriteAt(start, KindQuestion, fmt.Sprintf("экземпляр %d", i+1)); err != nil {
			t.Fatalf("запись экземпляра %d: %v", i+1, err)
		}
	}

	want := []string{
		"chat-2026-08-20_14-30-05.md",
		"chat-2026-08-20_14-30-05-2.md",
		"chat-2026-08-20_14-30-05-3.md",
	}
	for i, name := range want {
		path := filepath.Join(dir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ожидался файл %s: %v", name, err)
		}
		if !strings.Contains(string(body), fmt.Sprintf("экземпляр %d", i+1)) {
			t.Errorf("файл %s достался не тому экземпляру: %s", name, body)
		}
	}
}

// TestSessionPatternReturnsToOwnFile: /log off закрывает файл, /log on должен
// вернуть в него же, а не занять новое имя с суффиксом.
func TestSessionPatternReturnsToOwnFile(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 20, 14, 30, 5, 0, time.Local)
	l := sessionLogger(t, dir, start)
	defer l.Close()

	if err := l.WriteAt(start, KindQuestion, "до выключения"); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	l.SetEnabled(false)
	l.SetEnabled(true)
	if err := l.WriteAt(start, KindQuestion, "после включения"); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("ожидался один файл, получено %d", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(dir, "chat-2026-08-20_14-30-05.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "до выключения") || !strings.Contains(string(body), "после включения") {
		t.Fatalf("обе записи должны лежать в одном файле: %s", body)
	}
}

// TestSessionPatternDisabledCreatesNothing: выключенный журнал не занимает имя.
func TestSessionPatternDisabledCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 20, 14, 30, 5, 0, time.Local)
	p, err := ParsePattern("chat-%Y-%m-%d_%H-%M-%S.md")
	if err != nil {
		t.Fatal(err)
	}
	l := NewFromPattern(dir, p, start, false)
	defer l.Close()

	if err := l.WriteAt(start, KindQuestion, "вопрос"); err != nil {
		t.Fatalf("запись при выключенном журнале не должна возвращать ошибку: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("выключенный журнал создал файлы: %d", len(entries))
	}
}
