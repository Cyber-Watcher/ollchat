package tools

import (
	"sync"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Настройки выдачи, которые меняются на ходу.
//
// **Зачем.** Числа отбора — вес уместности связи, ширина пула, сколько
// фрагментов брать, порог косинуса — подбираются только замером: угадать их
// нельзя, а из книг они не берутся. До сих пор их правили в конфиге
// и перезапускали программу, и на один вопрос посмотреть «а если так?»
// стоило минуты. Теперь их правят командой в диалоге.
//
// **Почему отдельный тип с замком, а не поля в Options.** Значения читает
// инструмент в горутине агента, а пишет обработчик команды в горутине
// интерфейса. Без замка это гонка — та самая, которую детектор поймал бы
// на первом же прогоне.
//
// **В конфиг не пишется.** Как и окно контекста: подобрал, посмотрел, вернул.
// Понравилось — человек сам перенесёт значение в файл настроек.

// Live — изменяемые на ходу настройки выдачи.
type Live struct {
	mu sync.RWMutex

	rank      graph.NeighborRank
	topK      int
	maxPerDoc int
	minCos    float64
	semWeight float64
}

// NewLive заводит набор с начальными значениями из конфига.
func NewLive(rank graph.NeighborRank, topK, maxPerDoc int, minCos, semWeight float64) *Live {
	return &Live{rank: rank, topK: topK, maxPerDoc: maxPerDoc, minCos: minCos, semWeight: semWeight}
}

// Rank — ранжирование связей графа.
func (l *Live) Rank() graph.NeighborRank {
	if l == nil {
		return graph.NeighborRank{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.rank
}

// SetRank меняет ранжирование связей.
func (l *Live) SetRank(r graph.NeighborRank) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rank = r
}

// KB — числа поиска по книгам: сколько фрагментов, сколько из одной книги,
// порог косинуса, вес смысла против слов.
func (l *Live) KB() (topK, maxPerDoc int, minCos, semWeight float64) {
	if l == nil {
		return 0, 0, 0, 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.topK, l.maxPerDoc, l.minCos, l.semWeight
}

// SetKB меняет числа поиска по книгам.
func (l *Live) SetKB(topK, maxPerDoc int, minCos, semWeight float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.topK, l.maxPerDoc, l.minCos, l.semWeight = topK, maxPerDoc, minCos, semWeight
}
