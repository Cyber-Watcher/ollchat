// Тесты переписей про оглавления. Проверяется то, что можно проверить без
// библиотеки на диске: признаки строк и согласие прибора с правилом kb —
// именно расхождение прибора с правилом и было причиной слияния (этап 114, Г4).
package census

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Прибор калибровки обязан мерить ШИРЕ правила: он считает строки, кончающиеся
// любой цифрой, тогда как правило требует номер страницы после пробела или
// отточия. Если бы прибор считал ровно как правило, калибровать было бы нечего.
func TestDigitEndShareIsWiderThanTheRule(t *testing.T) {
	// «edx=00000» — строка дампа регистров: цифрой кончается, номером
	// страницы по правилу kb не считается (замер 07.09.2026 на стенограмме WinDbg).
	text := "Введение в тему 12\nГлава вторая 345\nregs: edx=00000\nОбычный абзац по делу."
	lines, ends := digitEndShare(text)
	if lines != 4 {
		t.Fatalf("непустых строк %d, ожидалось 4", lines)
	}
	if ends != 3 {
		t.Errorf("кончаются цифрой %d, ожидалось 3 (две строки оглавления и дамп регистров)", ends)
	}
	byRule := 0
	for _, l := range strings.Split(text, "\n") {
		if kb.LineEndsWithPageNumber(strings.TrimSpace(l)) {
			byRule++
		}
	}
	if byRule != 2 {
		t.Errorf("по правилу kb номером страницы кончаются %d строк, ожидалось 2", byRule)
	}
	if ends <= byRule {
		t.Errorf("прибор (%d) должен считать шире правила (%d), иначе он бесполезен для калибровки", ends, byRule)
	}
}

// Правило kb, которым мерит разведка EPUB, — то самое, что решает судьбу куска
// при нарезке. Проверяется через LooksLikeTOC, то есть тем же путём, что работа.
func TestMeasureUsesTheLiveTOCRule(t *testing.T) {
	// Четыре строки с номерами страниц — по правилу это оглавление
	// (порог: не меньше четырёх строк и половина с номерами).
	toc := "Введение . . . 7\nПервая глава . . . 12\nВторая глава . . . 45\nПриложение . . . 200"
	st := measure(toc)
	if st.pageEnds != 4 {
		t.Errorf("строк с номером страницы %d, ожидалось 4", st.pageEnds)
	}
	if !kb.LooksLikeTOC(toc) {
		t.Error("правило kb не признало оглавлением текст, который прибор считает таким целиком")
	}
	// Отточие шрифтом без Unicode (18.09.2026) — случай, которого не хватало
	// в отставшей копии правила в epubtocprobe.
	broken := "Введение �� 7"
	if !kb.LineEndsWithPageNumber(broken) {
		t.Errorf("правило не узнало отточие без Unicode: %q", broken)
	}
	if measure(broken).pageEnds != 1 {
		t.Errorf("прибор не узнал отточие без Unicode: %q", broken)
	}
}

// Проза оглавлением не считается: иначе перепись насчитает служебным полкниги.
func TestMeasureDoesNotTakeProseForTOC(t *testing.T) {
	prose := "Этот абзац говорит о деле и кончается точкой.\n" +
		"Второе предложение тоже, и номеров страниц здесь нет.\n" +
		"Третья строка, как и прочие, написана словами.\n" +
		"Четвёртая — тоже."
	st := measure(prose)
	if st.pageEnds != 0 {
		t.Errorf("в прозе нашлось %d строк с номером страницы", st.pageEnds)
	}
	if kb.LooksLikeTOC(prose) {
		t.Error("правило kb признало прозу оглавлением")
	}
	if st.noPunct != 0 {
		t.Errorf("строк без знака препинания на конце %d, в этой прозе их нет", st.noPunct)
	}
}

// Номер главы в начале строки — признак оглавления EPUB, где номеров страниц нет.
func TestStartsNumbered(t *testing.T) {
	yes := []string{"1.2 Устройство графа", "12 Введение", "Chapter 3 Practice", "Глава 4 Итоги", "Appendix A Ссылки"}
	no := []string{".2 не номер", "Введение", "1.2Устройство без пробела", ""}
	for _, s := range yes {
		if !startsNumbered(s) {
			t.Errorf("не признано номером главы: %q", s)
		}
	}
	for _, s := range no {
		if s == "" {
			continue // пустые строки перепись отбрасывает раньше
		}
		if startsNumbered(s) {
			t.Errorf("зря признано номером главы: %q", s)
		}
	}
}

// Ссылка на кусок складывается из книги и номера без потерь: на ней держится
// сверка журналов графа с кусками коллекции.
func TestChunkKeyIsReversible(t *testing.T) {
	cases := [][2]uint32{{0, 0}, {1, 1}, {104, 682}, {4294967295, 4294967295}}
	seen := map[uint64]bool{}
	for _, c := range cases {
		k := chunkKey(c[0], c[1])
		if got := uint32(k >> 32); got != c[0] {
			t.Errorf("книга потерялась: %d вместо %d", got, c[0])
		}
		if got := uint32(k); got != c[1] {
			t.Errorf("номер куска потерялся: %d вместо %d", got, c[1])
		}
		if seen[k] {
			t.Errorf("две разные ссылки дали один ключ: %v", c)
		}
		seen[k] = true
	}
	// Книга 1, кусок 0 и книга 0, кусок 1 — разные куски.
	if chunkKey(1, 0) == chunkKey(0, 1) {
		t.Error("ключ путает книгу с номером куска")
	}
}

// Без режима перепись отказывается и печатает список режимов, а не считает
// что-нибудь на своё усмотрение.
func TestRunRefusesWithoutMode(t *testing.T) {
	var out strings.Builder
	err := Run(&out, nil, "books", nil)
	if err == nil {
		t.Fatal("без -only перепись обязана отказать")
	}
	for _, m := range modes {
		if !strings.Contains(out.String(), m.name) {
			t.Errorf("в подсказке нет режима %q", m.name)
		}
	}
}

// Режиму калибровки нужны куски; без них — отказ, а не пустой прогон.
func TestCalibrateNeedsChunks(t *testing.T) {
	var out strings.Builder
	if err := Run(&out, nil, "books", []string{"-only", "toc-calibrate"}); err == nil {
		t.Fatal("режим toc-calibrate без -chunks обязан отказать")
	}
}

// Неизвестный режим называет себя и перечисляет известные.
func TestRunRejectsUnknownMode(t *testing.T) {
	var out strings.Builder
	err := Run(&out, nil, "books", []string{"-only", "нет-такого"})
	if err == nil {
		t.Fatal("неизвестный режим обязан привести к отказу")
	}
	if !strings.Contains(err.Error(), "нет-такого") {
		t.Errorf("в отказе не назван режим: %v", err)
	}
	if !strings.Contains(err.Error(), "toc") {
		t.Errorf("в отказе не перечислены известные режимы: %v", err)
	}
}
