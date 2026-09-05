package kb

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeEmbedder — эмбеддер без сети: вектор собирается из слов текста.
//
// Слова из одного «смыслового гнезда» дают близкие векторы, даже если написаны
// на разных языках, — этим он и повторяет главное свойство настоящей модели,
// ради которого весь этап и затевался.
type fakeEmbedder struct {
	dim   int
	model string

	mu    sync.Mutex
	calls int
	texts int
	fail  error
}

// synonyms — пары «русское слово ↔ английское», которые обязаны попасть
// в одну точку пространства.
var synonyms = map[string]string{
	"горутина":           "goroutine",
	"горутин":            "goroutine",
	"канал":              "channel",
	"буферизированный":   "buffered",
	"буферизованный":     "buffered",
	"планировщик":        "scheduler",
	"мьютекс":            "mutex",
	"развёртывание":      "deployment",
	"контейнер":          "container",
	"внедрение":          "injection",
	"уязвимость":         "vulnerability",
	"замыкание":          "closure",
	"сборщик":            "collector",
	"мусор":              "garbage",
	"параллельный":       "concurrent",
	"параллельность":     "concurrency",
	"синхронизация":      "synchronization",
	"блокировка":         "lock",
	"очередь":            "queue",
	"производительность": "performance",
	"память":             "memory",
	"указатель":          "pointer",
	"интерфейс":          "interface",
	"структура":          "struct",
	"ошибка":             "error",
	"тестирование":       "testing",
	"развертывание":      "deployment",
	"кластер":            "cluster",
	"сеть":               "network",
	"безопасность":       "security",
	"шифрование":         "encryption",
	"аутентификация":     "authentication",
	"журналирование":     "logging",
	"мониторинг":         "monitoring",
	"масштабирование":    "scaling",
	"отказоустойчивость": "resilience",
	"конфигурация":       "configuration",
	"зависимость":        "dependency",
	"библиотека":         "library",
	"компилятор":         "compiler",
	"оптимизация":        "optimization",
	"алгоритм":           "algorithm",
	"сортировка":         "sorting",
	"хеширование":        "hashing",
	"кэширование":        "caching",
	"транзакция":         "transaction",
	"индексирование":     "indexing",
	"репликация":         "replication",
	"резервное":          "backup",
	"восстановление":     "recovery",
	"виртуализация":      "virtualization",
	"оркестрация":        "orchestration",
	"непрерывная":        "continuous",
	"интеграция":         "integration",
	"доставка":           "delivery",
	"развитие":           "development",
	"сопровождение":      "maintenance",
	"документирование":   "documentation",
	"рефакторинг":        "refactoring",
	"отладка":            "debugging",
	"профилирование":     "profiling",
	"трассировка":        "tracing",
	"метрика":            "metric",
	"оповещение":         "alerting",
	"надёжность":         "reliability",
	"доступность":        "availability",
	"согласованность":    "consistency",
	"разделение":         "partition",
	"балансировка":       "balancing",
	"маршрутизация":      "routing",
	"прокси":             "proxy",
	"шлюз":               "gateway",
	"микросервис":        "microservice",
	"монолит":            "monolith",
	"архитектура":        "architecture",
	"шаблон":             "pattern",
	"наследование":       "inheritance",
	"полиморфизм":        "polymorphism",
	"инкапсуляция":       "encapsulation",
	"абстракция":         "abstraction",
	"композиция":         "composition",
	"обобщение":          "generics",
	"замер":              "benchmark",
	"нагрузка":           "load",
	"пропускная":         "throughput",
	"задержка":           "latency",
	"очередь_сообщений":  "messaging",
	"подписка":           "subscription",
	"публикация":         "publish",
	"поток":              "stream",
	"пакет":              "package",
	"модуль":             "module",
	"версия":             "version",
	"сборка":             "build",
	"развёртка":          "rollout",
	"откат":              "rollback",
	"среда":              "environment",
	"переменная":         "variable",
	"константа":          "constant",
	"функция":            "function",
	"метод":              "method",
	"класс":              "class",
	"объект":             "object",
	"экземпляр":          "instance",
	"наблюдаемость":      "observability",
	"устойчивость":       "stability",
	"изоляция":           "isolation",
	"песочница":          "sandbox",
	"права":              "permissions",
	"политика":           "policy",
	"аудит":              "audit",
	"соответствие":       "compliance",
	"шина":               "bus",
	"событие":            "event",
	"обработчик":         "handler",
	"промежуточный":      "middleware",
	"запрос":             "request",
	"ответ":              "response",
	"заголовок":          "header",
	"тело":               "body",
	"состояние":          "state",
	"сессия":             "session",
	"токен":              "token",
	"ключ":               "key",
	"значение":           "value",
	"хранилище":          "storage",
	"файловая":           "filesystem",
	"диск":               "disk",
	"процессор":          "cpu",
	"ядро":               "core",
	"нить":               "thread",
	"процесс":            "process",
	"планирование":       "scheduling",
	"приоритет":          "priority",
	"таймаут":            "timeout",
	"повтор":             "retry",
	"прерывание":         "cancellation",
	"контекст":           "context",
	"срез":               "slice",
	"карта":              "map",
	"массив":             "array",
	"строка":             "string",
	"число":              "number",
	"логический":         "boolean",
	"пустой":             "nil",
	"панике":             "panic",
	"паника":             "panic",
	"восстановить":       "recover",
	"отложенный":         "defer",
	"ожидание":           "wait",
	"группа":             "group",
	"счётчик":            "counter",
	"атомарный":          "atomic",
	"гонка":              "race",
	"взаимоблокировка":   "deadlock",
	"голодание":          "starvation",
	"справедливость":     "fairness",
	"пул":                "pool",
	"работник":           "worker",
	"конвейер":           "pipeline",
	"вентилятор":         "fanout",
	"выбор":              "select",
	"закрытие":           "close",
	"отправка":           "send",
	"получение":          "receive",
	"направление":        "direction",
	"ёмкость":            "capacity",
	"длина":              "length",
	"копирование":        "copy",
	"добавление":         "append",
	"удаление":           "delete",
	"поиск":              "search",
	"сравнение":          "compare",
	"равенство":          "equality",
	"порядок":            "order",
	"обход":              "traversal",
	"дерево":             "tree",
	"граф":               "graph",
	"список":             "list",
	"стек":               "stack",
	"куча":               "heap",
	"таблица":            "table",
	"столбец":            "column",
	"схема":              "schema",
	"миграция":           "migration",
	"соединение":         "connection",
	"драйвер":            "driver",
	"курсор":             "cursor",
	"выборка":            "query",
	"вставка":            "insert",
	"обновление":         "update",
	"удаление_строк":     "truncate",
	"блокировка_строк":   "locking",
	"уровень":            "level",
	"изолированность":    "isolation",
	"фиксация":           "commit",
	"отмена":             "abort",
	"журнал":             "log",
	"снимок":             "snapshot",
	"копия":              "replica",
	"ведущий":            "primary",
	"ведомый":            "secondary",
	"кворум":             "quorum",
	"консенсус":          "consensus",
	"выборы":             "election",
	"сердцебиение":       "heartbeat",
	"разбиение":          "sharding",
	"хеш":                "hash",
	"диапазон":           "range",
	"сжатие":             "compression",
	"кодирование":        "encoding",
	"сериализация":       "serialization",
	"формат":             "format",
	"протокол":           "protocol",
	"порт":               "port",
	"адрес":              "address",
	"сокет":              "socket",
	"пакетная":           "batch",
	"потоковая":          "streaming",
	"асинхронный":        "asynchronous",
	"синхронный":         "synchronous",
	"обратный":           "callback",
	"обещание":           "promise",
	"будущее":            "future",
	"актор":              "actor",
	"сообщение":          "message",
	"почтовый":           "mailbox",
	"надзор":             "supervision",
	"перезапуск":         "restart",
	"деградация":         "degradation",
	"предохранитель":     "circuit",
	"ограничение":        "throttling",
	"квота":              "quota",
	"бюджет":             "budget",
	"стоимость":          "cost",
	"эффективность":      "efficiency",
	"потребление":        "consumption",
	"утечка":             "leak",
	"фрагментация":       "fragmentation",
	"выравнивание":       "alignment",
	"кеш":                "cache",
	"промах":             "miss",
	"попадание":          "hit",
	"вытеснение":         "eviction",
	"устаревание":        "expiration",
	"недействительность": "invalidation",
	"согласование":       "reconciliation",
	"желаемое":           "desired",
	"фактическое":        "actual",
	"контроллер":         "controller",
	"оператор":           "operator",
	"ресурс":             "resource",
	"описание":           "manifest",
	"пространство":       "namespace",
	"метка":              "label",
	"аннотация":          "annotation",
	"селектор":           "selector",
	"служба":             "service",
	"вход":               "ingress",
	"выход":              "egress",
	"том":                "volume",
	"монтирование":       "mount",
	"секрет":             "secret",
	"настройка":          "configmap",
	"учётная":            "account",
	"роль":               "role",
	"привязка":           "binding",
	"допуск":             "toleration",
	"сродство":           "affinity",
	"размещение":         "placement",
	"узел":               "node",
	"под":                "pod",
	"реплика":            "replicaset",
	"набор":              "statefulset",
	"задание":            "job",
	"расписание":         "cronjob",
	"зонд":               "probe",
	"готовность":         "readiness",
	"живость":            "liveness",
	"запуск":             "startup",
}

