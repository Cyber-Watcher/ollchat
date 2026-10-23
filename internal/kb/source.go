package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Откуда берётся знание: локальные файлы или общее хранилище.
//
// **Зачем интерфейс.** До сих пор коллекция была только локальной — каталог
// файлов рядом с пользователем. Для одного человека это лучший вариант из
// возможных: ни служб, ни портов, ни бэкапов, и весь бинарь по-прежнему
// один файл. Но в организации библиотека должна быть общей: администратор
// собирает индекс и граф один раз, сотрудники читают готовое. Файлы этого
// не дают — по сети каталог не разделить.
//
// Поэтому чтение вынесено за интерфейс, а запись — нет. Разделение
// не случайное:
//
//   - **читают все, пишет один.** Индексация, счёт векторов, сборка графа
//     остаются операциями над локальными файлами и в интерфейс не входят.
//     Удалённый клиент их выполнить не может физически, и притворяться,
//     что может, — значит однажды получить наполовину собранный индекс;
//   - **интерфейс узкий нарочно.** В нём ровно то, что зовут поиск, инструменты
//     модели и подмешивание. Чем он шире, тем труднее написать второй драйвер,
//     а второй драйвер — весь смысл затеи.
//
// **`*Collection` удовлетворяет ему как есть**, без единой правки: подписи
// взяты с него. Это сделано намеренно — шов, ради которого пришлось бы
// переписывать работающий и замеренный код, был бы плохим швом.
//
// Образец уже есть в проекте: `internal/graph/build.go` объявляет свой узкий
// интерфейс вместо того, чтобы брать `*kb.Collection` целиком.

// Source — источник знаний, доступный на чтение.
//
// Имя выбрано потому, что `Store` в пакете уже занято хранилищем кусков
// (`store.go`), а переименовывать работающий тип ради нового интерфейса —
// плохая сделка.
//
// Реализации: локальный `*Collection` (умолчание) и сетевые драйверы
// в `internal/kbremote`.
type Source interface {
	// Name — имя коллекции. Печатается в ссылках выдачи.
	Name() string

	// SearchWith ищет с учётом смысла, когда есть чем его посчитать.
	//
	// Эмбеддер передаётся, а не вектор: у локальной коллекции на нём держится
	// вся мягкость отказа — недоступный сервер эмбеддингов не роняет поиск,
	// а оставляет словесную выдачу и объяснение в SearchNote. Сетевой драйвер
	// считает вектор сам и шлёт его вместе с запросом, сохраняя то же поведение.
	SearchWith(ctx context.Context, query string, opt SearchOpts, emb Embedder) ([]Result, error)

	// Around отдаёт кусок целиком вместе с соседями — за этим ходит kb_read,
	// когда в выдержке оборвана мысль.
	Around(id string, around int) ([]Result, error)

	// Books — реестр книг коллекции. Нужен отбору по книге и отчётам о составе.
	Books() []BookRec

	// Stats — состав коллекции: книг, кусков, векторов, чем считаны смыслы.
	Stats() Stats

	// SearchNote — почему смысловая часть последнего поиска не отработала
	// или отработала частично. Пусто — всё в порядке.
	//
	// Строка предназначена человеку и показывается в ленте: молчаливое
	// вырождение смыслового поиска в словесный — ровно та беда, ради которой
	// эта строка заведена.
	SearchNote() string
}

// Library — набор коллекций. То, что `Base` для локальных файлов.
type Library interface {
	// Names — имена доступных коллекций.
	Names() ([]string, error)

	// Source открывает коллекцию на чтение.
	Source(name string) (Source, error)
}

