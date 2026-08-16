package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Коллекции — верхний уровень базы знаний.
//
// Коллекция это папка с книгами, превращённая в индекс: реестр книг, тексты
// кусков, сегменты. Коллекций несколько, по темам, и собираются они по
// требованию: «сто книг по Go» — это `/kb add go <путь>`, а не индексация всей
// библиотеки.
//
// Раскладка на диске:
//
//	<kb.dir>/collections/<имя>/
//	  meta.json     имя, описание, версия разбора, размеры кусков, корни
//	  docs.jsonl    реестр книг, только дозапись
//	  deleted.ids   помеченные удалёнными книги, только дозапись
//	  journal.log   журнал коммитов, только дозапись
//	  LOCK          признак работающей индексации
//	  chunks.dat    тексты кусков
//	  chunks.idx    указатели
//	  seg-00001/    сегменты инвертированного индекса
//
// Всё, кроме сегментов, пишется дозаписью. Это и даёт возможность прервать
// работу в любой момент и продолжить с того же места.

// Meta — сведения о коллекции.
type Meta struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Analyzer    string     `json:"analyzer"`
	Chunk       ChunkOpts  `json:"chunk"`
	Roots       []string   `json:"roots,omitempty"`
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	NextDoc     uint32     `json:"next_doc"`
	NextSeg     int        `json:"next_seg"`
	State       StoreState `json:"state"`
}

// BookKind — что стало с книгой при индексации.
type BookKind string

const (
	BookOK      BookKind = "ok"      // прочитана и проиндексирована
	BookScan    BookKind = "scan"    // страницы-картинки, текста нет
	BookGarbled BookKind = "garbled" // текст есть, но шрифты не дают его прочесть
	BookBroken  BookKind = "broken"  // разбор не удался
	BookSkipped BookKind = "skipped" // слишком велика или не документ
	BookGone    BookKind = "gone"    // пропала с диска
)

// BookRec — запись о книге в реестре коллекции.
type BookRec struct {
	ID       uint32   `json:"id"`
	Path     string   `json:"path"`
	Size     int64    `json:"size"`
	ModTime  int64    `json:"mtime"`
	Kind     BookKind `json:"kind"`
	Title    string   `json:"title,omitempty"`
	Author   string   `json:"author,omitempty"`
	Format   string   `json:"format,omitempty"`
	Units    int      `json:"units,omitempty"`
	UnitWord string   `json:"unit,omitempty"`
	Chunks   int      `json:"chunks,omitempty"`
	Err      string   `json:"err,omitempty"`
	At       int64    `json:"at"`
}

// Unchanged сообщает, что файл на диске тот же самый: размер и время совпали.
// Хеш не считается намеренно — на десяти тысячах книг это часы чтения, а пара
// «размер и время» ошибается исчезающе редко.
func (b BookRec) Unchanged(info os.FileInfo) bool {
	return b.Size == info.Size() && b.ModTime == info.ModTime().UnixNano()
}

// DefaultAnswerStyle — как модель должна отвечать по найденным фрагментам.
//
// Это **политика**, а не договор инструмента: она про то, каким пользователь
// хочет видеть ответ, а не про то, что инструмент принимает и возвращает.
// Поэтому её можно переопределить настройкой kb.answer_style — договор
// остаётся в коде рядом с реализацией, а вкус переезжает в конфиг.
//
// Формулировка выстрадана. Первая версия звучала «ОБЯЗАТЕЛЬНО ссылайся…
// не додумывай», и модель поняла её буквально: на вопрос «как работают
// горутины» выдала подборку цитат без единой собственной мысли. Требование
// ссылаться обязано быть уравновешено требованием объяснять.
const DefaultAnswerStyle = "Отвечай своими словами и объясняй по существу — пересказ цитат " +
	"ответом не считается. Взятое из книги помечай [1] или (книга, стр. N). Собственные " +
	"пояснения и примеры приветствуются; нельзя лишь приписывать книгам того, чего в них нет. " +
	"Если во фрагментах ответа нет — скажи об этом и ответь сам."

