package kb

import (
	"strings"
	"testing"
)

// terms — вспомогательное: только термы, без позиций.
func terms(text string) []string {
	toks := Tokens(text, nil)
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		out = append(out, t.Term)
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestIdentifiersStayWhole — главное правило для технических книг: имя не должно
// рассыпаться. Половина запросов к таким книгам — точные имена, и разрезанное
// имя не найдётся никогда.
func TestIdentifiersStayWhole(t *testing.T) {
	cases := []struct{ text, want string }{
		{"использует sync.WaitGroup для ожидания", "sync.waitgroup"},
		{"файл go.mod задаёт модуль", "go.mod"},
		{"флаг --cap-add=NET_ADMIN", "cap-add"},
		{"пакет net/http", "net/http"},
		{"язык C++ и стандарт C11", "c++"},
		{"переменная http_proxy", "http_proxy"},
		{"метод httpClient.Do", "httpclient.do"},
		{"версия v1.21.3", "v1.21.3"},
	}
	for _, c := range cases {
		got := terms(c.text)
		if !has(got, c.want) {
			t.Errorf("%q: нет терма %q, получено %v", c.text, c.want, got)
		}
	}
}

// TestIdentifierPartsIndexed — имя кладётся и по частям, на той же позиции:
// иначе «http клиент» не найдёт HTTPClient, а «go mod» — go.mod.
func TestIdentifierPartsIndexed(t *testing.T) {
	toks := Tokens("вызов sync.WaitGroup здесь", nil)
	var whole, part uint32
	var seenWhole, seenPart bool
	for _, tk := range toks {
		switch tk.Term {
		case "sync.waitgroup":
			whole, seenWhole = tk.Pos, true
		case "waitgroup":
			part, seenPart = tk.Pos, true
		}
	}
	if !seenWhole || !seenPart {
		t.Fatalf("нет целого имени или его части: %v", terms("вызов sync.WaitGroup здесь"))
	}
	if whole != part {
		t.Fatalf("часть имени стоит на другой позиции: %d против %d", part, whole)
	}
}

// TestIdentifiersNotStemmed — имя не приводится к основе, иначе `Kubernetes`
// станет `kubernet` и перестанет совпадать с `kubernetes.io`.
func TestIdentifiersNotStemmed(t *testing.T) {
	got := terms("кластер kubernetes.io и адрес files.example.com")
	for _, want := range []string{"kubernetes.io", "kubernetes", "files.example.com"} {
		if !has(got, want) {
			t.Errorf("нет терма %q: %v", want, got)
		}
	}
}

// TestRussianFormsShareStem — то, ради чего стеммер и нужен: разные падежи
// одного слова должны попадать в индекс одним термом.
func TestRussianFormsShareStem(t *testing.T) {
	groups := [][]string{
		{"горутина", "горутины", "горутине", "горутину", "горутиной", "горутинам", "горутинами"},
		{"канал", "канала", "каналу", "каналом", "каналы", "каналов", "каналам"},
		{"блокировка", "блокировки", "блокировке", "блокировку"},
		{"память", "памяти", "памятью"},
		{"выполнение", "выполнения", "выполнению", "выполнением"},
		{"поток", "потока", "потоки", "потоков", "потокам"},
	}
	for _, g := range groups {
		first := stemRussian(g[0])
		for _, w := range g[1:] {
			if got := stemRussian(w); got != first {
				t.Errorf("%q → %q, а %q → %q — формы разошлись", g[0], first, w, got)
			}
		}
		if len([]rune(first)) < 3 {
			t.Errorf("основа %q слишком коротка для %q", first, g[0])
		}
	}
}

// TestRussianFleetingVowelKnownLimit фиксирует известный предел: беглая гласная
// («блокировка» — «блокировок») суффиксным стеммером не лечится в принципе.
// Такие случаи вытягивает смысловой поиск, когда он появится.
func TestRussianFleetingVowelKnownLimit(t *testing.T) {
	if stemRussian("блокировка") == stemRussian("блокировок") {
		t.Log("основы совпали — предел исчез, тест можно ужесточить")
	}
}

// TestRussianKeepsDifferentWordsApart — обратная проверка: стеммер не должен
// схлопывать разные слова, иначе поиск начнёт находить не то.
func TestRussianKeepsDifferentWordsApart(t *testing.T) {
	pairs := [][2]string{
		{"канал", "капитал"},
		{"поток", "потолок"},
		{"память", "паять"},
		{"сервер", "сервис"},
		{"запрос", "запрет"},
	}
	for _, p := range pairs {
		if stemRussian(p[0]) == stemRussian(p[1]) {
			t.Errorf("%q и %q свелись к одной основе %q", p[0], p[1], stemRussian(p[0]))
		}
	}
}

func TestEnglishFormsShareStem(t *testing.T) {
	groups := [][]string{
		{"channel", "channels"},
		{"connect", "connects", "connected", "connecting"},
		{"process", "processes", "processing", "processed"},
		{"allocate", "allocated", "allocating", "allocation"},
		{"concurrent", "concurrently"},
	}
	for _, g := range groups {
		first := stemEnglish(g[0])
		for _, w := range g[1:] {
			if got := stemEnglish(w); got != first {
				t.Errorf("%q → %q, а %q → %q — формы разошлись", g[0], first, w, got)
			}
		}
	}
}

func TestEnglishKeepsDifferentWordsApart(t *testing.T) {
	pairs := [][2]string{
		{"channel", "chance"},
		{"buffer", "buffalo"},
		{"pointer", "point"}, // разные понятия: указатель и точка
		{"mutex", "mute"},
	}
	for _, p := range pairs {
		if stemEnglish(p[0]) == stemEnglish(p[1]) {
			t.Errorf("%q и %q свелись к одной основе %q", p[0], p[1], stemEnglish(p[0]))
		}
	}
}

// TestStemmerChosenByAlphabet — правило выбирается по алфавиту самого слова,
// а не по языку книги: в русской книге про Go половина слов латиницей.
func TestStemmerChosenByAlphabet(t *testing.T) {
	got := terms("Горутины позволяют запускать concurrent обработчики")
	if !has(got, stemRussian("горутины")) {
		t.Errorf("русское слово не приведено к основе: %v", got)
	}
	if !has(got, stemEnglish("concurrent")) {
		t.Errorf("английское слово не приведено к основе: %v", got)
	}
}

// TestNumbersKept — «RFC 2616», «ГОСТ 34», «HTTP 404»: числа часть запроса.
func TestNumbersKept(t *testing.T) {
	got := terms("см. RFC 2616 и ГОСТ 34 разделы 7 и 12")
	for _, want := range []string{"2616", "34", "12"} {
		if !has(got, want) {
			t.Errorf("потеряно число %q: %v", want, got)
		}
	}
}

// TestStopWordsKept — стоп-слова не выбрасываются: без них рассыпается поиск
// устойчивых сочетаний.
func TestStopWordsKept(t *testing.T) {
	got := terms("передача по значению и по ссылке")
	if !has(got, "по") {
		t.Errorf("предлог выброшен: %v", got)
	}
}

// TestPositionsAdvance — позиции растут по словам, а части имени делят позицию
// с самим именем.
func TestPositionsAdvance(t *testing.T) {
	toks := Tokens("первое второе третье", nil)
	if len(toks) != 3 {
		t.Fatalf("термов %d, ожидалось 3", len(toks))
	}
	for i, tk := range toks {
		if tk.Pos != uint32(i) {
			t.Fatalf("терм %q на позиции %d, ожидалась %d", tk.Term, tk.Pos, i)
		}
	}
}

// TestSliceReused — срез переиспользуется: кусков миллионы, и выделение памяти
// на каждый заметно.
func TestSliceReused(t *testing.T) {
	buf := make([]Token, 0, 64)
	out := Tokens("первый текст здесь", buf)
	if cap(out) != cap(buf) {
		t.Fatalf("срез не переиспользован: было %d, стало %d", cap(buf), cap(out))
	}
	out = Tokens("другой", out)
	if len(out) != 1 {
		t.Fatalf("после повторного вызова осталось %d термов", len(out))
	}
}

// TestEdgeCases — разбор не должен ломаться на странном вводе.
func TestEdgeCases(t *testing.T) {
	cases := []string{
		"", "   ", "...", "—", "\n\n\t",
		"a", "я", "1",
		strings.Repeat("оченьдлинноеслово", 20),
		"смешанныйТекстWithMixedРегистр",
		"эмодзи 🚀 внутри текста",
		strings.Repeat(".", 1000),
	}
	for _, c := range cases {
		got := Tokens(c, nil)
		for _, tk := range got {
			if n := len([]rune(tk.Term)); n < minTermRunes || n > maxTermRunes {
				t.Errorf("терм %q длиной %d вне границ (вход %.30q)", tk.Term, n, c)
			}
		}
	}
}

// TestNoEmptyTerms — пустые термы не должны попадать в индекс ни при каком вводе.
func TestNoEmptyTerms(t *testing.T) {
	for _, c := range []string{"--", "..", "__", "-.-", "a--b", "//", "#"} {
		for _, tk := range Tokens(c, nil) {
			if strings.TrimSpace(tk.Term) == "" {
				t.Errorf("пустой терм из %q", c)
			}
		}
	}
}

// TestNounsNotEatenByVerbRules закрепляет два осознанных отступления от Snowball,
// найденных на настоящих книгах.
//
// Формально «л» после «а» — окончание прошедшего времени, а «нн» после «а» —
// суффикс причастия. Честное применение этих правил превращало «канал» в «кана»,
// а «данные» — в «да», сливая существительное с частицей. При этом «каналы»
// и «данных» давали «канал» и «дан», то есть запрос переставал находить книгу.
// В технических текстах существительные важнее прошедшего времени, поэтому
// правила первой группы (те, что срабатывают после «а» и «я») не применяются.
func TestNounsNotEatenByVerbRules(t *testing.T) {
	groups := [][]string{
		{"данные", "данных", "данными", "данное", "дан"},
		{"канал", "каналы", "каналов", "каналу"},
		{"экран", "экраны", "экранов", "экрану"},
		{"сигнал", "сигналы", "сигналов"},
		{"материал", "материалы", "материалов"},
	}
	for _, g := range groups {
		first := stemRussian(g[0])
		for _, w := range g[1:] {
			if got := stemRussian(w); got != first {
				t.Errorf("%q → %q, а %q → %q — формы разошлись", g[0], first, w, got)
			}
		}
	}
	// Обратная сторона размена: прошедшее время и инфинитив расходятся.
	// Это принято сознательно, тест фиксирует ожидание, а не дефект.
	if stemRussian("читал") == stemRussian("читать") {
		t.Log("прошедшее время и инфинитив совпали — размен больше не нужен")
	}
}

// Перенос слова на новой строке склеивается, но части сохраняются
// (этап 104, П6.8).
//
// В книгах слова переносят типографским дефисом: «алго‐\nритмы». До 17.09.2026
// такое слово попадало в индекс двумя обрубками и целиком не находилось
// ни по одному запросу. Замер: переносы есть в 9,29% кусков.
func TestTokensJoinsHyphenWrap(t *testing.T) {
	got := termsOf("популярные алго‐\nритмы машинного обучения")
	if !hasTerm(got, "алгоритм") {
		t.Fatalf("склеенное слово не попало в индекс: %v", got)
	}

	// Части остаются: перенос и составное слово внешне неразличимы, и терять
	// ни то ни другое нельзя. «специалистов-практиков» — не «специалистовпрактиков».
	got = termsOf("для специалистов‐\nпрактиков всех уровней")
	if !hasTerm(got, "специалист") || !hasTerm(got, "практик") {
		t.Fatalf("части составного слова потеряны: %v", got)
	}
}

// Обычный дефис внутри строки — часть слова, а не перенос.
func TestTokensKeepsPlainHyphen(t *testing.T) {
	got := termsOf("подход out-of-the-box работает")
	if !hasTerm(got, "out-of-the-box") {
		t.Fatalf("составное слово с обычным дефисом разорвано: %v", got)
	}
}

// Обычный дефис на конце строки — тоже перенос (правила v3).
//
// Версия v2 склеивала только U+00AD и U+2010. Перепись всей библиотеки
// 17.09.2026 показала, что это меньшая часть: обычным дефисом переносит
// большинство вёрсток — 311 тысяч переносов в 27% кусков против 94 тысяч
// у «типографских» знаков. Случаи ниже взяты из книг дословно.
func TestTokensJoinsAsciiHyphenWrap(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"искусст-\n      венные нейроны", []string{"искусствен", "нейрон"}},
		{"overwhelming at first; how-\never, upon", []string{"howev", "how", "ever"}},
		{"микро- и наносе-\n  кунды", []string{"наносекунд"}},
		{"коли\u00ad\n                         чество раз", []string{"количеств"}},
		{"сло-\r\nво", []string{"слов"}},
		// Составное слово на границе строки: части обязаны остаться.
		{"well-\nknown fact", []string{"well", "known"}},
		// Несколько переносов в одном слове: находится и целое, и середина.
		{"между-\nнарод-\nный", []string{"международн", "народ"}},
	}
	for _, c := range cases {
		got := termsOf(c.text)
		for _, w := range c.want {
			if !hasTerm(got, w) {
				t.Errorf("%q: нет терма %q в %v", c.text, w, got)
			}
		}
	}
}

