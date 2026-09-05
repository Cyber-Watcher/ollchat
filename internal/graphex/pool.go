package graphex

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
}

// poolNode — узел в работе.
type poolNode struct {
	name    string
	ex      *Extractor
	workers int

	// Под замком пула.
	done  int           // разобрано кусков
	errs  int           // неудач всего
	fails int           // неудач подряд; сбрасывается успехом
	spent time.Duration // суммарное время удачных запросов
	off   bool          // выключен из работы
}

// Pool — несколько серверов Ollama под одной моделью извлечения.
type Pool struct {
	model string
	nodes []*poolNode
	slots chan *poolNode // по Workers меток на каждый живой узел

	mu      sync.Mutex
	left    int           // сколько слотов ещё в обороте
	lastErr error         // чем умер последний узел
	dead    chan struct{} // закрыт, когда живых слотов не осталось
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
	p := &Pool{model: o.Model, dead: make(chan struct{})}
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
		p.nodes = append(p.nodes, &poolNode{name: n.Name, ex: ex, workers: w})
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
// останавливается, только когда живых узлов не осталось.
func (p *Pool) Extract(ctx context.Context, system, user string) (string, error) {
	var last error
	// Две попытки: своя и на другом узле. Больше незачем — куски, на которых
	// падают все узлы подряд, это дело сборки, она пометит их пропущенными.
	for try := 0; try < 2; try++ {
		n, err := p.take(ctx)
		if err != nil {
			if last != nil {
				return "", last
			}
			return "", err
		}
		started := time.Now()
		text, err := n.ex.Extract(ctx, system, user)
		if err == nil {
			p.ok(n, time.Since(started))
			return text, nil
		}
		// Отмена — не вина узла, и повторять её незачем.
		if ctx.Err() != nil {
			p.ok(n, time.Since(started))
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
	}
	return "", last
}

// take берёт слот. Слоты выключенных узлов изымаются из оборота здесь же:
// узел выключается, когда его метки уже разошлись по каналу.
func (p *Pool) take(ctx context.Context) (*poolNode, error) {
	for {
		select {
		case n := <-p.slots:
			p.mu.Lock()
			off := n.off
			if off {
				p.drop()
			}
			p.mu.Unlock()
			if off {
				continue
			}
			return n, nil
		case <-p.dead:
			p.mu.Lock()
			err := p.lastErr
			p.mu.Unlock()
			if err == nil {
				err = fmt.Errorf("не осталось живых узлов извлечения")
			}
			return nil, fmt.Errorf("все узлы извлечения выключены: %w", err)
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
	off := n.off
	if off {
		p.drop()
	}
	p.mu.Unlock()
	if !off {
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
		p.lastErr = err
	}
	off := n.off
	if off {
		p.drop()
	}
	p.mu.Unlock()
	if !off {
		p.slots <- n
	}
}

// drop изымает один слот из оборота. Вызывается под замком.
func (p *Pool) drop() {
	if p.left <= 0 {
		return
	}
	p.left--
	if p.left == 0 {
		close(p.dead)
	}
}

// Watch возвращает выключенные узлы в строй, пока идёт заход.
//
// Запускается вызывающим кодом отдельной горутиной и живёт до отмены ctx.
// Без неё узел, отвалившийся из-за оборванного туннеля, выпадал бы до конца
// сборки — то есть до утра.
func (p *Pool) Watch(ctx context.Context) {
	t := time.NewTicker(nodeRecheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.dead:
			return // живых не осталось, заход всё равно останавливается
		case <-t.C:
			p.revive(ctx)
		}
	}
}

// revive пробует вернуть каждый выключенный узел.
func (p *Pool) revive(ctx context.Context) {
	for _, n := range p.nodes {
		p.mu.Lock()
		off := n.off
		p.mu.Unlock()
		if !off {
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
		p.left += n.workers
		p.mu.Unlock()
		for i := 0; i < n.workers; i++ {
			p.slots <- n
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
}

// Stats — срез по узлам для строки хода и журнала.
func (p *Pool) Stats() []NodeStat {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]NodeStat, 0, len(p.nodes))
	for _, n := range p.nodes {
		s := NodeStat{Name: n.name, Done: n.done, Errs: n.errs, Off: n.off}
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
		} else if s.Errs > 0 {
			part += fmt.Sprintf(" ошибок %d", s.Errs)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}
