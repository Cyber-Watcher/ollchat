package kb

import (
	"fmt"
	"math/rand"
	"strings"
)

// Слепая выборка кусков коллекции.
//
// **Зачем слепая.** Куски для замерного набора нельзя отбирать поиском: набор
// тогда подсунет ровно то, что поиск и так находит, и замер подтвердит сам себя.
// Здесь равномерная выборка по всей коллекции, безо всякого отношения к тому,
// как эти куски ищутся.
//
// **Зачем зерно.** Замер обязан повторяться. С тем же зерном выборка та же,
// и разницу между двумя прогонами можно приписать правке, а не случайности.
// Поэтому зерно задаётся снаружи и печатается в набор.
//
// Метод резервуара: коллекция обходится один раз, память не зависит от её
// размера. На 300 тысячах кусков это секунды и десятки мегабайт.

// SampleOpts — как отбирать.
type SampleOpts struct {
	N        int   // сколько кусков нужно
	Seed     int64 // зерно случайности; замер должен повторяться
	MinWords int   // короче не брать: по обрывку не составить вопрос; 0 — 90
	SkipCode bool  // не брать куски, помеченные кодом
}

func (o SampleOpts) norm() SampleOpts {
	if o.N <= 0 {
		o.N = 60
	}
	if o.MinWords <= 0 {
		o.MinWords = 90
	}
	return o
}

// Sample — отобранный кусок вместе со всем, что о нём известно.
type Sample struct {
	ID    string `json:"id"`
	Book  string `json:"book"`
	Page  int    `json:"page"`
	Year  int    `json:"year"`
	Words int    `json:"words"`
	Text  string `json:"text"`
}

// SampleChunks отбирает случайные куски коллекции.
//
// Возвращает не больше N: если после отсева коротких и кодовых осталось меньше,
// столько и вернётся — молча добирать похожими значило бы нарушить слепоту.
func (c *Collection) SampleChunks(o SampleOpts) ([]Sample, error) {
	o = o.norm()
	rnd := rand.New(rand.NewSource(o.Seed))

	// Резервуар берём с запасом: часть кандидатов отсеется по длине и по коду,
	// и без запаса выборка окажется меньше заказанной.
	pool := make([]ChunkRef, 0, o.N*4)
	seen := 0
	err := c.EachChunkRef(ChunkFilter{}, func(r ChunkRef) error {
		seen++
		if len(pool) < cap(pool) {
			pool = append(pool, r)
			return nil
		}
		if j := rnd.Intn(seen); j < len(pool) {
			pool[j] = r
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]Sample, 0, o.N)
	for _, r := range pool {
		if len(out) >= o.N {
			break
		}
		id := fmt.Sprintf("%s/%d#%d", c.Name(), r.Doc, r.Ord)
		around, err := c.Around(id, 0)
		if err != nil || len(around) == 0 {
			continue
		}
		text := strings.TrimSpace(around[0].Text)
		words := len(strings.Fields(text))
		if words < o.MinWords {
			continue
		}
		if o.SkipCode && around[0].Code {
			continue // вопрос по голому коду выходит про синтаксис, а не про смысл
		}
		out = append(out, Sample{
			ID: id, Book: around[0].Book, Page: around[0].UnitFrom,
			Year: around[0].Year, Words: words, Text: text,
		})
	}
	return out, nil
}
