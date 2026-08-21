package main

import (
	"fmt"
	"strings"
	"time"
)

// Window — действующий промежуток, когда стенд наш.
//
// Start и End — границы, в которых сервер держится закрытым; Deadline — до
// какого момента раздаются задачи. Между ними лежит запас `schedule.gap`
// (по умолчанию четверть часа): за него надо успеть выгрузить модели,
// дописать сводку и открыть сервер людям вовремя.
type Window struct {
	Day      time.Weekday
	Start    time.Time
	End      time.Time
	Deadline time.Time
}

// дниНедели — как называть день в журнале прогона.
var дниНедели = map[time.Weekday]string{
	time.Monday: "понедельник", time.Tuesday: "вторник", time.Wednesday: "среда",
	time.Thursday: "четверг", time.Friday: "пятница", time.Saturday: "суббота",
	time.Sunday: "воскресенье",
}

// Name — как окно называть в журнале.
func (w Window) Name() string {
	if name, ok := дниНедели[w.Day]; ok {
		return "окно (" + name + ")"
	}
	return "окно"
}

// CurrentWindow возвращает промежуток, в который попадает момент now.
//
// Проверяются окна, начавшиеся вчера и сегодня: окно может переходить через
// полночь — и ночное (00:00–07:00 начинается «сегодня»), и выходное
// («22:00-02:00» относится к тому дню, в котором началось).
func CurrentWindow(now time.Time, s Schedule) (Window, bool, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return Window{}, false, fmt.Errorf("часовой пояс %q: %w", s.Timezone, err)
	}
	local := now.In(loc)
	for _, day := range []time.Time{local.AddDate(0, 0, -1), local} {
		windows, err := windowsForDay(day, s, loc)
		if err != nil {
			return Window{}, false, err
		}
		for _, w := range windows {
			if !local.Before(w.Start) && local.Before(w.End) {
				return w, true, nil
			}
		}
	}
	return Window{}, false, nil
}

// InWindow — короткий ответ на вопрос «сейчас можно?».
//
// Проверка нужна не для красоты. Скрипт прогона закрывает Ollama на localhost,
// и запуск среди рабочего дня отбирает сервер у людей. Один такой случай уже
// был: проба в 09:32 совпала с освобождением карты, прогон принял это
// за законный старт и закрыл сервер.
func InWindow(now time.Time, s Schedule) (bool, error) {
	_, ok, err := CurrentWindow(now, s)
	return ok, err
}

// windowsForDay строит окна, начинающиеся в указанный день недели.
//
// Все дни устроены одинаково: список промежутков из настроек. Разница между
// понедельником и субботой — только в числах, и это правильно: расписание
// правится под задачу, а не под то, что было заложено в код.
func windowsForDay(day time.Time, s Schedule, loc *time.Location) ([]Window, error) {
	spans := s.Days.For(day.In(loc).Weekday())
	gap := time.Duration(s.Gap)
	out := make([]Window, 0, len(spans))
	for _, span := range spans {
		from, to, err := parseSpan(span, loc)
		if err != nil {
			return nil, fmt.Errorf("schedule.days.%s: %w", strings.ToLower(day.In(loc).Weekday().String()), err)
		}
		start := atMinutes(day, from, loc)
		end := atMinutes(day, to, loc)
		if !end.After(start) {
			end = end.AddDate(0, 0, 1) // промежуток через полночь
		}
		// Задачи перестают раздаваться раньше конца окна: остаток нужен, чтобы
		// выгрузить модели, дописать сводку и открыть сервер вовремя.
		deadline := end.Add(-gap)
		if !deadline.After(start) {
			deadline = end
		}
		out = append(out, Window{Day: day.In(loc).Weekday(), Start: start, End: end, Deadline: deadline})
	}
	return out, nil
}

// parseSpan разбирает промежуток вида "00:00-23:59".
func parseSpan(span string, loc *time.Location) (from, to int, err error) {
	parts := strings.Split(strings.TrimSpace(span), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%q: нужен вид ЧЧ:ММ-ЧЧ:ММ", span)
	}
	if from, err = minutesOfDay(strings.TrimSpace(parts[0]), loc); err != nil {
		return 0, 0, fmt.Errorf("%q: %w", span, err)
	}
	if to, err = minutesOfDay(strings.TrimSpace(parts[1]), loc); err != nil {
		return 0, 0, fmt.Errorf("%q: %w", span, err)
	}
	return from, to, nil
}

func minutesOfDay(hhmm string, loc *time.Location) (int, error) {
	t, err := time.ParseInLocation("15:04", hhmm, loc)
	if err != nil {
		return 0, fmt.Errorf("%q: нужен вид ЧЧ:ММ", hhmm)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func atMinutes(day time.Time, minutes int, loc *time.Location) time.Time {
	d := day.In(loc)
	return time.Date(d.Year(), d.Month(), d.Day(), minutes/60, minutes%60, 0, 0, loc)
}