// AnswerStyle возвращает заданную пользователем политику или встроенную.
func AnswerStyle(custom string) string {
	if s := strings.TrimSpace(custom); s != "" {
		return s
	}
	return DefaultAnswerStyle
}

// Base — корень базы знаний: набор коллекций.
type Base struct {
	dir string

	mu   sync.Mutex
	open map[string]*Collection
}

// OpenBase открывает базу знаний, создавая каталог при надобности.
func OpenBase(dir string) (*Base, error) {
	if dir == "" {
		return nil, fmt.Errorf("не задан каталог базы знаний")
	}
	if err := os.MkdirAll(filepath.Join(dir, "collections"), 0o755); err != nil {
		return nil, err
	}
	return &Base{dir: dir, open: map[string]*Collection{}}, nil
}

// Dir — каталог базы.
func (b *Base) Dir() string { return b.dir }

func (b *Base) collDir(name string) string {
	return filepath.Join(b.dir, "collections", name)
}

// tempDir — рабочий каталог уплотнения. Имя с точки: Names() такие пропускает,
// иначе отставленная в сторону коллекция показалась бы отдельной.
func (b *Base) tempDir(name string) string {
	return filepath.Join(b.dir, "collections", "."+name)
}

// ValidName проверяет имя коллекции: оно становится именем каталога.
func ValidName(name string) error {
	if name == "" {
		return fmt.Errorf("пустое имя коллекции")
	}
	if len(name) > 64 {
		return fmt.Errorf("слишком длинное имя коллекции")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		case r >= 'A' && r <= 'Z':
		default:
			return fmt.Errorf("в имени коллекции допустимы только латинские буквы, цифры, дефис и подчёркивание")
		}
	}
	return nil
}

// Names перечисляет коллекции.
func (b *Base) Names() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(b.dir, "collections"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.collDir(e.Name()), "meta.json")); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Create заводит пустую коллекцию.
func (b *Base) Create(name, description string) (*Collection, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	dir := b.collDir(name)
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err == nil {
		return nil, fmt.Errorf("коллекция %q уже есть", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	meta := Meta{
		Name: name, Description: description,
		Analyzer: AnalyzerVersion, Chunk: DefaultChunkOpts(),
		Created: time.Now(), Updated: time.Now(),
		NextDoc: 1, NextSeg: 1,
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return nil, err
	}
	return b.Open(name)
}

// Open открывает коллекцию на чтение и поиск.
func (b *Base) Open(name string) (*Collection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c, ok := b.open[name]; ok {
		return c, nil
	}
	if err := ValidName(name); err != nil {
		return nil, err
	}
	dir := b.collDir(name)
	// Прерванное уплотнение доводится до конца прежде, чем читать meta.json:
	// каталога коллекции в этот момент может не быть вовсе.
	recoverCompaction(b, name, dir)
	var meta Meta
	if err := readJSON(filepath.Join(dir, "meta.json"), &meta); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("коллекции %q нет", name)
		}
		return nil, err
	}
	c := &Collection{base: b, name: name, dir: dir, meta: meta}
	if err := c.load(); err != nil {
		return nil, err
	}
	b.open[name] = c
	return c, nil
}

// Close закрывает все открытые коллекции.
func (b *Base) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var err error
	for name, c := range b.open {
		if cerr := c.closeFiles(); err == nil {
			err = cerr
		}
		delete(b.open, name)
	}
	return err
}

// Remove удаляет коллекцию целиком.
func (b *Base) Remove(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	b.mu.Lock()
	if c, ok := b.open[name]; ok {
		c.closeFiles()
		delete(b.open, name)
	}
	b.mu.Unlock()
	dir := b.collDir(name)
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		return fmt.Errorf("коллекции %q нет", name)
	}
	return os.RemoveAll(dir)
}

// Collection — открытая коллекция.
type Collection struct {
	base *Base
	name string
	dir  string

	mu      sync.RWMutex
	meta    Meta
	docs    []BookRec
	byPath  map[string]int // путь → место в docs
	deleted map[uint32]bool
	store   *Store
	segs    []*Segment
	index   *Index
	vectors *Vectors // смыслы кусков; nil, когда не посчитаны

	// Заметка о последней выдаче под своим замком: поиск идёт под замком
	// на чтение, а писать под ним нельзя — это гонка.
	noteMu sync.Mutex
	note   string
}

