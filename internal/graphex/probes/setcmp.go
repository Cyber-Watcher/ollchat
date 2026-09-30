// setcmp.go — общее ядро сравнения двух прогонов извлечения: множество
// понятий и множество троек (src, dst, тип). Вынесено из seqcheck и detcheck
// (этап 114, пункт Г7): там сравнивается разный материал — голый ответ модели
// дважды подряд против двух уже собранных графов, — но сама проверка «что
// есть в одном наборе и нет в другом» одна: жадное сопоставление по общему
// написанию.
package probes

// NameSet — одно понятие как набор написаний: нормализованное имя и, если
// они есть, синонимы (aliases графа). У seqcheck (сравнение двух голых
// ответов модели, синонимов ещё нет) в наборе всегда одно написание.
type NameSet map[string]bool

// overlaps — есть ли у двух наборов общее написание.
func overlaps(a, b NameSet) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// first — любое написание набора, для сообщения о расхождении. Не
// детерминировано порядком карты — этого достаточно, чтобы прочитать глазами,
// какое понятие имеется в виду.
func (a NameSet) first() string {
	for k := range a {
		return k
	}
	return "?"
}

// MatchNames — жадное сопоставление двух списков понятий по общему написанию:
// каждому элементу a ищется первый ещё не занятый элемент b с общим
// написанием. Возвращает число сопоставленных пар и индексы непарных с обеих
// сторон — их и показывают как расхождение.
func MatchNames(a, b []NameSet) (matched int, onlyA, onlyB []int) {
	usedB := make([]bool, len(b))
	for i := range a {
		found := false
		for j := range b {
			if !usedB[j] && overlaps(a[i], b[j]) {
				usedB[j] = true
				found = true
				break
			}
		}
		if found {
			matched++
		} else {
			onlyA = append(onlyA, i)
		}
	}
	for j := range usedB {
		if !usedB[j] {
			onlyB = append(onlyB, j)
		}
	}
	return matched, onlyA, onlyB
}

// EdgeSet — одна связь: наборы написаний двух концов и тип. Тип сравнивается
// точно строкой, концы — по overlaps, как понятия.
type EdgeSet struct {
	Src, Dst NameSet
	Type     string
}

// MatchEdges — то же сопоставление, но для связей. allowSwap разрешает
// засчитать совпадением и обратный порядок концов (src↔dst): так сравнивает
// detcheck, где кусок разбирается моделью заново и порядок концов одной и той
// же связи может отличаться между прогонами; seqcheck сравнивает сырые тройки
// без перестановки — там allowSwap ложно.
func MatchEdges(a, b []EdgeSet, allowSwap bool) (matched int, onlyA, onlyB []int) {
	usedB := make([]bool, len(b))
	same := func(x, y EdgeSet) bool {
		if x.Type != y.Type {
			return false
		}
		if overlaps(x.Src, y.Src) && overlaps(x.Dst, y.Dst) {
			return true
		}
		return allowSwap && overlaps(x.Src, y.Dst) && overlaps(x.Dst, y.Src)
	}
	for i := range a {
		found := false
		for j := range b {
			if !usedB[j] && same(a[i], b[j]) {
				usedB[j] = true
				found = true
				break
			}
		}
		if found {
			matched++
		} else {
			onlyA = append(onlyA, i)
		}
	}
	for j := range usedB {
		if !usedB[j] {
			onlyB = append(onlyB, j)
		}
	}
	return matched, onlyA, onlyB
}
