package tools

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Гонка: инструмент читает числа в горутине агента, команда пишет в горутине
// интерфейса. Проверяется детектором, иначе она вылезет у пользователя.
func TestLiveIsRaceSafe(t *testing.T) {
	l := NewLive(graph.NeighborRank{SenseWeight: 0, Pool: 3}, 8, 3, 0.2, 1.0)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			l.SetRank(graph.NeighborRank{SenseWeight: float64(i % 3), Pool: 3})
			l.SetKB(8+i%4, 3, 0.2, 1.0)
		}
		close(done)
	}()
	for i := 0; i < 500; i++ {
		_ = l.Rank()
		_, _, _, _ = l.KB()
	}
	<-done
}

// Нулевой указатель не роняет: инструменты могут работать и без живых настроек.
func TestLiveNilIsSafe(t *testing.T) {
	var l *Live
	if r := l.Rank(); r.SenseWeight != 0 {
		t.Errorf("у nil ожидались нули, получено %+v", r)
	}
	if k, _, _, _ := l.KB(); k != 0 {
		t.Errorf("у nil ожидались нули, получено %d", k)
	}
}
