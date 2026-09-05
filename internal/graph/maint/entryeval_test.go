package maint

import "testing"

// Счёт мест — то, на чём стоит весь замер: ошибка здесь тихо превратит
// улучшение в ухудшение и наоборот.
func TestScoreEntryRanksAndForeign(t *testing.T) {
	// Оба понятия нашлись, между ними одно постороннее.
	s := scoreEntry([2]uint32{10, 20}, []uint32{10, 99, 20, 98})
	if s.RankA != 1 || s.RankB != 3 {
		t.Fatalf("места посчитаны неверно: %+v", s)
	}
	if s.Foreign != 2 || s.Total != 4 {
		t.Fatalf("постороннее посчитано неверно: %+v", s)
	}
}

func TestScoreEntryNotFound(t *testing.T) {
	s := scoreEntry([2]uint32{10, 20}, []uint32{7, 8, 9})
	if s.RankA != 0 || s.RankB != 0 {
		t.Fatalf("ненайденное получило место: %+v", s)
	}
	if s.Foreign != 3 {
		t.Fatalf("всё выданное должно считаться посторонним: %+v", s)
	}
}

// Понятие, которого нет в графе, обозначено нулём и не должно превращать
// всю выдачу в «постороннее».
func TestScoreEntryMissingExpected(t *testing.T) {
	s := scoreEntry([2]uint32{10, 0}, []uint32{10, 55})
	if s.RankA != 1 || s.RankB != 0 {
		t.Fatalf("места: %+v", s)
	}
	if s.Foreign != 1 {
		t.Fatalf("постороннее: %+v", s)
	}
}

// Повтор понятия в выдаче не должен считаться дважды: место — первое,
// а сам повтор — не «постороннее»: это то же самое понятие, а не чужое.
// (Вход в граф повторов не выдаёт, но замер обязан быть честным и на них.)
func TestScoreEntryDuplicateKeepsFirstPlace(t *testing.T) {
	s := scoreEntry([2]uint32{10, 20}, []uint32{10, 10, 20})
	if s.RankA != 1 || s.RankB != 3 {
		t.Fatalf("места: %+v", s)
	}
	if s.Foreign != 0 {
		t.Fatalf("повтор ожидаемого посчитан посторонним: %+v", s)
	}
}

func TestTrimTo(t *testing.T) {
	if got := trimTo("короткая", 20); got != "короткая" {
		t.Fatalf("короткую строку резать не надо: %q", got)
	}
	if got := []rune(trimTo("оченьдлиннаястрокабезпробелов", 10)); len(got) != 10 {
		t.Fatalf("длина после обрезки %d, ожидалось 10", len(got))
	}
}
