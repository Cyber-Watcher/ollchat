package kb

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Ранжирование BM25.
//
// Формула складывается из трёх соображений, и каждое видно в коде ниже:
//
//  1. Чем чаще терм в куске, тем кусок важнее — но с насыщением. Десять
//     вхождений не в десять раз важнее одного, иначе побеждают куски, где слово
//     повторено ради повторения. За насыщение отвечает k1.
//  2. Чем в меньшем числе кусков встречается терм, тем он ценнее. Слово «данные»
//     есть в каждой второй книге и почти ничего не говорит, а `WaitGroup` —
//     в десятке кусков, и его вхождение почти наверняка значимо.
//  3. Одно вхождение в коротком куске весит больше, чем в длинном. Иначе
//     длинные куски выигрывают просто потому, что в них помещается больше слов.
//     Силу поправки задаёт b.
//
// Значения k1 = 1.2 и b = 0.75 — общепринятые; менять их без замеров смысла нет.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// snippetRunes — длина цитаты, по которой отсеиваются повторы. Она же
// разумна и для показа: около трёх строк текста.
const snippetRunes = 320

// Hit — найденный кусок.
type Hit struct {
	Chunk int     // номер куска в хранилище
	Score float64 // вес по BM25
	Terms int     // сколько разных термов запроса совпало
}

// SearchOpts задаёт, что и как искать.
type SearchOpts struct {
	TopK      int     // сколько кусков вернуть
	MaxPerDoc int     // не больше стольких кусков из одной книги
	Exact     string  // требовать точного вхождения этой подстроки
	Semantic  bool    // добавлять к словам поиск по смыслу
	MinCosine float64 // отсекать совпадения по смыслу слабее этого; 0 — не отсекать
	// SemanticWeight — сколько стоит мнение смыслового поиска против словесного.
	// Единица — поровну; 0.5 — словесный вдвое весомее. Ноль означает «по
	// умолчанию», а не «выключить»: выключает Semantic.
	SemanticWeight float64

	// TableBoost — надбавка кускам-таблицам при словесном поиске. 1.0 — без
	// надбавки, 0 — умолчание. Признак таблицы ставится при нарезке (FlagTable)
	// и до 03.09.2026 в отборе не участвовал.
	TableBoost float64

	// QueryTimeout — сколько ждать вектор вопроса. 0 — DefaultQueryTimeout.
	// Настройкой это стало потому, что срок зависит от машины: там, где карту
	// вечно занимает сборка графа, ждать вовсе не нужно (kb.query_timeout = 0).
	QueryTimeout time.Duration

	// RRFK — постоянная слияния списков по рангам; 0 — DefaultRRFK.
	// Настраивается только замером (--kb-eval --rrfk): в работе значение одно.
	RRFK float64

	// SemanticOnly убирает словесный список из слияния и оставляет один
	// смысловой. Нужен замеру: без этого нельзя сравнить, что даёт каждый
	// поиск по отдельности, а «стало лучше» без такого сравнения — вера.
	// В обычной работе не используется.
	SemanticOnly bool

	Docs      []uint32
	docFilter map[uint32]bool
}

// DefaultSearchOpts — разумные значения.
func DefaultSearchOpts() SearchOpts {
	return SearchOpts{TopK: 8, MaxPerDoc: 3, Semantic: true, TableBoost: DefaultTableBoost}
}

// DefaultTableBoost — надбавка кускам-таблицам при словесном поиске.
//
// **Зачем.** Таблица в книге — самый плотный вид ответа на вопрос «какое
// значение», «чем отличается», «что поддерживается»: строка таблицы отвечает
// целиком, а тот же факт в прозе размазан по абзацу. Признак таблицы у куска
// стоит с нарезки (FlagTable), но в отборе не участвовал вовсе.
//
// **Величина выбрана перебором 03.09.2026**, не на глаз:
//
//	надбавка   recall@10   MRR     nDCG    (набор терминов, 120 вопросов)
//	1.0        0.325       0.208   0.235
//	1.25       0.350       0.223   0.253
//	1.5        0.358       0.230   0.260   ← оптимум
//	2.0        0.350       0.229   0.257
//
// Проверено и на другом наборе — переформулированных вопросах, где термина
// в запросе нет: 0.129 → 0.131 recall, 0.101 → 0.104 nDCG. То есть надбавка
// не помогает одному виду вопросов за счёт другого.
const DefaultTableBoost = 1.5

