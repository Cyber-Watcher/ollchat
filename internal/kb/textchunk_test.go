package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// lineParts делает части так же, как их отдаёт document.Parts для текста:
// одна строка — одна часть, заголовок markdown тянется вниз по тексту.
func lineParts(text string) []document.Part {
	var out []document.Part
	head := ""
	for i, l := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(l); strings.HasPrefix(s, "# ") || strings.HasPrefix(s, "## ") {
			head = strings.TrimSpace(strings.TrimLeft(s, "#"))
		}
		out = append(out, document.Part{Number: i + 1, Title: head, Text: l})
	}
	return out
}

// Ссылка на кусок текста идёт по строкам, и номера настоящие.
func TestSplitTextRefersToLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "строка %d про настройку таймаутов и окно контекста\n", i)
	}
	chunks := SplitText(lineParts(b.String()), DefaultChunkOpts())
	if len(chunks) < 3 {
		t.Fatalf("кусков вышло %d, ожидалось хотя бы три", len(chunks))
	}
	for i, c := range chunks {
		if c.UnitFrom < 1 || c.UnitTo < c.UnitFrom || c.UnitTo > 301 {
			t.Fatalf("кусок %d ссылается на строки %d–%d — так не бывает", i, c.UnitFrom, c.UnitTo)
		}
		want := fmt.Sprintf("строка %d ", c.UnitFrom)
		if !strings.Contains(c.Text, want) {
			t.Errorf("кусок %d начинается со строки %d, а текста %q в нём нет", i, c.UnitFrom, want)
		}
	}
	if chunks[0].UnitFrom != 1 {
		t.Errorf("первый кусок начинается со строки %d", chunks[0].UnitFrom)
	}
}

// Куски идут с перекрытием: мысль на границе не теряется.
func TestSplitTextOverlaps(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "строка %d содержит достаточно текста, чтобы куски набирались\n", i)
	}
	chunks := SplitText(lineParts(b.String()), DefaultChunkOpts())
	if len(chunks) < 2 {
		t.Fatal("для проверки перекрытия нужно хотя бы два куска")
	}
	if chunks[1].UnitFrom > chunks[0].UnitTo {
		t.Errorf("перекрытия нет: первый кусок кончается на %d, второй начинается с %d",
			chunks[0].UnitTo, chunks[1].UnitFrom)
	}
}

// Заголовок раздела дописывается к куску, который начинается не с него.
func TestSplitTextCarriesHeading(t *testing.T) {
	text := "# Руководство\n\n## Настройка таймаутов\n\n" +
		strings.Repeat("Подробное описание поведения при обрыве связи и повторах.\n", 40)
	chunks := SplitText(lineParts(text), DefaultChunkOpts())
	if len(chunks) < 2 {
		t.Fatalf("кусков %d, нужно хотя бы два", len(chunks))
	}
	// Первый начинается с самого заголовка — приписывать нечего.
	if strings.HasPrefix(chunks[0].Text, "‹") {
		t.Errorf("кусок, начинающийся с заголовка, не должен получать приписку: %q",
			firstLine(chunks[0].Text))
	}
	// Следующий — из середины раздела, и заголовок ему нужен.
	last := chunks[len(chunks)-1]
	if !strings.HasPrefix(last.Text, "‹ Настройка таймаутов ›") {
		t.Errorf("к куску из середины раздела не приписан заголовок: %q", firstLine(last.Text))
	}
}

// Ограждённый блок кода помечается флагом даже тогда, когда кусок начался
// внутри него: иначе модель примет вывод команд за прозу.
func TestSplitTextMarksCode(t *testing.T) {
	text := "# Сборка\n\n```\n" + strings.Repeat("go test ./... && go vet ./...\n", 60) + "```\n"
	chunks := SplitText(lineParts(text), DefaultChunkOpts())
	if len(chunks) < 2 {
		t.Fatalf("кусков %d, нужно хотя бы два", len(chunks))
	}
	for i, c := range chunks[1:] {
		if c.Flags&FlagCode == 0 {
			t.Errorf("кусок %d внутри блока кода не помечен как код", i+1)
		}
	}
}