// Что переносом НЕ считается: разные алфавиты, цифра перед знаком, заглавная
// после обычного дефиса, тире между словами.
func TestTokensWrapRejects(t *testing.T) {
	cases := []struct {
		text string
		bad  string
	}{
		{"руководство по AI-\nассистенту", "aiассистент"},
		{"в 2020-\nгоду", "2020год"},
		{"Embry-\nRiddle Aeronautical", "embryriddl"},
		{"тире -\nслово", "тиреслов"},
	}
	for _, c := range cases {
		for _, tk := range termsOf(c.text) {
			if strings.HasPrefix(tk, c.bad) {
				t.Errorf("%q: склеено лишнее: %v", c.text, termsOf(c.text))
			}
		}
	}
	// Часть слова приводится к основе по своим признакам, а не по признакам
	// целого: «году» после «2020» — обычное слово.
	if got := termsOf("в 2020‐\nгоду"); !hasTerm(got, "год") {
		t.Errorf("часть не приведена к основе: %v", got)
	}
}

// Частота слова в куске не завышается: терм на одной позиции кладётся один раз.
func TestTokensWrapNoDuplicateTerms(t *testing.T) {
	seen := map[string]int{}
	for _, tk := range Tokens("клиент-сер‐\nверной архитектуры", nil) {
		if tk.Pos == 0 {
			seen[tk.Term]++
		}
	}
	for term, n := range seen {
		if n > 1 {
			t.Errorf("терм %q положен %d раза на одну позицию", term, n)
		}
	}
}

