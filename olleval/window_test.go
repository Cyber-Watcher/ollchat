package main

import (
	"testing"
	"time"
)

func мск(t *testing.T, date, hhmm string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("нет базы часовых поясов")
	}
	p, err := time.ParseInLocation("2006-01-02 15:04", date+" "+hhmm, loc)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// расписание собирает Schedule из промежутков по дням.
// 21.08.2026 — пятница, 22.08 — суббота, 23.08 — воскресенье, 24.08 — понедельник.
func расписание(days DaySchedule) Schedule {
	return Schedule{Timezone: "Europe/Moscow", Gap: Duration(15 * time.Minute), Days: days}
}

func ночьВезде() DaySchedule {
	n := []string{"00:00-07:00"}
	return DaySchedule{Monday: n, Tuesday: n, Wednesday: n, Thursday: n, Friday: n, Saturday: n, Sunday: n}
}

func TestInWindowНочноеОкно(t *testing.T) {
	s := расписание(ночьВезде())
	cases := map[string]bool{"00:00": true, "03:30": true, "06:59": true, "07:00": false, "09:32": false, "23:59": false}
	for hhmm, want := range cases {
		got, err := InWindow(мск(t, "2026-08-21", hhmm), s)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("пятница %s: %v, ожидалось %v", hhmm, got, want)
		}
	}
}

// Задачи прекращаются раньше конца окна: остаток нужен на выгрузку моделей
// и сводку, иначе сервер откроется позже обещанного.
func TestОкноОставляетЗапасПередЗакрытием(t *testing.T) {
	w, ok, err := CurrentWindow(мск(t, "2026-08-21", "03:00"), расписание(ночьВезде()))
	if err != nil || !ok {
		t.Fatalf("окно не найдено: %v %v", ok, err)
	}
	loc := w.End.Location()
	if got := w.End.In(loc).Format("15:04"); got != "07:00" {
		t.Errorf("конец окна = %s, ожидалось 07:00", got)
	}
	if got := w.Deadline.In(loc).Format("15:04"); got != "06:45" {
		t.Errorf("предел задач = %s, ожидалось 06:45 (запас 15 минут)", got)
	}
	if w.Day != time.Friday {
		t.Errorf("день окна = %v, ожидалась пятница", w.Day)
	}
}

// Любому дню можно задать своё расписание — механизм один на все семь.
func TestРасписаниеПоДнямНедели(t *testing.T) {
	days := ночьВезде()
	days.Wednesday = []string{"00:00-07:00", "13:00-14:00"} // среда: ночь и обеденный перерыв
	days.Friday = nil                                       // по пятницам не гоняем
	days.Saturday = []string{"00:00-23:59"}
	s := расписание(days)

	cases := []struct {
		date, hhmm, что string
		want            bool
	}{
		{"2026-08-19", "13:30", "среда, обеденный промежуток", true},
		{"2026-08-19", "15:00", "среда, вне промежутков", false},
		{"2026-08-21", "03:00", "пятница выключена", false},
		{"2026-08-22", "14:00", "суббота целиком", true},
		{"2026-08-24", "03:00", "понедельник, ночь", true},
		{"2026-08-24", "13:30", "понедельник, дня нет", false},
	}
	for _, c := range cases {
		got, err := InWindow(мск(t, c.date, c.hhmm), s)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s (%s %s): %v, ожидалось %v", c.что, c.date, c.hhmm, got, c.want)
		}
	}
}

// Промежуток через полночь относится к тому дню, в котором начался.
func TestПромежутокЧерезПолночь(t *testing.T) {
	days := DaySchedule{Saturday: []string{"22:00-02:00"}}
	s := расписание(days)
	if ok, _ := InWindow(мск(t, "2026-08-22", "23:30"), s); !ok {
		t.Error("суббота 23:30 не попала в промежуток 22:00-02:00")
	}
	if ok, _ := InWindow(мск(t, "2026-08-23", "01:30"), s); !ok {
		t.Error("ночь на воскресенье не попала в субботний промежуток через полночь")
	}
	if ok, _ := InWindow(мск(t, "2026-08-23", "03:00"), s); ok {
		t.Error("03:00 воскресенья попало в окно, хотя промежуток кончился в 02:00")
	}
}

// Короткий промежуток не должен получить предел раньше собственного начала.
func TestКороткийПромежутокНеТеряетВремя(t *testing.T) {
	s := расписание(DaySchedule{Saturday: []string{"13:00-13:10"}})
	w, ok, err := CurrentWindow(мск(t, "2026-08-22", "13:05"), s)
	if err != nil || !ok {
		t.Fatalf("окно не найдено: %v %v", ok, err)
	}
	if !w.Deadline.After(w.Start) {
		t.Errorf("предел %v не позже начала %v — задачи не раздались бы вовсе", w.Deadline, w.Start)
	}
}

// Пустое расписание означает «прогонов нет», а не ошибку.
func TestПустоеРасписание(t *testing.T) {
	if ok, err := InWindow(мск(t, "2026-08-22", "12:00"), расписание(DaySchedule{})); err != nil || ok {
		t.Errorf("пустое расписание дало окно: %v %v", ok, err)
	}
}

// Пояс задаётся явно: стенд перевели на Москву, но сервер могут перенастроить.
func TestОкноСчитаетсяПоЗаданномуПоясу(t *testing.T) {
	utc := time.Date(2026, 8, 21, 6, 32, 0, 0, time.UTC) // 09:32 МСК
	if ok, _ := InWindow(utc, расписание(ночьВезде())); ok {
		t.Error("09:32 по Москве принято за ночное окно")
	}
	s := расписание(ночьВезде())
	s.Timezone = "Etc/UTC"
	if ok, _ := InWindow(utc, s); !ok {
		t.Error("06:32 UTC не признано окном при поясе UTC")
	}
}

// Старый вид настроек (одно ночное окно плюс выходные) продолжает работать:
// при загрузке он разворачивается в расписание по дням.
func TestУстаревшееРасписаниеРазворачивается(t *testing.T) {
	s := Schedule{
		Timezone: "Europe/Moscow", Start: "00:00", Until: "06:45", Restore: "07:00",
		Weekend: Weekend{Enabled: true, Saturday: []string{"00:00-23:59"}, Sunday: []string{"00:00-23:59"}},
	}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if got := s.Days.For(time.Monday); len(got) != 1 || got[0] != "00:00-07:00" {
		t.Errorf("будни развернулись в %v", got)
	}
	if got := s.Days.For(time.Saturday); len(got) != 1 || got[0] != "00:00-23:59" {
		t.Errorf("суббота развернулась в %v", got)
	}
	if time.Duration(s.Gap) != 15*time.Minute {
		t.Errorf("запас = %v, ожидалось 15m (разница until и restore)", time.Duration(s.Gap))
	}
	if ok, _ := InWindow(мск(t, "2026-08-22", "14:00"), s); !ok {
		t.Error("суббота днём не попала в окно после разворачивания старых настроек")
	}
}
