package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func seconds(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

// Живая скорость считается от первого куска, а не от отправки запроса: иначе
// ожидание загрузки модели с диска размазывалось бы по строке и первые секунды
// скорость выглядела бы втрое ниже настоящей.
func TestLiveSpeedCountsFromFirstChunk(t *testing.T) {
	start := time.Now()
	var s liveSpeed
	s.Start(start)
	// Модель грузилась с диска пять секунд, потом выдала 21 кусок за две.
	s.Tick(start.Add(seconds(5)))
	for i := 1; i <= 20; i++ {
		s.Tick(start.Add(seconds(5 + 0.1*float64(i))))
	}
	got := s.Rate(start.Add(seconds(7)))
	if got < 9.5 || got > 10.5 {
		t.Errorf("скорость = %.1f, ожидалось около 10", got)
	}
	if s.TTFT() != seconds(5) {
		t.Errorf("время до первого токена = %s", s.TTFT())
	}
}

// До первого куска скорости нет.
func TestNoSpeedBeforeFirstChunk(t *testing.T) {
	start := time.Now()
	var s liveSpeed
	s.Start(start)
	if r := s.Rate(start.Add(seconds(3))); r != 0 {
		t.Errorf("скорость до ответа = %.1f, ожидался ноль", r)
	}
	s.Tick(start.Add(seconds(3)))
	if r := s.Rate(start.Add(seconds(4))); r != 0 {
		t.Errorf("по одному куску скорость не считается: %.1f", r)
	}
}

// Настройка скорости решает что показывать.
func TestSpeedSettingDecidesWhatToShow(t *testing.T) {
	start := time.Now()
	var s liveSpeed
	s.Start(start)
	s.Tick(start.Add(seconds(1)))
	for i := 1; i <= 10; i++ {
		s.Tick(start.Add(seconds(1 + 0.1*float64(i))))
	}
	now := start.Add(seconds(2))
	total := ollama.Stats{EvalCount: 200, EvalDuration: int64(4 * time.Second)}

	cases := []struct {
		mode     string
		live     bool
		contains string
		missing  string
	}{
		{SpeedOff, true, "", "ток/с"},
		{SpeedOff, false, "", "ток/с"},
		{SpeedLive, true, "≈", ""},
		{SpeedLive, false, "", "ток/с"},
		{SpeedFinal, true, "", "ток/с"},
		{SpeedFinal, false, "50 ток/с", "≈"},
		{SpeedFull, true, "первый токен", ""},
		{SpeedFull, false, "первый токен", ""},
	}
	for _, c := range cases {
		got := speedStatus(c.mode, c.live, &s, total, now)
		if c.contains != "" && !strings.Contains(got, c.contains) {
			t.Errorf("режим %q, идёт=%v: %q не содержит %q", c.mode, c.live, got, c.contains)
		}
		if c.missing != "" && strings.Contains(got, c.missing) {
			t.Errorf("режим %q, идёт=%v: %q содержит лишнее %q", c.mode, c.live, got, c.missing)
		}
	}
}

// Итог берётся из счётчиков сервера. Нет счётчиков — показывать нечего,
// выдумывать нельзя.
func TestNoServerNumbersNoTotal(t *testing.T) {
	var s liveSpeed
	if got := speedStatus(SpeedFinal, false, &s, ollama.Stats{}, time.Now()); got != "" {
		t.Errorf("итог без цифр сервера = %q", got)
	}
}

// Короткая длительность.
func TestShortDuration(t *testing.T) {
	pairs := map[time.Duration]string{
		400 * time.Millisecond: "0.4 с",
		12 * time.Second:       "12 с",
		125 * time.Second:      "2м05с",
	}
	for d, want := range pairs {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %q, ожидалось %q", d, got, want)
		}
	}
}
