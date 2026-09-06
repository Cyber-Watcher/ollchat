package graphex

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/nodeprobe"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Пул серверов Ollama: сборка графа на нескольких видеокартах (этап 95).
//
// **Зачем.** Модель извлечения `qwen3.8` не умеет параллельных запросов —
// Ollama пишет на каждый запрос «model architecture does not currently support
// parallel requests», и замер 26.08.2026 показал, что четыре запроса разом идут
// вчетверо дольше одного. То есть на одной карте `workers` почти ничего
// не покупает, а вторая карта — это настоящий второй слот. При скорости
// 0.24–0.31 куска/с это единственный способ сократить недели сборки,
// не меняя модель извлечения.
//
// **Как раздаётся работа.** Не по хешу куска и не по каталогам, а по
// освобождению слота: кто освободился, тот берёт следующий кусок. Карты разной
// скорости (A100-80 против 3090-24), и заранее их доли неизвестны; жёсткое
// закрепление кусков за узлом не умеет догонять — медленная карта задержала бы
// весь заход.
//
// **Почему не отдельные сборки со слиянием.** Номера понятий раздаёт локальный
// счётчик одного процесса, а `entities.vec` адресуется по этим номерам: два
// независимо собранных графа несовместимы, и слияние означает перенумерацию
// плюс пересчёт всех векторов. Здесь же каталог графа один, и сливать нечего:
// запись в append-only журнал — микросекунды против трёх-четырёх секунд
// генерации, поэтому карты и работают независимо, хоть пишут в один файл.
//
// Пул удовлетворяет graph.Extractor (Extract + Model), поэтому сама сборка
// о нём ничего не знает и не меняется.

const (
	// nodeFailLimit — сколько неудач подряд считать смертью узла. Каждая
	// неудача — это уже три попытки внутри Extractor, так что три подряд
	// означают девять запросов в пустоту.
	nodeFailLimit = 3

	// nodeRecheck — как часто пробовать вернуть выключенный узел. Заход идёт
	// часами, а туннель ssh к стенду рвётся: узел, отвалившийся на минуту,
	// не должен выпадать до конца сборки.
	nodeRecheck = 5 * time.Minute

	// deadRecheck — как часто проверять узлы, когда живых не осталось вовсе.
	// Чаще обычного: заход в это время стоит, и каждая лишняя минута ожидания
	// оплачивается простоем карт.
	deadRecheck = 30 * time.Second

	// probeEvery — как часто спрашивать наблюдателей о состоянии карт.
	// Чужое обучение на карте длится часами, ловить его посекундно незачем,
	// а снимок на сервере всё равно кэшируется.
	probeEvery = 30 * time.Second
)

// Node — один сервер Ollama в пуле.
type Node struct {
	// Name — короткое имя для показа, журнала и паспорта графа. Адрес туда
	// не пишется намеренно: паспорт и журналы читают посторонние.
	Name string
	// URL — адрес сервера.
	URL string
	// Workers — сколько запросов держать на этом узле разом. У карт разной
	// ёмкости оно разное: на 24 ГБ модель весом 17.7 ГБ оставляет под KV-кэш
	// около шести, и лишний слот выталкивает слои в системную память.
	Workers int

	// Probe и ProbeToken — наблюдатель ollnode на этом сервере (этап 96).
	// Пусто — узел без наблюдателя, всё работает как прежде.
	Probe      string
	ProbeToken string
}

// poolNode — узел в работе.
type poolNode struct {
	name    string
	ex      *Extractor
	workers int
	probe   *nodeprobe.Client

	// Под замком пула.
	done  int           // разобрано кусков
	errs  int           // неудач всего
	fails int           // неудач подряд; сбрасывается успехом
	spent time.Duration // суммарное время удачных запросов
	off   bool          // выключен из работы

	// downs — сколько раз узел уже выключался, nextTry — когда его пробовать
	// снова. Откат нужен против мигания: сервер, который отвечает на /api/version,
	// но валит чат (карта занята, модель выгружена), иначе возвращался бы каждые
	// пять минут и снова падал, тратя по три куска на каждый заход.
	// Когда живых узлов не осталось вовсе, откат не соблюдается: ждать нечего.
	downs   int
	nextTry time.Time

	// busy — почему узел временно не берёт работу: чужой процесс на его карте
	// или вытесненная в оперативную память модель. Отличается от off тем, что
	// узел исправен: он вернётся по чистому снимку наблюдателя, а не по Check.
	busy string
	// last — последний снимок наблюдателя, для показа человеку.
	last *nodeprobe.Report

	// out — сколько слотов этого узла изъято из оборота. Считается точно,
	// потому что слоты уходят не разом: часть висит в канале, часть занята
	// работой и изымается только при возврате. Возвращать «сколько было»
	// вместо «сколько изъято» значит наплодить лишних слотов.
	out int
}

