package graphex

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
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

	// banned — почему узел выключен до конца захода: вернулся в строй с другой
	// моделью, чем при старте (revive). Не пусто — revive его больше не трогает.
	banned string

	// downs — сколько раз узел уже выключался, nextTry — когда его пробовать
	// снова. Откат нужен против мигания: сервер, который отвечает на /api/version,
	// но валит чат (карта занята, модель выгружена), иначе возвращался бы каждые
	// пять минут и снова падал, тратя по три куска на каждый заход.
	// Когда живых узлов не осталось вовсе, откат не соблюдается: ждать нечего.
	downs   int
	nextTry time.Time

	// note — что наблюдатель говорит о карте узла: посторонний процесс на ней
	// или вытесненная в оперативную память модель. **Только для показа.**
	// До 12.09.2026 это поле называлось busy и отбирало у узла слоты; слово
	// владельца в тот день: «ollnode только выдаёт информацию и никак не должен
	// влиять на сборку». Влиять на работу может off — то есть сервер, который
	// не отвечает, — а не показания наблюдателя.
	note string
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
	notes []string       // предупреждения последней Check: не отказ, но сказать надо

	// nodeWait — сколько ждать возвращения узлов, когда живых нет. 0 — не ждать.
	nodeWait time.Duration
	// deadEvery — шаг проверки при мёртвом пуле. Поле, а не постоянная,
	// ради тестов: иначе проверка ожидания шла бы полминуты.
	deadEvery time.Duration
	// probeEvery — шаг опроса наблюдателей. Тоже поле ради тестов.
	probeEvery time.Duration

	mu sync.Mutex
	// ref — отпечаток модели, которой начат заход: первого узла последней
	// удачной Check, refName — имя этого узла. С ним revive сверяет узел,
	// возвращающийся в строй. nil — Check не вызывали или узел один: тогда
	// узел возвращается без сверки, как прежде.
	ref     *ModelStamp
	refName string
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

// Check проверяет все узлы разом и сверяет модель на них.
//
// Одно имя модели на двух серверах может указывать на разные файлы: другое
// квантование, другая дата загрузки. Смешанный граф выглядит исправным
// и не чинится ничем, кроме полной пересборки, — тот же довод, по которому
// сборка отказывается менять модель на полпути. Поэтому сверяются файлы весов
// и проектора, квантование и рецепт (шаблон, RENDERER/PARSER, системный
// промпт, параметры). Digest манифеста — только когда файлов не назвал ни один
// узел: он расходится и от лицензии (ModelStamp). Разные версии Ollama и
// несверенные веса — предупреждение (Notes), а не отказ: подсказку для
// RENDERER собирает код самой Ollama, и что это значит для извлечения, решает
// замер, а не догадка. Правила сверки — compareStamps.
//
// Удачная Check запоминает отпечаток первого узла эталоном захода: с ним
// сверяется узел, возвращающийся в строй (revive).
func (p *Pool) Check(ctx context.Context) error {
	p.notes = nil
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
	first := out[0]
	for _, r := range out[1:] {
		notes, err := compareStamps(first.name, r.name, first.stamp, r.stamp)
		p.notes = append(p.notes, notes...)
		if err != nil {
			return err
		}
	}
	// Эталон для revive: узел, вернувшийся посреди захода, сверяется с той
	// моделью, которой заход начат, — по тем же правилам, что здесь.
	ref := first.stamp
	p.mu.Lock()
	p.ref, p.refName = &ref, first.name
	p.mu.Unlock()
	return nil
}