// load читает реестр, помеченные удалёнными книги, хранилище и сегменты.
func (c *Collection) load() error {
	if err := c.loadDocs(); err != nil {
		return err
	}
	if err := c.loadDeleted(); err != nil {
		return err
	}
	return c.reopenIndex()
}

func (c *Collection) loadDocs() error {
	c.docs = nil
	c.byPath = map[string]int{}
	data, err := os.ReadFile(filepath.Join(c.dir, "docs.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec BookRec
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // битую строку пропускаем: реестр только дозаписывается
		}
		// Повторная запись о том же пути заменяет прежнюю: так книга,
		// переиндексированная после изменения, не двоится.
		if i, ok := c.byPath[rec.Path]; ok {
			c.docs[i] = rec
			continue
		}
		c.byPath[rec.Path] = len(c.docs)
		c.docs = append(c.docs, rec)
	}
	return nil
}

func (c *Collection) loadDeleted() error {
	c.deleted = map[uint32]bool{}
	data, err := os.ReadFile(filepath.Join(c.dir, "deleted.ids"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Fields(string(data)) {
		if id, err := strconv.ParseUint(line, 10, 32); err == nil {
			c.deleted[uint32(id)] = true
		}
	}
	return nil
}

// reopenIndex переоткрывает хранилище и сегменты: вызывается после индексации.
func (c *Collection) reopenIndex() error {
	c.closeFiles()
	if _, err := os.Stat(filepath.Join(c.dir, "chunks.idx")); err != nil {
		return nil // коллекция ещё пуста
	}
	store, err := OpenStore(c.dir)
	if err != nil {
		return err
	}
	dirs, err := segmentDirs(c.dir)
	if err != nil {
		store.Close()
		return err
	}
	var segs []*Segment
	for _, d := range dirs {
		seg, err := OpenSegment(d)
		if err != nil {
			continue // недостроенный или чужой сегмент пропускаем
		}
		segs = append(segs, seg)
	}
	// Векторы читаются вместе с индексом: их отсутствие — обычное дело,
	// а испорченный файл лучше заметить при открытии, а не в первом поиске.
	vecs, err := OpenVectors(c.dir)
	if err != nil {
		store.Close()
		for _, seg := range segs {
			seg.Close()
		}
		return err
	}
	c.store = store
	c.segs = segs
	c.vectors = vecs
	c.index = NewIndex(store, segs)
	return nil
}

func (c *Collection) closeFiles() error {
	for _, s := range c.segs {
		s.Close()
	}
	c.segs = nil
	c.vectors = nil
	if c.store != nil {
		c.store.Close()
		c.store = nil
	}
	c.index = nil
	return nil
}

// Name — имя коллекции.
func (c *Collection) Name() string { return c.name }

// Dir — каталог коллекции.
func (c *Collection) Dir() string { return c.dir }

// Meta возвращает сведения о коллекции.
func (c *Collection) Meta() Meta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.meta
}

// Books возвращает реестр книг.
func (c *Collection) Books() []BookRec {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]BookRec, len(c.docs))
	copy(out, c.docs)
	return out
}

// FolderStat — сколько книг и кусков пришло из одной подпапки.
type FolderStat struct {
	Folder string // путь относительно корня; "." — книги прямо в корне
	Books  int
	Chunks int
	Scans  int // сканы и сбойные: они в коллекции есть, но искать по ним нечего
}

