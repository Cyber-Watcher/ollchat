package pdfout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/pdf"
)

// Проверка «туда-обратно»: генерируем документ и читаем его нашим же
// разборщиком из internal/pdf.
//
// Это главная защита пакета, и выпиливать её нельзя. Внутри gopdf сборка
// документа (compilePdf) молча проглатывает ошибки записи объектов: испорченный
// шрифт или сломанный поток дадут не ошибку, а битый файл, который откроется
// пустым. Никакая проверка возвращаемых значений этого не поймает — только
// чтение получившегося документа обратно.

// build собирает документ и разбирает его назад в текст.
func build(t *testing.T, src string, opt Options) (*Result, *pdf.Result) {
	t.Helper()
	res, err := Build(src, opt)
	if err != nil {
		t.Fatalf("сборка документа: %v", err)
	}
	back, err := pdf.Extract(res.Data, pdf.Options{})
	if err != nil {
		t.Fatalf("документ не читается обратно: %v", err)
	}
	return res, back
}

// flat склеивает весь текст документа в одну строку с нормализованными пробелами.
func flat(r *pdf.Result) string {
	var sb strings.Builder
	for _, p := range r.Pages {
		sb.WriteString(p.Text)
		sb.WriteString(" ")
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func TestBuildRoundTripText(t *testing.T) {
	src := `# Каналы в Go

Канал — это **типизированная труба** для обмена между *горутинами*.
Создаётся вызовом ` + "`make(chan int)`" + `.

## Виды каналов

- небуферизованный: отправка ждёт получателя
- буферизованный: отправка ждёт только заполнения буфера

1. создать канал
2. запустить горутину
3. закрыть канал

> Закрывает канал отправитель, а не получатель.

---

Обычный текст после линии.`

	res, back := build(t, src, Options{})

	got := flat(back)
	for _, want := range []string{
		"Каналы в Go",
		"типизированная труба",
		"горутинами",
		"make(chan int)",
		"Виды каналов",
		"небуферизованный",
		"буферизованный",
		"создать канал",
		"закрыть канал",
		"Закрывает канал отправитель",
		"Обычный текст после линии",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в документе нет фрагмента %q", want)
		}
	}

	// Порядок обязан сохраниться: заголовок раньше текста под ним.
	if i, j := strings.Index(got, "Каналы в Go"), strings.Index(got, "Виды каналов"); i > j {
		t.Error("порядок блоков нарушен: заголовок разделa оказался раньше заголовка документа")
	}
	if res.Pages != back.TotalPages {
		t.Errorf("число страниц разошлось: у нас %d, в документе %d", res.Pages, back.TotalPages)
	}
}

// TestBuildRoundTripTableRowStaysOnOneLine проверяет геометрию таблицы:
// ячейки одной строки обязаны оказаться на одной строке и в документе.
//
// Разборщик собирает строки по координатам, поэтому это прямая проверка
// того, что ячейки выставлены по одной вертикали, а не съехали друг под друга.
func TestBuildRoundTripTableRowStaysOnOneLine(t *testing.T) {
	src := `| Параметр | Значение | Что делает |
|---|---|---|
| temperature | 0.6 | разброс ответа |
| top_k | 20 | ширина выбора |`

	_, back := build(t, src, Options{})

	var found bool
	for _, page := range back.Pages {
		for _, line := range strings.Split(page.Text, "\n") {
			norm := strings.Join(strings.Fields(line), " ")
			if strings.Contains(norm, "temperature") && strings.Contains(norm, "0.6") &&
				strings.Contains(norm, "разброс") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("ячейки одной строки таблицы разъехались по разным строкам:\n%s", flat(back))
	}
}

func TestBuildMultiPage(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Начало документа.\n\n")
	for i := 0; i < 400; i++ {
		sb.WriteString("Строка про горутины, каналы и планировщик Go, повторённая много раз.\n\n")
	}
	sb.WriteString("Конец документа.\n")

	res, back := build(t, sb.String(), Options{})

	if res.Pages < 2 {
		t.Fatalf("длинный текст обязан занять больше одной страницы, получено %d", res.Pages)
	}
	// Пробелы нормализуем: разборщик отдаёт текст страницы с переносами
	// в тех местах, где строка кончилась на странице, и фраза может
	// оказаться разорванной.
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(norm(back.Pages[0].Text), "Начало документа") {
		t.Error("первая фраза не на первой странице")
	}
	last := norm(back.Pages[len(back.Pages)-1].Text)
	if !strings.Contains(last, "Конец документа") {
		t.Errorf("последняя фраза не на последней странице — текст потерян при разрыве:\n...%s", tailOf(last, 120))
	}
}

// tailOf возвращает хвост строки для сообщений об ошибке.
func tailOf(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func TestBuildHeaderCarriesQuestionAndModel(t *testing.T) {
	at := time.Date(2026, 8, 18, 12, 30, 0, 0, time.Local)
	res, back := build(t, "Горутина — это лёгкий поток.", Options{
		WithHeader: true,
		Meta: Meta{
			Title:    "Про горутины",
			Question: "Как работают горутины?",
			Model:    "qwen3.5:122b",
			At:       at,
		},
	})

	got := flat(back)
	for _, want := range []string{
		"Про горутины",
		"Как работают горутины?",
		"qwen3.5:122b",
		"2026.08.18 12:30",
		"Горутина — это лёгкий поток",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в шапке нет %q", want)
		}
	}
	if back.Title != "Про горутины" {
		t.Errorf("заголовок в свойствах файла: %q", back.Title)
	}
	_ = res
}

func TestBuildWithoutHeaderHasNoQuestion(t *testing.T) {
	_, back := build(t, "Только ответ.", Options{
		Meta: Meta{Question: "Секретный вопрос", Model: "test-model"},
	})

	got := flat(back)
	if strings.Contains(got, "Секретный вопрос") {
		t.Error("без WithHeader вопрос не должен попадать в документ")
	}
	if !strings.Contains(got, "Только ответ") {
		t.Error("сам ответ потерян")
	}
}

// TestBuildEmojiSubstituted закрепляет обещание не терять символы молча.
func TestBuildEmojiSubstituted(t *testing.T) {
	res, back := build(t, "Готово 🚀 и ещё 🎯 текст.", Options{})

	if len(res.Missing) == 0 {
		t.Fatal("символы без глифа должны попадать в отчёт")
	}
	got := flat(back)
	if !strings.Contains(got, string(noGlyph)) {
		t.Errorf("вместо отсутствующего глифа ожидался %q:\n%s", string(noGlyph), got)
	}
	if !strings.Contains(got, "Готово") || !strings.Contains(got, "текст") {
		t.Error("соседний текст пострадал от замены")
	}
}

func TestBuildCodeLongLineNotLost(t *testing.T) {
	long := strings.Repeat("abcdefghij", 40) // 400 символов в одной строке
	src := "```go\n" + long + "\n```"

	_, back := build(t, src, Options{})

	// Метки переноса убираем и склеиваем — исходная строка обязана
	// восстановиться целиком.
	got := strings.ReplaceAll(flat(back), string(wrapMark), "")
	got = strings.ReplaceAll(got, " ", "")
	if !strings.Contains(got, long) {
		t.Error("длинная строка кода потеряна при переносе")
	}
}

func TestBuildEmptyRejected(t *testing.T) {
	if _, err := Build("   \n\n  ", Options{}); !errors.Is(err, ErrEmpty) {
		t.Errorf("ожидался ErrEmpty, получено: %v", err)
	}
}

func TestBuildTooLargeRejected(t *testing.T) {
	huge := strings.Repeat("a", MaxSourceBytes+1)
	if _, err := Build(huge, Options{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ожидался ErrTooLarge, получено: %v", err)
	}
}

// TestBuildSurvivesWeirdMarkdown — разметка бывает какой угодно, падать нельзя.
func TestBuildSurvivesWeirdMarkdown(t *testing.T) {
	cases := []string{
		"| одна |\n|---|\n| колонка |",
		"| a | b |\n|---|---|\n|  |  |",
		"- \n- \n",
		"> > двойная цитата",
		"***\n***\n",
		"# \n## \n",
		"```\n\n```",
		"1. один\n1. тоже один\n1. снова",
		strings.Repeat("- вложенный\n  - глубже\n    - ещё глубже\n", 20),
	}
	for i, src := range cases {
		if _, err := Build(src, Options{}); err != nil && !errors.Is(err, ErrEmpty) {
			t.Errorf("случай %d (%q): %v", i, src, err)
		}
	}
}

func TestWriteFileRefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ответ.pdf")
	if _, err := WriteFile(path, "текст", Options{}, false); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	_, err := WriteFile(path, "другой текст", Options{}, false)
	if !errors.Is(err, ErrExists) {
		t.Errorf("ожидался ErrExists, получено: %v", err)
	}
}

func TestWriteFileOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ответ.pdf")
	if _, err := WriteFile(path, "первый текст", Options{}, false); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	if _, err := WriteFile(path, "второй текст", Options{}, true); err != nil {
		t.Fatalf("перезапись: %v", err)
	}
	back, err := pdf.ExtractFile(path, pdf.Options{})
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if got := flat(back); !strings.Contains(got, "второй текст") {
		t.Errorf("перезапись не сработала: %q", got)
	}
}

// TestWriteFileLeavesNoFileOnBuildError: ошибка набора не должна оставлять
// на диске огрызок — пользователь увидел бы файл и решил, что всё вышло.
func TestWriteFileLeavesNoFileOnBuildError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "пусто.pdf")
	if _, err := WriteFile(path, "", Options{}, false); !errors.Is(err, ErrEmpty) {
		t.Fatalf("ожидался ErrEmpty, получено: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("после неудачного набора файла быть не должно")
	}
}