// compareStamps сверяет отпечатки одной модели на двух узлах. Ошибка — граф
// собрался бы двумя моделями; notes — то, что сборку не останавливает, но о чём
// человек должен узнать: веса сверить не удалось, версии Ollama разные.
// Одна на Check и на revive, чтобы узел, вернувшийся в строй, сверялся по тем
// же правилам, что и при старте захода.
//
// Веса сверяются по файлам из Modelfile, а когда их не назвал ни один узел —
// по digest манифеста. Файлы только у одного узла digest не заменяет: он
// расходится и от лицензии (ModelStamp), и отказ по нему был бы ложным.
// Веса, не сверенные ни так, ни так, — заметка с именами узлов: тогда
// сравниваются только квантование и рецепт, и молчать об этом нельзя.
func compareStamps(aName, bName string, a, b ModelStamp) (notes []string, err error) {
	model := a.Model
	differ := func(what string) error {
		return fmt.Errorf(
			"на узлах %s и %s модель %s — это разные веса (%s).\n"+
				"Собирать один граф разными весами нельзя: половина графа окажется "+
				"собрана одной моделью, половина другой, а видно этого не будет ниоткуда.\n"+
				"Приведите модель к одной версии на обоих серверах",
			aName, bName, model, what)
	}
	switch {
	case len(a.Files) > 0 && len(b.Files) > 0:
		if strings.Join(a.Files, " ") != strings.Join(b.Files, " ") {
			return notes, differ(fmt.Sprintf("файлы %s против %s", shortFiles(a.Files), shortFiles(b.Files)))
		}
	case len(a.Files) == 0 && len(b.Files) == 0 && a.Digest != "" && b.Digest != "":
		if a.Digest != b.Digest {
			return notes, differ(fmt.Sprintf("digest %s против %s", short(a.Digest), short(b.Digest)))
		}
	default:
		why := "Modelfile не назвал файлов весов ни у одного узла, а digest манифеста пуст"
		if len(a.Files) > 0 {
			why = "файлы весов в Modelfile названы только у узла " + aName
		} else if len(b.Files) > 0 {
			why = "файлы весов в Modelfile названы только у узла " + bName
		}
		notes = append(notes, fmt.Sprintf(
			"веса модели %s на узлах %s и %s сверить не удалось: %s; сверены только "+
				"квантование и рецепт", model, aName, bName, why))
	}
	for i := range a.Recipe {
		if i < len(b.Recipe) && a.Recipe[i].Value != b.Recipe[i].Value {
			line, ea, eb := firstDiff(a.Recipe[i].Value, b.Recipe[i].Value)
			return notes, fmt.Errorf(
				"на узлах %s и %s у модели %s отличается часть «%s», первое расхождение "+
					"в строке %d:\n  %s: %q\n  %s: %q\n"+
					"От этого зависит ответ модели, а значит и граф; приведите тег к одному "+
					"Modelfile на обоих серверах",
				aName, bName, model, a.Recipe[i].Name, line, aName, ea, bName, eb)
		}
	}
	if a.Version != b.Version {
		notes = append(notes, fmt.Sprintf(
			"версии Ollama на узлах разные: %s — %s, %s — %s; модель та же, "+
				"но подсказку и расчёт делает сама Ollama",
			aName, a.Version, bName, b.Version))
	}
	if a.Quant != b.Quant {
		return notes, fmt.Errorf(
			"на узлах %s и %s у модели %s разное квантование (%s против %s).\n"+
				"Это разные модели по качеству извлечения; один граф ими собирать нельзя",
			aName, bName, model, a.Quant, b.Quant)
	}
	return notes, nil
}

// diffContext — сколько знаков показывать по обе стороны от расхождения.
const diffContext = 200

// firstDiff — номер первой расходящейся строки (с единицы) и её окрестность
// у каждой стороны: до diffContext знаков до первого различного знака и после
// него, обрезка помечена «…». Части рецепта приходят с чужого сервера целиком
// (шаблон бывает в сотни килобайт), и отказ с обоими текстами занимал журнал
// и экран; человеку нужно место расхождения, а не всё остальное.
func firstDiff(a, b string) (line int, ea, eb string) {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	i := 0
	for i < len(la) && i < len(lb) && la[i] == lb[i] {
		i++
	}
	var sa, sb string
	if i < len(la) {
		sa = la[i]
	}
	if i < len(lb) {
		sb = lb[i]
	}
	ra, rb := []rune(sa), []rune(sb)
	k := 0
	for k < len(ra) && k < len(rb) && ra[k] == rb[k] {
		k++
	}
	return i + 1, around(ra, k), around(rb, k)
}