func newFakeEmbedder(dim int) *fakeEmbedder {
	return &fakeEmbedder{dim: dim, model: "fake-embed"}
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls++
	f.texts += len(texts)
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		out = append(out, f.vector(t))
	}
	return out, nil
}

// vector складывает вектор из слов: каждое слово толкает одну координату.
// Синонимы приводятся к общему виду, поэтому «горутина» и goroutine дают
// один и тот же вклад.
func (f *fakeEmbedder) vector(text string) []float32 {
	v := make([]float32, f.dim)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('а' <= r && r <= 'я') && r != 'ё'
	}) {
		if len(w) < 3 {
			continue
		}
		if en, ok := synonyms[w]; ok {
			w = en
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		v[int(h.Sum32())%f.dim] += 1
	}
	return v
}

func (f *fakeEmbedder) setFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

func (f *fakeEmbedder) counts() (calls, texts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.texts
}

// embedFixture собирает коллекцию из книг на двух языках.
func embedFixture(t *testing.T) (*Base, *Collection, string) {
	t.Helper()
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	// Английская книга про каналы — та, которую обязан находить русский запрос.
	makeBook(t, books, "gopl.pdf",
		longPage("buffered channel capacity and blocking send"),
		longPage("goroutine scheduler and work stealing"))
	// Русская книга про другое: она не должна выигрывать по смыслу.
	makeBook(t, books, "k8s-ru.pdf",
		longPage("развёртывание контейнеров и служб"),
		longPage("узлы кластера и планирование подов"))

	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	return base, coll, books
}