// Pool — несколько серверов Ollama под одной моделью извлечения.
type Pool struct {
	model string
	nodes []*poolNode
	slots chan *poolNode // по Workers меток на каждый живой узел

	// nodeWait — сколько ждать возвращения узлов, когда живых нет. 0 — не ждать.
	nodeWait time.Duration
	// deadEvery — шаг проверки при мёртвом пуле. Поле, а не постоянная,
	// ради тестов: иначе проверка ожидания шла бы полминуты.
	deadEvery time.Duration
	// probeEvery — шаг опроса наблюдателей. Тоже поле ради тестов.
	probeEvery time.Duration

	mu      sync.Mutex
	left    int           // сколько слотов ещё в обороте
	lastErr error         // чем умер последний узел
	dead    chan struct{} // закрыт, когда живых слотов не осталось
	// deadSince — когда живых слотов не стало. Срок node_wait отсчитывается
	// от него, а не от прихода воркера в ожидание: иначе каждый воркер,
	// заново упёршийся в мёртвый пул, начинал бы отсчёт с нуля, и мигающий
	// узел растягивал бы ожидание без предела.
	deadSince time.Time

	// reviveMu пропускает к проверке узлов по одному. Без него восемь воркеров,
	// одновременно упёршихся в мёртвый пул, слали бы на упавший сервер восемь
	// проверок разом — а он и так упал.
	reviveMu sync.Mutex
}

// NewPool собирает пул по списку узлов. Настройки запроса общие: модель,
// окно, температура и предел ответа у всех узлов обязаны совпадать — иначе
// половина графа собрана одними условиями, половина другими, и различить их
// потом неоткуда.
//
// Options.URL и Options.Workers не используются: адрес и число слотов у каждого
// узла свои.
func NewPool(nodes []Node, o Options, timeout time.Duration, headers map[string]string) (*Pool, error) {
	if o.Model == "" {
		return nil, fmt.Errorf("не задана модель извлечения")
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("не задан ни один узел")
	}
	p := &Pool{model: o.Model, dead: make(chan struct{}), nodeWait: o.NodeWait,
		deadEvery: deadRecheck}
	total := 0
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Name == "" || n.URL == "" {
			return nil, fmt.Errorf("узел без имени или адреса")
		}
		if seen[n.Name] {
			return nil, fmt.Errorf("узел %q назван дважды", n.Name)
		}
		seen[n.Name] = true
		w := n.Workers
		if w <= 0 {
			w = 4
		}
		if w > maxInFlight {
			w = maxInFlight
		}
		no := o
		no.URL, no.Workers = n.URL, w
		ex := New(no, "", timeout, headers)
		if ex == nil {
			return nil, fmt.Errorf("узел %q: не удалось собрать извлекатель", n.Name)
		}
		p.nodes = append(p.nodes, &poolNode{name: n.Name, ex: ex, workers: w,
			probe: nodeprobe.NewClient(n.Probe, n.ProbeToken, 5*time.Second)})
		total += w
	}
	p.slots = make(chan *poolNode, total)
	for _, n := range p.nodes {
		for i := 0; i < n.workers; i++ {
			p.slots <- n
		}
	}
	p.left = total
	return p, nil
}

// Model возвращает имя модели извлечения — одно на все узлы.
func (p *Pool) Model() string { return p.model }

// Slots — сколько запросов пул держит разом. По нему сборка выбирает число
// воркеров: меньше — часть карт простаивает, больше — очередь на слот.
func (p *Pool) Slots() int {
	n := 0
	for _, x := range p.nodes {
		n += x.workers
	}
	return n
}

