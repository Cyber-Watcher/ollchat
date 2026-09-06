package maint

import "testing"

// Цена пересчёта считается от перекроившихся тем, а не от всех.
//
// До 06.09.2026 `--graph-drift` печатал «пересчёт сотрёт описания у всех N тем»
// и звал цену за все N — текст пережил заведение переноса описаний (этап 79).
// Замер того дня: тем 6 830, перенеслось бы 5 369, заново нужны 1 461, то есть
// названная цена была впятеро выше настоящей. Вредило это прямо: по этой самой
// строке решают, пересчитывать или ждать.
func TestSummaryMinutesCountsChangedNotAll(t *testing.T) {
	const (
		allThemes     = 6830
		changedThemes = 1461
	)
	all := summaryMinutes(allThemes)
	changed := summaryMinutes(changedThemes)

	if changed >= all {
		t.Fatalf("цена перекроившихся (%d мин) не меньше цены всех (%d мин)", changed, all)
	}
	// Замер 27.08.2026: 2 590 резюме за 35 минут. На 1 461 тему это около
	// двадцати минут; допуск широкий — число оценочное и названо таковым.
	if changed < 15 || changed > 25 {
		t.Errorf("оценка на %d тем — %d мин, ожидалось около двадцати", changedThemes, changed)
	}
}

// Ноль тем — ноль минут: строка о цене тогда не печатается вовсе.
func TestSummaryMinutesZero(t *testing.T) {
	if got := summaryMinutes(0); got != 0 {
		t.Errorf("на нуле тем названо %d мин", got)
	}
	if got := summaryMinutes(-5); got != 0 {
		t.Errorf("на отрицательном числе тем названо %d мин", got)
	}
}

// Одна тема стоит не «ноль минут»: округление вниз до нуля читалось бы
// как «бесплатно», а карта занимается в любом случае.
func TestSummaryMinutesRoundsUpToOne(t *testing.T) {
	if got := summaryMinutes(1); got != 1 {
		t.Errorf("на одной теме названо %d мин, ожидалась одна", got)
	}
}