// TestBuildKeepsSpacesAroundStyles закрепляет ошибку, найденную на живом
// примере: «Канал — **труба** между горутинами» превращалось в «трубамежду».
//
// Разбиение на слова отбрасывает ведущий пробел куска, а он там единственный
// разделитель: пробел стоял перед куском, а не после предыдущего.
func TestBuildKeepsSpacesAroundStyles(t *testing.T) {
	cases := []struct{ src, want string }{
		{"Канал — **труба** между горутинами.", "труба между"},
		{"Текст *курсивом* и дальше.", "курсивом и"},
		{"Вызов `make(chan int)` создаёт канал.", "int) создаёт"},
		{"**Жирное** начало строки.", "Жирное начало"},
		{"Ссылка [сюда](https://go.dev) и текст.", ") и текст"},
	}
	for _, c := range cases {
		_, back := build(t, c.src, Options{})
		if got := flat(back); !strings.Contains(got, c.want) {
			t.Errorf("для %q ожидалось %q, получено:\n%s", c.src, c.want, got)
		}
	}
}

// TestRubleSurvivesRoundTrip — знак рубля обязан дойти до документа знаком
// рубля, а не прямоугольником.
//
// Найдено пользователем на живом ответе: модель посчитала цены серверов, и в
// PDF вместо «≈ 250 000 ₽» оказалось «≈ 250 000 □». В Liberation знака рубля
// нет вовсе — он вошёл в Unicode позже. Проверка идёт до самого конца: документ
// генерируется и читается обратно нашим же разборщиком, поэтому подтверждается
// не намерение, а то, что в файле действительно лежит U+20BD.
func TestRubleSurvivesRoundTrip(t *testing.T) {
	src := "Плата — **106 526 ₽**, блок питания *45 000 ₽*, всего ≈ `151 526 ₽`.\n\n" +
		"| Компонент | Цена |\n|---|---|\n| CPU | 200 000 ₽ |\n"

	res, back := build(t, src, Options{})
	got := flat(back)

	if strings.Contains(got, string(noGlyph)) {
		t.Errorf("в документе есть замена отсутствующего глифа:\n%s", got)
	}
	if n := strings.Count(got, "₽"); n != 4 {
		t.Errorf("знаков рубля в документе %d, ожидалось 4:\n%s", n, got)
	}
	if len(res.Missing) != 0 {
		t.Errorf("отчёт о недостающих знаках не пуст: %q", string(res.Missing))
	}
}