// around — знаки r в окрестности diffContext от позиции k.
func around(r []rune, k int) string {
	from, to := k-diffContext, k+diffContext
	pre, post := "…", "…"
	if from <= 0 {
		from, pre = 0, ""
	}
	if to >= len(r) {
		to, post = len(r), ""
	}
	return pre + string(r[from:to]) + post
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// shortFiles — имена блобов, укороченные для отказа: «sha256-f5f1dd8920d4».
func shortFiles(files []string) string {
	out := make([]string, len(files))
	for i, f := range files {
		if strings.HasPrefix(f, "sha256-") && len(f) > 19 {
			f = f[:19]
		}
		out[i] = f
	}
	return strings.Join(out, " ")
}

// Notes — предупреждения последней Check: расхождения, которые сборку
// не останавливают, но о которых человек должен узнать до её начала.
func (p *Pool) Notes() []string { return p.notes }

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
		// Пустой ответ — отказ модели на этом куске, а не беда узла: сервер
		// ответил как положено. Сборка пометит кусок пустым или пропущенным,
		// узел остаётся в строю, но и разобранным кусок не считается.
		if errors.Is(err, graph.ErrEmptyAnswer) {
			p.answered(n)
			return "", err
		}
		// Всё прочее — беда узла или запроса. Неповторяемая (4xx: на узле нет
		// модели, запрос не принят) прежде засчитывалась узлу как удача
		// и уходила наверх, где на ней останавливалась вся сборка. Теперь это
		// неудача узла, и в пуле из нескольких узлов кусок уходит другому:
		// модели может не оказаться на одном сервере. В пуле из одного узла
		// отдать кусок некому — ошибка уходит наверх сразу, как прежде.
		last = err
		p.fail(n, err)
		if !ollama.Retryable(err) && len(p.nodes) == 1 {
			return "", err
		}
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
			idle := n.off
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
	idle := n.off
	if idle {
		p.drop(n)
	}
	p.mu.Unlock()
	if !idle {
		p.slots <- n
	}
}

