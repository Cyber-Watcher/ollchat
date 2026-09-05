package kb

import (
	"context"
	"sort"
)

// Переранжирование выдачи вторым этапом.
//
// **Зачем.** Первый этап сравнивает вопрос и кусок порознь: у каждого свой
// вектор, считается угол между ними. Это позволяет перебрать 300 тысяч кусков
// за десятки миллисекунд, но модель при этом ни разу не видела вопрос и кусок
// рядом. Кросс-энкодер читает их вместе одним текстом — точнее, но настолько
// дороже, что годится только для верхушки.
//
// Книги описывают ровно это: «A cross-encoder reranker takes the query and each
// candidate document, concatenates them, and runs a transformer model over the
// joint pair» (Agentic RAG Systems, 2026, стр. 104).
//
// **Что он чинит у нас.** Замер 26.08.2026 на 457 вопросах: слияние находит
// нужный кусок не хуже прочих режимов, но ставит его в среднем на 2.6 место.
// Переранжирование бьёт именно в это.
//
// **Чего не чинит.** Оно только переставляет то, что нашёл первый этап. Если
// нужного куска нет среди кандидатов, переставлять нечего: потолок выигрыша —
// это полнота на глубине отбора, а не что-то большее.

// Reranker — то, что умеет оценить, насколько кусок отвечает на вопрос.
//
// Интерфейс на голых типах по той же причине, что и Embedder: пакет kb
// не должен знать, чем именно считается оценка. Сегодня это llama-server
// с bge-reranker, завтра может быть что угодно.
type Reranker interface {
	// Rerank возвращает оценки в том же порядке, что и документы.
	// Больше — уместнее. Шкала своя у каждой модели и у каждого вопроса,
	// поэтому значения годятся только для сравнения внутри одного вызова.
	Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
	Model() string
}

// RerankOpts — как переранжировать.
type RerankOpts struct {
	// Candidates — сколько кусков брать из первого этапа. 0 — двадцать.
	//
	// Чем больше, тем выше потолок и тем дороже каждый поиск: замер
	// 26.08.2026 — 40 кусков по 1200 знаков идут 1.7 с, то есть около
	// 42 мс на пару.
	Candidates int

	// Snippet — подавать выдержку вокруг совпадения вместо куска целиком.
	//
	// Кросс-энкодер читает пару целиком, и на куске в 1200 знаков половина
	// времени уходит на текст, к вопросу отношения не имеющий. Выдержка
	// дешевле, но может отрезать то, что решает. Что лучше — вопрос замера,
	// а не рассуждения.
	Snippet bool
}

func (o RerankOpts) Norm() RerankOpts {
	if o.Candidates <= 0 {
		o.Candidates = 20
	}
	return o
}

// Rerank переставляет выдачу по оценкам кросс-энкодера.
//
// Возвращает столько же результатов, сколько просили в SearchOpts.TopK, но
// выбранных из более широкой верхушки. Ошибка переранжирования не ошибка
// поиска: выдача первого этапа остаётся как есть, и об этом сообщается
// вторым значением.
func Rerank(ctx context.Context, rr Reranker, query string, hits []Result,
	topK int, o RerankOpts) ([]Result, error) {

	o = o.Norm()
	if rr == nil || len(hits) == 0 {
		return hits, nil
	}
	if topK <= 0 {
		topK = len(hits)
	}

	cand := hits
	if len(cand) > o.Candidates {
		cand = cand[:o.Candidates]
	}

	docs := make([]string, len(cand))
	for i, h := range cand {
		if o.Snippet && h.Snippet != "" {
			docs[i] = h.Snippet
		} else {
			docs[i] = h.Text
		}
	}

	scores, err := rr.Rerank(ctx, query, docs)
	if err != nil {
		return hits, err
	}
	if len(scores) != len(cand) {
		// Служба вернула не то число оценок — доверять такому нельзя,
		// но и ронять поиск незачем.
		return hits, errScoreCount{want: len(cand), got: len(scores)}
	}

	type scored struct {
		res   Result
		score float64
		pos   int // место в исходной выдаче: нужно для устойчивости
	}
	list := make([]scored, len(cand))
	for i := range cand {
		list[i] = scored{res: cand[i], score: scores[i], pos: i}
	}
	sort.SliceStable(list, func(a, b int) bool {
		if list[a].score != list[b].score {
			return list[a].score > list[b].score
		}
		return list[a].pos < list[b].pos
	})

	out := make([]Result, 0, topK)
	for _, s := range list {
		if len(out) >= topK {
			break
		}
		r := s.res
		r.Score = s.score // показываем оценку второго этапа: она и решила порядок
		out = append(out, r)
	}
	// Хвост, не попавший в кандидаты, дополняет выдачу, если её не хватило.
	for i := len(cand); i < len(hits) && len(out) < topK; i++ {
		out = append(out, hits[i])
	}
	return out, nil
}

// errScoreCount — служба вернула не столько оценок, сколько документов.
type errScoreCount struct{ want, got int }

func (e errScoreCount) Error() string {
	return "переранжирование: оценок " + itoa(e.got) + " на " + itoa(e.want) + " кусков"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