// tableBoost — действующая надбавка: ноль означает умолчание.
func (o SearchOpts) tableBoost() float64 {
	if o.TableBoost > 0 {
		return o.TableBoost
	}
	return DefaultTableBoost
}

// Index — открытая на поиск коллекция: хранилище кусков и её сегменты.
type Index = searcher

// NewIndex собирает поиск по хранилищу и сегментам.
func NewIndex(store *Store, segs []*Segment) *Index { return newSearcher(store, segs) }

// searcher — то, что нужно поиску от коллекции.
type searcher struct {
	store    *Store
	segments []*Segment
	total    int     // всего кусков
	avgLen   float64 // средняя длина куска в термах
}

// newSearcher собирает поиск по хранилищу и сегментам.
func newSearcher(store *Store, segs []*Segment) *searcher {
	s := &searcher{store: store, segments: segs}
	var tokens int64
	for _, rec := range store.Recs() {
		tokens += int64(rec.Tokens)
	}
	s.total = store.Count()
	if s.total > 0 {
		s.avgLen = float64(tokens) / float64(s.total)
	}
	if s.avgLen <= 0 {
		s.avgLen = 1
	}
	return s
}

// Search ищет куски по запросу и доводит выдачу до пригодного вида.
func (s *searcher) Search(query string, opt SearchOpts) ([]Hit, error) {
	hits, err := s.Candidates(query, opt)
	if err != nil || len(hits) == 0 {
		return nil, err
	}
	return s.refine(hits, query, opt)
}

// Candidates возвращает сырой список по BM25, без доводки.
//
// Отделён от Search ради слияния с поиском по смыслу: складывать надо ранги
// полных списков, а доводка (не больше N на книгу, отсев дублей, обрезка
// по TopK) применяется уже к результату слияния — иначе она выбросит куски,
// которые второй поиск ставит первыми.
func (s *searcher) Candidates(query string, opt SearchOpts) ([]Hit, error) {
	if opt.TopK <= 0 {
		opt = DefaultSearchOpts()
	}
	terms := queryTerms(query, s)
	if len(terms) == 0 {
		return nil, nil
	}
	if len(opt.Docs) > 0 {
		opt.docFilter = make(map[uint32]bool, len(opt.Docs))
		for _, d := range opt.Docs {
			opt.docFilter[d] = true
		}
	}

	scores := map[int]float64{}
	matched := map[int]int{}
	for _, term := range terms {
		df := 0
		for _, seg := range s.segments {
			df += seg.DF(term)
		}
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(s.total)-float64(df)+0.5)/(float64(df)+0.5))
		for _, seg := range s.segments {
			list, err := seg.Postings(term)
			if err != nil {
				return nil, err
			}
			for _, p := range list {
				id := int(p.chunk)
				if id >= s.total {
					continue
				}
				rec := s.store.Rec(id)
				if opt.docFilter != nil && !opt.docFilter[rec.Doc] {
					continue
				}
				length := float64(rec.Tokens)
				if length <= 0 {
					length = s.avgLen
				}
				tf := float64(p.tf)
				norm := tf + bm25K1*(1-bm25B+bm25B*length/s.avgLen)
				scores[id] += idf * tf * (bm25K1 + 1) / norm
				matched[id]++
			}
		}
	}
	// Надбавка кускам-таблицам: строка таблицы отвечает на вопрос целиком,
	// тогда как тот же факт в прозе размазан по абзацу.
	if boost := opt.tableBoost(); boost != 1 {
		for id := range scores {
			if s.store.Rec(id).Flags&uint16(FlagTable) != 0 {
				scores[id] *= boost
			}
		}
	}

	if len(scores) == 0 {
		return nil, nil
	}

	hits := make([]Hit, 0, len(scores))
	for id, sc := range scores {
		hits = append(hits, Hit{Chunk: id, Score: sc, Terms: matched[id]})
	}
	// Совпадение по нескольким термам ценнее одного частого: иначе запрос
	// из трёх слов выигрывает кусок, где десять раз повторено одно из них.
	for i := range hits {
		hits[i].Score *= 1 + 0.15*float64(hits[i].Terms-1)
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].Chunk < hits[b].Chunk
	})
	return hits, nil
}

