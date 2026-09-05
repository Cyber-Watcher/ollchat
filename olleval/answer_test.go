package main

import "testing"

// ExtractCode берёт самый длинный блок языка.
func TestExtractCodeTakesLongestLanguageBlock(t *testing.T) {
	answer := "Было так:\n\n```go\nfunc a() {}\n```\n\nСтало:\n\n```go\nfunc a() int {\n\treturn 42\n}\n```\n"
	got := ExtractCode(answer, "go")
	if want := "func a() int {\n\treturn 42\n}\n"; got != want {
		t.Errorf("ExtractCode вернул %q, ожидалось %q", got, want)
	}
}

// ExtractCode без пометки языка.
func TestExtractCodeWithoutLanguageTag(t *testing.T) {
	answer := "```\nprint(1)\n```"
	if got := ExtractCode(answer, "python"); got != "print(1)\n" {
		t.Errorf("блок без пометки не подхвачен: %q", got)
	}
}

// ExtractCode пусто когда кода нет.
func TestExtractCodeEmptyWhenNoCode(t *testing.T) {
	if got := ExtractCode("Просто текст без кода.", "go"); got != "" {
		t.Errorf("ExtractCode = %q, ожидалась пустая строка", got)
	}
}

func TestMixedScriptWords(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"обычный русский текст", 0},
		{"goroutine и num_ctx — латиница отдельными словами", 0},
		{"гoрутина", 1}, // «o» латинская
		{"гoрутина и кoнтекст", 2},
		{"CI-система работает", 0},
	}
	for _, c := range cases {
		if got := MixedScriptWords(c.text); got != c.want {
			t.Errorf("MixedScriptWords(%q) = %d, ожидалось %d", c.text, got, c.want)
		}
	}
}

func TestRefused(t *testing.T) {
	if !Refused("Извините, но я не могу помочь с этим запросом.") {
		t.Error("отказ не распознан")
	}
	if Refused("Вот разбор конфига sshd: PermitRootLogin yes — опасно.") {
		t.Error("нормальный ответ принят за отказ")
	}
}
