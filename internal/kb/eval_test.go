package kb

import "testing"

// Таблица воздержания: молчание считается честным там, где нужного куска
// не было, и зряшным там, где был; разрыв считается одной формулой с работой.
func TestAbstainTableCountsRightAndWrongSilence(t *testing.T) {
	points := []GapPoint{
		{Gap: 0.01, Hit: false}, {Gap: 0.02, Hit: true},
		{Gap: 0.10, Hit: false}, {Gap: 0.40, Hit: true},
	}
	rows := AbstainTable(points, []float64{0.05, 0.2})
	if rows[0].Silent != 2 || rows[0].SilentRight != 1 || rows[0].SilentWrong != 1 {
		t.Errorf("порог 0.05: %+v", rows[0])
	}
	if rows[1].Silent != 3 || rows[1].SilentRight != 2 || rows[1].SilentWrong != 1 {
		t.Errorf("порог 0.2: %+v", rows[1])
	}
	hits := []Result{{Score: 10}, {Score: 8}}
	if g := TopGap(hits); g < 0.199 || g > 0.201 {
		t.Errorf("TopGap = %v, ожидалось 0.2", g)
	}
	if TopGap(hits[:1]) != 0 {
		t.Error("один кусок — разрыва нет")
	}
}