// refine доводит выдачу до вида, пригодного для человека и модели.
func (s *searcher) refine(hits []Hit, query string, opt SearchOpts) ([]Hit, error) {
	// Смотрим не всё, а верхушку: дальше идёт заведомый шум.
	limit := opt.TopK * 12
	if limit > len(hits) {
		limit = len(hits)
	}
	head := hits[:limit]

	exact := strings.ToLower(strings.TrimSpace(opt.Exact))
	if exact == "" {
		exact = strings.ToLower(strings.TrimSpace(query))
	}

	texts, err := s.store.Texts(chunkIDs(head))
	if err != nil {
		return nil, err
	}

	// Точное вхождение запроса — сильный признак. Он же лечит переусердствовавший
	// стеммер: основы совпали случайно, а слова разные.
	if len([]rune(exact)) >= 3 {
		for i := range head {
			if strings.Contains(strings.ToLower(texts[head[i].Chunk]), exact) {
				head[i].Score *= 1.5
			}
		}
		sort.SliceStable(head, func(a, b int) bool { return head[a].Score > head[b].Score })
	}

	perDoc := map[uint32]int{}
	taken := map[uint32][]uint32{} // книга → порядковые номера взятых кусков
	seen := make([]string, 0, opt.TopK)
	out := make([]Hit, 0, opt.TopK)
	for _, h := range head {
		rec := s.store.Rec(h.Chunk)
		doc := rec.Doc
		if opt.MaxPerDoc > 0 && perDoc[doc] >= opt.MaxPerDoc {
			continue // одна книга не должна занимать всю выдачу
		}
		// Соседние куски перекрываются по построению — треть текста у них
		// общая. Выдавать оба бессмысленно: человек видит одну и ту же цитату
		// дважды, а модель тратит на это контекст.
		if adjacentTaken(taken[doc], rec.Ord) {
			continue
		}
		// Сравниваем не куски целиком, а то, что реально увидят: цитату.
		// У двух изданий одной книги куски совпадают лишь на 40% — границы
		// страниц разошлись, — но показанный абзац один и тот же, и выдавать
		// его дважды незачем. Измерено на двух изданиях «The Anatomy of Go»
		// в живой коллекции.
		snippet := Snippet(texts[h.Chunk], query, snippetRunes)
		if nearDuplicate(snippet, seen) {
			continue
		}
		perDoc[doc]++
		taken[doc] = append(taken[doc], rec.Ord)
		seen = append(seen, snippet)
		out = append(out, h)
		if len(out) >= opt.TopK {
			break
		}
	}
	return out, nil
}

// adjacentTaken сообщает, что рядом уже взят кусок той же книги.
func adjacentTaken(ords []uint32, ord uint32) bool {
	for _, o := range ords {
		if o == ord || o+1 == ord || ord+1 == o {
			return true
		}
	}
	return false
}

func chunkIDs(hits []Hit) []int {
	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.Chunk
	}
	return out
}

// nearDuplicate ловит почти одинаковые куски: перекрытие соседних кусков и одно
// и то же место в разных изданиях книги.
func nearDuplicate(text string, seen []string) bool {
	a := shingles(text)
	if len(a) == 0 {
		return false
	}
	for _, s := range seen {
		b := shingles(s)
		if len(b) == 0 {
			continue
		}
		common := 0
		for k := range a {
			if b[k] {
				common++
			}
		}
		smaller := len(a)
		if len(b) < smaller {
			smaller = len(b)
		}
		if common*10 >= smaller*7 {
			return true
		}
	}
	return false
}

// shingles — множество троек слов: устойчивый отпечаток текста.
func shingles(text string) map[string]bool {
	words := strings.Fields(strings.ToLower(text))
	if len(words) < 3 {
		return nil
	}
	out := make(map[string]bool, len(words))
	for i := 0; i+2 < len(words); i++ {
		out[words[i]+" "+words[i+1]+" "+words[i+2]] = true
	}
	return out
}

