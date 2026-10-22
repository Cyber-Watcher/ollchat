package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
}

// Names — коллекции службы, а если она не отвечает — свои.
func (f Fallback) Names() ([]string, error) {
	names, err := f.Remote.Names()
	if err == nil || !errors.Is(err, ErrUnreachable) || f.Local == nil {
		return names, err
	}
	local, lerr := f.Local.Names()
	if lerr != nil {
		return nil, fmt.Errorf("%w; свои файлы: %v", err, lerr)
	}
	return local, nil
}

// Source открывает коллекцию у службы, а если она не отвечает — у себя.
func (f Fallback) Source(name string) (Source, error) {
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
// Отказ по смыслу и отсутствие своих файлов возвращают cause как есть.
func (f Fallback) local(name string, cause error) (Source, error) {
	if !errors.Is(cause, ErrUnreachable) || f.Local == nil {
		return nil, cause
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
	return "", fmt.Errorf("коллекция не выбрана (kb.default), своих коллекций %d", len(names))
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
