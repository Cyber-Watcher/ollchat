package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

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

	// NodeWait — сколько ждать возвращения сервера при обрыве связи, прежде
	// чем остановить счёт; 0 — 15 минут, как у сборки графа.
	//
	// **Почему нужно.** 10.09.2026 в 17:59 полный пересчёт векторов books
	// (46 минут карты) упал на 115 тысячах понятий из 244: ssh-туннель к стенду
	// оборвался на семь секунд, сторож поднял его сам, а пересчёт уже вышел
	// с «connection refused». Обрыв дороги — не отказ сервера; ждать его
	// возвращения дешевле, чем считать заново.
	NodeWait time.Duration

	// Checkpoint — через сколько понятий фиксировать посчитанное на диске;
	// 0 — 4096, то есть около сорока секунд карты при 96 понятиях в секунду.
	//
	// **Почему нужно.** До 17.09.2026 счёт держал всё в памяти и писал один раз
	// в конце: обрыв посреди получасового счёта терял его целиком, а новые
	// понятия до следующей удачной докатки находились только точным написанием.
	// Теперь цена обрыва — одна порция, и повтор той же команды продолжает
	// с места: досчёт хвоста дописывает основной файл (vecappend.go), полный
	// пересчёт копит рядом в файлах `.part` (vecpart.go).
	Checkpoint int

	// OnWait зовётся перед каждым повтором после обрыва связи: что случилось,
	// сколько уже ждём и сколько готовы ждать. Зовётся из нескольких потоков.
	OnWait func(err error, waited, limit time.Duration)
}

// embedRetryEvery — как часто пробовать снова после обрыва. Переменная,
// а не постоянная, чтобы тест не ждал секунды.
var embedRetryEvery = 30 * time.Second

func (o EmbedOpts) norm() EmbedOpts {
	if o.Batch <= 0 {
		o.Batch = 64
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.NodeWait <= 0 {
		o.NodeWait = 15 * time.Minute
	}
	if o.Checkpoint <= 0 {
		o.Checkpoint = 4096
	}
	return o
}

// transientEmbedErr — обрыв дороги до сервера, а не отказ по существу:
// соединение отклонено, оборвано, истёк срок запроса.
//
// **Чем обрыв НЕ является** (аудит 17.09.2026, Б13). Любая ошибка HTTP-клиента
// Go завёрнута в `*url.Error`, а он сам удовлетворяет `net.Error` — поэтому
// прежняя проверка «это net.Error?» отвечала «да» на всё подряд: опечатку
// в адресе, неизвестную схему, негодный сертификат. Счёт на таких ошибках
// молча повторял запрос пятнадцать минут и выглядел зависшим. Обёртка
// снимается, и смотрим внутрь: имя, которого нет в DNS, — опечатка, а не обрыв;
// сеть, отказавшая на соединении, чтении или записи, — обрыв.
func transientEmbedErr(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return true
		}
		err = ue.Err
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		// Нет такого имени — так настроено; временный сбой DNS — дорога.
		return !de.IsNotFound && (de.IsTemporary || de.IsTimeout)
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Клиент Ollama отдаёт часть ошибок текстом, без обёрнутой причины.
	s := err.Error()
	return strings.Contains(s, "connection refused") || strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe")
}

