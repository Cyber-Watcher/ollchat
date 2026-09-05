package kb

import "strings"

// StemWord приводит слово к основе тем же способом, каким это делает поиск
// по словам.
//
// Экспортируется ради графа. Вход в граф ищет понятие по написанию, и без общей
// основы вопрос «чем переранжировать найденные куски» не находит понятие
// «переранжирование», хотя оно есть в графе синонимом к `reranking`. Замер
// 26.08.2026: из двенадцати живых вопросов граф промахнулся на двух, и один
// промах — ровно этот, разница глагола и существительного.
//
// Общий код важнее общей идеи: если бы граф завёл свой стеммер, две основы
// разошлись бы при первой же правке одного из них, и разошлись бы молча.
func StemWord(word string) string {
	if word == "" {
		return ""
	}
	var flags wordFlags
	for _, r := range word {
		flags |= classify(r)
	}
	s := stem(strings.ToLower(word), flags)
	if s == "" {
		return strings.ToLower(word)
	}
	return s
}

// StemPhrase приводит к основам каждое слово фразы.
//
// Разделитель на выходе — один пробел, поэтому «контекстное  окно» и
// «контекстное окно» дают одну и ту же основу. Порядок слов сохраняется:
// «окно контекста» и «контекстное окно» — разные понятия, и сливать их нельзя.
func StemPhrase(phrase string) string {
	fields := strings.Fields(phrase)
	if len(fields) == 0 {
		return ""
	}
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		out = append(out, StemWord(w))
	}
	return strings.Join(out, " ")
}
