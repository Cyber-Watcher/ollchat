package graph

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Фаззинг NameAt (и EntitySnippet, который режет текст по его ответу).
//
// Настоящий прогон — с пределом времени и памяти; после него смотреть execs/sec
// в строке прогресса (ниже тысячи на ядро — вход слишком тяжёл или цель зависла):
//
//	GOMEMLIMIT=512MiB go test ./internal/graph/ -run '^$' -fuzz '^FuzzNameAt$' -fuzztime 60s
//
// Без -fuzz прогоняются только затравки — это обычный быстрый тест. Найденный
// вход go сам кладёт в internal/graph/testdata/fuzz/FuzzNameAt/ — оттуда он
// становится затравкой и дальше.
//
// Свойства (а не только «не паникует»):
//  1. длина строчной копии в рунах равна длине текста в рунах (класс находки 8
//     аудита 16.10.2026: ToLower менял длину в байтах);
//  2. ответ — -1 либо место в РУНАХ внутри текста, и он равен ответу прямого
//     перебора по условию: первое из имён, у которого есть вхождение отдельным
//     словом, и первое такое вхождение;
//  3. имя по этому месту совпадает с текстом без учёта регистра и стоит
//     отдельным словом;
//  4. повторный вызов даёт то же (нет скрытого состояния);
//  5. EntitySnippet не падает, не длиннее окна runes плюс два многоточия, а для
//     корректного UTF-8 и окна, вмещающего имя, содержит найденное имя.
//
// Память: вход длиннее 1<<16 знаков отбрасывается; имена — до 1<<12. NameAt
// выделяет память по длине текста (две копии рун), по числу из входа — нет;
// худший случай по времени квадратичный (длинное имя из почти совпавших рун),
// на лимитах выше он укладывается в доли секунды.

func FuzzNameAt(f *testing.F) {
	// Затравки — из entitysnippet_test.go и entitysnippet_lower_test.go.
	f.Add("Ⱥ Ⱥ postgresql", "PostgreSQL", "", uint8(120))
	f.Add("a good algorithm", "Go", "", uint8(40))
	f.Add("written in Go, fast", "Go", "golang", uint8(40))
	f.Add(strings.Repeat("İ", 300)+" PostgreSQL хранит данные в таблицах.", "PostgreSQL", "Postgres", uint8(120))
	f.Add(strings.Repeat("Ⱥ", 300)+" PostgreSQL хранит данные.", "postgresql", "", uint8(120))
	f.Add("Горутина — лёгкий поток; ГОРУТИНА тоже.", "горутина", "поток", uint8(30))
	f.Add("x1 x 1x x", "x", "1", uint8(5))
	f.Add("", "go", "", uint8(0))
	f.Add("текст без имён", "  ", "", uint8(10))
	f.Add("a\xffb go \xfe", "go", "\xff", uint8(20))

	f.Fuzz(func(t *testing.T, text, n1, n2 string, runes uint8) {
		if len(text) > 1<<16 || len(n1) > 1<<12 || len(n2) > 1<<12 {
			t.Skip()
		}
		names := []string{n1, n2}
		total := utf8.RuneCountInString(text)

		if got := len(lowerRunes(text)); got != total {
			t.Fatalf("lowerRunes: %d рун, в тексте %d", got, total)
		}

		at := NameAt(text, names)
		if again := NameAt(text, names); again != at {
			t.Fatalf("NameAt недетерминирован: %d и %d", at, again)
		}
		want, w := fuzzNameAtOracle(text, names)
		if at != want {
			t.Fatalf("NameAt(%q, %q) = %d, перебор даёт %d", text, names, at, want)
		}
		if at < -1 || at >= total && at != -1 {
			t.Fatalf("NameAt = %d вне текста из %d рун", at, total)
		}

		snip := EntitySnippet(text, names, int(runes))
		if n := utf8.RuneCountInString(snip); n > int(runes)+2 {
			t.Fatalf("EntitySnippet: %d рун при окне %d", n, runes)
		}
		// Окно вмещает имя, если оно короче окна за вычетом отступа влево на треть.
		if at >= 0 && utf8.ValidString(text) && len(w)+int(runes)/3 <= int(runes) {
			if !strings.Contains(string(lowerRunes(snip)), string(w)) {
				t.Fatalf("EntitySnippet(%q, %q, %d) = %q — без найденного имени %q на месте %d",
					text, names, runes, snip, string(w), at)
			}
		}
	})
}

// fuzzNameAtOracle — NameAt по условию, прямым перебором: место первого
// вхождения (в рунах) и само имя в строчных рунах; -1 и nil — не найдено.
// Имена идут по порядку списка: побеждает первое имя, у которого есть
// вхождение отдельным словом, а не самое раннее место среди всех.
func fuzzNameAtOracle(text string, names []string) (int, []rune) {
	var low []rune
	for _, c := range []rune(text) {
		low = append(low, unicode.ToLower(c))
	}
	isWordRune := func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }
	for _, n := range names {
		var w []rune
		for _, c := range []rune(strings.TrimSpace(n)) {
			w = append(w, unicode.ToLower(c))
		}
		if len(w) == 0 {
			continue
		}
		for i := 0; i+len(w) <= len(low); i++ {
			if low[i] != w[0] || string(low[i:i+len(w)]) != string(w) {
				continue
			}
			leftOK := i == 0 || !isWordRune(low[i-1])
			rightOK := i+len(w) == len(low) || !isWordRune(low[i+len(w)])
			if leftOK && rightOK {
				return i, w
			}
		}
	}
	return -1, nil
}