// TestQuantizeKeepsCosine — квантование в int8 не должно портить близость.
func TestQuantizeKeepsCosine(t *testing.T) {
	f := newFakeEmbedder(256)
	a := f.vector("buffered channel capacity blocking send goroutine")
	b := f.vector("буферизированный канал ёмкость отправка горутина")
	c := f.vector("узлы кластера планирование подов развёртывание")

	exact := func(x, y []float32) float64 {
		var dot, nx, ny float64
		for i := range x {
			dot += float64(x[i]) * float64(y[i])
			nx += float64(x[i]) * float64(x[i])
			ny += float64(y[i]) * float64(y[i])
		}
		if nx == 0 || ny == 0 {
			return 0
		}
		return dot / (math.Sqrt(nx) * math.Sqrt(ny))
	}

	want, got := exact(a, b), Cosine(Quantize(a), Quantize(b))
	if math.Abs(want-got) > 0.01 {
		t.Fatalf("квантование увело близость: точно %.4f, после int8 %.4f", want, got)
	}
	if near := Cosine(Quantize(a), Quantize(c)); near >= got {
		t.Fatalf("чужой текст оказался не дальше своего: %.4f против %.4f", near, got)
	}
}

// TestEmbedCoverageIsPrefix закрепляет главный инвариант хранилища: покрытие
// векторами — всегда начальный отрезок. На нём держится и доливка книг,
// и уплотнение.
func TestEmbedCoverageIsPrefix(t *testing.T) {
	_, coll, books := embedFixture(t)
	emb := newFakeEmbedder(128)

	res, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Covered != res.Total || res.Total == 0 {
		t.Fatalf("покрыто %d из %d", res.Covered, res.Total)
	}
	if cov := coll.Coverage(emb.Model()); !cov.Full() || !cov.Usable {
		t.Fatalf("покрытие не полное: %+v", cov)
	}
	firstCalls, firstTexts := emb.counts()

	// Доливаем книгу: её куски встают в конец, покрытие перестаёт быть полным.
	makeBook(t, books, "new.pdf", longPage("мьютекс и блокировка памяти"))
	if _, err := coll.Sync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	cov := coll.Coverage(emb.Model())
	if cov.Full() {
		t.Fatal("после доливки покрытие осталось полным")
	}
	if cov.Covered != res.Total {
		t.Fatalf("покрытие сдвинулось: было %d, стало %d", res.Total, cov.Covered)
	}

	// Досчёт обязан обработать только хвост — это и есть обещание дешёвой доливки.
	res2, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Added == 0 || res2.Added >= res.Total {
		t.Fatalf("досчитано %d кусков при прежних %d — считали не только хвост", res2.Added, res.Total)
	}
	calls, texts := emb.counts()
	// +1 запрос на пробу размерности, остальное — только новые куски.
	if texts-firstTexts > res2.Added+8 {
		t.Fatalf("векторизовано %d текстов ради %d новых кусков", texts-firstTexts, res2.Added)
	}
	if calls <= firstCalls {
		t.Fatal("досчёт не обратился к модели вовсе")
	}
	if cov := coll.Coverage(emb.Model()); !cov.Full() {
		t.Fatalf("после досчёта покрытие не полное: %+v", cov)
	}
}