// Source открывает коллекцию на чтение.
//
// Отдельный метод рядом с Open нужен из-за системы типов: Open отдаёт
// `*Collection`, и подставить его туда, где ждут `Store`, нельзя — Go
// не выводит совместимость возвращаемых типов. Три строки здесь дешевле,
// чем обёртка на каждом месте вызова.
func (b *Base) Source(name string) (Source, error) {
	c, err := b.Open(name)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Проверки во время сборки: реализации обязаны соответствовать интерфейсам.
// Без них расхождение всплыло бы у первого же вызывающего, а не здесь.
var (
	_ Source  = (*Collection)(nil)
	_ Library = (*Base)(nil)
	_ Library = Fallback{}
	_ Source  = (*fallbackSource)(nil)
)

// ── Служба не отвечает — свои файлы (этап 121, А4) ──────────────────────────

// ErrUnreachable — служба общей библиотеки не дала ответа вовсе: отказ
// в соединении, срок ожидания, обрыв посреди ответа. Сетевой драйвер
// помечает ею такие ошибки, не меняя их текста: адрес и причина видны как
// прежде.
//
// **Только по ней клиент переходит на свои файлы** (решение владельца
// 22.10.2026). Отказ по смыслу — неверный ключ, неизвестная коллекция,
// неверный довод, служба без графа — ею не считается: свои файлы ответили бы
// на тот же вопрос по-своему, и беда на службе спряталась бы за ответом.
var ErrUnreachable = errors.New("служба библиотеки не отвечает")

// FallbackNote — что говорится человеку и журналу шагов, когда поиск ушёл
// на свои файлы.
const FallbackNote = "служба недоступна — искал по своим файлам"

// ErrBusy — служба ответила, но места в её очереди тяжёлых запросов за свой
// предел ожидания не нашлось (ответ 503 с признаком «занята»).
//
// **Это не ErrUnreachable** (решение владельца 23.10.2026: «не считать очередь
// недоступностью, но сообщать в выводе об очереди»). Служба жива и отвечает,
// просто занята: свои файлы открыли бы гигабайт графа на каждый вопрос, пока
// служба разбирает очередь, и спрятали бы занятость за ответом. Клиент говорит
// о занятости строкой в выводе и на свои файлы не переходит.
var ErrBusy = errors.New("служба библиотеки занята")

// QueueNote — строка о том, сколько вызов ждал места в очереди службы; пусто —
// не ждал. Одна на все входы: подмес, инструменты модели, команды человека.
func QueueNote(wait time.Duration) string {
	if ms := wait.Milliseconds(); ms > 0 {
		return fmt.Sprintf("служба занята: ждал в очереди %d мс", ms)
	}
	return ""
}

// queuedSource — источник, который знает, сколько его вызовы ждали места
// в очереди службы (kbremote).
type queuedSource interface {
	Queued() time.Duration
}

// Queued — сколько вызовы источника ждали места в очереди службы; 0 — не ждали
// или источник не сетевой. Источник через Fallback заводится на одно действие,
// поэтому это время одного подмеса или одного вызова инструмента.
func Queued(s Source) time.Duration {
	if q, ok := s.(queuedSource); ok {
		return q.Queued()
	}
	return 0
}

// fallbackTimeout — срок поиска у себя, когда срок вызывающего съела служба:
// ожидание ответа и есть одна из причин перехода, и с истёкшим контекстом
// поиск по своим файлам отказал бы, не начавшись. Тридцать секунд — то же,
// что kbremote ждёт службу по умолчанию.
const fallbackTimeout = 30 * time.Second

// Fallback — общая библиотека с запасным путём на свои файлы.
//
// Каждое обращение сперва идёт в службу; не ответила (ErrUnreachable) —
// то же обращение к своей коллекции. Своих файлов нет (Local == nil) —
// остаётся ошибка службы, она называет адрес.
//
// Источник, отданный Source, заводится на одно действие — вызов инструмента,
// один подмес: по нему Fell отвечает, искал ли он у себя, и отметка не
// перетекает в чужое действие.
type Fallback struct {
	// Remote — служба. Обязательна.
	Remote Library

	// Local — свои файлы, обычно `*Base`; nil — их нет. Пустой указатель
	// сюда класть нельзя: интерфейс с ним не равен nil.
	Local Library

	// Default — своя коллекция, когда имя не названо (kb.default). Служба
	// выбирает умолчание у себя, свои файлы — по настройке, а без неё
	// единственную коллекцию.
	Default string

	// Mode — положение /kb source (этап 121, Д3); нуль — SourceAuto, как было
	// до переключателя. SourceService — служба без перехода на свои файлы,
	// SourceLocal — сразу свои файлы, служба не спрашивается вовсе.
	Mode SourceMode
}

// Names — коллекции службы, а если она не отвечает — свои.
func (f Fallback) Names() ([]string, error) {
	if f.Mode == SourceLocal {
		if f.Local == nil {
			return nil, errNoLocal
		}
		return f.Local.Names()
	}
	names, err := f.Remote.Names()
	if err == nil || !errors.Is(err, ErrUnreachable) || f.Local == nil || f.Mode == SourceService {
		return names, f.serviceOnly(err)
	}
	local, lerr := f.Local.Names()
	if lerr != nil {
		return nil, fmt.Errorf("%w; свои файлы: %v", err, lerr)
	}
	return local, nil
}

// Source открывает коллекцию у службы, а если она не отвечает — у себя.
// В положении local — сразу у себя: источник отдаётся как есть, и Fell о нём
// молчит — это выбор человека, а не переход с недоступной службы.
func (f Fallback) Source(name string) (Source, error) {
	if f.Mode == SourceLocal {
		if f.Local == nil {
			return nil, errNoLocal
		}
		n, err := f.localName(name)
		if err != nil {
			return nil, err
		}
		return f.Local.Source(n)
	}
	rs, err := f.Remote.Source(name)
	if err == nil {
		return &fallbackSource{f: f, name: name, cur: rs}, nil
	}
	ls, lerr := f.local(name, err)
	if lerr != nil {
		return nil, lerr
	}
	return &fallbackSource{f: f, name: name, cur: ls, fell: true}, nil
}

// local открывает свою коллекцию вместо службы, ответившей ошибкой cause.
// Отказ по смыслу и отсутствие своих файлов возвращают cause как есть;
// положение service — тоже, с пометкой, почему своих файлов не брали.
func (f Fallback) local(name string, cause error) (Source, error) {
	if !errors.Is(cause, ErrUnreachable) || f.Local == nil || f.Mode == SourceService {
		return nil, f.serviceOnly(cause)
	}
	n, err := f.localName(name)
	if err == nil {
		var src Source
		if src, err = f.Local.Source(n); err == nil {
			return src, nil
		}
	}
	// %w — у cause: выше по стеку переход должен быть виден как «служба
	// не отвечает», а не как отказ своих файлов.
	return nil, fmt.Errorf("%w; свои файлы: %v", cause, err)
}

// localName — какую свою коллекцию открыть: названную, по настройке или
// единственную.
func (f Fallback) localName(name string) (string, error) {
	if n := strings.TrimSpace(name); n != "" {
		return n, nil
	}
	if f.Default != "" {
		return f.Default, nil
	}
	names, err := f.Local.Names()
	if err != nil {
		return "", err
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", fmt.Errorf("коллекция не выбрана (kb.default), своих коллекций %d: %s",
		len(names), strings.Join(names, ", "))
}

// serviceOnly дописывает к недоступности службы, что в положении service
// перехода на свои файлы нет: иначе человек искал бы, почему не сработал
// запасной путь. Прочие ошибки и прочие положения — как есть.
func (f Fallback) serviceOnly(err error) error {
	if f.Mode != SourceService || !errors.Is(err, ErrUnreachable) {
		return err
	}
	return fmt.Errorf("%w (%s)", err, ServiceOnlyNote)
}

// Fell сообщает, что источник, открытый через Fallback, искал по своим файлам.
func Fell(s Source) bool {
	fs, ok := s.(*fallbackSource)
	if !ok {
		return false
	}
	_, fell := fs.now()
	return fell
}

// fallbackSource — коллекция, открытая через Fallback: служба, пока она
// отвечает, и свои файлы с той минуты, как перестала.
type fallbackSource struct {
	f    Fallback
	name string // как просили; пусто — умолчание

	mu   sync.Mutex
	cur  Source
	fell bool
}

func (s *fallbackSource) now() (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.fell
}

// down — служба не ответила посреди работы: дальше этот источник ищет у себя.
func (s *fallbackSource) down(cause error) (Source, error) {
	ls, err := s.f.local(s.name, cause)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cur, s.fell = ls, true
	s.mu.Unlock()
	return ls, nil
}

func (s *fallbackSource) Name() string {
	cur, _ := s.now()
	return cur.Name()
}

func (s *fallbackSource) Books() []BookRec {
	cur, _ := s.now()
	return cur.Books()
}

func (s *fallbackSource) Stats() Stats {
	cur, _ := s.now()
	return cur.Stats()
}

func (s *fallbackSource) SearchNote() string {
	cur, _ := s.now()
	return cur.SearchNote()
}

// chunkByRef — источник, умеющий отдать кусок по ссылке (как graph.Chunks).
type chunkByRef interface {
	ChunkByRef(doc, ord uint32) (ChunkInfo, bool)
}

// ChunkByRef — кусок текущего источника, если тот это умеет: своя коллекция
// на запасном пути умеет, служба — нет. Без этого обёртка прятала кусок своей
// коллекции, и из карты на запасном пути пропадали годы связей
// (mix.relation_years; рецензия этапа 121). Не умеет — «нет такого куска»,
// для отрисовки карты это то же, что источника нет: года у связи нет.
func (s *fallbackSource) ChunkByRef(doc, ord uint32) (ChunkInfo, bool) {
	cur, _ := s.now()
	if c, ok := cur.(chunkByRef); ok {
		return c.ChunkByRef(doc, ord)
	}
	return ChunkInfo{}, false
}

// Queued — ожидание в очереди службы у текущего источника; свои файлы
// очереди не знают.
func (s *fallbackSource) Queued() time.Duration {
	cur, _ := s.now()
	return Queued(cur)
}

func (s *fallbackSource) SearchWith(ctx context.Context, query string, opt SearchOpts, emb Embedder) ([]Result, error) {
	cur, fell := s.now()
	res, err := cur.SearchWith(ctx, query, opt, emb)
	if err == nil || fell {
		return res, err
	}
	ls, lerr := s.down(err)
	if lerr != nil {
		return nil, lerr
	}
	lctx, cancel := afterDeadline(ctx)
	defer cancel()
	return ls.SearchWith(lctx, query, opt, emb)
}

func (s *fallbackSource) Around(id string, around int) ([]Result, error) {
	cur, fell := s.now()
	res, err := cur.Around(id, around)
	if err == nil || fell {
		return res, err
	}
	ls, lerr := s.down(err)
	if lerr != nil {
		return nil, lerr
	}
	return ls.Around(id, around)
}

// afterDeadline — контекст поиска у себя: прежний, если он жив, иначе свой
// срок (fallbackTimeout). Отмена вызывающим перехода не вызывает вовсе —
// сетевой драйвер её службе не приписывает.
func afterDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), fallbackTimeout)
}

