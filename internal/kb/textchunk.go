package kb

import (
	"regexp"
	"strings"
	"unicode"
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
	chunks, _ := splitText(parts, opt)
	return chunks
}

// splitText — SplitText, который сообщает ещё и объём текста в байтах,
// взятого в нарезку: после того как вставленные файлы base64 вырезаны.
//
// Этот объём нужен проверке на тощую книгу (parseBook): куски считаются
// по очищенному тексту, и сравнивать их число с размером файла вместе
// с картинками — значит мерить разными мерками (аудит 04.11.2026).
func splitText(parts []document.Part, opt ChunkOpts) ([]Chunk, int64) {
	if opt.Chars <= 0 {
		opt = DefaultChunkOpts()
	}
	if opt.Step <= 0 || opt.Step > opt.Chars {
		opt.Step = opt.Chars * 3 / 4
	}
	overlap := opt.Chars - opt.Step
	parts = dropBinary(parts)
	var size int64
	for _, p := range parts {
		size += int64(len(p.Text)) + 1 // и перевод строки, как в файле
	}
	parts = splitLongLines(parts, opt.Chars)
	fenced := fenceStates(parts)

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
			fence = fenced[i]
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
	return out, size
}

// dataURI — картинка или другой файл, вставленный в текст как `data:<тип>;base64,…`,
// в том числе с параметрами RFC 2397 между типом и `;base64,`
// (`data:image/png;name=scheme.png;base64,…`, `;charset=utf-8`).
// Тело короче 64 знаков не трогается: это пример в тексте о самих data:-ссылках,
// а не вставленный файл.
var dataURI = regexp.MustCompile(`data:([A-Za-z0-9.+-]+/[A-Za-z0-9.+-]+)((?:;[^;,\s"'()<>]+)*);base64,[A-Za-z0-9+/=]{64,}`)

// Пороги сырого base64 — без data:, по одному виду текста.
const (
	// minBase64Word — с какой длины «слово» из алфавита base64 считается
	// вставленным файлом. Самое длинное из того, что на него похоже и трогать
	// нельзя, — шестнадцатеричный sha512 (128 знаков) и адреса в пару сотен
	// знаков; у адреса к тому же точки и дефисы, которых в base64 нет.
	minBase64Word = 512
	// minBase64Width — с какой ширины строка из одного base64 может быть
	// строкой перенесённого блока: почта и HTML переносят по 76 знаков, PEM — по 64.
	minBase64Width = 60
)

// Виды знаков base64. Сжатые данные в base64 несут все три сразу, а хеш
// в шестнадцатеричной записи — только строчные (или только прописные) и цифры:
// по этому признаку столбец сумм sha256 не принимается за картинку.
const (
	b64Upper = 1 << iota
	b64Lower
	b64Digit
	b64All = b64Upper | b64Lower | b64Digit
)

// b64Class — вид знака алфавита base64 (без выравнивания `=`) или 0,
// если знак в алфавит не входит.
func b64Class(c byte) int {
	switch {
	case 'A' <= c && c <= 'Z':
		return b64Upper
	case 'a' <= c && c <= 'z':
		return b64Lower
	case '0' <= c && c <= '9':
		return b64Digit
	case c == '+' || c == '/':
		return -1 // в алфавите, но ни одного из трёх видов не даёт
	}
	return 0
}

// dropBinary вырезает из частей вставленные файлы: от тела остаётся
// многоточие, номера строк у оставшихся частей прежние.
//
// **Зачем.** Документ, выгруженный из редактора с картинками, несёт их строкой
// base64 по сотне-другой килобайт. С тех пор как splitLongLines режет длинные
// строки, каждая такая строка становилась сотнями кусков мусора: 02.11.2026
// файл плана в 641 строку с 14 картинками дал 2955 кусков вместо текста,
// и они ушли в словесный индекс и в векторы.
//
// Узнаются три формы (аудит 04.11.2026 нашёл, что первая была единственной):
//   - data:-ссылка, в том числе с параметрами перед `;base64,` — от неё
//     остаётся тип: `![](data:image/png;base64,…)`, читатель видит, что тут
//     была картинка;
//   - «слово» из алфавита base64 от minBase64Word знаков без пробелов — сырой
//     base64 в поле JSON, PEM одной строкой;
//   - base64, перенесённый по строкам одной ширины (base64Block): строки
//     блока выпадают из частей, многоточие встаёт на строку data:-ссылки,
//     если тело начиналось на ней, иначе — на место первой строки блока.
//
// Чистятся и текст строки, и заголовок раздела (Part.Title): withHeading
// приклеивает заголовок к каждому куску раздела, и `## Схема ![](data:…)`
// с картинкой в 200 КБ давал 18 кусков из 19 длиннее 100 КБ. Название
// документа чистит тот же scrubBinary — в parseBook.
func dropBinary(parts []document.Part) []document.Part {
	out := make([]document.Part, 0, len(parts))
	// Заголовок раздела один на сотни строк и делит с ними одну строку
	// в памяти: сравнение с прежним дешёвое, и чистится он один раз.
	var titleIn, titleOut string
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p.Title != titleIn {
			titleIn, titleOut = p.Title, scrubBinary(p.Title)
		}
		p.Title = titleOut
		n, tail := base64Block(parts[i:])
		if n == 0 {
			p.Text = scrubBinary(p.Text)
			out = append(out, p)
			continue
		}
		k := len(out) - 1
		if k < 0 || out[k].Number != p.Number-1 || !cutBase64Tail(&out[k].Text) {
			p.Text = "…"
			out = append(out, p)
		}
		i += n
		if i < len(parts) && tail > 0 {
			// Последняя строка блока короче прочих и кончается тем, что
			// закрывает ссылку (`)`, `">`): её хвост остаётся строкой.
			q := parts[i]
			q.Title = scrubBinary(q.Title)
			q.Text = scrubBinary(q.Text[tail:])
			if strings.TrimSpace(q.Text) != "" {
				out = append(out, q)
			}
			continue
		}
		i--
	}
	return out
}

