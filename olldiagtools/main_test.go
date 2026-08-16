package main

import "testing"

// Разбор аргументов командной строки живёт в самом инструменте.

func TestParseInts(t *testing.T) {
	got, err := parseInts("1, 2,4k , 8")
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	want := []int{1, 2, 4096, 8}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("получено %v, ожидалось %v", got, want)
		}
	}
	for _, bad := range []string{"", "abc", "0", "-3"} {
		if _, err := parseInts(bad); err == nil {
			t.Errorf("%q должно быть ошибкой", bad)
		}
	}
}

func TestSamplePointsStayWithinModelMaximum(t *testing.T) {
	pts, err := samplePoints("", 8192)
	if err != nil {
		t.Fatalf("подбор точек: %v", err)
	}
	for _, p := range pts {
		if p > 8192 {
			t.Errorf("точка %d выходит за максимум модели 8192", p)
		}
		if p%1024 != 0 {
			t.Errorf("точка %d не кратна 1024", p)
		}
	}
	if len(pts) < 2 {
		t.Errorf("для подгонки нужно хотя бы две точки, получено %v", pts)
	}
}

// -calc N разворачивается в весь ряд пользователей, а не в степени двойки:
// по сплошному ряду видно, где именно проходит граница.
func TestUserRange(t *testing.T) {
	cases := map[int]string{
		1: "1",
		3: "1,2,3",
		8: "1,2,3,4,5,6,7,8",
		0: "1", // бессмысленное значение приводим к одному пользователю
	}
	for n, want := range cases {
		if got := userRange(n); got != want {
			t.Errorf("userRange(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}
