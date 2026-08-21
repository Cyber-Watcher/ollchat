package main

import (
	"regexp"
	"strings"
	"unicode"
)

// fenceRe находит блоки кода в ответе модели: ```go … ``` .
// Язык блока может отсутствовать — тогда группа пуста.
var fenceRe = regexp.MustCompile("(?s)```([A-Za-z0-9_+-]*)[ \t]*\r?\n(.*?)```")

// ExtractCode достаёт из ответа код для проверки.
//
// Правило простое и предсказуемое: берётся самый длинный блок на нужном языке,
// а если таких нет — самый длинный блок вообще. Модели любят показывать сначала
// «было», потом «стало», и брать первый попавшийся блок значит собирать не то.
// Пусто означает, что кода в ответе нет, — это провал задачи, а не сбой прогона.
func ExtractCode(answer, lang string) string {
	matches := fenceRe.FindAllStringSubmatch(answer, -1)
	if len(matches) == 0 {
		return ""
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	best, bestAny := "", ""
	for _, m := range matches {
		got, body := strings.ToLower(m[1]), m[2]
		if len(body) > len(bestAny) {
			bestAny = body
		}
		if lang != "" && got == lang && len(body) > len(best) {
			best = body
		}
	}
	if best != "" {
		return best
	}
	if lang == "" {
		return bestAny
	}
	// Языка не нашлось: блок без пометки — обычное дело, и он всё равно
	// пойдёт в проверку, где компилятор сам решит, годится ли.
	return bestAny
}

// MixedScriptWords считает слова, в которых кириллица смешана с латиницей.
//
// Это болезнь `nemotron-3.5-lightning`: внутри русского слова оказывается
// латинская буква, похожая начертанием. Читаемость страдает, а поиск по тексту
// перестаёт находить слово вовсе. Слова из одних латинских букв (`goroutine`,
// `num_ctx`) не в счёт — считается именно смешение внутри слова.
func MixedScriptWords(text string) int {
	var count int
	var hasCyr, hasLat bool
	flush := func() {
		if hasCyr && hasLat {
			count++
		}
		hasCyr, hasLat = false, false
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			hasCyr = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLat = true
		case r == '\'' || r == '_' || unicode.IsDigit(r):
			// Часть слова: цифра в «qwen3», подчёркивание в «num_ctx».
			// Дефис — наоборот, разделитель: «CI-система» и «Go-программа»
			// написаны верно, а болезнь выглядит иначе — латинская буква
			// стоит внутри слова, без всякого знака.
		default:
			flush()
		}
	}
	flush()
	return count
}

// WordCount считает слова — знаменатель для частоты смешения алфавитов.
func WordCount(text string) int {
	return len(strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}))
}

// Refused распознаёт отказ отвечать. Нужен набору по информационной
// безопасности: модель, которая отказывается разбирать собственный конфиг
// администратора, для этой работы непригодна, и это надо считать, а не гадать.
var refusalRe = regexp.MustCompile(`(?i)(не могу помочь|не могу предоставить|не буду помогать|это может быть незаконн|i can'?t help|i cannot assist|i'?m sorry,? but i can'?t)`)

// Refused сообщает, похож ли ответ на отказ.
func Refused(answer string) bool {
	head := answer
	if len(head) > 1200 {
		head = head[:1200]
	}
	return refusalRe.MatchString(head)
}
