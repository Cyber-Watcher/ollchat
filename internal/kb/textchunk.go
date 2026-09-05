package kb

import (
	"strings"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// Нарезка текстовых документов: markdown и обычный текст.
//
// Почему отдельно от Split. Тот написан под PDF и полон приёмов, нужных
// именно там: снятие повторяющихся колонтитулов, поиск печатного номера
// страницы, отбрасывание оглавлений с отточиями. На текстовом файле эти
// приёмы вредны — «повторяющаяся строка» в markdown это `---` или пустая
// строка, и выбрасывать их нельзя.
//
// Единица ссылки здесь — строка. Кусок честно знает, с какой строки начался
// и какой кончился, поэтому ссылка «строки 120–140» открывается в редакторе
// без поиска.

// SplitText режет текстовый документ на куски.
//
// parts — по одной части на строку, как их отдаёт document.Parts для .md/.txt.
func SplitText(parts []document.Part, opt ChunkOpts) []Chunk {
	if opt.Chars <= 0 {
		opt = DefaultChunkOpts()
	}
	if opt.Step <= 0 || opt.Step > opt.Chars {
		opt.Step = opt.Chars * 3 / 4
	}
	overlap := opt.Chars - opt.Step

	var out []Chunk
	i := 0
	for i < len(parts) {
		// Пустые строки в начале куска не нужны: они съедают размер
		// и портят первую строку выдачи.
		for i < len(parts) && strings.TrimSpace(parts[i].Text) == "" {
			i++
		}
		if i >= len(parts) {
			break
		}

		var (
			buf   []string
			runes int
			fence = insideFence(parts, i)
			flags ChunkFlags
			j     = i
			brk   = -1 // последняя пустая строка: по ней разрыв аккуратнее
		)
		for ; j < len(parts); j++ {
			line := parts[j].Text
			n := utf8.RuneCountInString(line) + 1
			if runes > 0 && runes+n > opt.Chars {
				break
			}
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fence = !fence
				flags |= FlagCode
			} else if fence {
				flags |= FlagCode
			}
			if strings.TrimSpace(line) == "" && !fence && runes > opt.Chars/2 {
				brk = j
			}
			buf = append(buf, line)
			runes += n
		}
		if len(buf) == 0 { // одна строка длиннее целевого размера
			buf, runes, j = []string{parts[i].Text}, utf8.RuneCountInString(parts[i].Text), i+1
		}
		// Разрыв по пустой строке, если она нашлась во второй половине куска:
		// так абзац не рвётся посередине.
		if brk > i && brk < j-1 && !fence {
			buf, j = buf[:brk-i], brk
		}

		text := strings.TrimRight(strings.Join(buf, "\n"), "\n")
		if strings.TrimSpace(text) != "" && len([]rune(text)) >= opt.MinChars {
			out = append(out, Chunk{
				Text:     withHeading(parts[i], text),
				UnitFrom: parts[i].Number,
				UnitTo:   parts[j-1].Number,
				Flags:    flags | langFlags(text),
			})
		}

		// Следующий кусок начинается с перекрытием — отступив назад на
		// столько строк, сколько укладывается в overlap символов.
		next := j
		if overlap > 0 {
			back := 0
			for k := j - 1; k > i; k-- {
				back += utf8.RuneCountInString(parts[k].Text) + 1
				if back >= overlap {
					next = k
					break
				}
			}
		}
		if next <= i {
			next = i + 1
		}
		i = next
	}
	return out
}

// withHeading дописывает к куску заголовок раздела, если кусок начинается
// не с него самого.
//
// Заголовок нельзя положить в ссылку: она хранится двоичной записью
// фиксированного размера, и новое поле означало бы смену версии формата
// и переиндексацию всей библиотеки. А в тексте куска он не только виден
// в выдаче, но и участвует в поиске: запрос «настройка таймаутов» находит
// кусок, где этих слов нет в тексте, но есть в заголовке раздела.
func withHeading(first document.Part, text string) string {
	h := strings.TrimSpace(first.Title)
	if h == "" {
		return text
	}
	if strings.HasPrefix(strings.TrimSpace(text), "#") {
		return text // кусок и так начинается с заголовка
	}
	return "‹ " + h + " ›\n" + text
}

// insideFence сообщает, находится ли строка i внутри ограждённого блока кода.
//
// Считается от начала файла: кусок может начаться в середине блока кода,
// и тогда его надо пометить кодом целиком, иначе модель примет вывод команд
// за прозу и перескажет своими словами.
func insideFence(parts []document.Part, i int) bool {
	fence := false
	for k := 0; k < i && k < len(parts); k++ {
		if strings.HasPrefix(strings.TrimSpace(parts[k].Text), "```") {
			fence = !fence
		}
	}
	return fence
}