// Лигатуры, ударения, разложенные буквы и невидимые знаки слово не портят.
func TestTokensFoldsTypography(t *testing.T) {
	cases := []struct{ text, want string }{
		{"con\ufb01gured", "configur"},       // ﬁ
		{"o\ufb04ine mode", "offlin"},        // ﬄ
		{"Docker\ufb01le", "dockerfil"},      // имя файла с лигатурой
		{"замка\u0301ми", "замк"},            // ударение
		{"и\u0306од", "йод"},                 // разложенная «й»
		{"е\u0308ж", "еж"},                   // разложенная «ё» сводится к «е»
		{"пул\u00adреквесты", "реквест"},     // мягкий перенос как дефис (PDF)
		{"алго\u00adритмы", "алгоритм"},      // невидимый мягкий перенос (EPUB)
		{"five\u2010step saga", "five-step"}, // типографский дефис = дефис
		{"cloud.\u200bgoogle.\u200bcom", "cloud.google.com"},
	}
	for _, c := range cases {
		if got := termsOf(c.text); !hasTerm(got, c.want) {
			t.Errorf("%q: нет терма %q в %v", c.text, c.want, got)
		}
	}
}

// Границы слова в тексте: у склеенного слова они охватывают обе половины.
func TestTokensSpans(t *testing.T) {
	text := "про алго-\n   ритмы тут"
	r := []rune(text)
	for _, tk := range Tokens(text, nil) {
		if tk.Term == "алгоритм" {
			if got := string(r[tk.Start:tk.End]); got != "алго-\n   ритмы" {
				t.Fatalf("границы слова: %q", got)
			}
			return
		}
	}
	t.Fatal("склеенного слова нет")
}

// Пустая строка после переноса — конец абзаца, склеивать нечего.
func TestTokensNoWrapAcrossParagraph(t *testing.T) {
	got := termsOf("конец строки со знаком‐\n\nновый абзац")
	for _, tk := range got {
		if strings.Contains(tk, "знакомновый") {
			t.Fatalf("склеено через пустую строку: %v", got)
		}
	}
}

func termsOf(text string) []string {
	var out []string
	for _, tk := range Tokens(text, nil) {
		out = append(out, tk.Term)
	}
	return out
}

func hasTerm(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
