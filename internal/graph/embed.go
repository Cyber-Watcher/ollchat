package graph

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Счёт векторов понятий.
//
// Считается не одно имя, а имя вместе с синонимами: «RAG» и «retrieval
// augmented generation» — одно понятие, и вопрос может прийти в любой из этих
// форм. Тип понятия («технология», «понятие») в текст не идёт: он одинаков
// у тысяч понятий и только размывает вектор.

// EmbedOpts — как считать векторы понятий.
type EmbedOpts struct {
	Batch   int // сколько имён отдавать за один запрос; 0 — 64
	Workers int // сколько запросов держать одновременно; 0 — 4

	// Recount — пересчитать всё заново, а не только новые понятия.
	//
	// Нужен, когда прежние векторы негодны: сменился эмбеддер или у старых
	// понятий прибавились синонимы. В обычной работе выгоднее досчёт: граф
	// растёт заходами, и пересчитывать при каждом все 63 тысячи — двадцать
	// минут карты на две минуты новой работы.
	Recount bool
}

func (o EmbedOpts) norm() EmbedOpts {
	if o.Batch <= 0 {
		o.Batch = 64
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	return o
}

// EmbedProgress — ход счёта.
type EmbedProgress struct {
	Done  int
	Total int
}

// embedText собирает текст, который пойдёт в эмбеддер.
//
// Синонимы приписываются через запятую: так вектор понятия оказывается разом
// и в русском, и в английском углу пространства, а это ровно то, ради чего
// смысловой вход и заводится.
//
// **Порядок важнее числа.** Первая редакция брала синонимы подряд, как они
// лежат в записи, и обрезала хвост. Замер 03.09.2026: в векторе `Guard` от этого
// стояло слово «защита», а в векторе `Knowledge base` — «база данных» и «внешний
// источник», то есть смысловой вход уводил вопрос про защиту от внедрения
// в `Guard`, а вопрос про базу данных — в `Knowledge base`. Поэтому синонимы
// приходят уже проверенными и упорядоченными (Entities.SafeAliases):
// без чужих собственных имён, переводы впереди.
// limit — сколько частей (имя плюс синонимы) уходит в вектор (Rules.VectorAliases).
func embedText(e Entity, aliases []string, limit int) string {
	parts := []string{e.Name}
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a == "" || strings.EqualFold(a, e.Name) {
			continue
		}
		parts = append(parts, a)
		if len(parts) >= limit { // длинный хвост размывает вектор
			break
		}
	}
	return strings.Join(parts, ", ")
}

// EmbedTextOf — текст, который уходит эмбеддеру за это понятие: имя и до
// VectorAliases частей из безопасных синонимов. Наружу — ради замеров:
// сравнивать векторы, посчитанные в разное время, честно только по тому же
// тексту, что строит сам счёт, а не по его пересказу в скрипте.
func (g *Graph) EmbedTextOf(e Entity) string {
	if g == nil || g.ents == nil {
		return ""
	}
	return embedText(e, g.ents.SafeAliases(e), g.rules.VectorAliases)
}

// EmbedEntities считает векторы всех понятий графа и кладёт их рядом с ним.
//
// Считается всё разом, а не докатывается: понятий десятки тысяч против сотен
// тысяч кусков коллекции, счёт занимает минуты, и городить докатку ради минут
// не стоит. Граф при этом растёт, и после каждой докатки графа векторы надо
// пересчитать — команда об этом говорит.
func (g *Graph) EmbedEntities(ctx context.Context, emb kb.Embedder, o EmbedOpts,
	onProgress func(EmbedProgress)) error {

	if emb == nil {
		return fmt.Errorf("эмбеддер не задан")
	}
	o = o.norm()

	// Один счёт векторов на граф: второй писатель — отказ, а не гонка файлов.
	release, err := lockVectors(g.dir)
	if err != nil {
		return err
	}
	defer release()

	texts, err := g.embedTexts()
	if err != nil {
		return err
	}

	// Отпечаток снимается до счёта: узнать о чужих весах после того, как
	// хвост посчитан, значит выбросить его.
	digest := embedderDigest(ctx, emb)

	// Досчёт: уже посчитанное берём как есть, считаем только хвост.
	//
	// Понятия нумеруются подряд, и вектор понятия N лежит на месте N-1, поэтому
	// «хвост» — это ровно те понятия, чей номер больше прошлого счёта. Понятия,
	// у которых с тех пор прибавились синонимы, сохранят прежний вектор: он чуть
	// беднее нового, но верен, а пересчёт ради этого стоил бы всей работы.
	var (
		kept    []int8
		already int
	)
	if !o.Recount {
		kept, already = g.vecs.Existing(emb.Model(), 0)
		if already > len(texts) {
			already = len(texts) // граф ужался: досчитывать нечего, считаем заново
			kept = nil
		}
		// Досчёт хвостом на другом сервере — тот самый случай, когда одно имя
		// модели указывает на разные веса. Догонщик (vecfollow.go) сверяет
		// отпечаток перед каждой пачкой; обычный досчёт обязан делать то же,
		// иначе он не только смешает пространства, но и запишет в паспорт
		// новый отпечаток, стерев след смешения.
		if already > 0 {
			if err := checkDigest(g.vecs.Digest(), digest, emb.Model()); err != nil {
				return err
			}
		}
	}
	if already > 0 {
		texts = texts[already:]
		if len(texts) == 0 {
			return nil // всё уже посчитано
		}
	}

	dim, data, err := embedBatches(ctx, emb, texts, o, onProgress)
	if err != nil {
		return err
	}
	if already > 0 {
		if len(kept) != already*dim {
			// Размерность прежних векторов не та — склеивать нельзя.
			return fmt.Errorf("прежние векторы не той размерности: %d на %d понятий",
				len(kept), already)
		}
		data = append(kept, data...)
	}
	return g.SaveEntityVectors(emb.Model(), digest, dim, data)
}

