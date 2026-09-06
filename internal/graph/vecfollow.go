package graph

import (
	"context"
	"fmt"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Досчёт векторов понятий, появившихся после прошлого счёта.
//
// **Зачем отдельно от EmbedEntities.** Та считает всё и переписывает файл
// целиком — это правильно для захода «раз в неделю после докатки». Но векторы
// отстают от графа молча: замер 06.09.2026 — вектор был у 174 233 понятий
// из 195 338 (89%), и темы, собранные из свежих понятий, в смысловые меры
// не попадали вовсе. Отставание убирается не тем, что команду не забудут
// отдать, а тем, что счёт идёт по мере появления понятий.
//
// **Очередь заводить не надо — она уже есть.** Это номера от `Count + 1`
// паспорта векторов до числа понятий в реестре. Ни файла очереди, ни заботы
// о переживании перезапуска: состояние выводится из двух чисел, которые и так
// лежат на диске. Отсюда же берётся и устойчивость к обрывам — упавший
// догонщик, запущенный заново, продолжит ровно с того места.
//
// **Работа не для той карты.** Извлечение занимает A100 неделями, и досчёт
// векторов встаёт в ту же очередь, хотя нужен ему эмбеддер на 567 млн
// параметров. Отдельный узел (`--graph-embed-node`) вынимает эту работу
// из очереди целиком, а не делит её.

// EmbedNewResult — что дал один заход досчёта.
type EmbedNewResult struct {
	Before int // сколько понятий было покрыто до захода
	Added  int // сколько досчитано
	Total  int // сколько понятий в графе сейчас
	Dim    int
	Model  string
}

// Pending — сколько понятий ждут вектора.
func (r EmbedNewResult) Pending() int { return max(r.Total-r.Before-r.Added, 0) }

// EmbedNewEntities считает векторы понятий, появившихся после прошлого счёта,
// и **дописывает** их в хвост файла, не переписывая посчитанное.
//
// limit ограничивает число понятий за один заход; 0 — без предела. Предел
// нужен догонщику: заход обязан кончаться за обозримое время, чтобы отпечаток
// весов сверялся не раз в сутки, а каждую пачку, и чтобы остановка не теряла
// весь счёт.
//
// Возвращает EmbedNewResult даже при ошибке в середине: досчитанное до сбоя
// уже на диске, и знать об этом вызывающему надо.
func (g *Graph) EmbedNewEntities(ctx context.Context, emb kb.Embedder, o EmbedOpts, limit int,
	onProgress func(EmbedProgress)) (EmbedNewResult, error) {

	if emb == nil {
		return EmbedNewResult{}, fmt.Errorf("эмбеддер не задан")
	}
	o = o.norm()

	// Один счёт векторов на граф: догонщик рядом с --graph-embed или второй
	// догонщик — отказ, а не два писателя в одном файле.
	release, err := lockVectors(g.dir)
	if err != nil {
		return EmbedNewResult{}, err
	}
	defer release()

	texts, err := g.embedTexts()
	if err != nil {
		return EmbedNewResult{}, err
	}
	res := EmbedNewResult{Total: len(texts), Model: emb.Model()}

	// Прежние векторы нужны только числом: дозапись не склеивает массивы,
	// а пишет в хвост файла, и держать в памяти лишние 178 МБ незачем.
	if info := g.VectorsInfo(); info.Ready {
		if info.Model != emb.Model() {
			return res, fmt.Errorf("векторы посчитаны моделью %q, а досчитывать нечем, кроме %q: "+
				"нужен полный пересчёт (--graph-embed --graph-embed-recount)", info.Model, emb.Model())
		}
		res.Before, res.Dim = info.Count, info.Dim
	}
	if res.Before > len(texts) {
		return res, fmt.Errorf("векторов посчитано больше, чем понятий в графе (%d против %d): "+
			"граф пересобирали — нужен полный пересчёт", res.Before, len(texts))
	}

	// Отпечаток снимается **до** счёта: узнать, что веса другие, после того как
	// пачка посчитана, значит выбросить её.
	digest := embedderDigest(ctx, emb)
	if info := g.VectorsInfo(); info.Ready {
		if err := checkDigest(info.Digest, digest, emb.Model()); err != nil {
			return res, err
		}
	}

	tail := texts[res.Before:]
	if len(tail) == 0 {
		return res, nil
	}
	if limit > 0 && len(tail) > limit {
		tail = tail[:limit]
	}

	dim, data, err := embedBatches(ctx, emb, tail, o, onProgress)
	if err != nil {
		return res, err
	}
	if res.Dim > 0 && dim != res.Dim {
		return res, fmt.Errorf("размерность векторов %d, а посчитано %d", res.Dim, dim)
	}
	if err := g.AppendEntityVectors(emb.Model(), digest, dim, data); err != nil {
		return res, err
	}
	res.Added, res.Dim = len(tail), dim
	return res, nil
}
