package graph

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// Синоним, совпадающий с собственным именем другого понятия, — подмена,
// а не синоним. Замер 03.09.2026: каждый пятый синоним такой, и в 20 801 случае
// хозяин имени крупнее. Модель видела «goroutine, он же Go».
func TestSafeAliasesDropOtherName(t *testing.T) {
	// Случай берётся такой, который доходит до нашего фильтра: «ChatGPT»
	// у «ИИ» — замена через границу алфавита, прежнее правило usableAlias
	// её пропускает как перевод. А вот «goroutine ← Go» до фильтра не доходит
	// вовсе: usableAlias отвергает замену слова тем же алфавитом.
	e := loadFrom(t, []string{
		`{"id":1,"name":"ChatGPT","norm":"chatgpt","type":"технология","count":888,"at":1}`,
		`{"id":2,"name":"ИИ","norm":"ии","type":"технология",` +
			`"aliases":["ChatGPT","Искусственный интеллект"],"count":2031,"at":2}`,
	})
	ent, ok := e.Get(2)
	if !ok {
		t.Fatal("понятие не нашлось")
	}

	// В вектор и в запрос — без чужого имени: оно тянет вектор к чужому смыслу.
	got := e.SafeAliases(ent)
	for _, a := range got {
		if strings.EqualFold(a, "ChatGPT") {
			t.Fatalf("чужое собственное имя попало в безопасный список: %v", got)
		}
	}
	if len(got) != 1 {
		t.Fatalf("настоящий синоним потерян: %v", got)
	}

	// А человеку и модели показываем оба. Замер на эталоне 03.09.2026: правило
	// задевает каждый шестой верный синоним, и в карточке эта потеря ничем
	// не оправдана — «WaitGroup, он же sync.WaitGroup» это подсказка.
	shown := e.DisplayAliases(ent)
	if len(shown) != 2 {
		t.Fatalf("в показе должны остаться оба синонима: %v", shown)
	}
}

// Переводы идут первыми: в вектор понятия уходят первые синонимы, и мост между
// языками важнее прочих написаний.
func TestAliasesTranslationsFirst(t *testing.T) {
	e := loadFrom(t, []string{
		// «KB» проходит как аббревиатура, «база знаний» — как перевод.
		// А вот «knowledge bases» прежним правилом usableAlias отсеивается:
		// замена слова на другое того же алфавита синонимом не считается.
		`{"id":1,"name":"Knowledge base","norm":"knowledge base","type":"понятие",` +
			`"aliases":["KB","база знаний"],"count":236,"at":1}`,
	})
	ent, _ := e.Get(1)
	got := e.DisplayAliases(ent)
	if len(got) != 2 || got[0] != "база знаний" || got[1] != "KB" {
		t.Fatalf("перевод не впереди: %v", got)
	}
}

// Текст для эмбеддера строится из проверенных синонимов и обрезается по числу
// из настройки, а не по зашитому числу.
func TestEmbedTextUsesTrustedOrder(t *testing.T) {
	ent := Entity{ID: 1, Name: "Guard", Norm: "guard"}
	got := embedText(ent, []string{"защита", "guards", "guardrail"}, 3) // имя плюс два синонима
	if got != "Guard, защита, guards" {
		t.Fatalf("неожиданный текст вектора: %q", got)
	}
	if strings.Contains(got, "guardrail") {
		t.Fatalf("обрезка по настройке не сработала: %q", got)
	}
}

