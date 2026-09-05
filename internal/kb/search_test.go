package kb

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestIndex собирает хранилище и сегмент из готовых кусков.
// docs — по книге на элемент, внутри книги куски идут подряд.
func buildTestIndex(t *testing.T, docs [][]string) (*Store, *searcher) {
	t.Helper()
	dir := t.TempDir()
	w, err := CreateWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, texts := range docs {
		chunks := make([]Chunk, len(texts))
		for j, text := range texts {
			chunks[j] = Chunk{Text: text, UnitFrom: j + 1, UnitTo: j + 1}
		}
		if err := w.Append(uint32(i+1), chunks); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	w.Close()

	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	segDir := filepath.Join(dir, "seg-00001")
	if _, err := BuildSegment(segDir, store, 0, store.Count(), nil); err != nil {
		t.Fatal(err)
	}
	seg, err := OpenSegment(segDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seg.Close(); store.Close() })
	return store, newSearcher(store, []*Segment{seg})
}

// filler даёт куску правдоподобный объём, чтобы длина не перекашивала веса.
func filler(n int) string {
	return strings.Repeat("обычный текст книги про разработку и сопровождение систем. ", n)
}

func texts(t *testing.T, store *Store, hits []Hit) []string {
	t.Helper()
	out := make([]string, len(hits))
	for i, h := range hits {
		s, err := store.Text(h.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = s
	}
	return out
}

// TestSearchFindsByMeaningfulTerm — основа поиска: кусок с искомым словом
// должен оказаться первым.
func TestSearchFindsByMeaningfulTerm(t *testing.T) {
	store, s := buildTestIndex(t, [][]string{
		{
			"Горутина — это лёгкий поток выполнения. " + filler(6),
			"Каналы служат для обмена данными между горутинами. " + filler(6),
			"Пакет sync содержит примитивы синхронизации, например Mutex. " + filler(6),
		},
	})
	hits, err := s.Search("мьютекс sync.Mutex", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("ничего не найдено")
	}
	if got := texts(t, store, hits)[0]; !strings.Contains(got, "Mutex") {
		t.Fatalf("первым найден не тот кусок: %.80s", got)
	}
}

// TestSearchRussianForms — запрос в одной форме должен находить книгу в другой:
// ради этого и нужен стеммер.
func TestSearchRussianForms(t *testing.T) {
	store, s := buildTestIndex(t, [][]string{
		{
			"Планировщик распределяет горутины по потокам операционной системы. " + filler(6),
			"Сборщик мусора работает одновременно с программой. " + filler(6),
		},
	})
	hits, err := s.Search("горутина", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("«горутина» не нашла «горутины»")
	}
	if !strings.Contains(texts(t, store, hits)[0], "Планировщик") {
		t.Fatalf("найден не тот кусок: %.80s", texts(t, store, hits)[0])
	}
}

// TestSearchIdentifiers — точные имена должны находиться целиком, а не по частям.
func TestSearchIdentifiers(t *testing.T) {
	store, s := buildTestIndex(t, [][]string{
		{
			"Обработчик регистрируется вызовом http.HandleFunc с путём и функцией. " + filler(6),
			"Ответ пишется в http.ResponseWriter, запрос читается из http.Request. " + filler(6),
			"Соединение открывается функцией net.Dial с указанием сети и адреса. " + filler(6),
		},
	})
	hits, err := s.Search("http.ResponseWriter", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("точное имя не найдено")
	}
	if !strings.Contains(texts(t, store, hits)[0], "ResponseWriter") {
		t.Fatalf("найден не тот кусок: %.80s", texts(t, store, hits)[0])
	}
}

// TestSearchRareTermWinsOverCommon — редкое слово запроса важнее частого.
func TestSearchRareTermWinsOverCommon(t *testing.T) {
	var docs [][]string
	var book []string
	for i := 0; i < 40; i++ {
		book = append(book, fmt.Sprintf("Обычная страница %d, где встречается слово система. %s", i, filler(5)))
	}
	book = append(book, "Здесь описан планировщик и его вытесняющая природа. "+filler(5))
	docs = append(docs, book)

	store, s := buildTestIndex(t, docs)
	hits, err := s.Search("система планировщик", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("ничего не найдено")
	}
	if !strings.Contains(texts(t, store, hits)[0], "планировщик") {
		t.Fatalf("частое слово перевесило редкое: %.80s", texts(t, store, hits)[0])
	}
}

// TestSearchLimitsPerBook — одна книга не должна занимать всю выдачу.
func TestSearchLimitsPerBook(t *testing.T) {
	var big []string
	for i := 0; i < 20; i++ {
		big = append(big, fmt.Sprintf("Книга первая, страница %d про каналы и их буферизацию. %s", i, filler(5)))
	}
	other := []string{"Книга вторая тоже рассказывает про каналы. " + filler(5)}

	store, s := buildTestIndex(t, [][]string{big, other})
	opt := DefaultSearchOpts()
	opt.TopK = 6
	opt.MaxPerDoc = 2
	hits, err := s.Search("каналы", opt)
	if err != nil {
		t.Fatal(err)
	}
	perDoc := map[uint32]int{}
	for _, h := range hits {
		perDoc[store.Rec(h.Chunk).Doc]++
	}
	for doc, n := range perDoc {
		if n > 2 {
			t.Fatalf("из книги %d взято %d кусков при пределе 2", doc, n)
		}
	}
	if len(perDoc) < 2 {
		t.Fatal("вторая книга не попала в выдачу вовсе")
	}
}

// TestSearchDropsNearDuplicates — соседние куски перекрываются, и выдавать
// оба смысла нет.
func TestSearchDropsNearDuplicates(t *testing.T) {
	same := "Планировщик Go вытесняет горутины на границах вызовов функций. " + filler(6)
	store, s := buildTestIndex(t, [][]string{
		{same, same + " Дополнение в конце.", "Совсем другой текст про файловые системы. " + filler(6)},
	})
	hits, err := s.Search("планировщик вытеснение", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	got := texts(t, store, hits)
	if len(got) >= 2 && strings.HasPrefix(got[1], "Планировщик Go вытесняет") {
		t.Fatalf("почти одинаковые куски выданы оба:\n1: %.60s\n2: %.60s", got[0], got[1])
	}
}

// TestSearchExactSubstringWins — точное вхождение перевешивает совпадение
// по основам: так лечится переусердствовавший стеммер.
func TestSearchExactSubstringWins(t *testing.T) {
	store, s := buildTestIndex(t, [][]string{
		{
			"Здесь рассказано про данные и их обработку в системе. " + filler(6),
			"Термин «база данных» описывает хранилище записей. " + filler(6),
		},
	})
	opt := DefaultSearchOpts()
	opt.Exact = "база данных"
	hits, err := s.Search("база данных", opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("ничего не найдено")
	}
	if !strings.Contains(texts(t, store, hits)[0], "база данных") {
		t.Fatalf("точное вхождение не перевесило: %.80s", texts(t, store, hits)[0])
	}
}

// TestSearchByBookFilter — поиск можно сузить до нужных книг.
func TestSearchByBookFilter(t *testing.T) {
	store, s := buildTestIndex(t, [][]string{
		{"Первая книга про каналы. " + filler(5)},
		{"Вторая книга про каналы. " + filler(5)},
	})
	opt := DefaultSearchOpts()
	opt.Docs = []uint32{2}
	hits, err := s.Search("каналы", opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("ничего не найдено")
	}
	for _, h := range hits {
		if store.Rec(h.Chunk).Doc != 2 {
			t.Fatalf("в выдачу попала книга %d", store.Rec(h.Chunk).Doc)
		}
	}
}

// TestSearchEmptyAndUnknown — пустой запрос и слово, которого нет, не должны
// ломать поиск.
func TestSearchEmptyAndUnknown(t *testing.T) {
	_, s := buildTestIndex(t, [][]string{{"Текст про каналы. " + filler(5)}})
	for _, q := range []string{"", "   ", "!!!", "несуществующееслово"} {
		hits, err := s.Search(q, DefaultSearchOpts())
		if err != nil {
			t.Fatalf("запрос %q: %v", q, err)
		}
		if len(hits) != 0 {
			t.Fatalf("запрос %q неожиданно что-то нашёл", q)
		}
	}
}

// TestSegmentSurvivesReopen — сегмент неизменяем, и повторное открытие должно
// давать те же ответы.
func TestSegmentSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	w, _ := CreateWriter(dir)
	w.Append(1, chunksOf("Горутины и каналы составляют основу конкурентности в Go. "+filler(5)))
	w.Commit()
	w.Close()

	store, _ := OpenStore(dir)
	defer store.Close()
	segDir := filepath.Join(dir, "seg-00001")
	meta, err := BuildSegment(segDir, store, 0, store.Count(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Terms == 0 || meta.Postings == 0 {
		t.Fatalf("сегмент пуст: %+v", meta)
	}

	for i := 0; i < 2; i++ {
		seg, err := OpenSegment(segDir)
		if err != nil {
			t.Fatalf("открытие %d: %v", i, err)
		}
		if seg.DF(stemRussian("горутины")) == 0 {
			t.Fatalf("открытие %d: терм потерян", i)
		}
		list, err := seg.Postings(stemRussian("каналы"))
		if err != nil || len(list) == 0 {
			t.Fatalf("открытие %d: постинги потеряны (%v)", i, err)
		}
		seg.Close()
	}
}

// TestUnfinishedSegmentIgnored — сегмент без seg.meta остался от прерванной
// работы и не должен участвовать в поиске.
func TestUnfinishedSegmentIgnored(t *testing.T) {
	dir := t.TempDir()
	mkdir := func(name string, withMeta bool) {
		p := filepath.Join(dir, name)
		if err := ensureDir(p); err != nil {
			t.Fatal(err)
		}
		if withMeta {
			if err := writeJSON(filepath.Join(p, "seg.meta"), SegMeta{Magic: segMagic}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mkdir("seg-00001", true)
	mkdir("seg-00002", false)
	mkdir("seg-00003", true)

	dirs, err := segmentDirs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("готовых сегментов %d, ожидалось 2: %v", len(dirs), dirs)
	}
	for _, d := range dirs {
		if strings.HasSuffix(d, "seg-00002") {
			t.Fatal("недостроенный сегмент попал в список")
		}
	}
}

// TestSnippetShowsTheMatch закрепляет требование, найденное при разборе живой
// выдачи: показывать надо место совпадения, а не начало куска. Кусок длиной
// в 1200 символов, и его начало сплошь и рядом не имеет отношения к запросу —
// тогда кажется, что поиск ошибся, хотя он нашёл верно.
func TestSnippetShowsTheMatch(t *testing.T) {
	text := strings.Repeat("Вводные слова, ничего по существу вопроса. ", 20) +
		"А вот здесь описан буферизованный канал и его ёмкость. " +
		strings.Repeat("И снова посторонний текст без отношения к делу. ", 20)

	got := Snippet(text, "буферизованный канал", 300)
	if !strings.Contains(got, "буферизованный канал") {
		t.Fatalf("в цитату не попало совпадение:\n%s", got)
	}
	if n := len([]rune(got)); n > 340 {
		t.Fatalf("цитата длиной %d символов при пределе 300", n)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("не отмечено, что цитата вырезана из середины: %.40s", got)
	}
}

// TestSnippetPrefersManyDistinctTerms — участок с тремя разными словами запроса
// ценнее, чем участок с одним словом, повторённым трижды.
func TestSnippetPrefersManyDistinctTerms(t *testing.T) {
	text := strings.Repeat("канал канал канал. ", 12) +
		strings.Repeat("прокладка. ", 30) +
		"Здесь канал, горутина и планировщик встречаются вместе. " +
		strings.Repeat("хвост. ", 30)

	got := Snippet(text, "канал горутина планировщик", 260)
	if !strings.Contains(got, "планировщик") {
		t.Fatalf("выбран участок с повтором одного слова:\n%s", got)
	}
}

// TestSnippetShortTextUnchanged — короткий кусок отдаётся целиком.
func TestSnippetShortTextUnchanged(t *testing.T) {
	const text = "Короткий кусок про каналы."
	if got := Snippet(text, "каналы", 300); got != text {
		t.Fatalf("короткий текст изменён: %q", got)
	}
}

// TestSearchDropsAdjacentChunks — соседние куски перекрываются на треть
// по построению, поэтому выдавать оба нельзя: человек видит одну и ту же цитату
// дважды, а модель впустую тратит на это контекст.
func TestSearchDropsAdjacentChunks(t *testing.T) {
	// Куски различаются достаточно, чтобы проверка по сходству текста их
	// не поймала, но идут подряд в одной книге.
	var book []string
	for i := 0; i < 6; i++ {
		book = append(book, fmt.Sprintf(
			"Планировщик вытесняет горутины, часть %d. %s Особенность номер %d описана здесь.",
			i, filler(5), i))
	}
	store, s := buildTestIndex(t, [][]string{book})
	opt := DefaultSearchOpts()
	opt.MaxPerDoc = 5
	hits, err := s.Search("планировщик вытесняет горутины", opt)
	if err != nil {
		t.Fatal(err)
	}
	var ords []uint32
	for _, h := range hits {
		ords = append(ords, store.Rec(h.Chunk).Ord)
	}
	for i := 0; i < len(ords); i++ {
		for j := i + 1; j < len(ords); j++ {
			if ords[i]+1 == ords[j] || ords[j]+1 == ords[i] {
				t.Fatalf("в выдаче соседние куски: %d и %d", ords[i], ords[j])
			}
		}
	}
}

// TestSearchDropsSameParagraphFromTwoEditions — в библиотеке лежат разные
// издания одной книги. Куски у них совпадают лишь частично (границы страниц
// разошлись), но показанный абзац один и тот же, и выдавать его дважды незачем.
func TestSearchDropsSameParagraphFromTwoEditions(t *testing.T) {
	shared := "Плавное завершение — самый частый повод поставить обработчик сигнала. " +
		"Программа ловит SIGINT и SIGTERM, дожидается конца текущих запросов и выходит. "
	first := filler(4) + shared + "Дальше в первом издании идёт разбор системных вызовов. " + filler(4)
	second := "Во втором издании этому предшествует другое вступление. " + filler(4) +
		shared + filler(4)

	store, s := buildTestIndex(t, [][]string{{first}, {second}})
	hits, err := s.Search("плавное завершение обработчик сигнала", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("ничего не найдено")
	}
	if len(hits) > 1 {
		a := Snippet(mustText(t, store, hits[0].Chunk), "плавное завершение обработчик сигнала", 320)
		b := Snippet(mustText(t, store, hits[1].Chunk), "плавное завершение обработчик сигнала", 320)
		if strings.Contains(a, "SIGINT") && strings.Contains(b, "SIGINT") {
			t.Fatalf("один и тот же абзац выдан из двух изданий:\n1: %.70s\n2: %.70s", a, b)
		}
	}
}

func mustText(t *testing.T, store *Store, id int) string {
	t.Helper()
	s, err := store.Text(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