// TestSemanticFindsAcrossLanguages — то, ради чего этап и затевался: запрос
// по-русски находит английскую книгу. Поиск по словам этого не может
// в принципе.
func TestSemanticFindsAcrossLanguages(t *testing.T) {
	_, coll, _ := embedFixture(t)
	emb := newFakeEmbedder(256)
	if _, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 16}, nil); err != nil {
		t.Fatal(err)
	}

	const query = "буферизированный канал"
	opt := SearchOpts{TopK: 3, MaxPerDoc: 3, Semantic: true}

	// Сначала убеждаемся, что словесный поиск с этим не справляется.
	words, err := coll.SearchWith(context.Background(), query, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range words {
		if strings.Contains(r.Path, "gopl") {
			t.Skip("словесный поиск неожиданно справился — проверка потеряла смысл")
		}
	}

	got, err := coll.SearchWith(context.Background(), query, opt, emb)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("смысловой поиск не нашёл ничего")
	}
	found := false
	for _, r := range got {
		if strings.Contains(r.Path, "gopl") {
			found = true
		}
	}
	if !found {
		var titles []string
		for _, r := range got {
			titles = append(titles, filepath.Base(r.Path))
		}
		t.Fatalf("английская книга про buffered channel не найдена по русскому запросу; выдано: %v", titles)
	}
}

// TestSemanticSurvivesDeadServer — сервер эмбеддингов недоступен. Поиск обязан
// продолжиться по словам и сказать почему, а не упасть.
func TestSemanticSurvivesDeadServer(t *testing.T) {
	_, coll, _ := embedFixture(t)
	emb := newFakeEmbedder(128)
	if _, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 16}, nil); err != nil {
		t.Fatal(err)
	}
	opt := SearchOpts{TopK: 3, MaxPerDoc: 3, Semantic: true}
	want, err := coll.SearchWith(context.Background(), "buffered channel", opt, nil)
	if err != nil {
		t.Fatal(err)
	}

	emb.setFail(errors.New("connection refused"))
	got, err := coll.SearchWith(context.Background(), "buffered channel", opt, emb)
	if err != nil {
		t.Fatalf("недоступный сервер уронил поиск: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("выдача изменилась: %d против %d", len(got), len(want))
	}
	if note := coll.SearchNote(); !strings.Contains(note, "недоступен") {
		t.Fatalf("не сказано, почему смысл не участвовал: %q", note)
	}
}

// TestSemanticRefusesForeignVectors — векторы чужой модели несравнимы,
// и молчаливо пользоваться ими нельзя.
func TestSemanticRefusesForeignVectors(t *testing.T) {
	_, coll, _ := embedFixture(t)
	old := newFakeEmbedder(128)
	if _, err := coll.Embed(context.Background(), old, EmbedOpts{Batch: 16}, nil); err != nil {
		t.Fatal(err)
	}
	other := newFakeEmbedder(128)
	other.model = "другая-модель"

	opt := SearchOpts{TopK: 3, MaxPerDoc: 3, Semantic: true}
	if _, err := coll.SearchWith(context.Background(), "goroutine", opt, other); err != nil {
		t.Fatal(err)
	}
	note := coll.SearchNote()
	if !strings.Contains(note, "--recount") {
		t.Fatalf("не сказано, как чинить: %q", note)
	}
	if cov := coll.Coverage(other.Model()); cov.Usable {
		t.Fatal("векторы чужой модели признаны годными")
	}
}