// TestFallbackGlyphsSurviveRoundTrip: то же для остальных знаков, которых
// в Liberation нет, — во всех начертаниях сразу.
func TestFallbackGlyphsSurviveRoundTrip(t *testing.T) {
	src := "Готово ✓, отменено ✗, оценка ★★★, следствие ⇒ вывод.\n\n" +
		"**Жирно ✓ ₽** и *курсивом ✓ ₽*.\n\n```\nрамка ╔═╗ ┏━┓ и галочка ✓ ₽\n```\n"

	res, back := build(t, src, Options{})
	got := flat(back)

	for _, r := range []string{"✓", "✗", "★", "⇒", "₽", "╔", "┏"} {
		if !strings.Contains(got, r) {
			t.Errorf("знак %q не дошёл до документа:\n%s", r, got)
		}
	}
	if len(res.Missing) != 0 {
		t.Errorf("отчёт о недостающих знаках не пуст: %q", string(res.Missing))
	}
}

// TestEmojiStillReported: то, чего нет и в резерве, по-прежнему заменяется
// прямоугольником и попадает в отчёт. Молчаливой потери быть не должно.
func TestEmojiStillReported(t *testing.T) {
	res, back := build(t, "Запуск 🚀 прошёл 🔥 успешно.", Options{})

	if len(res.Missing) == 0 {
		t.Fatal("эмодзи не попали в отчёт о недостающих знаках")
	}
	for _, r := range []rune{'🚀', '🔥'} {
		if !strings.ContainsRune(string(res.Missing), r) {
			t.Errorf("в отчёте нет %q", string(r))
		}
	}
	if got := flat(back); !strings.Contains(got, string(noGlyph)) {
		t.Errorf("вместо эмодзи в документе не прямоугольник:\n%s", got)
	}
}