// Строка длиннее куска режется на куски целевого размера, а не становится
// одним куском на 30 000 знаков.
//
// Кусок набирается целыми строками, и абзац markdown одной строкой или сжатый
// JSON давали кусок длиной во весь файл: он не влезал ни в выдачу, ни в окно
// модели. Ветка «одна строка длиннее целевого размера» была мёртвой.
func TestSplitTextCutsLongLine(t *testing.T) {
	opt := DefaultChunkOpts()
	var prose strings.Builder
	for i := 0; i < 550; i++ { // около 30 000 знаков
		fmt.Fprintf(&prose, "Предложение %d рассказывает о настройке таймаутов службы. ", i)
	}
	blob := strings.Repeat("QUJDRA", 5000) // base64 без единого пробела
	for name, line := range map[string]string{"проза": prose.String(), "сплошная строка": blob} {
		text := "# Раздел\n\n" + line + "\n"
		chunks := SplitText(lineParts(text), opt)
		if len(chunks) < 10 {
			t.Fatalf("%s: строка в %d знаков дала %d кусков", name, len([]rune(line)), len(chunks))
		}
		total := 0
		for i, c := range chunks {
			body := strings.TrimPrefix(c.Text, "‹ Раздел ›\n")
			if n := len([]rune(body)); n > opt.Chars+1 {
				t.Fatalf("%s: кусок %d длиной %d знаков при целевом %d", name, i, n, opt.Chars)
			}
			if c.UnitFrom < 1 || c.UnitTo > 4 { // строки файла: заголовок, пустая, длинная, пустая
				t.Fatalf("%s: кусок %d ссылается на строки %d–%d", name, i, c.UnitFrom, c.UnitTo)
			}
			total += len([]rune(body))
		}
		// С перекрытием текста в кусках больше, чем в строке, но не меньше.
		if total < len([]rune(line)) {
			t.Fatalf("%s: в кусках %d знаков из %d — текст потерян", name, total, len([]rune(line)))
		}
	}
	// Конец строки на месте: последний кусок кончается её последним словом.
	chunks := SplitText(lineParts("# Раздел\n\n"+prose.String()+"\n"), opt)
	if last := chunks[len(chunks)-1].Text; !strings.HasSuffix(strings.TrimSpace(last), "службы.") {
		t.Fatalf("конец строки потерян: …%q", last[max(0, len(last)-60):])
	}
}

// Пустой и почти пустой файл не дают кусков, а не падают.
func TestSplitTextEmpty(t *testing.T) {
	if got := SplitText(lineParts(""), DefaultChunkOpts()); len(got) != 0 {
		t.Errorf("из пустого файла вышло %d кусков", len(got))
	}
	if got := SplitText(lineParts("\n\n   \n\n"), DefaultChunkOpts()); len(got) != 0 {
		t.Errorf("из пробелов вышло %d кусков", len(got))
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Картинка, вставленная в markdown строкой base64, в куски не идёт: от неё
// остаётся тип и многоточие на той же строке, а пример data:-ссылки в тексте
// не трогается. Без этого строка в 330 КБ резалась splitLongLines на сотни
// кусков мусора (файл плана, 02.11.2026: 2955 кусков на 641 строку).
func TestSplitTextDropsDataURIs(t *testing.T) {
	payload := strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAA", 4000)
	var b strings.Builder
	b.WriteString("# Отчёт за неделю\n\n")
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "строка %d: проверили сеть, диски и резервные копии стенда\n", i)
	}
	b.WriteString("![](data:image/png;base64," + payload + ")\n") // строка 13
	b.WriteString("Пример ссылки из справки: data:image/png;base64,iVBORw0KGgo=\n")
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "итог %d: замечаний к инфраструктуре нет, работы идут по плану\n", i)
	}

	chunks := SplitText(lineParts(b.String()), DefaultChunkOpts())
	if len(chunks) == 0 || len(chunks) > 3 {
		t.Fatalf("кусков %d, ожидалось от одного до трёх — картинка не должна давать куски", len(chunks))
	}
	var all strings.Builder
	imageLine := false
	for _, c := range chunks {
		if strings.Contains(c.Text, "AAAANSUhEUgAAAiVBOR") {
			t.Fatalf("тело картинки попало в кусок: %.80q…", c.Text)
		}
		if c.UnitFrom <= 13 && 13 <= c.UnitTo {
			imageLine = true
		}
		all.WriteString(c.Text)
	}
	for _, want := range []string{"![](data:image/png;base64,…)", "data:image/png;base64,iVBORw0KGgo=", "итог 10"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("в кусках нет %q", want)
		}
	}
	if !imageLine {
		t.Error("строка с картинкой выпала из нумерации кусков")
	}
}

