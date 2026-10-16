package maint

import (
	"strings"
	"testing"
)

// Проверка пары «вопрос, факт» к куску A при отвлекателях (этап 112, Ч8).
func TestDistractOK(t *testing.T) {
	a := "Команда EXPLAIN показывает план выполнения запроса, а EXPLAIN ANALYZE ещё и выполняет его."
	b := "Индексы ускоряют выборку, но замедляют вставку строк в таблицу."
	c := "Планировщик выбирает порядок соединений по статистике таблиц."
	others := []string{b, c}

	cases := []struct {
		name, q, must string
		others        []string
		want          string // подстрока причины отказа; пусто — годна
	}{
		{"годная пара", "Чем смотрят план выполнения запроса?", "EXPLAIN", others, ""},
		{"служебный кусок", "ПРОПУСК", "", others, "служебный"},
		{"факт есть и в отвлекателе", "Чем смотрят план выполнения запроса?", "EXPLAIN",
			[]string{b, "В другом месте тоже сказано про EXPLAIN."}, "в отвлекателе"},
		{"факта нет в куске", "Чем смотрят план выполнения запроса?", "VACUUM", others, "нет в куске"},
		{"факт в самом вопросе", "Что делает EXPLAIN с планом запроса?", "EXPLAIN", others, "в самом вопросе"},
		{"слишком короткий вопрос", "Что за план?", "EXPLAIN", others, "длина"},
		{"про устройство книги", "Что сказано в оглавлении про планы запросов?", "EXPLAIN", others, "устройство книги"},
	}
	for _, tc := range cases {
		got := distractOK(tc.q, tc.must, a, tc.others)
		if tc.want == "" && got != "" {
			t.Errorf("%s: отказ %q, ждали годную", tc.name, got)
		}
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: причина %q, ждали «%s»", tc.name, got, tc.want)
		}
	}
}

func TestCutRunes(t *testing.T) {
	if got := cutRunes("абвгд", 3); got != "абв" {
		t.Errorf("cutRunes по рунам: %q", got)
	}
	if got := cutRunes("аб", 3); got != "аб" {
		t.Errorf("короткая строка не меняется: %q", got)
	}
}
