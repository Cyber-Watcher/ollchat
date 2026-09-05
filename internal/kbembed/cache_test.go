package kbembed

import "testing"

func reset() {
	queryCache.mu.Lock()
	defer queryCache.mu.Unlock()
	queryCache.byKey = map[string][]float32{}
	queryCache.order = nil
}

// Вектор возвращается тот же и по той же строке.
func TestCacheKeepsVector(t *testing.T) {
	reset()
	cachePut("bge-m3", "горутина", []float32{0.1, 0.2})
	v, ok := cacheGet("bge-m3", "горутина")
	if !ok || len(v) != 2 || v[0] != 0.1 {
		t.Fatalf("вектор не вернулся: %v %v", v, ok)
	}
}

// Разные модели — разные векторы. Подменять один другим нельзя: смысл у них
// свой, и в kb совместимость сверяется по имени модели ровно поэтому.
func TestCacheSeparatesModels(t *testing.T) {
	reset()
	cachePut("bge-m3", "канал", []float32{1})
	if _, ok := cacheGet("другая-модель", "канал"); ok {
		t.Error("вектор одной модели отдан под именем другой")
	}
}

// Пустое не кэшируется: это неудача, а не ответ, и запоминать её значит
// закрепить сбой до конца работы программы.
func TestCacheIgnoresEmpty(t *testing.T) {
	reset()
	cachePut("bge-m3", "пусто", nil)
	if _, ok := cacheGet("bge-m3", "пусто"); ok {
		t.Error("пустой вектор попал в кэш")
	}
	if CacheSize() != 0 {
		t.Errorf("размер кэша %d, ожидался ноль", CacheSize())
	}
}

// Переполнение вытесняет самый старый, а не растёт без предела: иначе долгий
// сеанс с тысячами вопросов съест память.
func TestCacheEvictsOldest(t *testing.T) {
	reset()
	for i := 0; i < queryCacheMax+10; i++ {
		cachePut("bge-m3", string(rune('a'+i%26))+string(rune('0'+i/26)), []float32{float32(i)})
	}
	if CacheSize() > queryCacheMax {
		t.Errorf("в кэше %d записей при пределе %d", CacheSize(), queryCacheMax)
	}
}

// Повторная укладка той же строки не двоит записи в очереди вытеснения.
func TestCacheDoesNotDuplicate(t *testing.T) {
	reset()
	for i := 0; i < 5; i++ {
		cachePut("bge-m3", "одна и та же", []float32{1})
	}
	if CacheSize() != 1 {
		t.Errorf("в кэше %d записей, ожидалась одна", CacheSize())
	}
	queryCache.mu.Lock()
	n := len(queryCache.order)
	queryCache.mu.Unlock()
	if n != 1 {
		t.Errorf("в очереди вытеснения %d записей, ожидалась одна", n)
	}
}