// Breakdown разбивает коллекцию по подпапкам корней.
//
// Нужна, чтобы понимать состав библиотеки. На двух сотнях книг «книг: 198»
// не говорит ничего: выдача по запросу про каналы упирается в то, что книг
// по Go шесть, а по C# — шестьдесят шесть, и без разбивки этого не увидеть.
//
// Группировка по первому уровню под корнем: глубже дробить смысла нет, темы
// в библиотеках раскладывают именно так.
func (c *Collection) Breakdown() []FolderStat {
	c.mu.RLock()
	defer c.mu.RUnlock()

	byFolder := map[string]*FolderStat{}
	for _, d := range c.docs {
		if c.deleted[d.ID] {
			continue
		}
		f := topFolder(d.Path, c.meta.Roots)
		st := byFolder[f]
		if st == nil {
			st = &FolderStat{Folder: f}
			byFolder[f] = st
		}
		st.Books++
		st.Chunks += d.Chunks
		if d.Kind != BookOK {
			st.Scans++
		}
	}
	out := make([]FolderStat, 0, len(byFolder))
	for _, st := range byFolder {
		out = append(out, *st)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Books != out[b].Books {
			return out[a].Books > out[b].Books
		}
		return out[a].Folder < out[b].Folder
	})
	return out
}

// topFolder возвращает первый уровень пути книги под её корнем.
//
// Книга может лежать и вне записанных корней — например, если корень потом
// убрали. Тогда честнее показать имя её каталога, чем прятать её в «прочее».
func topFolder(path string, roots []string) string {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if i := strings.IndexByte(rel, filepath.Separator); i > 0 {
			return rel[:i]
		}
		return "." // книга лежит прямо в корне
	}
	return filepath.Base(filepath.Dir(path))
}

// Book находит книгу по её номеру.
func (c *Collection) Book(id uint32) (BookRec, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, d := range c.docs {
		if d.ID == id {
			return d, true
		}
	}
	return BookRec{}, false
}

// Stats — сводка по коллекции.
type Stats struct {
	Books    int
	Indexed  int // книг с текстом
	Scans    int
	Broken   int
	Deleted  int
	Chunks   int
	Terms    int
	Segments int
	Bytes    int64
	Analyzer string
	Stale    bool // версия разбора разошлась с нынешней

	Vectors  int    // сколько кусков обеспечено смыслами
	VecModel string // какой моделью посчитаны
	VecDim   int
}

// Stats собирает сводку.
func (c *Collection) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := Stats{Analyzer: c.meta.Analyzer, Stale: c.meta.Analyzer != AnalyzerVersion}
	for _, d := range c.docs {
		if c.deleted[d.ID] {
			st.Deleted++
			continue
		}
		st.Books++
		switch d.Kind {
		case BookOK:
			st.Indexed++
		case BookScan:
			st.Scans++
		case BookBroken, BookGarbled:
			st.Broken++
		}
	}
	if c.store != nil {
		st.Chunks = c.store.Count()
	}
	st.Segments = len(c.segs)
	for _, s := range c.segs {
		st.Terms += s.Terms()
	}
	st.Bytes = dirSize(c.dir)
	if c.vectors != nil {
		m := c.vectors.Meta()
		st.Vectors, st.VecModel, st.VecDim = m.Count, m.Model, m.Dim
	}
	return st
}

// Result — найденный кусок вместе со сведениями о книге.
type Result struct {
	ID       string // «go/12#37» — по нему читается окружение куска
	Chunk    int    // номер куска в хранилище
	Book     string // заголовок книги
	Author   string
	Path     string
	UnitFrom int
	UnitTo   int
	Unit     string // «стр.» или «разд.»
	Score    float64
	Text     string // текст куска целиком
	Snippet  string // вырезанная цитата вокруг совпадения
	Code     bool
}

// Search ищет по коллекции.
func (c *Collection) Search(query string, opt SearchOpts) ([]Result, error) {
	return c.SearchWith(context.Background(), query, opt, nil)
}

