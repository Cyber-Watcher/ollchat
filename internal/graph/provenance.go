package graph

import ()

// Целостность провенанса: у каждой ли связи есть живой кусок-источник.
//
// **Зачем такая проверка.** «Agentic RAG Systems» (Norman, 2026, стр. 129)
// перечисляет метрики, по которым за графом надо следить в работе, и среди
// них provenance integrity — «как часто у связей есть годные куски-источники».
// Причина там же: граф, который тихо портится, «даёт поиск, постепенно
// становящийся неверным, причём ничто не выглядит поломанным».
//
// Доктор до 16.09.2026 считал ЧИСЛО подтверждений у связи, но не смотрел,
// существует ли кусок, на который они указывают. Разница важная: число
// подтверждений остаётся прежним и когда выдержку уже не показать.
//
// **Почему выборка, а не все связи.** Связей 1,6 миллиона, и у каждой надо
// прочитать кусок из хранилища. Замер 16.09.2026: три тысячи связей — секунды,
// и этого хватает, чтобы заметить беду (одна битая ссылка на тысячу дала бы
// в выборке три).
//
// **Почему проверяются и синонимы.** Граф двуязычный, и модель извлечения
// законно называет понятие написанием, которого в этом куске нет («горутина»
// против «goroutine»). Первый прогон замера искал только имена и дал 6,73%
// связей «не видно в куске»; с синонимами осталось 2,9%. Без синонимов
// проверка записывала бы законные связи в ошибки.

// ProvenanceReport — что вышло у проверки провенанса.
type ProvenanceReport struct {
	Checked int // сколько связей проверено
	Missing int // кусок-источник не найден вовсе — настоящая беда
	Dropped int // книга отброшена из выдачи: это норма, не ошибка
	Both    int // оба имени связи встречаются в куске
	One     int // только одно имя
	None    int // ни одного имени: вероятная ошибка извлечения

	// Examples — по нескольку случаев каждой беды, чтобы человек посмотрел
	// глазами, а не верил доле на слово.
	Examples []string
}

// Provenance проверяет на выборке связей, что кусок-источник существует
// и что имена связи в нём действительно встречаются.
//
// src — откуда читать куски (коллекция базы знаний). sample — сколько связей
// взять; seed — зерно выборки, чтобы повтор давал те же числа.
func (g *Graph) Provenance(src Chunks, sample int, seed int64) ProvenanceReport {
	var rep ProvenanceReport
	if g == nil || src == nil || sample <= 0 {
		return rep
	}
	// Выборка равномерна ПО ЗАПИСЯМ СВЯЗЕЙ. Прежняя — «случайное понятие,
	// затем случайная его связь» — брала связь с вероятностью, обратной степени
	// понятия, а у нас 6,8% понятий держат 61,6% связей: доли выходили
	// по понятиям, а подписаны были как доли связей (аудит 17.09.2026, S9).
	// Заодно выборка стала воспроизводимой: прежняя при одном зерне давала
	// 79% и 78% на двух запусках подряд из-за порядка обхода поглощённых.
	for _, ed := range g.SampleEdges(sample, seed) {
		e, ok := g.ents.Get(ed.Src)
		if !ok {
			continue
		}
		dst, ok := g.ents.Get(ed.Dst)
		if !ok {
			continue
		}
		rep.Checked++

		if g.dropped.Dropped(ed.Evidence.Doc) {
			rep.Dropped++
			continue
		}
		ci, ok := src.ChunkByRef(ed.Evidence.Doc, ed.Evidence.Ord)
		if !ok {
			rep.Missing++
			rep.addExample("нет куска " + ed.Evidence.String() + ": " + e.Name + " → " + dst.Name)
			continue
		}
		joined, hyphened := MatchText(ci.Text)
		a := g.nameSeen(joined, hyphened, e)
		b := g.nameSeen(joined, hyphened, dst)
		switch {
		case a && b:
			rep.Both++
		case a || b:
			rep.One++
		default:
			rep.None++
			rep.addExample("в куске " + ed.Evidence.String() + " нет ни одного имени: " +
				e.Name + " → " + dst.Name)
		}
	}
	return rep
}

// nameSeen — встречается ли понятие в тексте под своим именем или синонимом.
//
// Сверка идёт общей функцией SeenInText: фразой целиком, по границам слов,
// с поправкой на запись текста (переносы, лигатуры, невидимые знаки). Прежняя
// сверка подстрокой ошибалась в обе стороны: «Go» находилось внутри «google»,
// а «Abuse Existing Functionality» не находилось в «Func‐\ntionality»; замер
// 17.09.2026 на одной выборке в 3 000 связей: «ни одного имени» 3,27% → 2,67%.
//
// Имя короче трёх знаков («C», «R», «Go») сверяется только по границам слова
// и только как имя: короткие синонимы не проверяются вовсе.
func (g *Graph) nameSeen(joined, hyphened string, e Entity) bool {
	if SeenInText(joined, hyphened, e.Name) {
		return true
	}
	if n := MatchName(e.Name); len([]rune(n)) < 3 && containsWord(joined, n) {
		return true
	}
	for _, al := range g.ents.DisplayAliases(e) {
		if SeenInText(joined, hyphened, al) {
			return true
		}
	}
	return false
}

func (r *ProvenanceReport) addExample(s string) {
	if len(r.Examples) < 5 {
		r.Examples = append(r.Examples, s)
	}
}

// Bad — доля связей, у которых кусок-источник не найден. Именно она и есть
// беда: остальные разряды говорят о качестве извлечения, а не о целостности.
func (r ProvenanceReport) Bad() float64 {
	if r.Checked == 0 {
		return 0
	}
	return float64(r.Missing) / float64(r.Checked)
}
