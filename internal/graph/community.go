package graph

import (
	"encoding/json"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Сообщества графа: разбиение Louvain по весам связей.
//
// Зачем. Плоский граф отвечает на вопрос «как связаны X и Y», но не отвечает
// на «что библиотека вообще говорит про X»: понятий тысячи, и подборка из них
// картины не даёт. Сообщество — это группа тесно связанных понятий, то есть
// тема; по нему модель пишет короткое резюме, и обзор темы собирается
// из резюме, а не из абзацев.
//
// Два уровня, как задумано в плане: мелкие сообщества (уровень 0) и их
// объединения (уровень 1). Мелкое — это «переранжирование, кросс-энкодер,
// полнота выдачи», объединение — «поиск в RAG» целиком.
//
// **Устойчивость важнее оптимальности.** Louvain — жадный алгоритм, и при
// равных приростах выбор произволен; произвол означает, что два запуска
// на одних данных дадут разные сообщества, а значит и разные резюме,
// и объяснить это пользователю будет нечем. Поэтому узлы обходятся в порядке
// возрастания номера, а при равном приросте берётся сообщество с меньшим
// номером. Цена — чуть худшее разбиение, выгода — воспроизводимость.

// CommunityFile — имя файла с сообществами внутри каталога графа.
const CommunityFile = "communities.json"

// Community — одно сообщество понятий.
type Community struct {
	ID     int `json:"id"`
	Level  int `json:"level"`  // 0 — мелкие, 1 — объединения
	Parent int `json:"parent"` // сообщество уровня выше; -1 на верхнем уровне

	// Members — понятия сообщества, по убыванию числа упоминаний: первые
	// в списке и есть то, о чём сообщество.
	Members []uint32 `json:"members"`

	// Weight — суммарный вес связей внутри сообщества. По нему видно,
	// насколько оно связное, а не просто большое.
	Weight float64 `json:"weight"`

	// Ниже — то, что заполняет модель на втором шаге этапа. Пусто, пока
	// резюме не построены.
	Title   string   `json:"title,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Key     []string `json:"key,omitempty"`
	Books   []string `json:"books,omitempty"`

	// Rating — насколько тема важна для понимания предметной области, 1..10,
	// и одним предложением почему. Приём взят у MS GraphRAG: там оценка нужна
	// глобальному поиску, который отбирает сообщества по ней, прежде чем
	// сводить их резюме в ответ. Разбор — в GraphRAGPlan.md, этап 4.
	Rating int    `json:"rating,omitempty"`
	Why    string `json:"why,omitempty"`

	// Findings — разбор темы: что именно про неё известно, в отличие от Summary,
	// который говорит, о чём она. Пятый раздел отчёта о сообществе в MS
	// GraphRAG. Считается не для всех тем и отдаётся не в обзоре, а по запросу:
	// причины и замеры — в internal/graph/findings.go.
	Findings []Finding `json:"findings,omitempty"`

	// CarriedFrom — номер темы прежнего разбиения, чьё описание сюда перенесли,
	// и насколько составы похожи. Ноль — описание писалось по этому набору.
	//
	// Пометка нужна человеку: перенесённое резюме составлялось не по этому
	// в точности набору понятий, и знать об этом он должен, а не догадываться.
	CarriedFrom int     `json:"carried_from,omitempty"`
	CarriedSim  float64 `json:"carried_sim,omitempty"`
}

// Communities — разбиение целиком, как оно лежит на диске.
type Communities struct {
	Built time.Time `json:"built"`

	// Carry — что дал перенос описаний с прежнего разбиения. Пусто, если
	// переносить было не с чего или пересчёт делался начисто.
	Carry CarryResult `json:"carry,omitempty"`

	Entities int         `json:"entities"` // сколько понятий было в графе
	Edges    int         `json:"edges"`
	List     []Community `json:"list"`
}

// Level возвращает сообщества одного уровня.
func (c *Communities) Level(n int) []Community {
	var out []Community
	for _, com := range c.List {
		if com.Level == n {
			out = append(out, com)
		}
	}
	return out
}

// Get находит сообщество по номеру.
func (c *Communities) Get(id int) (Community, bool) {
	for _, com := range c.List {
		if com.ID == id {
			return com, true
		}
	}
	return Community{}, false
}

// Of находит сообщество уровня 0, в котором лежит понятие.
func (c *Communities) Of(entity uint32) (Community, bool) {
	for _, com := range c.List {
		if com.Level != 0 {
			continue
		}
		for _, m := range com.Members {
			if m == entity {
				return com, true
			}
		}
	}
	return Community{}, false
}

// CommunityOpts — как дробить.
//
// Числа вынесены в настройку не для гибкости, а чтобы их можно было **измерить**:
// подобранные на глаз пределы на графе вдвое большего размера перестают работать,
// и проверять это надо замером, а не рассуждением.
type CommunityOpts struct {
	// Fresh — считать начисто, не перенося описания прежних тем.
	//
	// Нужен, когда прежние описания заведомо негодны: сменилась модель,
	// переписан промпт, изменилась нарезка. В обычной работе перенос выгоден.
	Fresh bool

	// CarrySimilarity — с какой доли общих понятий описание переносится.
	// 0 — семь десятых. Подбирается замером, а не назначается.
	CarrySimilarity float64

	// MaxSize — сообщество крупнее дробится заново на своём подграфе.
	// 0 — двести: столько понятий модель ещё способна назвать одной темой.
	MaxSize int

	// MaxDepth — сколько раз подряд дробить. 0 — значение по умолчанию.
	MaxDepth int

	// Resolution — множитель штрафа за размер сообщества (γ). 0 — единица,
	// обычная модулярность. Больше единицы — сообщества мельче сразу.
	Resolution float64
}

func (o CommunityOpts) norm() CommunityOpts {
	if o.MaxSize <= 0 {
		o.MaxSize = 200
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = defaultSplitDepth
	}
	if o.Resolution <= 0 {
		o.Resolution = DefaultResolution
	}
	return o
}

// BuildCommunities размечает граф на сообщества и записывает разбиение рядом
// с ним. Модель здесь не участвует: это чистая арифметика по весам связей.
func (g *Graph) BuildCommunities() (*Communities, error) {
	return g.BuildCommunitiesWith(CommunityOpts{})
}

// PartitionOnly размечает сообщества, **не записывая** разбиение на диск.
// Нужен замерам: подбор пределов не должен затирать рабочий файл.
func (g *Graph) PartitionOnly(opt CommunityOpts) (*Communities, error) {
	return g.partition(opt, false)
}

// BuildCommunitiesWith — то же с явными пределами дробления, с записью.
func (g *Graph) BuildCommunitiesWith(opt CommunityOpts) (*Communities, error) {
	return g.partition(opt, true)
}

func (g *Graph) partition(opt CommunityOpts, save bool) (*Communities, error) {
	adj, order := g.undirected()
	if len(order) == 0 {
		return nil, fmt.Errorf("в графе нет связей — разбивать нечего")
	}

	opt = opt.norm()
	small := splitLarge(adj, order, louvain(adj, order, opt.Resolution),
		opt.MaxSize, opt.MaxDepth, opt.Resolution)
	// Второй уровень: сообщества первого сворачиваются в узлы, и разбиение
	// повторяется на них.
	rolled, rolledOrder := rollUp(adj, order, small)
	big := louvain(rolled, rolledOrder, opt.Resolution)

	res := &Communities{
		Built:    time.Now(),
		Entities: g.Entities().Count(),
		Edges:    g.Edges().Count(),
	}
	res.List = g.assemble(adj, order, small, big)
	if !save {
		return res, nil
	}

	// Описания прежних тем переносятся на новые, где состав почти не изменился.
	// Без этого каждый пересчёт стоил бы полутора часов работы карты на то,
	// чтобы заново написать девять описаний из десяти — см. carry.go.
	if !opt.Fresh {
		if old, err := g.LoadCommunities(); err == nil && old != nil {
			res.Carry = carryDescriptions(old, res, opt.CarrySimilarity)
		}
	}

	if err := g.saveCommunities(res); err != nil {
		return nil, err
	}
	return res, nil
}

// LoadCommunities читает разбиение. Отсутствие файла — не ошибка: сообщества
// строятся отдельной командой и у графа их может не быть.
func (g *Graph) LoadCommunities() (*Communities, error) {
	b, err := os.ReadFile(filepath.Join(g.dir, CommunityFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c Communities
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("разбиение на сообщества не читается: %w", err)
	}
	return &c, nil
}

// PrevCommunityFile — прежнее разбиение, сохранённое перед перезаписью.
const PrevCommunityFile = "communities.prev.json"

func (g *Graph) saveCommunities(c *Communities) error {
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	path := filepath.Join(g.dir, CommunityFile)

	// Прежнее разбиение сохраняется перед перезаписью.
	//
	// **Замер 02.09.2026.** Пересчёт на выросшем графе стёр 1584 описания тем
	// и все разборы, кроме 35: преемника с совпадением состава от 70% им
	// не нашлось. Половина этой потери неизбежна — темы как группировки
	// перестали существовать, — но текст писала модель, и это двадцать минут
	// карты, выброшенных безвозвратно.
	//
	// Копия стоит одного файла на диске (14 МБ на нашем графе) и даёт три
	// возможности: посмотреть, что именно потеряно; перенести описания заново
	// с меньшим порогом, если решим, что 0.7 строг; вернуть прежнее разбиение
	// целиком, если пересчёт оказался неудачным.
	//
	// Хранится ровно одно поколение: копить их незачем, а место они занимают.
	if _, err := os.Stat(path); err == nil {
		_ = os.Rename(path, filepath.Join(g.dir, PrevCommunityFile))
	}
	return fsx.WriteFileAtomic(path, b, 0o644)
}

// undirected собирает неориентированный взвешенный граф.
//
// Связи хранятся направленными («RAG использует эмбеддинги»), но для
// разбиения направление не важно: тесно связаны — значит про одно и то же.
// Веса встречных связей складываются.
func (g *Graph) undirected() (map[uint32]map[uint32]float64, []uint32) {
	adj := map[uint32]map[uint32]float64{}
	add := func(a, b uint32, w float64) {
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
		}
		adj[a][b] += w
	}
	// Live, а не All: поглощённое понятие отдало связи выжившему, и обход
	// по всем записям реестра добавил бы их второй раз.
	for _, ent := range g.Entities().Live() {
		for _, ed := range g.Edges().Of(ent.ID) {
			w := float64(ed.Weight)
			if w <= 0 {
				w = 1
			}
			add(ed.Src, ed.Dst, w)
			add(ed.Dst, ed.Src, w)
		}
	}
	order := make([]uint32, 0, len(adj))
	for id := range adj {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	return adj, order
}

// assemble превращает две разметки в список сообществ.
//
// Участники каждого сообщества сортируются по убыванию числа упоминаний:
// первые в списке и есть то, о чём сообщество, и именно их увидит модель,
// когда будет писать резюме.
func (g *Graph) assemble(adj map[uint32]map[uint32]float64, order []uint32,
	small, big map[uint32]uint32) []Community {

	counts := map[uint32]int{}
	for _, e := range g.Entities().Live() {
		counts[e.ID] = e.Count
	}
	return g.assembleFor(adj, order, small, big, counts)
}

// assembleFor — та же сборка, но со счётчиками упоминаний, переданными
// снаружи. Отдельный вход нужен проверке: она гоняет разбиение на выдуманном
// графе, где реестра понятий нет вовсе.
func (g *Graph) assembleFor(adj map[uint32]map[uint32]float64, order []uint32,
	small, big map[uint32]uint32, counts map[uint32]int) []Community {

	members := map[uint32][]uint32{}
	for _, id := range order {
		c := small[id]
		members[c] = append(members[c], id)
	}

	ids := make([]uint32, 0, len(members))
	for c := range members {
		ids = append(ids, c)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var out []Community
	upper := map[uint32][]uint32{} // сообщество уровня 1 → его мелкие
	for _, c := range ids {
		list := members[c]
		sort.Slice(list, func(i, j int) bool {
			if counts[list[i]] != counts[list[j]] {
				return counts[list[i]] > counts[list[j]]
			}
			return list[i] < list[j] // при равных упоминаниях — по номеру
		})
		parent := -1
		if p, ok := big[c]; ok {
			parent = upperID(ids, p)
			upper[p] = append(upper[p], c)
		}
		out = append(out, Community{
			ID: int(c), Level: 0, Parent: parent,
			Members: list, Weight: inner(adj, list),
		})
	}

	// Верхний уровень: участниками записываются понятия всех вложенных
	// мелких сообществ — так обзор темы можно собрать, не спускаясь вниз.
	ups := make([]uint32, 0, len(upper))
	for p := range upper {
		ups = append(ups, p)
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i] < ups[j] })
	for _, p := range ups {
		var all []uint32
		for _, c := range upper[p] {
			all = append(all, members[c]...)
		}
		sort.Slice(all, func(i, j int) bool {
			if counts[all[i]] != counts[all[j]] {
				return counts[all[i]] > counts[all[j]]
			}
			return all[i] < all[j]
		})
		out = append(out, Community{
			ID: upperID(ids, p), Level: 1, Parent: -1,
			Members: all, Weight: inner(adj, all),
		})
	}
	return out
}

// inner — суммарный вес связей внутри группы понятий.
func inner(adj map[uint32]map[uint32]float64, list []uint32) float64 {
	in := map[uint32]bool{}
	for _, id := range list {
		in[id] = true
	}
	var sum float64
	for _, id := range list {
		for nb, w := range adj[id] {
			if in[nb] {
				sum += w
			}
		}
	}
	return sum / 2 // каждая связь посчитана с обоих концов
}

// upperID даёт объединению верхнего уровня номер, не совпадающий ни с одним
// мелким сообществом.
//
// Оба уровня нумеруются с нуля независимо, и без сдвига номера накладываются:
// «сообщество №5» означает разное на разных уровнях. Поймано на живом графе —
// обзор выдал тему на 1 715 понятий при пределе в 200, подставив объединение
// вместо мелкого сообщества с тем же номером.
func upperID(small []uint32, p uint32) int {
	var max uint32
	for _, c := range small {
		if c > max {
			max = c
		}
	}
	return int(max) + 1 + int(p)
}
