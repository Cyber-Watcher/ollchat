package graph

import "testing"

// Случаи взяты из разбора «неподтверждённых» связей глазами (этап 104, П6.1)
// и из переписи порчи текста 17.09.2026.
func TestSeenInText(t *testing.T) {
	cases := []struct {
		text, name string
		want       bool
	}{
		{"Abuse Existing Func\u2010\n      tionality of the app", "Abuse Existing Functionality", true},
		{"на прокси\u00adсервере хранится", "прокси-сервер", false}, // падеж — не наша забота
		{"на прокси\u00adсервере хранится", "прокси-сервере", true},
		{"популярные алго-\n   ритмы машинного обучения", "алгоритмы машинного обучения", true},
		{"для специалистов-\nпрактиков всех уровней", "специалистов-практиков", true},
		{"a well con\ufb01gured cluster", "configured cluster", true},
		{"называемые замка\u0301ми", "замками", true},
		{"cloud.\u200bgoogle.\u200bcom", "cloud.google.com", true},
		{"ёлка и елка", "елка", true},
		{"knowledge\ngraph construction", "Knowledge Graph", true}, // имя через перевод строки
		{"we are going to google it", "Go", false},                 // короткое имя не ищется вовсе
		{"ongoing work", "going", false},                           // границы слова
		{"five\u2010step saga", "five-step saga", true},
		{"конец-\n\nабзаца", "конецабзаца", false},
	}
	for _, c := range cases {
		j, h := MatchText(c.text)
		if got := SeenInText(j, h, c.name); got != c.want {
			t.Errorf("текст %q, имя %q: получено %v (чтения %q / %q)", c.text, c.name, got, j, h)
		}
	}
}
