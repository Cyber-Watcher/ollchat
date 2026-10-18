package maint

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Фаззинг loadTranslations — чтение файла-кэша переводов (строки JSON).
//
// Настоящий прогон — с пределом времени и памяти; после него смотреть execs/sec
// в строке прогресса (цель пишет файл на каждый вход, десятки тысяч в секунду
// не ждать; ниже сотни на ядро — смотреть, что тормозит):
//
//	GOMEMLIMIT=512MiB go test ./internal/kb/maint/ -run '^$' -fuzz '^FuzzLoadTranslations$' -fuzztime 60s
//
// Без -fuzz прогоняются только затравки — обычный быстрый тест. Найденный вход
// go сам кладёт в internal/kb/maint/testdata/fuzz/FuzzLoadTranslations/.
// Файл пишется только в t.TempDir().
//
// Свойства:
//  1. без паники на любых байтах; повторное чтение даёт тот же кэш;
//  2. при успехе кэш равен тому, что получается независимым разбором строк
//     (деление по «\n», json.Unmarshal в {q,en}; пустой вопрос или перевод
//     пропускается; при повторе вопроса побеждает последняя строка);
//  3. пустых ключей и значений в кэше нет;
//  4. строка длиннее предела сканера (4 МиБ) — ошибка, а не молчаливая потеря
//     хвоста и не раздутая память (отдельный TestLoadTranslationsLongLine).
//
// Память: вход длиннее 1<<16 байт отбрасывается; на лимит строки в 4 МиБ
// (его-то и держит сканер) смотрит отдельный тест ниже.

func FuzzLoadTranslations(f *testing.F) {
	// Затравки — из TestLoadTranslations (kbtranslate_test.go) и формата translateEntry.
	f.Add([]byte(`{"q":"Что такое горутина?","en":"What is a goroutine?"}
не json
{"q":"без перевода","en":""}
{"q":"Как работает канал?","en":"How does a channel work?"}
`))
	f.Add([]byte(`{"q":"a","en":"b","ms":12,"workers":4}`))
	f.Add([]byte(`{"q":"a","en":"b"}` + "\r\n" + `{"q":"a","en":"c"}`))
	f.Add([]byte(""))
	f.Add([]byte("\n\n\n"))
	f.Add([]byte(`{"q":"","en":"x"}`))
	f.Add([]byte(`{"q":"a","en":"b"`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`{"q":1,"en":2}`))
	f.Add([]byte(`{"q":"\ud800","en":"\xff"}`))
	f.Add([]byte("\x00\xff\xfe{"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		path := filepath.Join(t.TempDir(), "en.jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadTranslations(path)
		if err != nil {
			t.Fatalf("ошибка на входе короче предела строки (%d байт): %v", len(data), err)
		}
		want := fuzzTranslationsOracle(data)
		if len(got) != len(want) {
			t.Fatalf("кэш из %d записей, независимый разбор даёт %d", len(got), len(want))
		}
		for q, en := range got {
			if q == "" || en == "" {
				t.Fatalf("в кэше пустая запись %q → %q", q, en)
			}
			if want[q] != en {
				t.Fatalf("кэш[%q] = %q, независимый разбор даёт %q", q, en, want[q])
			}
		}
		again, err := loadTranslations(path)
		if err != nil || len(again) != len(got) {
			t.Fatalf("повторное чтение: %d записей, ошибка %v; первое — %d", len(again), err, len(got))
		}
	})
}

// fuzzTranslationsOracle — разбор кэша по условию, без сканера: строки по «\n»,
// каждая — JSON {"q","en"}; битые и неполные пропускаются.
func fuzzTranslationsOracle(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var e translateEntry
		if json.Unmarshal(line, &e) == nil && e.Query != "" && e.En != "" {
			out[e.Query] = e.En
		}
	}
	return out
}

// Строка длиннее предела сканера (4 МиБ) — ошибка, а память остаётся в разумных
// границах: сканер растит буфер удвоением до предела и дальше не идёт.
func TestLoadTranslationsLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "en.jsonl")
	line := `{"q":"` + strings.Repeat("я", 3*1024*1024) + `","en":"x"}` + "\n" // ~6 МиБ в байтах
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := loadTranslations(path)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatalf("строка длиннее предела принята молча: %d записей", len(got))
	}
	if got != nil {
		t.Errorf("при ошибке кэш должен быть nil, а он %v", got)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
		t.Errorf("чтение строки сверх предела выделило %d МиБ — предел буфера не держит память", grown>>20)
	}
}

// Путь — каталог, а не файл: ошибка чтения, не паника и не пустой кэш «как будто всё хорошо».
func TestLoadTranslationsDirectory(t *testing.T) {
	got, err := loadTranslations(t.TempDir())
	if err == nil {
		t.Fatalf("каталог прочитан как кэш: %v", got)
	}
}