// embedTexts собирает тексты понятий по местам: место N-1 — понятие с номером N.
func (g *Graph) embedTexts() ([]string, error) {
	all := g.ents.All()
	if len(all) == 0 {
		return nil, fmt.Errorf("в графе нет понятий")
	}
	// Номера идут подряд с единицы, но на всякий случай ищем наибольший:
	// место в файле определяется номером, а не порядком в списке.
	maxID := uint32(0)
	for _, e := range all {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	texts := make([]string, maxID)
	for _, e := range all {
		if e.ID >= 1 && e.ID <= maxID {
			texts[e.ID-1] = embedText(e, g.ents.SafeAliases(e), g.rules.VectorAliases)
		}
	}
	return texts, nil
}

// embedderDigest снимает отпечаток весов эмбеддера, если сервер его отдаёт.
//
// Ошибку глотаем намеренно: отпечаток — проверка, а не условие работы, и её
// отсутствие не повод отказываться считать. Пустое значение просто выключает
// сверку — так же, как у пула узлов сборки.
func embedderDigest(ctx context.Context, emb kb.Embedder) string {
	st, ok := emb.(interface {
		Stamp(context.Context) (string, error)
	})
	if !ok {
		return ""
	}
	d, err := st.Stamp(ctx)
	if err != nil {
		return ""
	}
	return d
}

// embedBatches считает векторы для всех текстов и возвращает их подряд.
func embedBatches(ctx context.Context, emb kb.Embedder, texts []string, o EmbedOpts,
	onProgress func(EmbedProgress)) (int, []int8, error) {

	// Пустое имя эмбеддер отвергнет, а место в файле занять обязано:
	// подставляем пробел, вектор такого понятия всё равно ни с чем не совпадёт.
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			texts[i] = " "
		}
	}

	// Пачки считаются одновременно: упор здесь не в счёт, а в дорогу до сервера
	// и обратно, ровно как при счёте векторов кусков. Место каждой пачки
	// в итоговом массиве известно заранее по её номеру, поэтому порядок ответов
	// значения не имеет и сшивать ничего не надо.
	type job struct{ from, to int }
	var jobs []job
	for from := 0; from < len(texts); from += o.Batch {
		to := from + o.Batch
		if to > len(texts) {
			to = len(texts)
		}
		jobs = append(jobs, job{from, to})
	}

	parts := make([][][]float32, len(jobs))
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		first error
		done  int
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	queue := make(chan int)
	for w := 0; w < o.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					return
				}
				j := jobs[i]
				vecs, err := emb.Embed(ctx, texts[j.from:j.to])
				mu.Lock()
				switch {
				case err != nil:
					if first == nil {
						first = fmt.Errorf("счёт векторов понятий %d-%d: %w", j.from+1, j.to, err)
						cancel()
					}
				case len(vecs) != j.to-j.from:
					if first == nil {
						first = fmt.Errorf("сервер вернул %d векторов на %d имён", len(vecs), j.to-j.from)
						cancel()
					}
				default:
					parts[i] = vecs
					done += j.to - j.from
					if onProgress != nil {
						onProgress(EmbedProgress{Done: done, Total: len(texts)})
					}
				}
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		select {
		case queue <- i:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	if first != nil {
		return 0, nil, first
	}

	var dim int
	var data []int8
	for _, vecs := range parts {
		for _, v := range vecs {
			if dim == 0 {
				dim = len(v)
			}
			if len(v) != dim {
				return 0, nil, fmt.Errorf("размерность векторов разъехалась: %d против %d", len(v), dim)
			}
			data = append(data, kb.Quantize(v)...)
		}
	}
	if dim == 0 {
		return 0, nil, fmt.Errorf("сервер не вернул ни одного вектора")
	}
	if len(data)/dim != len(texts) {
		return 0, nil, fmt.Errorf("посчитано %d векторов на %d понятий", len(data)/dim, len(texts))
	}
	return dim, data, nil
}