// Синоним, совпавший с собственным именем другого понятия, — не «он же».
// Среди таких есть верные переводы («WaitGroup ← sync.WaitGroup») и прямая
// ложь («ИИ ← ChatGPT»), и текстом их не различить. Поэтому обоим читателям
// они показываются отдельной строкой с оговоркой «не то же самое» (этап 89, А1):
// подсказка остаётся, подмены нет, и карточки человека и модели совпадают.
func TestRenderShowsClashesAsNearNotSame(t *testing.T) {
	e := FoundEntity{
		Entity:      Entity{ID: 1, Name: "ИИ", Norm: "ии", Type: "технология"},
		Mentions:    2031,
		Books:       107,
		Aliases:     []string{"Искусственный интеллект", "ChatGPT"},
		AliasesSafe: []string{"Искусственный интеллект"},
	}
	res := SearchResult{Entities: []FoundEntity{e}}

	for _, opt := range []RenderOpts{{Collection: "books"}, {Collection: "books", ForModel: true}} {
		out := Render(nil, res, opt)
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "он же") && strings.Contains(line, "ChatGPT") {
				t.Fatalf("чужое имя ушло как «он же» (ForModel=%v): %q", opt.ForModel, line)
			}
		}
		if !strings.Contains(out, "он же: Искусственный интеллект") {
			t.Fatalf("верный синоним пропал (ForModel=%v): %q", opt.ForModel, out)
		}
		if !strings.Contains(out, "близкие понятия графа (не то же самое): ChatGPT") {
			t.Fatalf("близкое понятие не показано с оговоркой (ForModel=%v): %q", opt.ForModel, out)
		}
	}
	card := RenderEntity(nil, e, nil, RenderOpts{Collection: "books", ForModel: true})
	if !strings.Contains(card, "близкие понятия графа (не то же самое): ChatGPT") ||
		!strings.Contains(card, "он же: Искусственный интеллект") {
		t.Fatalf("карточка понятия: %q", card)
	}
}

// Смягчённое правило: раскрытие, уточнение и перевод с другим числом слов.
// Замер 03.09.2026 на эталоне: 46 верных переводов из 65 против 23 у прежнего.
// Cross-script пары с разным числом слов НЕ принимаются: их текстом не отличить
// от подмены понятия (см. TestAliasIsKeyOnlyIfSimilar).
func TestUsableAliasRelaxed(t *testing.T) {
	yes := [][2]string{
		{"net.dial", "net.dial function"},            // уточнение (подпоследовательность)
		{"cpu profiling", "cpu"},                     // сужение
		{"toolcall", "tool call"},                    // тот же, знак-разделитель
		{"вытеснение контекста", "context eviction"}, // перевод при равном числе слов
	}
	for _, p := range yes {
		if !usableAlias(p[0], p[1]) {
			t.Errorf("должен приниматься: %q ← %q", p[0], p[1])
		}
	}
	no := [][2]string{
		{"машинное обучение", "глубокое обучение"}, // русское на русское — другое понятие
		{"дерево решений", "дерево прогнозирования"},
		{"goroutine", "go"},  // «go» внутри слова — не разделитель, другое понятие
		{"hostname", "host"}, // подстрока, а не то же написание
	}
	for _, p := range no {
		if usableAlias(p[0], p[1]) {
			t.Errorf("должен отвергаться (другое понятие): %q ← %q", p[0], p[1])
		}
	}
}

// subseqWords — подпоследовательность слов, а не любое вхождение.
func TestSubseqWords(t *testing.T) {
	if !subseqWords([]string{"a", "c"}, []string{"a", "b", "c"}) {
		t.Fatal("a c должно быть подпоследовательностью a b c")
	}
	if subseqWords([]string{"c", "a"}, []string{"a", "b", "c"}) {
		t.Fatal("порядок важен: c a не подпоследовательность a b c")
	}
	if subseqWords(nil, []string{"a"}) {
		t.Fatal("пустой набор не подпоследовательность")
	}
}

// Совокупный итог на эталоне: смягчённое правило сохраняет заметно больше верных
// переводов, чем прежнее (замер 03.09.2026 — 52 из 65 против 23). Проверяем не
// отдельные трудные случаи, а именно итог: правило принимали по нему.
func TestUsableAliasKeepsMostTranslations(t *testing.T) {
	path := aliasSamplePath()
	if path == "" {
		t.Skip("эталона нет: путь задаётся в OLLCHAT_ALIASES_SAMPLE")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Skip("эталона нет")
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	kept, all := 0, 0
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) < 8 || c[0] != "перевод" {
			continue
		}
		all++
		if usableAlias(Normalize(c[1]), Normalize(c[4])) {
			kept++
		}
	}
	if all == 0 {
		t.Skip("в эталоне нет переводов")
	}
	if kept*100 < all*65 {
		t.Fatalf("смягчённое правило сохраняет %d из %d переводов (<65%%) — регрессия", kept, all)
	}
	t.Logf("сохранено переводов: %d из %d", kept, all)
}