// ── Положение /kb source: кто исполняет поиск (этап 121, Д3) ────────────────

// SourceMode — откуда библиотека берёт книги и граф при заданном
// kb.server_url. Имена инструментов модели во всех положениях одни и те же,
// меняется только исполнитель. Нулевое значение — SourceAuto: так было до
// переключателя.
type SourceMode int32

const (
	// SourceAuto — сперва служба; не ответила (ErrUnreachable) — свои файлы
	// с пометкой FallbackNote (А4).
	SourceAuto SourceMode = iota
	// SourceService — только служба: не ответила — честная ошибка, свои файлы
	// и свой граф не открываются.
	SourceService
	// SourceLocal — только свои файлы и свой граф (через кэш графов), даже
	// когда служба задана и отвечает.
	SourceLocal
)

// sourceModeNames — написания положений: в команде и в строке состояния.
var sourceModeNames = [...]string{SourceAuto: "auto", SourceService: "service", SourceLocal: "local"}

func (s SourceMode) String() string {
	if s >= 0 && int(s) < len(sourceModeNames) {
		return sourceModeNames[s]
	}
	return fmt.Sprintf("SourceMode(%d)", int32(s))
}

// ParseSourceMode разбирает положение по написанию: auto, service, local.
func ParseSourceMode(s string) (SourceMode, error) {
	want := strings.ToLower(strings.TrimSpace(s))
	for m, name := range sourceModeNames {
		if want == name {
			return SourceMode(m), nil
		}
	}
	return SourceAuto, fmt.Errorf("неизвестное положение %q: auto, service или local", s)
}