// queryTerms разбирает запрос и отбрасывает слишком частые слова, если есть
// более редкие: иначе «как работает канал» выродится в поиск по слову «как».
func queryTerms(query string, s *searcher) []string {
	toks := Tokens(query, nil)
	seen := map[string]bool{}
	type termDF struct {
		term string
		df   int
	}
	var all []termDF
	for _, t := range toks {
		if seen[t.Term] {
			continue
		}
		seen[t.Term] = true
		df := 0
		for _, seg := range s.segments {
			df += seg.DF(t.Term)
		}
		all = append(all, termDF{t.Term, df})
	}
	if len(all) == 0 {
		return nil
	}
	// Редкие термы есть — выбрасываем те, что встречаются больше чем в трети
	// кусков: они ничего не отбирают, только замедляют.
	rare := false
	cut := s.total / 3
	for _, t := range all {
		if t.df > 0 && t.df <= cut {
			rare = true
			break
		}
	}
	out := make([]string, 0, len(all))
	for _, t := range all {
		if rare && t.df > cut {
			continue
		}
		out = append(out, t.term)
	}
	return out
}

// Snippet вырезает из куска место, где встретились слова запроса.
//
// Показывать начало куска нельзя: кусок длиной в 1200 символов, а совпадение
// может быть в конце — тогда и человек, и модель видят не то, что нашлось,
// и решают, что поиск ошибся. Проверено на живых книгах: первые 150 символов
// куска сплошь и рядом не имеют отношения к запросу.
func Snippet(text, query string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 320
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return strings.TrimSpace(text)
	}

	wanted := map[string]bool{}
	for _, t := range Tokens(query, nil) {
		wanted[t.Term] = true
	}
	if len(wanted) == 0 {
		return strings.TrimSpace(string(runes[:maxRunes])) + "…"
	}

	// Окна привязываются к самим совпадениям, а не ко всем подряд словам.
	// Иначе выигрывает окно, которое начинается задолго до совпадения и лишь
	// краем задевает его, — а после отступа назад совпадение вовсе выпадает
	// за конец цитаты.
	words, offsets := wordPositions(runes)
	if len(words) == 0 {
		return strings.TrimSpace(string(runes[:maxRunes])) + "…"
	}
	hitAt := make([]bool, len(words))
	var anchors []int
	for i, w := range words {
		for _, t := range Tokens(w, nil) {
			if wanted[t.Term] {
				hitAt[i] = true
				anchors = append(anchors, i)
				break
			}
		}
	}
	if len(anchors) == 0 {
		return strings.TrimSpace(string(runes[:maxRunes])) + "…"
	}

	// Небольшой зачин перед совпадением: цитата, начинающаяся ровно с искомого
	// слова, читается плохо.
	lead := maxRunes / 6
	best, bestScore := anchors[0], -1
	for _, a := range anchors {
		start := offsets[a] - lead
		if start < 0 {
			start = 0
		}
		seen := map[string]bool{}
		for j := 0; j < len(words); j++ {
			if offsets[j] < start {
				continue
			}
			if offsets[j]+len([]rune(words[j])) > start+maxRunes {
				break
			}
			if !hitAt[j] {
				continue
			}
			for _, t := range Tokens(words[j], nil) {
				if wanted[t.Term] {
					seen[t.Term] = true
				}
			}
		}
		if len(seen) > bestScore {
			best, bestScore = a, len(seen)
		}
	}

	start := offsets[best] - lead
	if start < 0 {
		start = 0
	}
	start = wordStart(runes, start)
	end := start + maxRunes
	if end > len(runes) {
		end = len(runes)
	}
	end = wordEnd(runes, end)

	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// wordPositions возвращает слова и позиции их начал.
func wordPositions(runes []rune) ([]string, []int) {
	var words []string
	var offs []int
	start := -1
	for i, r := range runes {
		if isWordRune(r) || isConnector(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words = append(words, string(runes[start:i]))
			offs = append(offs, start)
			start = -1
		}
	}
	if start >= 0 {
		words = append(words, string(runes[start:]))
		offs = append(offs, start)
	}
	return words, offs
}

// wordStart сдвигает границу к началу слова, чтобы цитата не начиналась
// с обрубка.
func wordStart(runes []rune, i int) int {
	for i > 0 && isWordRune(runes[i-1]) {
		i--
	}
	return i
}

func wordEnd(runes []rune, i int) int {
	for i < len(runes) && isWordRune(runes[i]) {
		i++
	}
	return i
}