// TestEmbedTruncatesTornTail — обрыв между дозаписью и сохранением счётчика.
func TestEmbedTruncatesTornTail(t *testing.T) {
	_, coll, _ := embedFixture(t)
	emb := newFakeEmbedder(64)
	if _, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 16}, nil); err != nil {
		t.Fatal(err)
	}
	dat := filepath.Join(coll.Dir(), "vectors.dat")
	info, err := os.Stat(dat)
	if err != nil {
		t.Fatal(err)
	}
	// Дописываем мусор, как будто работу прервали после записи данных.
	f, err := os.OpenFile(dat, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(make([]byte, 64*5))
	f.Close()

	v, err := OpenVectors(coll.Dir())
	if err != nil {
		t.Fatalf("хвост не пережит: %v", err)
	}
	if int64(v.Count())*int64(v.Dim()) != info.Size() {
		t.Fatalf("прочитано %d байт вместо %d", int64(v.Count())*int64(v.Dim()), info.Size())
	}
}

// TestMergeMovesVectors — уплотнение меняет сквозные номера кусков, а номер
// куска и есть адрес вектора. Без переноса смыслы указывали бы на чужой текст.
func TestMergeMovesVectors(t *testing.T) {
	_, coll, books := embedFixture(t)
	drop := makeBook(t, books, "drop.pdf", longPage("совершенно посторонняя тема про садоводство"))
	if _, err := coll.Sync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	emb := newFakeEmbedder(256)
	if _, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 16}, nil); err != nil {
		t.Fatal(err)
	}
	if err := coll.Forget(drop); err != nil {
		t.Fatal(err)
	}

	res, err := coll.Merge(context.Background(), MergeOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.VectorsAfter != res.ChunksAfter {
		t.Fatalf("векторов %d при %d кусках — покрытие разъехалось", res.VectorsAfter, res.ChunksAfter)
	}
	// И, главное, смысл по-прежнему указывает на нужный текст.
	opt := SearchOpts{TopK: 3, MaxPerDoc: 3, Semantic: true}
	got, err := coll.SearchWith(context.Background(), "буферизированный канал", opt, emb)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range got {
		if strings.Contains(r.Path, "gopl") {
			found = true
		}
	}
	if !found {
		t.Fatal("после уплотнения смысловой поиск перестал находить нужную книгу")
	}
}

// TestFuseByRanks — слияние обязано складывать места, а не веса: устойчиво
// хороший кусок должен обходить того, кто первый у одного и далёкий у другого.
func TestFuseByRanks(t *testing.T) {
	words := []Hit{{Chunk: 1, Score: 100}, {Chunk: 2, Score: 9}, {Chunk: 3, Score: 8}}
	vecs := []Hit{{Chunk: 2, Score: 0.9}, {Chunk: 3, Score: 0.8}}
	for i := 0; i < 30; i++ {
		vecs = append(vecs, Hit{Chunk: 100 + i, Score: 0.5})
	}
	vecs = append(vecs, Hit{Chunk: 1, Score: 0.1})

	got := fuse(DefaultRRFK, nil, words, vecs)
	if got[0].Chunk != 2 {
		t.Fatalf("первым стал кусок %d, ожидался 2", got[0].Chunk)
	}
	// Кусок 1 первый по словам, но тридцать третий по смыслу — он не должен
	// побеждать устойчивого середняка.
	pos := map[int]int{}
	for i, h := range got {
		pos[h.Chunk] = i
	}
	if pos[1] < pos[3] {
		t.Fatalf("кусок с одним сильным списком обошёл устойчивый: места %d и %d", pos[1], pos[3])
	}
}

// TestSemanticOffByOption — выключенный смысл не должен ничего считать.
func TestSemanticOffByOption(t *testing.T) {
	_, coll, _ := embedFixture(t)
	emb := newFakeEmbedder(128)
	if _, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 16}, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := emb.counts()
	if _, err := coll.SearchWith(context.Background(), "goroutine",
		SearchOpts{TopK: 3, MaxPerDoc: 3, Semantic: false}, emb); err != nil {
		t.Fatal(err)
	}
	if after, _ := emb.counts(); after != before {
		t.Fatalf("при выключенном смысле сделано %d обращений к модели", after-before)
	}
}

