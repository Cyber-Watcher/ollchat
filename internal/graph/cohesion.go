package graph

import "sort"

// Cohesion — насколько тема замкнута на себя: доля связей, которые не выходят
// за её пределы.
type Cohesion struct {
	ID      int
	Members int
	Inside  int     // связей внутри темы
	Outside int     // связей наружу
	Share   float64 // Inside / (Inside + Outside)
}

// LowCohesion возвращает темы нулевого уровня с самой низкой связностью.
//
// Зачем. Разбиение Louvain режет граф на части и тогда, когда резать нечего:
// понятия, попавшие в одну часть только потому, что все они упоминались рядом
// со словом «ИИ», выглядят темой ровно так же, как настоящая тема. Модель,
// которой показывают такой список, охотно придумывает ему связное название —
// замер 26.08.2026: `glm-4.7-flash` описала кластер с «белым медведем»
// и «скрапбукингом» как «Искусственный интеллект и тестирование» с оценкой 8
// из 10, тогда как `qwen3.8` на том же входе честно написала «бессвязный набор»
// и поставила 2.
//
// Связность отличает такой кластер: у него связи чаще уходят наружу, чем
// остаются внутри. Сама по себе она не приговор — низкая связность бывает
// и у настоящих тем-концентраторов вроде «Docker и контейнеризация», которые
// связаны со всем графом. Поэтому отбор служит не заменой модели, а сужением
// круга: подозреваемых мало, и их можно передоописать честной моделью, не
// пересчитывая все тысячи тем.
//
// minMembers отсекает мелочь: у темы из трёх понятий доля связей ничего
// не значит. limit — сколько вернуть, 0 — все подходящие.
func (g *Graph) LowCohesion(c *Communities, minMembers, limit int) []Cohesion {
	if c == nil {
		return nil
	}
	// Понятие → тема. Только нулевой уровень: у объединений в Members лежат
	// номера тем, а не понятий, и связи по ним не ищутся.
	owner := make(map[uint32]int)
	for _, com := range c.List {
		if com.Level != 0 {
			continue
		}
		for _, m := range com.Members {
			owner[m] = com.ID
		}
	}

	inside := make(map[int]int)
	outside := make(map[int]int)
	for member, id := range owner {
		for _, ed := range g.Edges().Of(member) {
			if owner[ed.Dst] == id {
				inside[id]++
			} else {
				outside[id]++
			}
		}
	}

	var out []Cohesion
	for _, com := range c.List {
		if com.Level != 0 || len(com.Members) < minMembers {
			continue
		}
		in, ex := inside[com.ID], outside[com.ID]
		if in+ex == 0 {
			continue
		}
		out = append(out, Cohesion{
			ID:      com.ID,
			Members: len(com.Members),
			Inside:  in,
			Outside: ex,
			Share:   float64(in) / float64(in+ex),
		})
	}
	// По возрастанию связности, при равенстве — крупные вперёд: в большой
	// теме цена ошибки выше.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Share != out[j].Share {
			return out[i].Share < out[j].Share
		}
		return out[i].Members > out[j].Members
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ForgetSummary снимает с темы описание, чтобы её описали заново.
func (c *Communities) ForgetSummary(id int) bool {
	for i := range c.List {
		if c.List[i].ID == id {
			c.List[i].Title = ""
			c.List[i].Summary = ""
			c.List[i].Key = nil
			c.List[i].Rating = 0
			c.List[i].Why = ""
			return true
		}
	}
	return false
}
