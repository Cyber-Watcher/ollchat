// Тесты пробы документа. Признаки порчи проверяются на строке, а не на файле
// книги: прибор должен считать их одинаково независимо от того, чем разобран
// файл, и тест не должен зависеть от библиотеки на диске.
package probe

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// Точки между буквами — тот самый признак, из-за которого прибор написан
// (6,34 % кусков библиотеки, этап 104, П6.5).
func TestFlawsCountsDotsBetweenLetters(t *testing.T) {
	// Четыре точки между буквами: этом|процессе, процессе|бизнес, бизнес|аспекты
	// и once|more. Точка после «году» стоит перед пробелом — это конец
	// предложения, признаком порчи она не является.
	text := "В.этом.процессе.бизнес аспекты. В 2024 году. And once.more"
	dots, soft, around := flaws(text)
	const want = 4 // «В.э», «м.п», «е.б» (совпадения не перекрываются) и «e.m»
	if dots != want {
		t.Errorf("точек между буквами: %d, ожидалось %d в %q", dots, want, text)
	}
	if soft != 0 {
		t.Errorf("мягких переносов: %d, ожидалось 0", soft)
	}
	if around != "" {
		t.Errorf("окно вокруг переноса при нуле переносов: %q", around)
	}
}

// Мягкие переносы считаются все четыре вида, а окно вокруг первого не рубит
// букву надвое — в `pagedump` оно резалось по байтам и портило вывод.
func TestFlawsSoftHyphensWindowIsValidUTF8(t *testing.T) {
	text := strings.Repeat("слово ", 20) + "пул­реквест " + strings.Repeat("ещё ", 20) +
		"со‐единение раз‑рыв связь⁃перенос"
	dots, soft, around := flaws(text)
	if soft != 4 {
		t.Errorf("мягких переносов: %d, ожидалось 4 (U+00AD, U+2010, U+2011, U+2043)", soft)
	}
	if dots != 0 {
		t.Errorf("точек между буквами: %d, ожидалось 0", dots)
	}
	if !utf8.ValidString(around) {
		t.Errorf("окно вокруг первого переноса не UTF-8: %q", around)
	}
	if !strings.ContainsRune(around, '­') {
		t.Errorf("в окне нет первого переноса: %q", around)
	}
	// Окно — не больше 121 знака (60 слева, сам знак, 60 справа).
	if n := len([]rune(around)); n > 121 {
		t.Errorf("окно шире 121 знака: %d", n)
	}
}

// Обрезка длинной строки идёт по знакам: иначе русский текст рубится посреди
// буквы, и вывод прибора нельзя прочитать.
func TestCutCountsRunesNotBytes(t *testing.T) {
	s := strings.Repeat("я", 50)
	got := cut(s, 10)
	if n := len([]rune(strings.TrimSuffix(got, "…"))); n != 10 {
		t.Errorf("обрезано до %d знаков, ожидалось 10: %q", n, got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("обрезка испортила UTF-8: %q", got)
	}
	if short := cut("коротко", 10); short != "коротко" {
		t.Errorf("короткая строка изменена: %q", short)
	}
}

// Прибор отказывается без файла, а не показывает пустую пробу.
func TestRunRefusesWithoutFile(t *testing.T) {
	var out bytes.Buffer
	if err := Run(&out, "", nil); err == nil {
		t.Fatal("без файла прибор обязан отказать")
	}
}

// Несуществующий файл — понятная ошибка с именем файла, а не паника: в
// `docprobe` здесь стоял panic(err).
func TestRunReportsMissingFile(t *testing.T) {
	var out bytes.Buffer
	err := Run(&out, "/несуществующий/путь/книга.pdf", nil)
	if err == nil {
		t.Fatal("на несуществующем файле прибор обязан вернуть ошибку")
	}
	if !strings.Contains(err.Error(), "книга.pdf") {
		t.Errorf("в ошибке нет имени файла: %v", err)
	}
}
