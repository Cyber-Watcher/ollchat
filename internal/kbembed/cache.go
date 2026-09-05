package kbembed

import "sync"

// Кэш векторов одиночных запросов.
//
// **Зачем.** Один и тот же вопрос в пределах обмена векторизуется несколько раз:
// вход в граф считает вектор, поиск по книгам считает его же, подмешивание —
// в третий раз. Строка одна, модель одна, ответ один и тот же, а карта каждый раз
// беспокоится заново.
//
// **Дело не в деньгах, а в устойчивости.** Замер 30.08.2026: пока модель извлечения
// держала карту, запрос вектора к `bge-m3` **висел бесконечно** — 90 с с рабочей
// машины, 120 с с самого сервера. Повторный вопрос при занятой карте должен
// отвечать из памяти, а не ждать вместе со всеми.
//
// Кэшируются только **одиночные** запросы: пачки — это векторизация библиотеки,
// там повторов нет, а память они съедят.
//
// Ключ включает имя модели: у векторов, посчитанных разными моделями, разный
// смысл, и подменять один другим нельзя. Ровно поэтому совместимость векторов
// в kb тоже сверяется по имени модели.
const queryCacheMax = 1024

var queryCache = struct {
	mu    sync.Mutex
	byKey map[string][]float32
	order []string // порядок укладки; вытесняем самый старый
}{byKey: make(map[string][]float32, queryCacheMax)}

func cacheKey(model, text string) string { return model + "\x00" + text }

// cacheGet отдаёт готовый вектор. Второе значение — был ли он в кэше.
func cacheGet(model, text string) ([]float32, bool) {
	queryCache.mu.Lock()
	defer queryCache.mu.Unlock()
	v, ok := queryCache.byKey[cacheKey(model, text)]
	return v, ok
}

// cachePut запоминает вектор, вытесняя самый старый при переполнении.
func cachePut(model, text string, vec []float32) {
	if len(vec) == 0 {
		return // пустое не кэшируем: это не ответ, а неудача
	}
	key := cacheKey(model, text)
	queryCache.mu.Lock()
	defer queryCache.mu.Unlock()
	if _, ok := queryCache.byKey[key]; ok {
		return
	}
	if len(queryCache.order) >= queryCacheMax {
		oldest := queryCache.order[0]
		queryCache.order = queryCache.order[1:]
		delete(queryCache.byKey, oldest)
	}
	queryCache.byKey[key] = vec
	queryCache.order = append(queryCache.order, key)
}

// cachePutAndPersist кладёт вектор в память и на диск.
func cachePutAndPersist(model, text string, vec []float32) {
	if _, ok := cacheGet(model, text); ok {
		return
	}
	cachePut(model, text, vec)
	diskPut(model, text, vec)
}

// CacheSize — сколько векторов сейчас в кэше. Для проверок и отчётов.
func CacheSize() int {
	queryCache.mu.Lock()
	defer queryCache.mu.Unlock()
	return len(queryCache.byKey)
}
