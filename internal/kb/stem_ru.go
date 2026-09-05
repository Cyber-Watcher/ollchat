package kb

import "strings"

// Приведение русского слова к основе.
//
// Реализован алгоритм Snowball для русского языка. Без него поиск разваливается
// на падежах: «горутина», «горутины», «горутинам» становятся тремя разными
// термами, и запрос промахивается в двух случаях из трёх.
//
// Алгоритм работает не со всем словом, а с областями:
//
//	RV — всё после первой гласной,
//	R1 — после первой согласной, идущей за гласной,
//	R2 — то же самое внутри R1.
//
// Окончания снимаются только в своей области: иначе «дом» превратился бы в «д».

var vowelsRU = map[rune]bool{'а': true, 'е': true, 'и': true, 'о': true, 'у': true,
	'ы': true, 'э': true, 'ю': true, 'я': true}

// Окончания перечислены длинными сначала: снимается самое длинное подходящее.
var (
	perfectiveGerund1 = []string{"вшись", "вши", "в"} // после а или я
	perfectiveGerund2 = []string{"ывшись", "ившись", "ывши", "ивши", "ыв", "ив"}

	adjective = []string{"ыми", "ими", "его", "ого", "ему", "ому", "ее", "ие", "ые", "ое",
		"ей", "ий", "ый", "ой", "ем", "им", "ым", "ом", "их", "ых", "ую", "юю",
		"ая", "яя", "ою", "ею"}

	// Суффиксы причастий первой группы (после «а» или «я») не применяются
	// по той же причине, что и глагольные окончания той же группы: «данные»
	// теряло «нн» и превращалось в «да», сливаясь с частицей. Вторая группа
	// однозначна и остаётся.
	participle2 = []string{"ующ", "ивш", "ывш"}

	reflexive = []string{"ся", "сь"}

	// Глагольные окончания первой группы (те, что снимаются после «а» или «я»)
	// намеренно НЕ применяются — это осознанное отступление от Snowball.
	//
	// Формально «л» после «а» — окончание прошедшего времени, и алгоритм честно
	// превращает «канал» в «кана». Но «каналы» — существительное, у него
	// снимается «ы», получается «канал», и запрос «канал» перестаёт находить
	// «каналы». То же самое с «экран», «сигнал», «материал», «модуль»,
	// «память» — это костяк технических текстов. Прошедшее время в книгах
	// ищут несравнимо реже, поэтому размен в пользу существительных.
	//
	// Осечка обратной стороны — «читать» и «читал» останутся разными термами.
	verb2 = []string{"уйте", "ейте", "ила", "ыла", "ена", "ите", "или", "ыли", "ило", "ыло",
		"ено", "ует", "уют", "ены", "ить", "ыть", "ишь", "ей", "уй", "ил", "ыл",
		"им", "ым", "ен", "ят", "ит", "ыт", "ую", "ю"}

	noun = []string{"иями", "ями", "ами", "иях", "ях", "ах", "ией", "иям", "ием", "ии",
		"ие", "ье", "ев", "ов", "еи", "ей", "ой", "ий", "ям", "ем", "ам", "ом",
		"ию", "ью", "ия", "ья", "а", "е", "и", "й", "о", "у", "ы", "ь", "ю", "я"}

	superlative  = []string{"ейше", "ейш"}
	derivational = []string{"ость", "ост"}
)

// stemRussian возвращает основу слова.
func stemRussian(word string) string {
	w := []rune(word)
	rv, r2 := regionsRU(w)

	// Шаг 1: деепричастие, иначе возвратная частица и затем прилагательное,
	// глагол или существительное.
	if cut, ok := removeGroup(w, rv, perfectiveGerund2, perfectiveGerund1); ok {
		w = cut
	} else {
		if cut, ok := removeAny(w, rv, reflexive); ok {
			w = cut
		}
		switch {
		case tryAdjectival(&w, rv):
		case tryGroup(&w, rv, verb2, nil):
		default:
			if cut, ok := removeAny(w, rv, noun); ok {
				w = cut
			}
		}
	}

	// Шаг 2: снять «и».
	if cut, ok := removeAny(w, rv, []string{"и"}); ok {
		w = cut
	}

	// Шаг 3: словообразовательный суффикс, но только в R2.
	if cut, ok := removeAny(w, r2, derivational); ok {
		w = cut
	}

	// Шаг 4: удвоенное «н», превосходная степень, мягкий знак.
	switch {
	case strings.HasSuffix(string(w), "нн"):
		w = w[:len(w)-1]
	default:
		if cut, ok := removeAny(w, rv, superlative); ok {
			w = cut
			if strings.HasSuffix(string(w), "нн") {
				w = w[:len(w)-1]
			}
		}
	}
	if len(w) > 0 && w[len(w)-1] == 'ь' {
		w = w[:len(w)-1]
	}
	return string(w)
}

// regionsRU вычисляет начала областей RV и R2.
func regionsRU(w []rune) (rv, r2 int) {
	rv, r2 = len(w), len(w)
	first := -1
	for i, r := range w {
		if vowelsRU[r] {
			first = i
			break
		}
	}
	if first < 0 {
		return rv, r2
	}
	rv = first + 1

	// R1: после первой согласной, идущей за гласной.
	r1 := len(w)
	for i := first + 1; i < len(w); i++ {
		if !vowelsRU[w[i]] && vowelsRU[w[i-1]] {
			r1 = i + 1
			break
		}
	}
	// R2: то же правило внутри R1.
	for i := r1 + 1; i < len(w); i++ {
		if !vowelsRU[w[i]] && vowelsRU[w[i-1]] {
			r2 = i + 1
			break
		}
	}
	return rv, r2
}

// removeAny снимает самое длинное подходящее окончание, если оно целиком
// лежит в области, начинающейся с from.
func removeAny(w []rune, from int, endings []string) ([]rune, bool) {
	if from > len(w) {
		return w, false
	}
	best := -1
	for _, e := range endings {
		n := len([]rune(e))
		if n > len(w)-from {
			continue
		}
		if string(w[len(w)-n:]) == e && n > best {
			best = n
		}
	}
	if best <= 0 {
		return w, false
	}
	return w[:len(w)-best], true
}

// removeGroup снимает окончание из второй группы, а из первой — только если
// перед ним стоит «а» или «я»: так различаются «читав» и «игравши».
func removeGroup(w []rune, from int, group2, group1 []string) ([]rune, bool) {
	if cut, ok := removeAny(w, from, group2); ok {
		return cut, true
	}
	for _, e := range group1 {
		n := len([]rune(e))
		if n > len(w)-from {
			continue
		}
		head := w[:len(w)-n]
		if string(w[len(w)-n:]) == e && len(head) > 0 {
			if last := head[len(head)-1]; last == 'а' || last == 'я' {
				return head, true
			}
		}
	}
	return w, false
}

func tryGroup(w *[]rune, from int, group2, group1 []string) bool {
	if cut, ok := removeGroup(*w, from, group2, group1); ok {
		*w = cut
		return true
	}
	return false
}

// tryAdjectival снимает окончание прилагательного, а за ним — суффикс причастия,
// если он там оказался: «читающего» → «чита».
func tryAdjectival(w *[]rune, from int) bool {
	cut, ok := removeAny(*w, from, adjective)
	if !ok {
		return false
	}
	*w = cut
	if cut, ok := removeGroup(*w, from, participle2, nil); ok {
		*w = cut
	}
	return true
}