// Names — имена узлов для паспорта графа.
func (p *Pool) Names() []string {
	out := make([]string, 0, len(p.nodes))
	for _, n := range p.nodes {
		out = append(out, n.name)
	}
	sort.Strings(out)
	return out
}

// Check проверяет все узлы разом и сверяет веса модели.
//
// Одно имя модели на двух серверах может указывать на разные файлы: другое
// квантование, другая дата загрузки. Смешанный граф выглядит исправным
// и не чинится ничем, кроме полной пересборки, — тот же довод, по которому
// сборка отказывается менять модель на полпути. Поэтому сверяются digest
// и уровень квантования из /api/tags.
func (p *Pool) Check(ctx context.Context) error {
	type res struct {
		name  string
		stamp ModelStamp
		err   error
	}
	out := make([]res, len(p.nodes))
	var wg sync.WaitGroup
	for i, n := range p.nodes {
		wg.Add(1)
		go func(i int, n *poolNode) {
			defer wg.Done()
			r := res{name: n.name}
			if r.err = n.ex.Check(ctx); r.err == nil {
				r.stamp, r.err = n.ex.Stamp(ctx)
			}
			out[i] = r
		}(i, n)
	}
	wg.Wait()

	var bad []string
	for _, r := range out {
		if r.err != nil {
			bad = append(bad, fmt.Sprintf("узел %s: %v", r.name, r.err))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s", strings.Join(bad, "\n"))
	}
	if len(out) < 2 {
		return nil
	}
	// Сверка весов. Пустой digest бывает у старых сборок Ollama — тогда
	// сравнивается только квантование, и молчать об этом нельзя.
	first := out[0]
	for _, r := range out[1:] {
		if r.stamp.Digest != "" && first.stamp.Digest != "" && r.stamp.Digest != first.stamp.Digest {
			return fmt.Errorf(
				"на узлах %s и %s модель %s — это разные веса (digest %s против %s).\n"+
					"Собирать один граф разными весами нельзя: половина графа окажется "+
					"собрана одной моделью, половина другой, а видно этого не будет ниоткуда.\n"+
					"Приведите модель к одной версии на обоих серверах",
				first.name, r.name, p.model, short(first.stamp.Digest), short(r.stamp.Digest))
		}
		if r.stamp.Quant != first.stamp.Quant {
			return fmt.Errorf(
				"на узлах %s и %s у модели %s разное квантование (%s против %s).\n"+
					"Это разные модели по качеству извлечения; один граф ими собирать нельзя",
				first.name, r.name, p.model, first.stamp.Quant, r.stamp.Quant)
		}
	}
	return nil
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// Extract отдаёт кусок первому освободившемуся узлу.
//
// Ошибка дороги или сервера — не повод ронять заход: узел, ошибившийся
// nodeFailLimit раз подряд, выключается, а кусок уходит другому. Заход
// останавливается, только когда живых узлов не осталось и они не вернулись
// за node_wait.
//
// **Почему повторов не две, а «пока есть кому».** Сборка (build.go) на любой
// ошибке извлечения, кроме плохого ответа модели, ставит firstErr и снимает
// заход. Значит, ошибка дороги, отданная отсюда наверх, — это конец сборки,
// и всё ожидание возвращения узла (waitAlive) до сборки не доходит. Проверено
// 06.09.2026 на настройках владельца: один сервер, четыре воркера, обрыв
// туннеля — первый же воркер, исчерпавший две попытки, останавливал заход,
// пока остальные ждали узла. Поэтому наверх уходит только то, что отдал
// take: отмена или «не вернулись за node_wait».
//
// Страховка от ядовитого куска — предел неудач на кусок: 2·nodeFailLimit
// на узел. Кусок, на котором здоровые узлы падают повторяемой ошибкой,
// выключит их все дважды и только тогда остановит заход — это редкость,
// неотличимая снаружи от обрыва, и платить за неё двумя ожиданиями честно.
func (p *Pool) Extract(ctx context.Context, system, user string) (string, error) {
	limit := 2 * nodeFailLimit * len(p.nodes)
	var last error
	for fails := 0; ; {
		n, err := p.take(ctx)
		if err != nil {
			if last != nil && ctx.Err() == nil {
				return "", fmt.Errorf("%w; последняя ошибка узла: %v", err, last)
			}
			return "", err
		}
		started := time.Now()
		text, err := n.ex.Extract(ctx, system, user)
		if err == nil {
			p.ok(n, time.Since(started))
			return text, nil
		}
		// Отмена — не вина узла, и повторять её незачем. Кусок не засчитывается:
		// он не разобран.
		if ctx.Err() != nil {
			p.release(n)
			return "", err
		}
		// Пустой или неразбираемый ответ — вина модели, а не сервера:
		// такой кусок сборка пометит пропущенным, а узел остаётся в строю.
		if !ollama.Retryable(err) {
			p.ok(n, time.Since(started))
			return "", err
		}
		last = err
		p.fail(n, err)
		if fails++; fails >= limit {
			return "", fmt.Errorf("кусок не разобран за %d попыток на узлах: %w", fails, last)
		}
	}
}

// take берёт слот. Слоты выключенных узлов изымаются из оборота здесь же:
// узел выключается, когда его метки уже разошлись по каналу.
func (p *Pool) take(ctx context.Context) (*poolNode, error) {
	for {
		select {
		case n := <-p.slots:
			p.mu.Lock()
			idle := n.off || n.busy != ""
			if idle {
				p.drop(n)
			}
			p.mu.Unlock()
			if idle {
				continue
			}
			return n, nil
		case <-p.deadCh():
			if err := p.waitAlive(ctx); err != nil {
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ok возвращает слот и засчитывает удачу.
func (p *Pool) ok(n *poolNode, spent time.Duration) {
	p.mu.Lock()
	n.done++
	n.fails = 0
	n.spent += spent
	idle := n.off || n.busy != ""
	if idle {
		p.drop(n)
	}
	p.mu.Unlock()
	if !idle {
		p.slots <- n
	}
}

// release возвращает слот, ничего не засчитывая: запрос отменён, кусок
// не разобран, и в числе разобранных ему не место.
func (p *Pool) release(n *poolNode) {
	p.mu.Lock()
	idle := n.off || n.busy != ""
	if idle {
		p.drop(n)
	}
	p.mu.Unlock()
	if !idle {
		p.slots <- n
	}
}

// fail засчитывает неудачу и, если их накопилось подряд nodeFailLimit,
// выключает узел вместе с его слотом.
func (p *Pool) fail(n *poolNode, err error) {
	p.mu.Lock()
	n.errs++
	n.fails++
	kill := !n.off && n.fails >= nodeFailLimit
	if kill {
		n.off = true
		n.downs++
		p.lastErr = err
		wait := time.Duration(n.downs) * nodeRecheck
		if wait > 30*time.Minute {
			wait = 30 * time.Minute
		}
		n.nextTry = time.Now().Add(wait)
	}
	idle := n.off || n.busy != ""
	if idle {
		p.drop(n)
	}
	p.mu.Unlock()
	if !idle {
		p.slots <- n
	}
}

// drop изымает один слот узла из оборота. Вызывается под замком.
func (p *Pool) drop(n *poolNode) {
	if p.left <= 0 {
		return
	}
	p.left--
	n.out++
	if p.left == 0 {
		p.deadSince = time.Now()
		close(p.dead)
	}
}

// restore возвращает в оборот слоты узла, изъятые раньше. Под замком считает,
// после замка отправляет: канал вмещает все слоты пула, но держать замок
// на записи в него незачем.
func (p *Pool) restore(n *poolNode) {
	p.mu.Lock()
	back := n.out
	if back > 0 {
		if p.left == 0 {
			// Пул оживает: канал «живых нет» закрыт, и ждавшие на нём воркеры
			// уже проснулись. Новый нужен на случай следующей смерти.
			p.dead = make(chan struct{})
			p.deadSince = time.Time{}
		}
		p.left += back
		n.out = 0
	}
	p.mu.Unlock()
	for i := 0; i < back; i++ {
		p.slots <- n
	}
}

// deadCh — канал «живых узлов не осталось». Читается под замком: канал
// пересоздаётся, когда узел возвращается в строй.
func (p *Pool) deadCh() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// waitAlive ждёт, пока хоть один узел вернётся в строй.
//
// **Зачем ждать, а не падать.** Узлы стоят за интернетом, и обрыв на пять
// минут не должен стоить ночной сборки: заход возобновляем, но до утра
// его никто не возобновит, и карты простоят зря. Предел ожидания —
// graph.node_wait; за ним честная остановка, а не бесконечное висение.
//
// Срок считается от момента, когда пул умер, а не от прихода сюда: воркер,
// закончивший долгий запрос на последнем живом узле, застаёт пул мёртвым
// уже давно, и ждать ему остаток, а не полный срок заново.
func (p *Pool) waitAlive(ctx context.Context) error {
	p.mu.Lock()
	last, since, alive := p.lastErr, p.deadSince, p.left > 0
	p.mu.Unlock()
	if alive {
		// Пул ожил, пока воркер шёл сюда: канал «живых нет» пересоздаётся,
		// и старый, уже закрытый, мог достаться воркеру в самый момент
		// возвращения узла. Ждать первого тика незачем.
		return nil
	}
	if last == nil {
		last = fmt.Errorf("не осталось живых узлов извлечения")
	}
	if p.nodeWait <= 0 {
		return fmt.Errorf("все узлы извлечения выключены: %w", last)
	}
	if since.IsZero() {
		since = time.Now()
	}
	deadline := since.Add(p.nodeWait)

	every := p.deadEvery
	if every <= 0 {
		every = deadRecheck
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		// Проверяет один, остальные ждут его результата: сервер, который лежит,
		// не надо будить восемью запросами разом.
		if p.reviveMu.TryLock() {
			p.revive(ctx)
			p.reviveMu.Unlock()
		}
		p.mu.Lock()
		alive := p.left > 0
		p.mu.Unlock()
		if alive {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("все узлы извлечения выключены и не вернулись за %s: %w",
				p.nodeWait, last)
		}
	}
}

// shortReason укорачивает причину для строки хода: она и так длинная,
// а подробность видна по команде /nodes.
func shortReason(s string) string {
	if i := strings.Index(s, ";"); i > 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 40 {
		return string(r[:39]) + "…"
	}
	return s
}

// HasProbes — есть ли у пула хоть один наблюдатель.
func (p *Pool) HasProbes() bool {
	for _, n := range p.nodes {
		if n.probe != nil {
			return true
		}
	}
	return false
}

// pollProbes спрашивает наблюдателей и выводит из раздачи узлы, чьи карты
// заняты чужой работой или чья модель вытеснена в оперативную память.
//
// **Зачем уводить работу заранее.** Без наблюдателя узел выключается только
// после трёх неудач подряд — то есть после девяти запросов, каждый из которых
// либо ждёт в очереди за чужой моделью, либо считается процессором втрое
// дольше. Снимок говорит об этом до первой потери.
//
// **Последний узел по занятости не выводится никогда.** Иначе наблюдатель
// останавливал бы сборку вместо того, чтобы её беречь: на единственной карте
// «занято» означает «медленнее», а не «нельзя». Это же правило спасает
// от общей беды — когда занятыми выглядят все узлы разом.
func (p *Pool) pollProbes(ctx context.Context) { p.Poll(ctx, true) }

// Poll — то же, но с выбором полноты снимка: сборке перед стартом полезен
// журнал службы, а частому опросу по ходу работы — нет.
func (p *Pool) Poll(ctx context.Context, light bool) {
	for _, n := range p.nodes {
		if n.probe == nil {
			continue
		}
		rep, err := n.probe.Node(ctx, light)
		if err != nil {
			// Наблюдатель недоступен — это не повод трогать узел: данные
			// вспомогательные, сломанный градусник работу не отменяет.
			continue
		}
		reason := rep.Busy(0)

		p.mu.Lock()
		n.last = rep
		was := n.busy
		switch {
		case reason == "":
			n.busy = ""
		case p.aloneLocked(n):
			// Единственный работающий узел: причину запоминаем для показа,
			// но работу не отбираем.
			n.busy = ""
		default:
			n.busy = reason
		}
		now, off := n.busy, n.off
		p.mu.Unlock()

		// Слоты возвращаются только узлу, который может работать. Выключенному
		// (off) их вернёт revive, когда сервер ответит на проверку; вернуть
		// раньше значит оживить мёртвый пул на мгновение — воркеры проснутся,
		// отбросят слоты обратно и начнут ожидание заново.
		if was != "" && now == "" && !off {
			p.restore(n)
		}
	}
}

// aloneLocked — этот узел единственный, кто ещё может работать.
// Вызывается под замком.
func (p *Pool) aloneLocked(n *poolNode) bool {
	for _, other := range p.nodes {
		if other == n || other.off || other.busy != "" {
			continue
		}
		return false
	}
	return true
}

// Watch возвращает выключенные узлы в строй, пока идёт заход.
//
// Запускается вызывающим кодом отдельной горутиной и живёт до отмены ctx.
// Без неё узел, отвалившийся из-за оборванного туннеля, выпадал бы до конца
// сборки — то есть до утра. Когда живых узлов не осталось, ожиданием
// занимаются сами воркеры (waitAlive) — чаще и с общим пределом.
func (p *Pool) Watch(ctx context.Context) {
	t := time.NewTicker(nodeRecheck)
	defer t.Stop()
	every := p.probeEvery
	if every <= 0 {
		every = probeEvery
	}
	probe := time.NewTicker(every)
	defer probe.Stop()
	// Первый опрос сразу: если карта занята чужим уже сейчас, узнать об этом
	// лучше до того, как на неё уйдут первые куски.
	p.pollProbes(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p.reviveMu.TryLock() {
				p.revive(ctx)
				p.reviveMu.Unlock()
			}
		case <-probe.C:
			p.pollProbes(ctx)
		}
	}
}

// revive пробует вернуть каждый выключенный узел.
//
// Узел, выключавшийся не в первый раз, проверяется реже — но только пока
// в строю есть кто-то ещё. Когда живых узлов нет, откат не соблюдается:
// заход всё равно стоит, и ждать лишнее незачем.
func (p *Pool) revive(ctx context.Context) {
	for _, n := range p.nodes {
		p.mu.Lock()
		off, early := n.off, time.Now().Before(n.nextTry) && p.left > 0
		p.mu.Unlock()
		if !off || early {
			continue
		}
		if err := n.ex.Check(ctx); err != nil {
			continue
		}
		p.mu.Lock()
		if !n.off { // вернули, пока проверяли
			p.mu.Unlock()
			continue
		}
		n.off, n.fails = false, 0
		busy := n.busy != ""
		p.mu.Unlock()
		// Узлу, чья карта занята чужим, слоты вернёт опрос наблюдателя,
		// когда занятость снимется: возвращать их сейчас значило бы тут же
		// отбросить их в take.
		if !busy {
			p.restore(n)
		}
	}
}

// NodeStat — что узел сделал за заход.
type NodeStat struct {
	Name string
	Done int
	Errs int
	Off  bool
	Avg  time.Duration // средняя длительность удачного запроса

	// Busy — почему узел временно не берёт работу (чужое на карте, вытеснение).
	// Пусто — работает.
	Busy string
	// Probe — последний снимок наблюдателя; nil, если наблюдателя нет.
	Probe *nodeprobe.Report
}

// Stats — срез по узлам для строки хода и журнала.
func (p *Pool) Stats() []NodeStat {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]NodeStat, 0, len(p.nodes))
	for _, n := range p.nodes {
		s := NodeStat{Name: n.name, Done: n.done, Errs: n.errs, Off: n.off,
			Busy: n.busy, Probe: n.last}
		if n.done > 0 {
			s.Avg = n.spent / time.Duration(n.done)
		}
		out = append(out, s)
	}
	return out
}

// Line — срез по узлам одной строкой: «a100 1214 (3.6 с) · rtx3090 620 (7.1 с)».
func (p *Pool) Line() string {
	var parts []string
	for _, s := range p.Stats() {
		part := fmt.Sprintf("%s %d", s.Name, s.Done)
		if s.Avg > 0 {
			part += fmt.Sprintf(" (%.1f с)", s.Avg.Seconds())
		}
		if s.Off {
			part += " ВЫКЛ"
		} else if s.Busy != "" {
			part += " ждёт (" + shortReason(s.Busy) + ")"
		} else if s.Errs > 0 {
			part += fmt.Sprintf(" ошибок %d", s.Errs)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}