// SearchWith ищет с учётом смысла, когда есть чем его посчитать.
//
// emb необязателен, и это принципиально: без него, при недоступном сервере
// эмбеддингов или при непосчитанных векторах выдача равна той, что была
// до появления смыслового поиска. Ошибка векторизации запроса не роняет поиск,
// а лишь оставляет один список из двух — о ней сообщается отдельно, через
// SearchNote последнего вызова.
func (c *Collection) SearchWith(ctx context.Context, query string, opt SearchOpts, emb Embedder) ([]Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.index == nil {
		return nil, nil
	}
	// Удалённые книги из выдачи исключаем: помеченные удалёнными куски
	// физически остаются до уплотнения.
	if len(c.deleted) > 0 && len(opt.Docs) == 0 {
		for _, d := range c.docs {
			if !c.deleted[d.ID] && d.Kind == BookOK {
				opt.Docs = append(opt.Docs, d.ID)
			}
		}
	}
	hits, note, err := c.hybrid(ctx, query, opt, emb)
	if err != nil {
		return nil, err
	}
	c.setNote(note)
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		rec := c.store.Rec(h.Chunk)
		text, err := c.store.Text(h.Chunk)
		if err != nil {
			continue
		}
		res := Result{
			ID:       fmt.Sprintf("%s/%d#%d", c.name, rec.Doc, rec.Ord),
			Chunk:    h.Chunk,
			UnitFrom: int(rec.UnitFrom), UnitTo: int(rec.UnitTo),
			Unit:    "стр.",
			Score:   h.Score,
			Text:    text,
			Snippet: Snippet(text, query, snippetRunes),
			Code:    ChunkFlags(rec.Flags)&FlagCode != 0,
		}
		for _, d := range c.docs {
			if d.ID == rec.Doc {
				res.Book, res.Author, res.Path = d.Title, d.Author, d.Path
				if d.UnitWord == "разделов" {
					res.Unit = "разд."
				}
				if res.Book == "" {
					res.Book = filepath.Base(d.Path)
				}
				break
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// hybrid собирает выдачу из двух поисков: по словам и по смыслу.
func (c *Collection) hybrid(ctx context.Context, query string, opt SearchOpts, emb Embedder) ([]Hit, string, error) {
	if opt.TopK <= 0 {
		opt = DefaultSearchOpts()
	}
	words, err := c.index.Candidates(query, opt)
	if err != nil {
		return nil, "", err
	}

	vecs, note := c.semanticHits(ctx, query, opt, emb)
	if len(vecs) == 0 {
		if len(words) == 0 {
			return nil, note, nil
		}
		hits, err := c.index.refine(words, query, opt)
		return hits, note, err
	}
	// Сырые списки обрезаем до разумной верхушки: дальше идёт шум, а слияние
	// должно сравнивать места, а не длину списков.
	if len(words) > vectorCandidates {
		words = words[:vectorCandidates]
	}
	wv := opt.SemanticWeight
	if wv <= 0 {
		wv = defaultSemanticWeight
	}
	hits, err := c.index.refine(fuse([]float64{1, wv}, words, vecs), query, opt)
	return hits, note, err
}

// semanticHits возвращает ближайшие по смыслу куски или ничего, если смысл
// сейчас недоступен. Причина недоступности записывается в note.
func (c *Collection) semanticHits(ctx context.Context, query string, opt SearchOpts, emb Embedder) ([]Hit, string) {
	if emb == nil || !opt.Semantic || c.vectors == nil {
		return nil, ""
	}
	m := c.vectors.Meta()
	if !m.Compatible(emb.Model(), 0) {
		return nil, fmt.Sprintf("смыслы посчитаны моделью %s, а в настройках %s — поиск идёт только по словам; "+
			"пересчитать: /kb embed %s --recount", m.Model, emb.Model(), c.name)
	}
	q, err := embedQuery(ctx, emb, query, m.Dim)
	if err != nil {
		return nil, "сервер эмбеддингов недоступен (" + err.Error() + ") — поиск идёт только по словам"
	}
	if len(q) == 0 {
		return nil, ""
	}
	var allow map[uint32]bool
	if len(opt.Docs) > 0 {
		allow = make(map[uint32]bool, len(opt.Docs))
		for _, d := range opt.Docs {
			allow[d] = true
		}
	}
	hits := c.searchVectors(q, vectorCandidates, allow, opt.MinCosine)
	note := ""
	if total := c.store.Count(); m.Count < total {
		note = fmt.Sprintf("смыслы посчитаны для %d%% кусков — остальное найдено только по словам",
			m.Count*100/total)
	}
	return hits, note
}

// setNote запоминает заметку о последней выдаче.
func (c *Collection) setNote(note string) {
	c.noteMu.Lock()
	c.note = note
	c.noteMu.Unlock()
}

// DebugVectorHits возвращает чистую выдачу поиска по смыслу с косинусами.
// Нужна для настройки: без неё непонятно, сильное совпадение вытеснило
// словесное или случайное.
func (c *Collection) DebugVectorHits(ctx context.Context, emb Embedder, query string, n int) ([]Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.vectors == nil || emb == nil {
		return nil, nil
	}
	q, err := embedQuery(ctx, emb, query, c.vectors.Dim())
	if err != nil {
		return nil, err
	}
	hits := c.searchVectors(q, n, nil, 0)
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		rec := c.store.Rec(h.Chunk)
		r := Result{Chunk: h.Chunk, Score: h.Score, UnitFrom: int(rec.UnitFrom)}
		if b, ok := c.bookByID(rec.Doc); ok {
			r.Book, r.Path = bookTitle(b), b.Path
		}
		out = append(out, r)
	}
	return out, nil
}

// SearchNote — что стоит знать о последней выдаче: почему смысл не участвовал
// или участвовал наполовину. Пусто, когда всё в порядке.
func (c *Collection) SearchNote() string {
	c.noteMu.Lock()
	defer c.noteMu.Unlock()
	return c.note
}

// Around возвращает кусок и его соседей: модель просит расширить цитату,
// когда найденного мало.
func (c *Collection) Around(id string, around int) ([]Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil {
		return nil, fmt.Errorf("коллекция %q пуста", c.name)
	}
	doc, ord, err := parseChunkID(id, c.name)
	if err != nil {
		return nil, err
	}
	if around < 0 {
		around = 0
	}
	if around > 5 {
		around = 5
	}
	var out []Result
	for i := 0; i < c.store.Count(); i++ {
		rec := c.store.Rec(i)
		if rec.Doc != doc {
			continue
		}
		if int(rec.Ord) < int(ord)-around || int(rec.Ord) > int(ord)+around {
			continue
		}
		text, err := c.store.Text(i)
		if err != nil {
			continue
		}
		res := Result{
			ID:       fmt.Sprintf("%s/%d#%d", c.name, rec.Doc, rec.Ord),
			Chunk:    i,
			UnitFrom: int(rec.UnitFrom), UnitTo: int(rec.UnitTo),
			Unit: "стр.", Text: text, Snippet: text,
			Code: ChunkFlags(rec.Flags)&FlagCode != 0,
		}
		for _, d := range c.docs {
			if d.ID == rec.Doc {
				res.Book, res.Author, res.Path = d.Title, d.Author, d.Path
				if res.Book == "" {
					res.Book = filepath.Base(d.Path)
				}
				break
			}
		}
		out = append(out, res)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("куска %q нет в коллекции", id)
	}
	return out, nil
}

// parseChunkID разбирает «go/12#37».
func parseChunkID(id, coll string) (doc uint32, ord uint32, err error) {
	s := id
	if i := strings.Index(s, "/"); i >= 0 {
		if s[:i] != coll {
			return 0, 0, fmt.Errorf("кусок %q не из коллекции %q", id, coll)
		}
		s = s[i+1:]
	}
	parts := strings.SplitN(s, "#", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("непонятный номер куска: %q (нужен вид «%s/12#37»)", id, coll)
	}
	d, err1 := strconv.ParseUint(parts[0], 10, 32)
	o, err2 := strconv.ParseUint(parts[1], 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("непонятный номер куска: %q", id)
	}
	return uint32(d), uint32(o), nil
}

// lock защищает коллекцию от двух индексаций сразу.
func (c *Collection) lock() error {
	path := filepath.Join(c.dir, "LOCK")
	if data, err := os.ReadFile(path); err == nil {
		pid, _ := strconv.Atoi(strings.Fields(string(data))[0])
		if pid > 0 && processAlive(pid) {
			return fmt.Errorf("коллекция %q уже индексируется (процесс %d)", c.name, pid)
		}
		// Замок от умершего процесса снимаем сами.
		os.Remove(path)
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))), 0o644)
}

func (c *Collection) unlock() { os.Remove(filepath.Join(c.dir, "LOCK")) }

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(sigZero) == nil
}

func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
