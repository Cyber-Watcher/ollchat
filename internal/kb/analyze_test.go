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