// TestEmbedShrinksBatch — сервер не тянет большую пачку. Так было живьём:
// пока видеопамять свободна, проходят пачки по 64 куска, но стоит кому-то
// загрузить рядом модель на 82 ГБ — обработчик падает уже на 16, а на 8
// работает. Предел зависит не от нас, поэтому его нащупывают на ходу,
// а не записывают в настройки.
func TestEmbedShrinksBatch(t *testing.T) {
	_, coll, _ := embedFixture(t)
	// Предел в два куска: коллекция в тесте маленькая, и без жёсткого предела
	// первая же волна прошла бы целиком.
	const limit = 2
	emb := &pickyEmbedder{fakeEmbedder: *newFakeEmbedder(64), limit: limit}

	res, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 64, Workers: 2}, nil)
	if err != nil {
		t.Fatalf("работа не пережила капризный сервер: %v", err)
	}
	if !res.Shrunk {
		t.Fatal("пачка не уменьшалась")
	}
	if res.Batch > limit {
		t.Fatalf("итоговая пачка %d, а сервер держит только %d", res.Batch, limit)
	}
	if res.Covered != res.Total {
		t.Fatalf("покрыто %d из %d", res.Covered, res.Total)
	}
	// И смысл после этого работает так же, как если бы сбоев не было.
	got, err := coll.SearchWith(context.Background(), "буферизированный канал",
		SearchOpts{TopK: 3, MaxPerDoc: 2, Semantic: true}, emb)
	if err != nil || len(got) == 0 {
		t.Fatalf("после подстройки поиск не работает: %v, найдено %d", err, len(got))
	}
}

// TestEmbedGivesUpOnRealFailure — уменьшать бесконечно нельзя: если не проходит
// и пачка в один кусок, сбой настоящий и о нём надо сказать.
func TestEmbedGivesUpOnRealFailure(t *testing.T) {
	_, coll, _ := embedFixture(t)
	emb := &pickyEmbedder{fakeEmbedder: *newFakeEmbedder(64), limit: 0}

	_, err := coll.Embed(context.Background(), emb, EmbedOpts{Batch: 64, Workers: 2}, nil)
	if err == nil {
		t.Fatal("настоящий сбой проглочен")
	}
	if !strings.Contains(err.Error(), "не тянет") {
		t.Fatalf("причина потерялась: %v", err)
	}
}

// pickyEmbedder падает на пачках больше limit — как настоящий обработчик,
// которому не хватает видеопамяти.
type pickyEmbedder struct {
	fakeEmbedder
	limit int
}

func (p *pickyEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) > p.limit {
		return nil, errors.New("сервер не тянет пачку такого размера")
	}
	return p.fakeEmbedder.Embed(ctx, texts)
}

// TestAnswerStyleOverride — политика ответа задаётся пользователем, а не только
// зашита в код. Живой случай: встроенная формулировка «ОБЯЗАТЕЛЬНО ссылайся…
// не додумывай» превратила ответ в подборку цитат, и чтобы поправить одну фразу,
// пришлось пересобирать бинарь. Теперь это строка в конфиге.
func TestAnswerStyleOverride(t *testing.T) {
	if got := AnswerStyle(""); got != DefaultAnswerStyle {
		t.Fatal("пустая настройка не дала встроенную политику")
	}
	if got := AnswerStyle("   \n\t "); got != DefaultAnswerStyle {
		t.Fatal("пробелы не считаются пустой настройкой")
	}
	const mine = "Отвечай кратко и по-английски."
	if got := AnswerStyle(mine); got != mine {
		t.Fatalf("своя политика не применилась: %q", got)
	}
	// Встроенная обязана содержать оба требования сразу: одно без другого
	// и приводит к беде — либо выдумки, либо цитатник.
	// «Название» — отдельным требованием: без него модель ссылается фамилиями
	// автора, а по ним книгу не найти (замечание владельца 24.08.2026).
	// «перевод» — отдельным требованием: библиотека английская, разговор русский
	// (замечание владельца 25.08.2026).
	for _, want := range []string{"своими словами", "помечай", "ответь сам", "Название", "перевод"} {
		if !strings.Contains(DefaultAnswerStyle, want) {
			t.Fatalf("во встроенной политике нет %q", want)
		}
	}
}
