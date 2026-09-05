package kb

import "strings"

// Приведение английского слова к основе — алгоритм Портера.
//
// Для английского выигрыш меньше, чем для русского, но нужен по той же причине:
// без него «channels» и «channel», «buffered» и «buffer» — разные термы.
//
// Мера слова (m) — сколько раз в нём чередуются группы гласных и согласных.
// Почти все правила снимают окончание только при достаточной мере: иначе
// от короткого слова осталась бы пара букв.

func stemEnglish(word string) string {
	w := []rune(word)
	if len(w) <= 2 {
		return word
	}
	w = step1a(w)
	w = step1b(w)
	w = step1c(w)
	w = step2(w)
	w = step3(w)
	w = step4(w)
	w = step5(w)
	return string(w)
}

func isVowelEN(w []rune, i int) bool {
	switch w[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	case 'y':
		// «y» — гласная, если перед ней согласная: «try» да, «yes» нет.
		return i > 0 && !isVowelEN(w, i-1)
	}
	return false
}

// measure считает меру основы: число переходов «гласные → согласные».
func measure(w []rune) int {
	n, i := 0, 0
	for i < len(w) && !isVowelEN(w, i) {
		i++
	}
	for i < len(w) {
		for i < len(w) && isVowelEN(w, i) {
			i++
		}
		if i >= len(w) {
			break
		}
		n++
		for i < len(w) && !isVowelEN(w, i) {
			i++
		}
	}
	return n
}

func hasVowel(w []rune) bool {
	for i := range w {
		if isVowelEN(w, i) {
			return true
		}
	}
	return false
}

func doubleConsonant(w []rune) bool {
	n := len(w)
	return n >= 2 && w[n-1] == w[n-2] && !isVowelEN(w, n-1)
}

// cvc — согласная, гласная, согласная, где последняя не w, x, y.
// Такое окончание требует восстановления «e»: «hop» → «hoping», не «hopping».
func cvc(w []rune) bool {
	n := len(w)
	if n < 3 {
		return false
	}
	if isVowelEN(w, n-1) || !isVowelEN(w, n-2) || isVowelEN(w, n-3) {
		return false
	}
	switch w[n-1] {
	case 'w', 'x', 'y':
		return false
	}
	return true
}

func hasSuffix(w []rune, s string) bool { return strings.HasSuffix(string(w), s) }

func trim(w []rune, n int) []rune { return w[:len(w)-n] }

// replaceIf снимает окончание и приписывает новое, если мера основы больше min.
func replaceIf(w []rune, suffix, with string, min int) ([]rune, bool) {
	if !hasSuffix(w, suffix) {
		return w, false
	}
	stem := trim(w, len([]rune(suffix)))
	if measure(stem) <= min {
		return w, false
	}
	return append(stem, []rune(with)...), true
}

// step1a — множественное число.
func step1a(w []rune) []rune {
	switch {
	case hasSuffix(w, "sses"):
		return trim(w, 2)
	case hasSuffix(w, "ies"):
		return trim(w, 2)
	case hasSuffix(w, "ss"):
		return w
	case hasSuffix(w, "s"):
		return trim(w, 1)
	}
	return w
}

// step1b — прошедшее время и герундий.
func step1b(w []rune) []rune {
	if hasSuffix(w, "eed") {
		if measure(trim(w, 3)) > 0 {
			return trim(w, 1)
		}
		return w
	}
	cut := false
	switch {
	case hasSuffix(w, "ed") && hasVowel(trim(w, 2)):
		w, cut = trim(w, 2), true
	case hasSuffix(w, "ing") && hasVowel(trim(w, 3)):
		w, cut = trim(w, 3), true
	}
	if !cut {
		return w
	}
	switch {
	case hasSuffix(w, "at"), hasSuffix(w, "bl"), hasSuffix(w, "iz"):
		return append(w, 'e')
	case doubleConsonant(w) && !hasSuffix(w, "l") && !hasSuffix(w, "s") && !hasSuffix(w, "z"):
		return trim(w, 1)
	case measure(w) == 1 && cvc(w):
		return append(w, 'e')
	}
	return w
}

// step1c — «y» после гласной становится «i»: «happy» → «happi».
func step1c(w []rune) []rune {
	if hasSuffix(w, "y") && hasVowel(trim(w, 1)) {
		w = trim(w, 1)
		return append(w, 'i')
	}
	return w
}

var step2Rules = [][2]string{
	{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"}, {"anci", "ance"},
	{"izer", "ize"}, {"abli", "able"}, {"alli", "al"}, {"entli", "ent"},
	{"eli", "e"}, {"ousli", "ous"}, {"ization", "ize"}, {"ation", "ate"},
	{"ator", "ate"}, {"alism", "al"}, {"iveness", "ive"}, {"fulness", "ful"},
	{"ousness", "ous"}, {"aliti", "al"}, {"iviti", "ive"}, {"biliti", "ble"},
}

func step2(w []rune) []rune {
	for _, r := range step2Rules {
		if out, ok := replaceIf(w, r[0], r[1], 0); ok {
			return out
		}
	}
	return w
}

var step3Rules = [][2]string{
	{"icate", "ic"}, {"ative", ""}, {"alize", "al"}, {"iciti", "ic"},
	{"ical", "ic"}, {"ful", ""}, {"ness", ""},
}

func step3(w []rune) []rune {
	for _, r := range step3Rules {
		if out, ok := replaceIf(w, r[0], r[1], 0); ok {
			return out
		}
	}
	return w
}

var step4Suffixes = []string{
	"al", "ance", "ence", "er", "ic", "able", "ible", "ant", "ement",
	"ment", "ent", "ou", "ism", "ate", "iti", "ous", "ive", "ize",
}

func step4(w []rune) []rune {
	// «ion» снимается только после s или t: «adoption» → «adopt», но не «lion».
	if hasSuffix(w, "ion") {
		stem := trim(w, 3)
		if measure(stem) > 1 && len(stem) > 0 && (stem[len(stem)-1] == 's' || stem[len(stem)-1] == 't') {
			return stem
		}
	}
	for _, s := range step4Suffixes {
		if !hasSuffix(w, s) {
			continue
		}
		if stem := trim(w, len([]rune(s))); measure(stem) > 1 {
			return stem
		}
		return w
	}
	return w
}

// step5 снимает немую «e» и удвоенную «l».
func step5(w []rune) []rune {
	if hasSuffix(w, "e") {
		stem := trim(w, 1)
		if m := measure(stem); m > 1 || (m == 1 && !cvc(stem)) {
			w = stem
		}
	}
	if measure(w) > 1 && doubleConsonant(w) && hasSuffix(w, "l") {
		w = trim(w, 1)
	}
	return w
}
