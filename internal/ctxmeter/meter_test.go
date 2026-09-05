package ctxmeter

import "testing"

func TestCapacityPriority(t *testing.T) {
	var m Meter
	if m.Percent() != -1 {
		t.Error("без ёмкости процент должен быть -1")
	}
	m.SetCapacity(0, SourcePS)
	if m.Capacity != 0 {
		t.Error("нулевая ёмкость не должна применяться")
	}
	m.SetCapacity(32768, SourceConfig)
	if m.Capacity != 32768 || m.Source != SourceConfig {
		t.Errorf("ёмкость = %d из %v", m.Capacity, m.Source)
	}
}

func TestObserveIsExact(t *testing.T) {
	var m Meter
	m.SetCapacity(32768, SourceConfig)
	m.Observe(1000, 240)

	if !m.Exact {
		t.Error("после ответа сервера значение должно быть точным")
	}
	if m.Used != 1240 {
		t.Errorf("занято = %d, ожидалось 1240", m.Used)
	}
	if got := m.Percent(); got != 3 {
		t.Errorf("процент = %d, ожидалось 3", got)
	}
}

func TestEstimateIsMarkedInexact(t *testing.T) {
	var m Meter
	m.SetCapacity(1000, SourceConfig)
	m.Observe(100, 100)
	m.AddEstimate("текст запроса")

	if m.Exact {
		t.Error("после добавления оценки значение не должно считаться точным")
	}
	s := m.String(10)
	if len(s) == 0 || s[:3] != "ctx" {
		t.Errorf("строка индикатора = %q", s)
	}
	if !containsRune(s, '≈') {
		t.Errorf("оценка должна помечаться знаком ≈, получено %q", s)
	}
}

func TestPercentCappedAt100(t *testing.T) {
	m := Meter{Capacity: 100, Used: 250, Exact: true}
	if got := m.Percent(); got != 100 {
		t.Errorf("процент = %d, ожидалось 100", got)
	}
	bar := m.Bar(10)
	if len([]rune(bar)) != 10 {
		t.Errorf("длина полосы = %d, ожидалось 10", len([]rune(bar)))
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		0: "0", 999: "999", 1234: "1.2k", 12345: "12k", 262144: "262k",
	}
	for in, want := range cases {
		if got := FormatTokens(in); got != want {
			t.Errorf("FormatTokens(%d) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestEstimateChars(t *testing.T) {
	if got := EstimateChars(0); got != 0 {
		t.Errorf("EstimateChars(0) = %d", got)
	}
	if got := EstimateChars(300); got != 100 {
		t.Errorf("EstimateChars(300) = %d, ожидалось 100", got)
	}
	if Estimate("abc") != EstimateChars(3) {
		t.Error("Estimate и EstimateChars должны давать одинаковый результат")
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