// ServiceOnlyNote — почему при недоступной службе поиск не ушёл на свои файлы.
const ServiceOnlyNote = "положение service (/kb source): свои файлы не берутся"

// errNoLocal — положение local без своих файлов.
var errNoLocal = errors.New("своих файлов библиотеки нет (каталог kb.dir не открылся): положение local искать негде")

// SourceSwitch — живое положение, одно на программу.
//
// **Почему один экземпляр, а не поле в каждой библиотеке.** Его читают
// инструменты модели в горутине агента, подмешивание в горутине команды
// и команды интерфейса, а меняет /kb source в цикле событий. По образцу
// tools.Live: будь у каждого своя копия, человек переключил бы интерфейс,
// а модель искала бы по-старому — расхождение, которого не заметишь.
type SourceSwitch struct{ mode atomic.Int32 }

// NewSourceSwitch заводит переключатель в положении m.
func NewSourceSwitch(m SourceMode) *SourceSwitch {
	s := &SourceSwitch{}
	s.mode.Store(int32(m))
	return s
}

// Mode — нынешнее положение; nil — SourceAuto, как без переключателя.
func (s *SourceSwitch) Mode() SourceMode {
	if s == nil {
		return SourceAuto
	}
	return SourceMode(s.mode.Load())
}

// Set ставит положение.
func (s *SourceSwitch) Set(m SourceMode) { s.mode.Store(int32(m)) }
