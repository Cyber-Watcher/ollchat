package mixer

import (
	"strings"
	"unicode"
)

// Вопрос-продолжение (этап 112, У9). Короткая реплика «а как настроить сбор
// логов?» темы прошлого вопроса не несёт, и подмешивание, ищущее по ней одной,
// находит мимо. Правило снято замером на 50 случаях (`docs/eval/kb_followups*.toml`,
// `evalpair.py --followup-rule`): склейка «предыдущий вопрос + реплика» только для
// продолжений находит 14 против 7 у голой реплики, парно +8 −1, и теряет одну
// смену темы из 10. Склейка всегда находила 15, но теряла все смены темы.

// followupMaxWords — реплика не длиннее стольких слов считается продолжением.
const followupMaxWords = 5

// followupStarts — зачины продолжения после «а» и «и» (их ловит первое слово).
var followupStarts = []string{"то есть", "что насчёт", "что насчет"}

// IsFollowup — похожа ли реплика на продолжение прошлого вопроса: не длиннее
// пяти слов или начинается с «а», «и», «то есть», «что насчёт». То же правило,
// что `is_followup` в `privatescripts/evalpair.py`, которым оно замерено.
func IsFollowup(text string) bool {
	q := strings.ToLower(strings.TrimSpace(text))
	words := strings.Fields(q)
	if len(words) == 0 {
		return false
	}
	if len(words) <= followupMaxWords {
		return true
	}
	// Первое слово без знаков после него: «а,» и «и…» — тоже зачин, «ананас» — нет.
	first := strings.TrimRightFunc(words[0], func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if first == "а" || first == "и" {
		return true
	}
	for _, s := range followupStarts {
		if rest, ok := strings.CutPrefix(q, s); ok {
			if rest == "" {
				return true
			}
			r := []rune(rest)[0]
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return true
			}
		}
	}
	return false
}

// FollowupQuery — запрос подмешивания для реплики: «предыдущий вопрос + реплика»,
// если реплика — продолжение и предыдущий вопрос есть; иначе сама реплика.
// Склейка через пробел — как в наборе `kb_followups_concat.toml`.
func FollowupQuery(prev, text string) string {
	prev = strings.TrimSpace(prev)
	if prev == "" || !IsFollowup(text) {
		return text
	}
	return prev + " " + strings.TrimSpace(text)
}