// answered возвращает слот узла, который ответил, но разобранного куска
// не дал (пустой ответ модели): узел жив — счёт неудач подряд сбрасывается,
// а в число разобранных кусок не идёт.
func (p *Pool) answered(n *poolNode) {
	p.mu.Lock()
	n.fails = 0
	idle := n.off
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
	idle := n.off
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
	idle := n.off
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
		alive, hopeless, why := p.left > 0, p.allBanned(), p.lastErr
		p.mu.Unlock()
		if alive {
			return nil
		}
		// Все узлы выключены до конца захода (вернулись с другой моделью):
		// ждать node_wait незачем — вернуть их в строй revive не станет.
		if hopeless {
			return fmt.Errorf("все узлы извлечения выключены до конца захода: %w", why)
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
// **Наблюдатель только показывает, работу не отбирает** (слово владельца
// 12.09.2026: «ollnode только выдаёт информацию и никак не должен влиять
// на сборку»). Прежде узел с посторонним процессом на карте выводился из
// раздачи заранее — и 12.09 это едва не стоило очереди книг: своими же
// счётчиками моделей Ollama наблюдатель считал чужую работу, единственный
// узел объявлялся занятым, а заход отменялся. Снимок теперь запоминается
// для показа человеку, а решает человек.
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
		// Ни слотов, ни отказов: снимок только запоминается.
		p.mu.Lock()
		n.last = rep
		n.note = rep.Busy(0)
		p.mu.Unlock()
	}
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
//
// **Узел сверяется с эталоном захода** (p.ref, его ставит Check): сервер,
// пролежавший пять минут, мог вернуться после `ollama pull` с другими весами
// или другим рецептом, и без сверки граф дальше собирался бы двумя моделями —
// молча. Разошёлся отпечаток — узел выключается до конца захода (banned)
// и больше не проверяется: часть захода он уже пропустил, а сервер, сменивший
// модель однажды, сменит её и снова. Отпечаток снять не удалось — узел ждёт
// следующей проверки, как не ответивший.
func (p *Pool) revive(ctx context.Context) {
	for _, n := range p.nodes {
		p.mu.Lock()
		off, early := n.off, time.Now().Before(n.nextTry) && p.left > 0
		banned, ref, refName := n.banned != "", p.ref, p.refName
		p.mu.Unlock()
		if !off || early || banned {
			continue
		}
		if err := n.ex.Check(ctx); err != nil {
			continue
		}
		if ref != nil {
			st, err := n.ex.Stamp(ctx)
			if err != nil {
				continue
			}
			if _, err := compareStamps(refName+" (при старте захода)", n.name, *ref, st); err != nil {
				p.ban(n, err)
				continue
			}
		}
		p.mu.Lock()
		if !n.off { // вернули, пока проверяли
			p.mu.Unlock()
			continue
		}
		n.off, n.fails = false, 0
		p.mu.Unlock()
		// Слоты возвращаются сразу: сервер снова отвечает, а показания
		// наблюдателя работу не отменяют (12.09.2026).
		p.restore(n)
	}
}

// ban выключает узел до конца захода: он вернулся с другой моделью, чем при
// старте. Узел уже выключен (revive берёт только такие), его слоты изъяты из
// оборота и там и остаются. Причина идёт в NodeStat.Banned, в строку хода
// и в ошибку «все узлы выключены», если других узлов не осталось.
func (p *Pool) ban(n *poolNode, err error) {
	reason := fmt.Errorf("узел %s вернулся в строй с другой моделью, чем при старте захода, "+
		"и выключен до его конца: %w", n.name, err)
	p.mu.Lock()
	if n.off && n.banned == "" {
		n.banned = reason.Error()
		p.lastErr = reason
	}
	p.mu.Unlock()
}

// allBanned — все узлы выключены до конца захода: ждать возвращения некого.
// Вызывается под замком.
func (p *Pool) allBanned() bool {
	for _, n := range p.nodes {
		if n.banned == "" {
			return false
		}
	}
	return true
}

// NodeStat — что узел сделал за заход.
type NodeStat struct {
	Name string
	Done int
	Errs int
	Off  bool
	Avg  time.Duration // средняя длительность удачного запроса

	// Busy — что наблюдатель увидел на карте узла: посторонняя работа или
	// вытесненная в оперативную память модель. **Только для показа человеку:**
	// узел с такой отметкой работает наравне с остальными (12.09.2026).
	// Пусто — наблюдателю сказать нечего или его нет вовсе.
	Busy string
	// Probe — последний снимок наблюдателя; nil, если наблюдателя нет.
	Probe *nodeprobe.Report
	// Banned — почему узел выключен до конца захода (вернулся с другой
	// моделью, чем при старте); пусто — узел не выключен навсегда. Полный
	// текст для журнала; строка хода (Line) говорит короче.
	Banned string
}

// Stats — срез по узлам для строки хода и журнала.
func (p *Pool) Stats() []NodeStat {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]NodeStat, 0, len(p.nodes))
	for _, n := range p.nodes {
		s := NodeStat{Name: n.name, Done: n.done, Errs: n.errs, Off: n.off,
			Busy: n.note, Probe: n.last, Banned: n.banned}
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
		if s.Banned != "" {
			part += " ВЫКЛ до конца захода (вернулся с другой моделью)"
		} else if s.Off {
			part += " ВЫКЛ"
		} else if s.Busy != "" {
			// Не «ждёт»: узел работает, просто на его карте есть посторонняя
			// работа. Наблюдатель сообщает, а не отменяет (12.09.2026).
			part += " карта занята (" + shortReason(s.Busy) + ")"
		} else if s.Errs > 0 {
			part += fmt.Sprintf(" ошибок %d", s.Errs)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}