// embedWithWait считает пачку, пережидая обрыв связи до o.NodeWait.
// О каждом ожидании сообщает o.OnWait: молчащий счёт неотличим от зависшего.
func embedWithWait(ctx context.Context, emb kb.Embedder, texts []string, o EmbedOpts) ([][]float32, error) {
	started := time.Now()
	for {
		vecs, err := emb.Embed(ctx, texts)
		if err == nil || !transientEmbedErr(err) || time.Since(started) >= o.NodeWait {
			return vecs, err
		}
		if o.OnWait != nil {
			o.OnWait(err, time.Since(started), o.NodeWait)
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(embedRetryEvery):
		}
	}
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
//
// **desc — описание понятия**, приписываемое отдельным предложением после
// синонимов; пусто у всех, кроме хабов, и подаётся сюда, только когда включено
// Rules.VectorDesc. Отдельным предложением, а не ещё одним синонимом через
// запятую, потому что это не написание понятия, а фраза о нём: смешивать их
// в один перечень значит врать эмбеддеру о том, что он читает.
func embedText(e Entity, aliases []string, limit int, desc string) string {
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
	text := strings.Join(parts, ", ")
	if desc = strings.TrimSpace(desc); desc != "" {
		if r := []rune(desc); len(r) > descMaxRunes {
			desc = strings.TrimSpace(string(r[:descMaxRunes]))
		}
		text += ". " + desc
	}
	return text
}

// descFor — описание понятия для вектора: пусто, пока правило выключено.
//
// Одно место на все сборки текста: забыть проверить правило в одном из двух
// вызовов значило бы считать часть векторов по одному тексту, а часть —
// по другому, и расхождение вылезло бы только на поиске.
func (g *Graph) descFor(id uint32) string {
	if g == nil || !g.rules.VectorDesc {
		return ""
	}
	return g.desc.Of(id)
}

// EmbedTextOf — текст, который уходит эмбеддеру за это понятие: имя и до
// VectorAliases частей из безопасных синонимов. Наружу — ради замеров:
// сравнивать векторы, посчитанные в разное время, честно только по тому же
// тексту, что строит сам счёт, а не по его пересказу в скрипте.
func (g *Graph) EmbedTextOf(e Entity) string {
	if g == nil || g.ents == nil {
		return ""
	}
	return embedText(e, g.ents.SafeAliases(e), g.rules.VectorAliases, g.descFor(e.ID))
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

	if len(texts) == 0 {
		return fmt.Errorf("в графе нет понятий — считать нечего")
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
	already := 0
	if !o.Recount {
		_, already = g.vecs.Existing(emb.Model(), 0)
		if already > len(texts) {
			already = 0 // граф ужался: досчитывать нечего, считаем заново
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
	if already == len(texts) && already > 0 {
		return nil // всё уже посчитано
	}
	if already == 0 {
		// Счёт с нуля: полный пересчёт, смена эмбеддера, первый счёт. Прежние
		// векторы (если есть) работают до самой подмены — см. vecpart.go.
		return g.recountInParts(ctx, emb, o, texts, digest, onProgress)
	}

	// Досчёт хвоста порциями: посчитали — дописали — зафиксировали.
	total := len(texts) - already
	for from := already; from < len(texts); from += o.Checkpoint {
		to := from + o.Checkpoint
		if to > len(texts) {
			to = len(texts)
		}
		// Копия: embedBatches подменяет пустые имена пробелом, а отпечатки
		// считаются от настоящих текстов.
		seg := append([]string(nil), texts[from:to]...)
		dim, data, err := embedBatches(ctx, emb, seg, o, shifted(onProgress, from-already, total))
		if err != nil {
			return savedSoFar(err, from-already, total)
		}
		if err := g.vecs.appendVectors(emb.Model(), digest, dim, data); err != nil {
			return savedSoFar(err, from-already, total)
		}
		// Свежие — только эта порция: отпечатки головы остаются прежними, иначе
		// устаревшие векторы перестали бы находиться (saveEntityVectors).
		fresh := make([]uint32, 0, to-from)
		for id := from + 1; id <= to; id++ {
			fresh = append(fresh, uint32(id))
		}
		_ = saveStamps(g.dir, mergeStamps(loadStamps(g.dir), texts[:to], fresh))
	}
	return nil
}

// recountInParts считает векторы всех понятий с нуля, фиксируя посчитанное
// порциями в файлах `.part`; оборванный счёт продолжается повтором команды.
func (g *Graph) recountInParts(ctx context.Context, emb kb.Embedder, o EmbedOpts,
	texts []string, digest string, onProgress func(EmbedProgress)) error {

	part := loadVecPart(g.dir, emb.Model(), digest, len(texts))
	for from := part.meta.Count; from < len(texts); from += o.Checkpoint {
		to := from + o.Checkpoint
		if to > len(texts) {
			to = len(texts)
		}
		seg := append([]string(nil), texts[from:to]...)
		dim, data, err := embedBatches(ctx, emb, seg, o, shifted(onProgress, from, len(texts)))
		if err != nil {
			return savedSoFar(err, from, len(texts))
		}
		if err := part.add(dim, data, texts[from:to]); err != nil {
			return savedSoFar(err, from, len(texts))
		}
	}
	// Подмена: прежняя атомарная запись целиком, отпечатки — те, что копились
	// вместе с векторами (текст мог измениться между обрывом и продолжением).
	if err := g.vecs.save(emb.Model(), digest, part.meta.Dim, part.data); err != nil {
		return err
	}
	_ = saveStamps(g.dir, part.stamps)
	part.drop()
	return nil
}

// shifted сдвигает ход порции в общий счёт: человеку показывается «сделано
// из всего», а не «сделано из порции».
func shifted(onProgress func(EmbedProgress), done, total int) func(EmbedProgress) {
	if onProgress == nil {
		return nil
	}
	return func(p EmbedProgress) {
		onProgress(EmbedProgress{Done: done + p.Done, Total: total})
	}
}

// savedSoFar дописывает к ошибке счёта, сколько уже лежит на диске: после
// обрыва человеку важно знать, что работа не пропала и чем её продолжить.
func savedSoFar(err error, saved, total int) error {
	if saved <= 0 {
		return err
	}
	return fmt.Errorf("%w\nпосчитанное сохранено: %d из %d; повтор той же команды продолжит с этого места",
		err, saved, total)
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
			texts[e.ID-1] = embedText(e, g.ents.SafeAliases(e), g.rules.VectorAliases, g.descFor(e.ID))
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
				vecs, err := embedWithWait(ctx, emb, texts[j.from:j.to], o)
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
