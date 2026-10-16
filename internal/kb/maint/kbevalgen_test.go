package maint

import "testing"

// Ответ модели принимается и голой строкой, и JSON, и JSON в ограде; с must
// факт достаётся из того же JSON.
func TestParseGenAnswer(t *testing.T) {
	cases := []struct {
		raw, q, must string
		bad          bool
	}{
		{raw: "Как ограничить память контейнера?", q: "Как ограничить память контейнера?"},
		{raw: `{"question": "Чем ограничить память контейнера?", "must": "--memory"}`,
			q: "Чем ограничить память контейнера?", must: "--memory"},
		{raw: "```json\n{\"question\": \"Какой порт у PostgreSQL по умолчанию?\", \"must\": \"5432\"}\n```",
			q: "Какой порт у PostgreSQL по умолчанию?", must: "5432"},
		{raw: "ПРОПУСК", q: "ПРОПУСК"},
		{raw: "это не вопрос", bad: true},
		{raw: "", bad: true},
	}
	for _, c := range cases {
		q, must, err := parseGenAnswer(c.raw)
		if c.bad {
			if err == nil {
				t.Errorf("%q: ждали отказ, получили %q / %q", c.raw, q, must)
			}
			continue
		}
		if err != nil || q != c.q || must != c.must {
			t.Errorf("%q: получили %q / %q / %v, ждали %q / %q", c.raw, q, must, err, c.q, c.must)
		}
	}
}

// Факт годится, только если он есть в куске дословно и его нет в вопросе:
// иначе подстрока засчитала бы память модели или повтор вопроса.
func TestBadMust(t *testing.T) {
	chunk := "Default port for PostgreSQL is 5432.\nUse the --memory flag\n to cap a container, e.g. docker run --memory 512m."
	cases := []struct {
		must, q string
		ok      bool
	}{
		{"5432", "Какой порт у PostgreSQL по умолчанию?", true},
		{"--memory", "Чем ограничить память контейнера?", true},
		{"MEMORY flag", "Чем ограничить память контейнера?", true},        // регистр и перенос строки не мешают
		{"6543", "Какой порт у PostgreSQL по умолчанию?", false},          // нет в куске
		{"PostgreSQL", "Какой порт у PostgreSQL по умолчанию?", false},    // есть в вопросе
		{"5432, 5433", "Какой порт?", false},                              // запятая ломает список must
		{"ab", "Какой порт?", false},                                      // слишком коротко
		{"Use the --memory flag to cap", "Чем ограничить память?", false}, // длиннее четырёх слов
		{"", "Какой порт?", false},
	}
	for _, c := range cases {
		why := badMust(c.must, c.q, chunk)
		if (why == "") != c.ok {
			t.Errorf("факт %q к вопросу %q: причина %q, годен ожидалось %v", c.must, c.q, why, c.ok)
		}
	}
}