// scrubBinary заменяет многоточием тела вставленных файлов в одной строке:
// data:-ссылки и длинные «слова» из алфавита base64.
func scrubBinary(s string) string {
	if strings.Contains(s, ";base64,") {
		s = dataURI.ReplaceAllString(s, "data:${1}${2};base64,…")
	}
	return dropBase64Words(s)
}

// dropBase64Words заменяет многоточием «слова» из алфавита base64 длиной
// от minBase64Word знаков, в которых есть и прописные, и строчные буквы,
// и цифры. Проход ручной, а не регулярным выражением: у `{512,}` автомат
// держит сотни нитей на каждый знак, а строка бывает в десятки мегабайт.
func dropBase64Words(s string) string {
	if len(s) < minBase64Word {
		return s
	}
	var (
		b    strings.Builder
		last int // до какого места s уже переписана в b
	)
	for i := 0; i < len(s); {
		if b64Class(s[i]) == 0 {
			i++
			continue
		}
		j, cls := i, 0
		for j < len(s) {
			c := b64Class(s[j])
			if c == 0 {
				break
			}
			if c > 0 {
				cls |= c
			}
			j++
		}
		end := j
		for end < len(s) && end-j < 2 && s[end] == '=' {
			end++
		}
		if j-i >= minBase64Word && cls == b64All {
			b.WriteString(s[last:i])
			b.WriteString("…")
			last = end
		}
		i = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// base64Line — ширина строки, если вся она (без отступа) — base64 с не более
// чем двумя знаками выравнивания в конце, и виды знаков в ней; иначе 0.
func base64Line(s string) (width, cls int) {
	t := strings.TrimSpace(s)
	body := strings.TrimSuffix(strings.TrimSuffix(t, "="), "=")
	if body == "" {
		return 0, 0
	}
	for i := 0; i < len(body); i++ {
		c := b64Class(body[i])
		if c == 0 {
			return 0, 0
		}
		if c > 0 {
			cls |= c
		}
	}
	return len(t), cls
}

// base64Block узнаёт base64, перенесённый по строкам одной ширины, начиная
// с parts[0]: n — сколько строк целиком занимает блок, tail — сколько байт
// с начала следующей строки ещё принадлежат ему (последняя строка блока
// короче прочих). n = 0 — блока здесь нет.
//
// Блок — это строки одной ширины от minBase64Width знаков, всего от
// minBase64Word знаков, и во всём блоке есть все три вида знаков: столбец
// шестнадцатеричных сумм блоком не считается.
func base64Block(parts []document.Part) (n, tail int) {
	w, cls := base64Line(parts[0].Text)
	if w < minBase64Width {
		return 0, 0
	}
	total := w
	for n = 1; n < len(parts); n++ {
		lw, lc := base64Line(parts[n].Text)
		if lw != w {
			break
		}
		total += lw
		cls |= lc
	}
	if n < len(parts) {
		var tw, tc int
		tail, tw, tc = base64Tail(parts[n].Text, w)
		total += tw
		cls |= tc
	}
	if total < minBase64Word || cls != b64All {
		return 0, 0
	}
	return n, tail
}

// base64Tail — сколько байт с начала строки s составляют последнюю строку
// блока шириной w: отступ, base64 короче w и выравнивание. 0 — хвоста нет.
//
// Хвост берётся, только если похож на данные: в нём есть цифра, `+` или `/`,
// либо он кончается выравниванием, — иначе строка «Done» сразу за блоком
// пропала бы вместе с ним.
func base64Tail(s string, w int) (cut, width, cls int) {
	start := len(s) - len(strings.TrimLeft(s, " \t"))
	j, data := start, false
	for j < len(s) {
		c := b64Class(s[j])
		if c == 0 {
			break
		}
		if c > 0 {
			cls |= c
		}
		if c == b64Digit || c < 0 {
			data = true
		}
		j++
	}
	end := j
	for end < len(s) && end-j < 2 && s[end] == '=' {
		end++
		data = true
	}
	if j == start || end-start > w || !data {
		return 0, 0, 0
	}
	return end, end - start, cls
}

// cutBase64Tail заменяет многоточием начало тела base64, стоящее в конце
// строки data:-ссылки (`<img src="data:image/png;base64,iVBOR…`), когда
// остальное тело перенесено на следующие строки. false — строка не такая.
func cutBase64Tail(s *string) bool {
	k := strings.LastIndex(*s, ";base64,")
	if k < 0 || !strings.Contains((*s)[:k], "data:") {
		return false
	}
	// Многоточие уже стоит, если начало тела на строке ссылки было длинным
	// и его заменил scrubBinary.
	rest := strings.TrimSuffix((*s)[k+len(";base64,"):], "…")
	for i := 0; i < len(rest); i++ {
		if b64Class(rest[i]) == 0 && rest[i] != '=' {
			return false
		}
	}
	*s = (*s)[:k] + ";base64,…"
	return true
}

// splitLongLines режет строки длиннее целевого размера куска на части
// не длиннее limit знаков; у частей тот же номер строки и тот же раздел.
//
// **Зачем.** Кусок набирается целыми строками, и строка длиннее куска
// становилась куском целиком: абзац markdown, записанный одной строкой, или
// сжатый JSON в 30 000 знаков давали один кусок на всё. Такой кусок не влезает
// ни в выдачу, ни в окно модели, а его вектор размазан по всему тексту. Ветка,
// которая будто бы ловила этот случай, была мёртвой: первая строка куска
// берётся всегда, и буфер пустым не бывает (замечание staticcheck, 07.10.2026).
//
// Режется так же, как `cut` у книг: прозу — по предложениям, код (внутри
// ограждения из трёх обратных кавычек) — по пробелам; слово длиннее предела —
// ровно по пределу.
func splitLongLines(parts []document.Part, limit int) []document.Part {
	long := false
	for _, p := range parts {
		if utf8.RuneCountInString(p.Text) > limit {
			long = true
			break
		}
	}
	if !long {
		return parts
	}
	out := make([]document.Part, 0, len(parts)+16)
	fence := false
	for _, p := range parts {
		code := fence
		if strings.HasPrefix(strings.TrimSpace(p.Text), "```") {
			fence = !fence
			code = true
		}
		if utf8.RuneCountInString(p.Text) <= limit {
			out = append(out, p)
			continue
		}
		var pieces []string
		if code {
			pieces = wrapRunes(p.Text, limit)
		} else {
			pieces = packSentences(p.Text, limit)
		}
		for _, piece := range pieces {
			q := p
			q.Text = piece
			out = append(out, q)
		}
	}
	return out
}

// packSentences собирает предложения строки в части не длиннее limit знаков.
func packSentences(line string, limit int) []string {
	var out, buf []string
	n := 0
	for _, s := range sentences(line) {
		for _, w := range wrapRunes(s, limit) {
			l := utf8.RuneCountInString(w)
			if n > 0 && n+1+l > limit {
				out = append(out, strings.Join(buf, " "))
				buf, n = buf[:0], 0
			}
			if n > 0 {
				n++ // пробел между предложениями
			}
			buf = append(buf, w)
			n += l
		}
	}
	if len(buf) > 0 {
		out = append(out, strings.Join(buf, " "))
	}
	return out
}

// wrapRunes режет текст на части не длиннее limit знаков по последнему пробелу
// во второй половине части, а без пробела — ровно по limit.
func wrapRunes(text string, limit int) []string {
	r := []rune(text)
	var out []string
	for len(r) > limit {
		cut := limit
		for k := limit; k > limit/2; k-- {
			if unicode.IsSpace(r[k]) {
				cut = k
				break
			}
		}
		out = append(out, strings.TrimRightFunc(string(r[:cut]), unicode.IsSpace))
		r = r[cut:]
		for len(r) > 0 && unicode.IsSpace(r[0]) {
			r = r[1:]
		}
	}
	if len(r) > 0 {
		out = append(out, string(r))
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

// fenceStates сообщает для каждой части, стоит ли она внутри ограждённого
// блока кода — по состоянию до самой части.
//
// Считается от начала файла: кусок может начаться в середине блока кода,
// и тогда его надо пометить кодом целиком, иначе модель примет вывод команд
// за прозу и перескажет своими словами.
//
// Одним проходом на весь файл, а не на каждый кусок: до 04.11.2026 состояние
// считалось заново от начала для каждого куска, и нарезка была квадратичной —
// 8192 строки резались 1,2 с, 32768 — 18,5 с, сырой base64 в 50 МБ — 40 с.
func fenceStates(parts []document.Part) []bool {
	out := make([]bool, len(parts))
	fence := false
	for k, p := range parts {
		out[k] = fence
		if strings.HasPrefix(strings.TrimSpace(p.Text), "```") {
			fence = !fence
		}
	}
	return out
}