// fakeBase64 — правдоподобное тело base64 длиной n знаков: псевдослучайное,
// с обоими регистрами, цифрами и знаками «+/», как у настоящей картинки.
// Повтор одной короткой строки сюда не годится: он не похож на сжатые данные.
func fakeBase64(n int) string {
	const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	b.Grow(n)
	x := uint32(2463534242)
	for i := 0; i < n; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b.WriteByte(abc[x%64])
	}
	return b.String()
}

// wrapEvery режет строку на строки по width знаков — так base64 переносят
// почта (76) и PEM (64).
func wrapEvery(s string, width int) []string {
	var out []string
	for len(s) > width {
		out = append(out, s[:width])
		s = s[width:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// base64Chunks — сколько кусков несут «слово» из алфавита base64 длиной от 60
// знаков: столько мусора дошло бы до словесного индекса и до векторов.
func base64Chunks(chunks []Chunk) int {
	n := 0
	for _, c := range chunks {
		for _, w := range strings.Fields(c.Text) {
			w = strings.Trim(w, `"'(){}<>[],;:`)
			if len(w) >= 60 && strings.Trim(w, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=") == "" {
				n++
				break
			}
		}
	}
	return n
}

// proseLines — абзац обычного текста, которого хватает на целый кусок: без него
// куски вокруг мусора были бы короче MinChars и выпали бы, и проверка «мусора
// в кусках нет» проходила бы на пустом месте.
func proseLines(tag string) []string {
	var out []string
	for i := 1; i <= 6; i++ {
		out = append(out, fmt.Sprintf("%s %d: проверили сеть, диски и резервные копии стенда, замечаний нет.", tag, i))
	}
	return out
}

// TestSplitTextDropsBase64Forms — вставленные файлы в других формах тоже не
// становятся кусками: base64, перенесённый по строкам (HTML и почта — по 76
// знаков, PEM — по 64), data:-ссылка с параметром RFC 2397 перед `;base64,`
// и сырой base64 без data: (поле JSON, PEM одной строкой). До правки каждая
// из этих форм давала куски из одного base64 — их ловила только та data:-ссылка,
// у которой между типом и `;base64,` ничего нет и тело стоит на той же строке.
func TestSplitTextDropsBase64Forms(t *testing.T) {
	body := fakeBase64(20000)
	join := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	cases := []struct {
		name  string
		lines []string
	}{
		{"data: с переносом по 76 знаков", join(proseLines("начало"),
			[]string{`<img src="data:image/png;base64,`}, wrapEvery(body, 76), []string{`">`}, proseLines("конец"))},
		{"data: с переносом, первая строка на месте ссылки", join(proseLines("начало"),
			wrapEvery("![](data:image/png;base64,"+body+")", 76), proseLines("конец"))},
		{"параметр RFC 2397", join(proseLines("начало"),
			[]string{"![](data:image/png;name=scheme.png;base64," + body + ")"}, proseLines("конец"))},
		{"сырой base64 в JSON", join(proseLines("начало"),
			[]string{`{"thumbnail": "` + body + `"}`}, proseLines("конец"))},
		{"PEM одной строкой", join(proseLines("начало"),
			[]string{"-----BEGIN CERTIFICATE-----" + body + "-----END CERTIFICATE-----"}, proseLines("конец"))},
		{"PEM по 64 знака", join(proseLines("начало"),
			[]string{"-----BEGIN CERTIFICATE-----"}, wrapEvery(body, 64), []string{"-----END CERTIFICATE-----"}, proseLines("конец"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := SplitText(lineParts(strings.Join(tc.lines, "\n")), DefaultChunkOpts())
			var all strings.Builder
			for _, c := range chunks {
				all.WriteString(c.Text)
			}
			if !strings.Contains(all.String(), "конец 6") || !strings.Contains(all.String(), "начало 1") {
				t.Fatalf("текст вокруг вставленного файла пропал из кусков (кусков %d)", len(chunks))
			}
			if g := base64Chunks(chunks); g > 0 {
				t.Errorf("кусков с телом base64: %d из %d", g, len(chunks))
			}
		})
	}
}

// TestSplitTextKeepsHashesAndURLs — чистка base64 не задевает то, что похоже
// на него, но им не является: хеши sha256 и sha512 (шестнадцатеричные, в том
// числе столбцом по одному на строку, как в списке сумм), длинный адрес
// в 200 знаков и длинное слово без цифр.
func TestSplitTextKeepsHashesAndURLs(t *testing.T) {
	sha256 := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	sha512 := strings.Repeat("ee26b0dd4af7e749aa1a8ee3c10ae992", 4)
	url := "https://docs.example.org/guide/" + strings.Repeat("section/", 20) + "page?token=" + strings.Repeat("Ab1", 10)
	if len(url) < 200 {
		t.Fatalf("адрес короче 200 знаков: %d", len(url))
	}
	lines := proseLines("начало")
	lines = append(lines, "sha256: "+sha256, "sha512: "+sha512, "Адрес: "+url)
	for i := 0; i < 12; i++ {
		lines = append(lines, sha256) // столбец сумм: двенадцать строк по 64 знака
	}
	lines = append(lines, proseLines("конец")...)

	chunks := SplitText(lineParts(strings.Join(lines, "\n")), DefaultChunkOpts())
	var all strings.Builder
	for _, c := range chunks {
		all.WriteString(c.Text)
	}
	text := all.String()
	for name, want := range map[string]string{"sha256": "sha256: " + sha256, "sha512": sha512, "адрес": url} {
		if !strings.Contains(text, want) {
			t.Errorf("%s пропал из кусков", name)
		}
	}
	if got := strings.Count(text, sha256); got < 13 {
		t.Errorf("строк с sha256 в кусках %d, а в тексте их 13", got)
	}
}

// TestSplitTextDropsDataURIInHeading — картинка base64 в заголовке раздела
// не размножается по кускам и не становится названием документа.
//
// Заголовок раздела тянется вниз по тексту (Part.Title), и withHeading
// приклеивает его к каждому куску раздела; первый заголовок первого уровня —
// название документа в реестре. Чистка base64 до правки шла только по Part.Text,
// и `## Схема ![](data:…)` с картинкой в 200 КБ дал 18 кусков из 19 длиннее
// 100 КБ — каждый с той же картинкой.
func TestSplitTextDropsDataURIInHeading(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	img := fakeBase64(200 << 10)
	var b strings.Builder
	fmt.Fprintf(&b, "# Отчёт о стенде ![](data:image/png;base64,%s)\n\n", img)
	fmt.Fprintf(&b, "## Схема ![](data:image/png;base64,%s)\n\n", img)
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "Абзац %d про настройку таймаутов службы и проверку индекса книг по разделам.\n", i)
	}
	path := filepath.Join(books, "doc.md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Куски — тем же путём, что у индексации: document.Parts, затем SplitText.
	_, parts, err := document.Parts(path, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	opt := DefaultChunkOpts()
	chunks := SplitText(parts, opt)
	if len(chunks) < 10 {
		t.Fatalf("кусков %d — текст раздела пропал", len(chunks))
	}
	headed := 0
	for _, c := range chunks {
		if n := len([]rune(c.Text)); n > 2*opt.Chars {
			t.Fatalf("кусок строк %d–%d длиной %d знаков при размере куска %d: %.80q…",
				c.UnitFrom, c.UnitTo, n, opt.Chars, c.Text)
		}
		if strings.Contains(c.Text, "‹ Схема") {
			headed++
		}
	}
	if g := base64Chunks(chunks); g > 0 {
		t.Errorf("кусков с телом картинки из заголовка: %d из %d", g, len(chunks))
	}
	if headed == 0 {
		t.Error("заголовок раздела пропал из кусков — чистить надо тело картинки, а не заголовок")
	}

	// Название документа в реестре — тоже без картинки.
	c, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	var rec BookRec
	for _, d := range c.LiveBooks() {
		rec = d
	}
	if len(rec.Title) > 200 || !strings.Contains(rec.Title, "Отчёт о стенде") {
		t.Errorf("название в реестре — %d байт: %.80q…", len(rec.Title), rec.Title)
	}
}

// splitLines — строки одинаковой длины в 1200 знаков: после splitLongLines
// каждая даёт две части, и на каждую выходит по куску — так число кусков
// растёт вместе с числом строк, и любой проход «от начала файла на кусок»
// виден сразу.
func splitLines(n int) []document.Part {
	line := strings.Repeat("слово ", 200)
	parts := make([]document.Part, n)
	for i := range parts {
		parts[i] = document.Part{Number: i + 1, Text: line}
	}
	return parts
}

// TestSplitTextLinearInLines — время нарезки растёт с числом строк линейно.
//
// До правки состояние ограждения кода (insideFence) считалось для каждого
// куска заново от начала файла, и нарезка была квадратичной: 8192 строки —
// 1,2 с, 32768 — 18,5 с (в 15 раз дольше при вчетверо большем входе), а сырой
// base64 в 50 МБ резался 40 с. Порог — 8: среднее геометрическое между
// линейным ростом (4) и квадратичным (16), так замер отличает одно от другого
// с запасом на шум с обеих сторон.
func TestSplitTextLinearInLines(t *testing.T) {
	if testing.Short() {
		t.Skip("замер времени нарезки — долгий, в -short пропускается")
	}
	measure := func(n int) time.Duration {
		parts := splitLines(n)
		best := time.Duration(0)
		for k := 0; k < 2; k++ {
			start := time.Now()
			SplitText(parts, DefaultChunkOpts())
			if d := time.Since(start); best == 0 || d < best {
				best = d
			}
		}
		return best
	}
	small, big := measure(8<<10), measure(32<<10)
	ratio := float64(big) / float64(small)
	t.Logf("8192 строк — %v, 32768 строк — %v, отношение %.1f", small, big, ratio)
	if ratio > 8 {
		t.Errorf("вчетверо больший вход режется в %.1f раза дольше — рост не линейный", ratio)
	}
}

// BenchmarkSplitTextLines — нарезка по числу строк: сравнивать время на
// строку между размерами, при линейной нарезке оно одно и то же.
func BenchmarkSplitTextLines(b *testing.B) {
	for _, n := range []int{8 << 10, 16 << 10, 32 << 10} {
		parts := splitLines(n)
		b.Run(fmt.Sprintf("строк=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				SplitText(parts, DefaultChunkOpts())
			}
		})
	}
}
