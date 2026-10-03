// Пакет stats — исследовательские счёты по готовому графу: на процессоре,
// только чтение, карта не нужна. Вызываются из ollchat одним ключом:
//
//	ollchat --graph-stats books                          подтверждения связей и PageRank
//	ollchat --graph-stats books -- -only rank -top 40
//	ollchat --graph-stats books -- -hubs
//
// Свои ключи идут ПОСЛЕ «--»: иначе их разбирает ollchat и отказывает.
//
// **История.** До 29.09.2026 это была отдельная программа
// `privatescripts/graphstats` (30 счётов, бинарь 16,8 МБ). Бинарь в `~/bin`
// не собирался, прибор не находили и писали заново — с ошибкой. Слово
// владельца 29.09.2026: «конечно ключом, с хуя ли отдельный бинарь», и
// затем — перенести все счёты в ollchat целиком. Счёт «что разобрано по
// книгам» (бывший `-books`) живёт отдельно: `--graph-status … --graph-books`.
//
// **Зачем.** Этап 101, пункты A3 и A4.
//
// A3 — распределение подтверждений на связь. Книги («Neo4j: The Definitive
// Guide», стр. 354–355) строят co-occurrence-ребро по ПОРОГУ пересечения,
// а у нас связь рождается от одного совместного упоминания в куске. Именно
// это дало «всё со всем» на оглавлениях (этап 99). Прежде чем вводить порог,
// надо знать, сколько связей держится на единственном подтверждении.
//
// A4 — PageRank. Замер показал: 6.8% понятий держат 61.6% связей, то есть
// у графа есть ядро. «Agentic GraphRAG» (стр. 466, разд. 17) описывает
// продакшн-паттерн: считать центральность отдельной фазой и класть результат
// обратно в граф. Здесь — только счёт и сравнение с числом степеней: стоит ли
// вообще заводить поле у понятия.
//
// Ничего не меняет: открывает граф на чтение и печатает числа.
package stats

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// stop — ошибка, поднятая из глубины счёта через die: у счётов нет
// обратного пути для ошибки (тридцать функций писались как разовые замеры
// с выходом из программы), и переписывать их все ради одного возврата —
// дороже, чем поймать здесь.
type stop struct{ err error }

// die прерывает счёт с ошибкой; ловится в Run.
func die(err error) {
	if err != nil {
		panic(stop{err})
	}
}

// Run выполняет один счёт по графу коллекции. args — ключи счёта
// (после «--» в строке ollchat), разбираются своим набором.
func Run(stdout io.Writer, cfg *config.Config, collName string, args []string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if st, ok := r.(stop); ok {
				err = st.err
				return
			}
			panic(r)
		}
	}()
	run(stdout, cfg, collName, args)
	return nil
}

func run(stdout io.Writer, cfg *config.Config, collName string, args []string) {
	fs := flag.NewFlagSet("graph-stats", flag.ContinueOnError)
	fs.SetOutput(stdout)
	coll := &collName
	only := fs.String("only", "", "edges | rank — считать что-то одно")
	chains := fs.String("chains", "", "замер цепочек по набору пар понятий: -chains docs/eval/graph_relations.toml")
	doubles := fs.Bool("doubles", false, "где ловятся двойники: связывание при сборке против ночного разбора")
	erq := fs.Bool("erq", false, "ERQ: согласие порога близости с разметкой doubles-judged.tsv (граф не открывается)")
	leidenCmp := fs.Bool("leiden", false, "Лувен против Лейдена на одном графе: темы, модулярность, несвязные, уровни, согласие (этап 90, Н3)")
	resGrid := fs.String("resolution-grid", "", "калибровка γ Louvain по сетке значений: -resolution-grid 1,2,3,4,5,6,8 (этап 103, Ш1)")
	methodSet := fs.String("methodeval", "", "замер входа на методологических вопросах: -methodeval docs/eval/graph_method_questions.toml (этап 89)")
	domain := fs.String("domainnoise", "", "Н10: откуда подтверждения связей у названных понятий: -domainnoise 'RAG,GraphRAG,Knowledge graph'")
	domainWant := fs.String("domainnoise-folder", "", "с -domainnoise, -overvieweval, -topiceval, -localityeval, -methodeval: какой каталог библиотеки считать своим (обязателен)")
	localityEv := fs.String("localityeval", "", "замер формул локальности выдачи связей: -localityeval docs/eval/graph_method_questions.toml (этап 89)")
	localityPairs := fs.String("localityeval-pairs", "docs/eval/graph_relations.toml", "с -localityeval: набор пар «как связаны X и Y» — проверка, что полезное не пропало")
	localityPool := fs.Int("localityeval-pool", 4, "с -localityeval: во сколько раз шире брать связи перед пересортировкой")
	impact := fs.String("bookimpact", "", "сколько графа держится на этих книгах: -bookimpact 'Книга А,Книга Б' (этап 104)")
	evPick := fs.Int("evidencepick", 0, "что попадает в показанные выдержки связи: -evidencepick 1500 (этап 104, П5.2)")
	typeUse := fs.String("typeuse", "", "нужны ли в графе понятия этих типов: -typeuse человек,организация (этап 104, П2.3)")
	ageQN := fs.Int("agequality", 0, "стареет ли качество связей: -agequality 4000 (этап 104, П10.2)")
	nameN := fs.Int("namecheck", 0, "цена проверки имён понятий по тексту куска: -namecheck 4000 (этап 104, П6.3)")
	onceN := fs.Int("oncecheck", 0, "чем связи с одним подтверждением отличаются от многократных: -oncecheck 4000 (этап 104, П1.1)")
	ageN := fs.Int("agecheck", 0, "возраст знания: каким годом подтверждены связи: -agecheck 3000 (этап 104, П10)")
	ageOld := fs.Int("agecheck-old", 2023, "с -agecheck: год, старше которого подтверждение считается устаревшим")
	provN := fs.Int("provenance", 0, "целостность провенанса: у скольких связей выборки живой кусок-источник: -provenance 2000")
	provShow := fs.Int("provenance-show", 0, "с -provenance: показать N неподтверждённых связей С ТЕКСТОМ куска (для разбора глазами, П6.1)")
	aliasChk := fs.Int("aliascheck", 0, "что изменит перевод сверки синонимов при сборке на SeenInText: -aliascheck 20000 (этап 104, А3.4)")
	beyond := fs.Bool("beyondshown", false, "как часто нужная связь есть в графе, но не попала в выдачу: -beyondshown (этап 104, П3.1)")
	staleYear := fs.Int("stalerels", 0, "связи технологий, подтверждённые только книгами не новее года: -stalerels 2021 (этап 104, П10.3)")
	relTypesOf := fs.String("reltypes", "", "покрытие типов связей по всему графу и по каталогу: -reltypes /Раздел (паспорт опытного графа, решение 13)")
	cohGrid := fs.String("cohesiongrid", "", "при каком γ понятия одного вопроса попадают в одну тему: -cohesiongrid 0.1,0.3,1,2,5 (этап 104, П9.2)")
	evRule := fs.Int("evidencerule", 0, "какое правило отбора выдержки связи лучше: -evidencerule 1500 (этап 104, П5.3)")
	oldSample := fs.Bool("oldsample", false, "с -namecheck и -evidencepick: ПРЕЖНЯЯ выборка «понятие, затем его запись» — для сравнения «до/после» одним бинарём")
	provSeed := fs.Int64("provenance-seed", 20260916, "с -provenance: зерно выборки")
	tripleEv := fs.String("tripleeval", "", "находится ли сама связь X↔Y среди ближайших троек к вектору вопроса: -tripleeval docs/eval/graph_relations_softarch.toml (этап 105, В1)")
	tripleK := fs.Int("tripleeval-k", 5, "с -tripleeval: сколько ближайших троек смотреть")
	overviewEv := fs.String("overvieweval", "", "обзор тем против входа по понятиям на тематических вопросах: -overvieweval docs/eval/graph_method_questions.toml (Н12)")
	overviewTop := fs.Int("overvieweval-topics", 5, "с -overvieweval: сколько тем брать в обзор")
	topicEv := fs.String("topiceval", "", "тема как признак уместности понятия входа: -topiceval docs/eval/graph_method_questions.toml (этап 89)")
	topicLevel := fs.Int("topiceval-level", 0, "с -topiceval: уровень разбиения (0 — мелкие темы, 1 — объединения)")
	aliasNoiseN := fs.Int("aliasnoise", 0, "ложные раскрытия аббревиатур в синонимах: -aliasnoise 30 (этап 103, Ш3.5)")
	seedsOff := fs.Bool("seed-relations-off", false, "открыть граф БЕЗ подъёма связей между понятиями вопроса — для сравнения «до/после» одним бинарём")
	singles := fs.Bool("singletons", false, "одиночные темы: откуда и есть ли куда приклеить (этап 103, Ш1.3а)")
	vecSampleN := fs.Int("vecsample", 0, "оценка по выборке: сколько похожих пар отбор не видит: -vecsample 1500 (этап 103, Ш3.1а)")
	vecSampleCos := fs.Float64("vecsample-min-cos", 0.95, "с -vecsample: порог близости")
	vecSampleSeed := fs.Int64("vecsample-seed", 20260914, "с -vecsample: зерно выборки")
	vecSampleShow := fs.Int("vecsample-show", 15, "с -vecsample: сколько примеров невидимых пар показать")
	erqMin := fs.Float64("erq-threshold", 0, "с -erq: порог близости; 0 — взять graph.link_min_cos (умолчание 0.85)")
	cap := fs.String("cap", "", "замер предела «N связей на узел» по набору пар: -cap docs/eval/graph_relations.toml")
	partexp := fs.Bool("partition", false, "опыт: разбиение на всех связях против разбиения без нетипизированных «связано»")
	corr := fs.String("corroboration", "", "Г9: подтверждения связей по источникам, а не кускам; значение — файл пар «оригинал<TAB>перевод» (docs/eval/works-pairs.tsv) или «-» без пар")
	originpart := fs.Bool("originpartition", false, "Г9, шаг 2: разбиение тем на весах по источникам против весов по кускам, сравнение составов")
	typecheck := fs.Bool("typecheck", false, "Г10: тройки «тип источника — связь — тип цели» с редкими сочетаниями")
	summarycheck := fs.Int("summarycheck", 0, "Г11: чужие понятия в описаниях тем: -summarycheck 20 (сколько примеров)")
	orphans := fs.Int("orphans", 0, "разбор понятий без единой связи: -orphans 30 (сколько примеров показать)")
	hubs := fs.Bool("hubs", false, "распределение степеней: какой доле верхушки отвечает нынешний порог хаба (этап 101, Г3)")
	hubList := fs.Bool("hublist", false, "выписать хабы с контекстом для описаний: имя, синонимы, соседи, выдержки (этап 103, Ш2)")
	hubOut := fs.String("hublist-out", "", "с -hublist: файл TSV (пусто — только верхушка на экран)")
	hubMinDeg := fs.Int("hublist-mindeg", 0, "с -hublist: порог связей; 0 — graph.chain_hub_limit (умолчание 500)")
	hubLimit := fs.Int("hublist-limit", 0, "с -hublist: сколько хабов взять, 0 — все")
	hubQuotes := fs.Int("hublist-quotes", 3, "с -hublist: сколько выдержек из книг на хаб")
	hubQuoteLen := fs.Int("hublist-quote-len", 600, "с -hublist: предел длины выдержки в знаках")
	minw := fs.Bool("minweight", false, "опыт: разбиение с порогом веса связи — все связи против w≥2 и w≥3 (этап 101, Г2)")
	topics := fs.Int("topics", 0, "сравнить имя темы от модели с центральным понятием по PageRank: -topics 200")
	between := fs.Int("between", 0, "приближённый betweenness по N случайным источникам: -between 300")
	ppr := fs.String("ppr", "", "замер персонализированного ранга по набору пар: -ppr docs/eval/graph_relations.toml")
	hops := fs.Int("hops", 3, "с -chains: предел длины цепочки")
	chainsList := fs.Bool("chains-list", false, "с -chains: печатать построчно каждую пару — прямая (типы), цепочка, нет")
	chainsFolder := fs.String("chains-folder", "", "с -chains: прямой считать только связь, подтверждённую куском из этого каталога (/Раздел): честное сравнение с графом, собранным по одному каталогу")
	hubMeas := fs.String("hubmeasure", "", "какой мерой считать «хаб» в цепочках: -hubmeasure docs/eval/graph_path_log.toml (этап 105, Б5)")
	hubMeasLimit := fs.Int("hubmeasure-limit", 0, "с -hubmeasure: порог хаба (0 — из настроек, умолчание 500)")
	top := fs.Int("top", 25, "сколько понятий показать в верхушке PageRank")
	iters := fs.Int("iters", 20, "итераций PageRank")
	die(fs.Parse(args))
	if fs.NArg() > 0 {
		die(fmt.Errorf("лишние слова после ключей: %v", fs.Args()))
	}

	// ERQ читает один текстовый файл рядом с графом — ни база, ни граф ему
	// не нужны. Рабочий граф не открывается вовсе: правило владельца
	// 12.09.2026 — беречь его всеми средствами.
	if *erq {
		minCos := *erqMin
		if minCos <= 0 {
			minCos = cfg.Graph.LinkMinCos
		}
		if minCos <= 0 {
			minCos = 0.85 // умолчание кода, см. config.Graph.LinkMinCos
		}
		collDir := filepath.Join(config.ExpandPath(cfg.KB.Dir), "collections", *coll)
		erqStats(filepath.Join(collDir, graph.DirFor(cfg.Graph.Rules().Name)), minCos)
		return
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	die(err)
	defer base.Close()
	c, err := base.Open(*coll)
	die(err)
	rules := cfg.Graph.Rules()
	rules.SeedRelationsOff = *seedsOff
	g, err := graph.Open(c.Dir(), c.ChunkCount(), rules)
	die(err)
	defer g.Close()

	if *hubMeas != "" {
		hubMeasure(g, *hubMeas, *hops, *hubMeasLimit)
		return
	}
	if *chains != "" {
		var onlyDocs map[uint32]bool
		if *chainsFolder != "" {
			onlyDocs = map[uint32]bool{}
			for _, b := range c.MatchingDocs(kb.ChunkFilter{Folder: *chainsFolder}) {
				onlyDocs[b.ID] = true
			}
		}
		chainStats(g, *chains, *hops, *chainsList, onlyDocs)
		return
	}
	if *doubles {
		doubleStats(g)
		return
	}
	if *impact != "" {
		bookImpact(g, c, strings.Split(*impact, ","))
		return
	}
	if *aliasChk > 0 {
		aliasCheck(g, c, *aliasChk, 20260917)
		return
	}
	if *evRule > 0 {
		evidenceRule104(cfg, g, c, *evRule, 20260917,
			[]string{"docs/eval/graph_method_questions.toml", "docs/eval/graph_relations.toml"})
		return
	}
	if *evPick > 0 {
		// Правила — открытого графа: в них умолчание уже подставлено. Сырое
		// значение из конфига без max_evidences — ноль, и «показанных» не было бы.
		evidencePick(g, c, *evPick, g.Rules().MaxEvidences, 20260917, *oldSample)
		return
	}
	if *typeUse != "" {
		typeUsefulness(g, strings.Split(*typeUse, ","), 3000, 12, 20260917)
		return
	}
	if *ageQN > 0 {
		ageQuality(g, c, *ageQN, *ageOld, 20260916)
		return
	}
	if *nameN > 0 {
		nameCheck(g, c, *nameN, 20260916, *oldSample)
		return
	}
	if *onceN > 0 {
		onceCheck(g, c, *onceN, 20260916)
		return
	}
	if *ageN > 0 {
		ageCheck(g, c, *ageN, *ageOld, 20260916)
		return
	}
	if *provN > 0 {
		showText = *provShow
		provenanceEval(g, c, *provN, *provSeed)
		return
	}
	if *overviewEv != "" {
		needFolder(*domainWant)
		overviewEval(cfg, g, c, *overviewEv, *domainWant, *overviewTop)
		return
	}
	if *tripleEv != "" {
		tripleEval(cfg, c, g, *tripleEv, *tripleK)
		return
	}
	if *topicEv != "" {
		needFolder(*domainWant)
		topicEval(cfg, g, c, *topicEv, *domainWant, *topicLevel)
		return
	}
	if *aliasNoiseN > 0 {
		aliasNoise(g, *aliasNoiseN)
		return
	}
	if *localityEv != "" {
		needFolder(*domainWant)
		localityEval(cfg, g, c, *localityEv, *localityPairs, *domainWant, *localityPool)
		return
	}
	if *methodSet != "" {
		needFolder(*domainWant)
		methodEval(cfg, g, c, *methodSet, *domainWant)
		return
	}
	if *domain != "" {
		var names []string
		for _, n := range strings.Split(*domain, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		needFolder(*domainWant)
		domainNoise(g, c, names, *domainWant)
		return
	}
	if *vecSampleN > 0 {
		vecSample(g, *vecSampleN, *vecSampleCos, *vecSampleSeed, *vecSampleShow)
		return
	}
	if *singles {
		singletonStats(g)
		return
	}
	if *beyond {
		beyondShown(cfg, g, c, []string{"docs/eval/graph_relations.toml", "docs/eval/graph_relations_softarch.toml"})
		return
	}
	if *staleYear > 0 {
		staleRels(g, c, *staleYear, 3, 150)
		return
	}
	if *relTypesOf != "" {
		relTypes(g, c, *relTypesOf)
		return
	}
	if *cohGrid != "" {
		grid, err := parseGrid(*cohGrid)
		die(err)
		cur := cfg.Graph.Resolution
		if cur <= 0 {
			cur = 5
		}
		cohesionGrid(g, c, grid, cur, cfg.Graph.Rules().RelatedWeightOr(), cfg.Graph.WeightsByOrigins,
			"docs/eval/graph_relations.toml", "docs/eval/graph_method_questions.toml", 20260917)
		return
	}
	if *leidenCmp {
		cur := cfg.Graph.Resolution
		if cur <= 0 {
			cur = 5.0
		}
		leidenCompare(g, cur, cfg.Graph.Rules().RelatedWeightOr(), cfg.Graph.WeightsByOrigins)
		return
	}
	if *resGrid != "" {
		grid, err := parseGrid(*resGrid)
		die(err)
		cur := cfg.Graph.Resolution
		if cur <= 0 {
			cur = 5.0 // умолчание кода, см. config.Graph.Resolution
		}
		resolutionGrid(g, grid, cur, cfg.Graph.Rules().RelatedWeightOr(), cfg.Graph.WeightsByOrigins)
		return
	}
	if *ppr != "" {
		pprStats(g, *ppr)
		return
	}
	if *cap != "" {
		capStats(g, *cap)
		return
	}
	if *between > 0 {
		betweenStats(g, *between)
		return
	}
	if *topics > 0 {
		topicStats(g, *topics)
		return
	}
	if *partexp {
		partitionExperiment(g)
		return
	}
	if *minw {
		weightExperiment(g)
		return
	}
	if *hubList {
		hubListStats(g, c, hubListOpts{MinDeg: *hubMinDeg, Limit: *hubLimit,
			Quotes: *hubQuotes, QuoteLen: *hubQuoteLen, Out: *hubOut})
		return
	}
	if *hubs {
		hubStats(g)
		return
	}
	if *orphans > 0 {
		orphanStats(g, *orphans)
		return
	}
	if *corr != "" {
		path := *corr
		if path == "-" {
			path = ""
		}
		corroborationStats(c, g, path)
		return
	}
	if *typecheck {
		typecheckStats(g)
		return
	}
	if *originpart {
		originPartitionStats(g)
		return
	}
	if *summarycheck > 0 {
		summarycheckStats(g, *summarycheck)
		return
	}

	// Live, а не All: поглощённое склейкой понятие отдаёт через Of связи своего
	// выжившего, и с All каждая связь выжившего считалась столько раз, сколько
	// узлов он поглотил, плюс один — «на одном подтверждении» занижалось,
	// а сами поглощённые шли в «без соседей».
	ents := g.Entities().Live()
	fmt.Printf("коллекция %s: понятий %d, связей %d\n", *coll, len(ents), g.Edges().Count())

	// Рёбра берутся публичным обходом по каждому понятию: так они приходят
	// уже со снятыми склейками, то есть ровно такими, какими их видит поиск.
	conf := make(map[edgeKey]int, len(ents)*4)
	for _, e := range ents {
		for _, ed := range g.Edges().Of(e.ID) {
			conf[edgeKey{ed.Src, ed.Dst, ed.Type}]++
		}
	}

	if *only != "rank" {
		edgeStats(conf)
	}
	if *only != "edges" {
		pageRank(g, ents, conf, *iters, *top)
	}
	if *only == "" || *only == "threshold" {
		thresholdStats(ents, conf)
	}
}

// needFolder — счёт делит связи на «свои» и «чужие» по каталогу, и без
// названного каталога считать нечего. Умолчания нет нарочно: имя каталога
// чужой библиотеки в коде — это её устройство, зашитое в чужую программу.
func needFolder(folder string) {
	if strings.TrimSpace(folder) == "" {
		die(fmt.Errorf("укажите каталог библиотеки: -domainnoise-folder /Раздел"))
	}
}

// edgeKey — связь без учёта подтверждения: два конца и вид.
type edgeKey struct {
	src, dst uint32
	typ      uint8
}

// edgeStats — A3: на скольких подтверждениях держатся связи.
//
// Число подтверждений — это сколько разных кусков дали одну и ту же связь.
// Связь с единственным подтверждением может быть и настоящей, и случайным
// соседством двух понятий в одном абзаце; отличить их можно только по тому,
// сколько таких в графе и что они несут.
func edgeStats(conf map[edgeKey]int) {
	if len(conf) == 0 {
		fmt.Println("связей нет")
		return
	}
	hist := map[int]int{}
	total, sum := 0, 0
	for _, n := range conf {
		hist[n]++
		total++
		sum += n
	}
	fmt.Printf("\nA3. Подтверждения на связь: различных связей %d, подтверждений всего %d\n",
		total, sum)
	counts := make([]int, 0, len(hist))
	for n := range hist {
		counts = append(counts, n)
	}
	sort.Ints(counts)
	var acc int
	for _, n := range counts {
		acc += hist[n]
		label := fmt.Sprintf("%d", n)
		if n >= 10 {
			continue // хвост печатаем одной строкой ниже
		}
		fmt.Printf("  подтверждений %-3s связей %8d  (%5.1f%%, накопленно %5.1f%%)\n",
			label, hist[n], 100*float64(hist[n])/float64(total), 100*float64(acc)/float64(total))
	}
	var tail, tailEdges int
	for _, n := range counts {
		if n >= 10 {
			tail++
			tailEdges += hist[n]
		}
	}
	fmt.Printf("  подтверждений 10+  связей %8d  (%5.1f%%), различных значений %d\n",
		tailEdges, 100*float64(tailEdges)/float64(total), tail)
	fmt.Printf("  на одном подтверждении держится %.1f%% связей — это и есть цена порога\n",
		100*float64(hist[1])/float64(total))
}

// partitionExperiment — Ф1: что даст разбиение без нетипизированных связей.
//
// Книга («Graph-Powered Machine Learning», Negro, стр. 451) считает сообщества
// на отдельном подграфе нужных связей, «ignoring all the rest». У нас Louvain
// идёт по всему графу разом, включая «связано» — а это самый шумный вид связи:
// он ставится, когда модель не смогла назвать связь точнее.
//
// Ничего не меняет: считает два разбиения и печатает, чем они отличаются.
func partitionExperiment(g *graph.Graph) {
	fmt.Println("\nФ1. Разбиение на всех связях против разбиения без «связано»")
	fmt.Printf("  %-22s %10s %10s %8s %11s %9s %8s\n",
		"опыт", "понятий", "записей", "тем", "из одного", "крупнейшая", "медиана")

	show := func(label string, r graph.PartitionExperiment) {
		fmt.Printf("  %-22s %10d %10d %8d %11d %9d %8d\n",
			label, r.Nodes, r.Edges, r.Themes, r.Singleton, r.Largest, r.Median)
	}
	full := g.ExperimentPartition(graph.PartitionOpts{})
	show("все связи (как сейчас)", full)
	half := g.ExperimentPartition(graph.PartitionOpts{Weights: map[uint8]float64{graph.RelRelated: 0.5}})
	show("«связано» вполсилы", half)
	typed := g.ExperimentPartition(graph.PartitionOpts{Weights: map[uint8]float64{graph.RelRelated: 0}})
	show("без «связано»", typed)

	fmt.Printf("\n  крупнейшие темы, все связи:     %v\n", full.Sizes[:min(8, len(full.Sizes))])
	fmt.Printf("  крупнейшие темы, вполсилы:      %v\n", half.Sizes[:min(8, len(half.Sizes))])
	fmt.Printf("  крупнейшие темы, без «связано»: %v\n", typed.Sizes[:min(8, len(typed.Sizes))])
	if full.Edges > 0 {
		fmt.Printf("  «связано» — %.1f%% всех связей\n",
			100*float64(full.Edges-typed.Edges)/float64(full.Edges))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// topicStats — совпадает ли имя темы с её центральным понятием (этап 101, E2).
//
// **Зачем.** Имена темам даёт модель по составу — это самый дорогой шаг докатки
// (2–6 минут карты на книгу). Книга («Graph Algorithms for Data Science»,
// стр. 219) говорит, что PageRank внутри co-occurrence-сети находит
// представителей сообществ. Если центральное понятие темы и так совпадает
// с названием, которое дала модель, — модель делает работу, которую можно
// посчитать даром.
//
// Меряем на темах нижнего уровня с описанием: доля тем, где имя от модели
// совпадает с самым центральным понятием (или входит в тройку центральных).
func topicStats(g *graph.Graph, limit int) {
	var doc struct {
		List []struct {
			Level   int      `json:"level"`
			Members []uint32 `json:"members"`
			Title   string   `json:"title"`
		} `json:"list"`
	}
	f, err := os.Open(filepath.Join(g.Dir(), "communities.json"))
	if err != nil {
		die(err)
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		die(err)
	}

	name := func(id uint32) string {
		if e, ok := g.Entities().Get(id); ok {
			return e.Name
		}
		return ""
	}
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

	var checked, exact, inTop3, contains int
	var examples []string
	for _, t := range doc.List {
		if checked >= limit {
			break
		}
		if t.Title == "" || len(t.Members) < 4 {
			continue
		}
		// Центральность внутри темы: вес растекается только по её членам.
		inside := make(map[uint32]bool, len(t.Members))
		for _, id := range t.Members {
			inside[id] = true
		}
		rank := make(map[uint32]float64, len(t.Members))
		for _, id := range t.Members {
			rank[id] = 1
		}
		for step := 0; step < 5; step++ {
			next := make(map[uint32]float64, len(t.Members))
			for id, w := range rank {
				var ns []uint32
				for _, n := range g.Edges().Neighbors(id) {
					if inside[n.ID] {
						ns = append(ns, n.ID)
					}
				}
				if len(ns) == 0 {
					continue
				}
				share := 0.85 * w / float64(len(ns))
				for _, n := range ns {
					next[n] += share
				}
			}
			for id := range rank {
				rank[id] = 0.15 + next[id]
			}
		}
		order := append([]uint32(nil), t.Members...)
		sort.Slice(order, func(i, j int) bool { return rank[order[i]] > rank[order[j]] })

		checked++
		// Модель называет тему фразой («Разработка и управление API»), а
		// центральное понятие — одним словом («API»). Строгое равенство такие
		// случаи теряет, поэтому считаем и вхождение.
		if strings.Contains(norm(t.Title), norm(name(order[0]))) && name(order[0]) != "" {
			contains++
		}
		if norm(name(order[0])) == norm(t.Title) {
			exact++
			inTop3++
			continue
		}
		hit := false
		for i := 0; i < 3 && i < len(order); i++ {
			if norm(name(order[i])) == norm(t.Title) {
				hit = true
				break
			}
		}
		if hit {
			inTop3++
		} else if len(examples) < 5 {
			examples = append(examples, fmt.Sprintf("тема «%s» — центральное «%s» (членов %d)",
				cut(t.Title, 30), cut(name(order[0]), 30), len(t.Members)))
		}
	}

	fmt.Printf("\nE2. Имя темы против её центрального понятия (тем проверено %d)\n", checked)
	fmt.Printf("  совпало с самым центральным: %d (%.1f%%)\n", exact, pct(exact, checked))
	fmt.Printf("  попало в тройку центральных:  %d (%.1f%%)\n", inTop3, pct(inTop3, checked))
	fmt.Printf("  имя темы СОДЕРЖИТ центральное: %d (%.1f%%)\n", contains, pct(contains, checked))
	if len(examples) > 0 {
		fmt.Println("  где расходится:")
		for _, e := range examples {
			fmt.Println("    ·", e)
		}
	}
}

// betweenStats — совпадают ли «мосты» с «хабами» (этап 101, E3).
//
// **Зачем.** Цепочкам между понятиями мы запретили ходить через узлы с большой
// СТЕПЕНЬЮ. Книга («Graph Algorithms for Data Science», стр. 222, 239) говорит,
// что мосты между сообществами — это betweenness, а степень означает лишь
// популярность. Если множества расходятся, запрет режет как раз те узлы,
// которые и объясняют связь далёких тем.
//
// Считается приближённо, по выборке источников (Brandes на подмножестве):
// точный betweenness на 225 тысячах узлов — часы, а нам нужен порядок величин
// и пересечение верхушек, а не абсолютные числа.
func betweenStats(g *graph.Graph, sources int) {
	// Live: поглощённый склейкой узел получил бы соседей выжившего и встал
	// бы в верхушку его двойником.
	ents := g.Entities().Live()
	if len(ents) == 0 {
		fmt.Println("\nE3. Мосты против хабов: в графе нет понятий — считать нечего")
		return
	}
	idx := make(map[uint32]int, len(ents))
	for i, e := range ents {
		idx[e.ID] = i
	}
	adj := make([][]int, len(ents))
	for i, e := range ents {
		// Neighbors отдаёт соседа дважды, если связь идёт в обе стороны
		// (ключ — сосед и направление). В списке смежности он нужен один раз:
		// иначе число кратчайших путей через такую пару удваивается.
		seen := map[int]bool{}
		for _, n := range g.Edges().Neighbors(e.ID) {
			if j, ok := idx[n.ID]; ok && j != i && !seen[j] {
				seen[j] = true
				adj[i] = append(adj[i], j)
			}
		}
	}

	bc := make([]float64, len(ents))
	rnd := rand.New(rand.NewSource(20260908))
	for s := 0; s < sources; s++ {
		src := rnd.Intn(len(ents))
		// Brandes: обход в ширину, счёт кратчайших путей, накопление вклада.
		sigma := make(map[int]float64, 1024)
		dist := make(map[int]int, 1024)
		var order []int
		preds := make(map[int][]int, 1024)
		sigma[src], dist[src] = 1, 0
		queue := []int{src}
		for len(queue) > 0 {
			v := queue[0]
			queue = queue[1:]
			order = append(order, v)
			for _, w := range adj[v] {
				if _, seen := dist[w]; !seen {
					dist[w] = dist[v] + 1
					queue = append(queue, w)
				}
				if dist[w] == dist[v]+1 {
					sigma[w] += sigma[v]
					preds[w] = append(preds[w], v)
				}
			}
			if len(order) > 200000 { // дальше вклад пренебрежимо мал
				break
			}
		}
		delta := make(map[int]float64, len(order))
		for i := len(order) - 1; i >= 0; i-- {
			w := order[i]
			for _, v := range preds[w] {
				delta[v] += sigma[v] / sigma[w] * (1 + delta[w])
			}
			if w != src {
				bc[w] += delta[w]
			}
		}
	}

	top := func(vals []float64, n int) []int {
		ord := make([]int, len(vals))
		for i := range ord {
			ord[i] = i
		}
		sort.Slice(ord, func(i, j int) bool { return vals[ord[i]] > vals[ord[j]] })
		if len(ord) > n {
			ord = ord[:n]
		}
		return ord
	}
	deg := make([]float64, len(ents))
	for i := range ents {
		deg[i] = float64(len(adj[i]))
	}

	const n = 25
	byBetween, byDeg := top(bc, n), top(deg, n)
	inDeg := make(map[int]bool, n)
	for _, i := range byDeg {
		inDeg[i] = true
	}
	same := 0
	for _, i := range byBetween {
		if inDeg[i] {
			same++
		}
	}

	fmt.Printf("\nE3. Мосты против хабов (источников %d, понятий %d)\n", sources, len(ents))
	fmt.Printf("  верхушка %d по betweenness совпадает с верхушкой по степени: %d из %d\n", n, same, n)
	fmt.Printf("\n  %-40s %14s %10s\n", "понятие (по betweenness)", "betweenness", "связей")
	for _, i := range byBetween[:min(10, len(byBetween))] {
		fmt.Printf("  %-40s %14.0f %10.0f\n", cut(ents[i].Name, 38), bc[i], deg[i])
	}
	// Сколько мостов попало бы под наш запрет по степени (500 связей).
	blocked := 0
	for _, i := range byBetween {
		if deg[i] >= 500 {
			blocked++
		}
	}
	fmt.Printf("\n  из верхушки мостов под запрет «хаб ≥500 связей» попадает %d из %d\n", blocked, n)
}

// capStats — что даёт предел «не больше N связей на узел» (этап 101, E1).
//
// **Зачем.** Глобальный порог по подтверждениям отвергнут замером: он оставлял
// без соседей 61.4% понятий. Книга («Graph Algorithms for Data Science»,
// Bratanic, стр. 219) предлагает другой параметр — предел числа связей на узел.
// У него та беда невозможна по устройству: узел без соседей не остаётся, а
// «всё со всем» у частых понятий обрезается.
//
// Меряем две вещи: сколько связей отсекается и не теряются ли нужные — то есть
// остаётся ли второе понятие вопроса среди N лучших соседей первого.
func capStats(g *graph.Graph, path string) {
	var set struct {
		Case []struct {
			ConceptA string `toml:"concept_a"`
			ConceptB string `toml:"concept_b"`
		} `toml:"case"`
	}
	if _, err := toml.DecodeFile(path, &set); err != nil {
		die(err)
	}
	// Live: с All связи выжившего считались и за каждый поглощённый им узел.
	ents := g.Entities().Live()

	caps := []int{4, 8, 16, 32, 64}
	fmt.Printf("\nE1. Предел «N связей на узел» (понятий %d, пар в наборе %d)\n", len(ents), len(set.Case))
	fmt.Printf("  %-8s %14s %12s %16s\n", "предел", "связей всего", "отсечено", "пар сохранено")

	// Всего связей — сумма по узлам (каждая считается дважды, как и при отборе).
	total := 0
	for _, e := range ents {
		total += len(g.Edges().Neighbors(e.ID))
	}

	for _, n := range caps {
		kept := 0
		for _, e := range ents {
			d := len(g.Edges().Neighbors(e.ID))
			if d > n {
				d = n
			}
			kept += d
		}
		// Сохраняются ли связи, о которых спрашивает набор.
		pairs, ok := 0, 0
		for _, c := range set.Case {
			a, okA := g.Entities().Lookup(c.ConceptA)
			b, okB := g.Entities().Lookup(c.ConceptB)
			if !okA || !okB {
				continue
			}
			ns := g.Edges().Neighbors(a.ID)
			pos := -1
			for i, x := range ns {
				if x.ID == b.ID {
					pos = i
					break
				}
			}
			if pos < 0 {
				continue // прямой связи нет — предел её и не тронет
			}
			pairs++
			if pos < n {
				ok++
			}
		}
		fmt.Printf("  %-8d %14d %11.1f%% %13d/%d\n",
			n, kept, pct(total-kept, total), ok, pairs)
	}
	fmt.Println("  «пар сохранено» — у скольких пар набора прямая связь осталась в пределах N")
}

// pprStats — стоит ли ранжировать соседей персонализированным рангом (этап 101, A4).
//
// **Как меряется.** В наборе 60 пар понятий, встретившихся в одном куске книги.
// Для каждой пары берём первое понятие как точку входа и смотрим, на каком месте
// окажется второе: нынешним способом (соседи по весу связи) и персонализированным
// рангом (вес растекается от точки входа с затуханием, три шага). Метрика простая
// и без модели: попало ли второе понятие в первую пятёрку и десятку.
//
// Смысл замера: нынешний порядок видит только прямых соседей, а ранг — ещё и тех,
// кто в двух-трёх шагах, но связан плотно. Если ранг ставит нужное выше, его стоит
// внедрять; если нет — это лишний счёт на каждом вопросе.
func pprStats(g *graph.Graph, path string) {
	var set struct {
		Case []struct {
			ConceptA string `toml:"concept_a"`
			ConceptB string `toml:"concept_b"`
		} `toml:"case"`
	}
	if _, err := toml.DecodeFile(path, &set); err != nil {
		die(err)
	}

	place := func(order []uint32, want uint32) int {
		for i, id := range order {
			if id == want {
				return i + 1
			}
		}
		return 0
	}

	var byWeightTop5, byWeightTop10, byWeightNone int
	var byRankTop5, byRankTop10, byRankNone int
	var sumWeight, sumRank, nWeight, nRank int

	for _, c := range set.Case {
		a, okA := g.Entities().Lookup(c.ConceptA)
		b, okB := g.Entities().Lookup(c.ConceptB)
		if !okA || !okB {
			continue
		}
		// Нынешний способ: соседи по весу связи, как их отдаёт граф.
		var byWeight []uint32
		for _, n := range g.Edges().Neighbors(a.ID) {
			byWeight = append(byWeight, n.ID)
		}
		if p := place(byWeight, b.ID); p > 0 {
			nWeight++
			sumWeight += p
			if p <= 5 {
				byWeightTop5++
			}
			if p <= 10 {
				byWeightTop10++
			}
		} else {
			byWeightNone++
		}

		// Персонализированный ранг от точки входа.
		if p := place(personalRank(g, a.ID, 3, 0.85, 200), b.ID); p > 0 {
			nRank++
			sumRank += p
			if p <= 5 {
				byRankTop5++
			}
			if p <= 10 {
				byRankTop10++
			}
		} else {
			byRankNone++
		}
	}

	total := len(set.Case)
	fmt.Printf("\nA4. Персонализированный ранг против порядка по весу (пар %d)\n", total)
	fmt.Printf("  %-26s %8s %8s %10s %12s\n", "способ", "в топ-5", "в топ-10", "не найдено", "ср. место")
	fmt.Printf("  %-26s %8d %8d %10d %12.1f\n", "соседи по весу (сейчас)",
		byWeightTop5, byWeightTop10, byWeightNone, avg(sumWeight, nWeight))
	fmt.Printf("  %-26s %8d %8d %10d %12.1f\n", "персонализированный ранг",
		byRankTop5, byRankTop10, byRankNone, avg(sumRank, nRank))
}

// personalRank — вес растекается от одной точки входа с затуханием.
//
// Возвращает узлы по убыванию веса, не больше limit. Обход ограничен тремя
// шагами: дальше вес размазывается по всему графу и порядок перестаёт зависеть
// от вопроса.
func personalRank(g *graph.Graph, seed uint32, steps int, damp float64, limit int) []uint32 {
	cur := map[uint32]float64{seed: 1}
	acc := map[uint32]float64{}
	for s := 0; s < steps; s++ {
		next := map[uint32]float64{}
		for id, w := range cur {
			ns := g.Edges().Neighbors(id)
			if len(ns) == 0 {
				continue
			}
			share := w * damp / float64(len(ns))
			for _, n := range ns {
				next[n.ID] += share
				acc[n.ID] += share
			}
		}
		cur = next
	}
	delete(acc, seed)
	order := make([]uint32, 0, len(acc))
	for id := range acc {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return acc[order[i]] > acc[order[j]] })
	if len(order) > limit {
		order = order[:limit]
	}
	return order
}

// doubleStats — где на самом деле ловятся двойники (этап 101, A1).
//
// Два пути: связывание при сборке (`--graph-link-new`, журнал `links.jsonl`)
// и ночной разбор (`graphdoubles.sh`, реестр `doubles-judged.tsv`). Считаем оба
// по их же файлам, чтобы не гадать, какой из них несёт нагрузку.
func doubleStats(g *graph.Graph) {
	dir := g.Dir()
	type rec struct {
		Source  string `json:"source"`
		Verdict string `json:"verdict"`
		By      string `json:"by"`
	}
	atBuild, byNight := map[string]int{}, map[string]int{}
	f, err := os.Open(filepath.Join(dir, "links.jsonl"))
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r rec
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			if r.Source == "двойники" {
				byNight[r.Verdict]++ // очередь человеку от ночного разбора
			} else {
				atBuild[r.Verdict]++
			}
		}
	}

	judged := map[string]int{}
	if jf, err := os.Open(filepath.Join(dir, "doubles-judged.tsv")); err == nil {
		defer jf.Close()
		sc := bufio.NewScanner(jf)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue
			}
			p := strings.Split(sc.Text(), "\t")
			if len(p) >= 3 {
				judged[p[2]]++
			}
		}
	}

	merges := map[string]int{}
	if mf, err := os.Open(filepath.Join(dir, "merges.jsonl")); err == nil {
		defer mf.Close()
		sc := bufio.NewScanner(mf)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r struct {
				Level string `json:"level"`
			}
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				merges[r.Level]++
			}
		}
	}

	sum := func(m map[string]int) int {
		t := 0
		for _, n := range m {
			t += n
		}
		return t
	}
	fmt.Printf("\nA1. Где ловятся двойники (граф %s)\n", dir)
	fmt.Printf("  связывание при сборке (links.jsonl, не «двойники»): пар %d %v\n", sum(atBuild), atBuild)
	fmt.Printf("  ночной разбор (doubles-judged.tsv):                 пар %d %v\n", sum(judged), judged)
	fmt.Printf("  из ночного в очередь человеку (links.jsonl):        пар %d %v\n", sum(byNight), byNight)
	fmt.Printf("  склейки в графе (merges.jsonl):                     %d %v\n", sum(merges), merges)
	if sum(atBuild) == 0 {
		fmt.Println("  вход не поймал НИ ОДНОЙ пары — проверьте, включён ли --graph-link-new")
	}
}

// chainStats — замер цепочек между парами понятий из замерного набора.
//
// Отвечает на три вопроса (этап 101, D1): у скольких пар есть прямая связь,
// у скольких находится цепочка и через какие узлы она идёт. Последнее важнее
// всего: путь ищется кратчайший, поэтому охотно идёт через «хабы» — понятия
// с тысячами связей, и такая цепочка формально верна, но объясняет мало.
//
// Карта не нужна: понятия в наборе уже названы, вход по вопросу не считается.
func chainStats(g *graph.Graph, path string, hops int, listOnly bool, onlyDocs map[uint32]bool) {
	var set struct {
		Case []struct {
			Query     string `toml:"query"`
			ConceptA  string `toml:"concept_a"`
			ConceptB  string `toml:"concept_b"`
			GraphEdge bool   `toml:"graph_edge"`
		} `toml:"case"`
	}
	if _, err := toml.DecodeFile(path, &set); err != nil {
		die(err)
	}
	deg := func(name string) int {
		ent, ok := g.Entities().Lookup(name)
		if !ok {
			return 0
		}
		return len(g.Edges().Neighbors(ent.ID))
	}
	// Вторая мера степени — уникальные соседи. `Neighbors` ключуется парой
	// {сосед, направление}, и связанный в обе стороны сосед считается дважды
	// (этап 105, Б5, 25.09.2026). Для просмотра глазами важны обе: первая
	// решает, что запрещено обходом, вторая — насколько понятие правда общее.
	degUniq := func(name string) int {
		ent, ok := g.Entities().Lookup(name)
		if !ok {
			return 0
		}
		seen := map[uint32]bool{}
		for _, n := range g.Edges().Neighbors(ent.ID) {
			seen[n.ID] = true
		}
		return len(seen)
	}

	var direct, found, missing, unknown int
	lenSum, hubMax := 0, 0
	hubby := 0
	// Что теряется от запрета хабов: пары и их число (этап 104, П4.1).
	var lostToHub []string
	lostTotal := 0
	var examples []string
	const hubLimit = 500 // столько соседей — это уже «хаб», а не понятие вопроса
	// Специфичность середины (этап 105, А7): степени и число книг у
	// промежуточных узлов против концов. Цепочка «через общее понятие»
	// («data», «graph») — та, у которой середина шире концов.
	books := func(name string) int {
		ent, ok := g.Entities().Lookup(name)
		if !ok {
			return 0
		}
		seen := map[uint32]bool{}
		for _, k := range g.Mentions().Of(ent.ID) {
			seen[k.Doc] = true
		}
		return len(seen)
	}
	var midDegs []int
	midBooksSum, midCount, midWider := 0, 0, 0
	var widerExamples []string

	for _, c := range set.Case {
		a, okA := g.Entities().Lookup(c.ConceptA)
		b, okB := g.Entities().Lookup(c.ConceptB)
		if !okA || !okB {
			unknown++
			if listOnly {
				fmt.Printf("%s | %s\tне в графе\n", c.ConceptA, c.ConceptB)
			}
			continue
		}
		eds := append(g.Edges().Between(a.ID, b.ID), g.Edges().Between(b.ID, a.ID)...)
		if onlyDocs != nil {
			kept := eds[:0]
			for _, ed := range eds {
				if onlyDocs[ed.Evidence.Doc] {
					kept = append(kept, ed)
				}
			}
			eds = kept
		}
		if len(eds) > 0 {
			direct++
			if listOnly {
				// Построчно (сравнение графов, 19.09.2026): пара, «прямая», типы связей.
				types := map[string]int{}
				for _, ed := range eds {
					types[graph.RelName(ed.Type)]++
				}
				fmt.Printf("%s | %s\tпрямая\t%v\n", c.ConceptA, c.ConceptB, types)
			}
			continue
		}
		steps, ok := g.Path(c.ConceptA, c.ConceptB, hops)
		if !ok || len(steps) == 0 {
			missing++
			if listOnly {
				fmt.Printf("%s | %s\tнет\n", c.ConceptA, c.ConceptB)
			}
			continue
		}
		found++
		if listOnly {
			// Сама цепочка, а не только её длина (25.09.2026). Прежде здесь
			// печаталось «цепочка 2», и по такому файлу нельзя было выполнить
			// собственный же пункт плана «просмотреть пути глазами»: чем
			// оказалась середина, из него не видно. У середины показаны обе
			// меры степени и число книг — общее понятие видно сразу.
			line := c.ConceptA
			for i, st := range steps {
				arrow := "—" + st.Type + "→"
				if st.Back {
					arrow = "←" + st.Type + "—"
				}
				line += " " + arrow + " " + st.To
				if i < len(steps)-1 { // середина, не конец пути
					line += fmt.Sprintf(" [%d/%d св., %d кн.]",
						deg(st.To), degUniq(st.To), books(st.To))
				}
			}
			fmt.Printf("%s | %s\tцепочка %d\t%s\n", c.ConceptA, c.ConceptB, len(steps), line)
		}
		lenSum += len(steps)
		worst := 0
		endMax := max(deg(c.ConceptA), deg(c.ConceptB))
		for i, st := range steps {
			if i == len(steps)-1 {
				break // последний конец — само понятие вопроса
			}
			d := deg(st.To)
			if d > worst {
				worst = d
			}
			midDegs = append(midDegs, d)
			midBooksSum += books(st.To)
			midCount++
		}
		if worst > endMax {
			midWider++
			if len(widerExamples) < 6 {
				widerExamples = append(widerExamples, fmt.Sprintf("%s → %s → … → %s (середина %d связей, концы до %d)",
					cut(c.ConceptA, 24), cut(steps[0].To, 24), cut(c.ConceptB, 24), worst, endMax))
			}
		}
		if worst > hubMax {
			hubMax = worst
		}
		if worst >= hubLimit {
			hubby++
			if len(examples) < 5 {
				examples = append(examples, fmt.Sprintf("%s → %s → %s (у середины %d связей)",
					steps[0].From, steps[0].To, steps[len(steps)-1].To, worst))
			}
		}
	}

	// Второй проход: тот же поиск, но через «хабы» ходить нельзя. Нужен,
	// чтобы понять, чинится ли слабость запретом — или чистых путей просто нет.
	var cleanFound, cleanMissing, cleanLen int
	for _, c := range set.Case {
		a, okA := g.Entities().Lookup(c.ConceptA)
		b, okB := g.Entities().Lookup(c.ConceptB)
		if !okA || !okB {
			continue
		}
		if len(g.Edges().Between(a.ID, b.ID)) > 0 || len(g.Edges().Between(b.ID, a.ID)) > 0 {
			continue
		}
		if n := bfsAvoidingHubs(g, a.ID, b.ID, hops, hubLimit); n > 0 {
			cleanFound++
			cleanLen += n
		} else {
			cleanMissing++
			// Пара, у которой цепочка ЕСТЬ без запрета и пропадает с ним:
			// это и есть цена запрета хабов (этап 104, П4.1). Знать её
			// в штуках мало — надо видеть, что именно теряется.
			// «Без запрета» — предел заведомо выше любой степени, а НЕ ноль:
			// проверка внутри «len(neighbors) >= hubLimit», и при нуле хабом
			// считается каждый узел, обход блокируется целиком. На этом
			// список вышел пустым в первом прогоне 16.09.2026.
			if bfsAvoidingHubs(g, a.ID, b.ID, hops, noHubLimit) > 0 {
				lostTotal++
				if len(lostToHub) < 12 {
					lostToHub = append(lostToHub, fmt.Sprintf("%s ↔ %s",
						cut(c.ConceptA, 28), cut(c.ConceptB, 28)))
				}
			}
		}
	}

	if listOnly {
		return
	}

	total := len(set.Case)
	fmt.Printf("\nD1. Цепочки по набору %s (пар %d, предел %d шага)\n", path, total, hops)
	fmt.Printf("  прямая связь есть      %3d (%4.1f%%) — цепочка не нужна\n", direct, pct(direct, total))
	fmt.Printf("  цепочка найдена        %3d (%4.1f%%), средняя длина %.2f шага\n",
		found, pct(found, total), avg(lenSum, found))
	fmt.Printf("  цепочки нет            %3d (%4.1f%%)\n", missing, pct(missing, total))
	fmt.Printf("  понятие не в графе     %3d (%4.1f%%)\n", unknown, pct(unknown, total))
	if found > 0 {
		fmt.Printf("  из найденных через хаб (500+ связей): %d (%4.1f%% от найденных), худший узел %d связей\n",
			hubby, pct(hubby, found), hubMax)
		for _, e := range examples {
			fmt.Printf("    · %s\n", e)
		}
		if midCount > 0 {
			sort.Ints(midDegs)
			fmt.Printf("  середина цепочек: узлов %d, медиана связей %d, среднее книг %.1f; цепочек, где середина шире концов: %d (%4.1f%% от найденных)\n",
				midCount, midDegs[len(midDegs)/2], float64(midBooksSum)/float64(midCount), midWider, pct(midWider, found))
			for _, e := range widerExamples {
				fmt.Printf("    · %s\n", e)
			}
		}
		fmt.Printf("  если хабы запретить: цепочка найдена %d, не найдена %d, средняя длина %.2f\n",
			cleanFound, cleanMissing, avg(cleanLen, cleanFound))
		fmt.Printf("  ЦЕНА ЗАПРЕТА: пар, у которых цепочка есть без запрета и пропадает с ним — %d\n", lostTotal)
		for _, x := range lostToHub {
			fmt.Printf("    · %s\n", x)
		}
		if lostTotal > len(lostToHub) {
			fmt.Printf("    …и ещё %d\n", lostTotal-len(lostToHub))
		}
	}
}

// bfsAvoidingHubs — длина кратчайшего пути, не проходящего через «хабы».
//
// Ноль означает «пути нет». Концы пути хабами быть могут: их назвал вопрос,
// а запрет касается только промежуточных узлов.
// noHubLimit — предел, при котором хабы не запрещены вовсе: он должен быть
// заведомо больше степени любого узла (у нас наибольшая — 11 466).
const noHubLimit = 1 << 30

func bfsAvoidingHubs(g *graph.Graph, from, to uint32, maxHops, hubLimit int) int {
	type node struct {
		id   uint32
		dist int
	}
	seen := map[uint32]bool{from: true}
	queue := []node{{from, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.dist >= maxHops {
			continue
		}
		for _, n := range g.Edges().Neighbors(cur.id) {
			if seen[n.ID] {
				continue
			}
			if n.ID == to {
				return cur.dist + 1
			}
			if len(g.Edges().Neighbors(n.ID)) >= hubLimit {
				continue // через хаб не ходим
			}
			seen[n.ID] = true
			queue = append(queue, node{n.ID, cur.dist + 1})
		}
	}
	return 0
}

// edgesAround — связи понятия в ОБЕ стороны, со снятыми склейками.
// `Edges.Of` отдаёт только исходящие; прибор, которому нужно окружение
// понятия, с одним Of видел произвольную его половину: понятие, на которое
// только ссылаются, выходило вовсе «без связей».
func edgesAround(g *graph.Graph, id uint32) []graph.Edge {
	out := g.Edges().Of(id)
	me := g.Merges().Resolve(id)
	for _, n := range g.Edges().Neighbors(id) {
		if !n.In {
			continue
		}
		for _, ed := range g.Edges().Between(n.ID, id) {
			if ed.Dst == me {
				out = append(out, ed)
			}
		}
	}
	return out
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func avg(sum, n int) float64 {
	if n == 0 {
		return 0
	}
	return float64(sum) / float64(n)
}

// thresholdStats — что потеряет выдача, если показывать связи от N подтверждений.
//
// Считается по графу, без карты и без поиска: сколько связей уходит из показа
// и — важнее — у скольких понятий не остаётся НИ ОДНОГО соседа. Второе и есть
// цена порога: у такого понятия карта понятий для модели окажется пустой.
func thresholdStats(ents []graph.Entity, conf map[edgeKey]int) {
	deg := make(map[uint32]map[int]int, len(ents)) // понятие → порог → соседей
	for k, n := range conf {
		for _, id := range []uint32{k.src, k.dst} {
			m := deg[id]
			if m == nil {
				m = map[int]int{}
				deg[id] = m
			}
			for t := 1; t <= 4; t++ {
				if n >= t {
					m[t]++
				}
			}
		}
	}
	total := len(ents)
	fmt.Printf("\nA3. Цена порога подтверждений (понятий всего %d)\n", total)
	fmt.Printf("  %-8s %12s %12s %14s\n", "порог", "связей", "с соседями", "без соседей")
	var edgesAt = map[int]int{}
	for _, n := range conf {
		for t := 1; t <= 4; t++ {
			if n >= t {
				edgesAt[t]++
			}
		}
	}
	for t := 1; t <= 4; t++ {
		with := 0
		for _, m := range deg {
			if m[t] > 0 {
				with++
			}
		}
		fmt.Printf("  %-8d %12d %12d %13d (%4.1f%%)\n",
			t, edgesAt[t], with, total-with, pct(total-with, total))
	}
	fmt.Println("  «без соседей» — понятия, у которых при этом пороге карта понятий пуста")
}

// pageRank — A4: центральность понятий по связям графа.
//
// Считается на неориентированном взгляде: связь «A —использует→ B» говорит
// о близости обоих концов, и вход в граф нужен с любой стороны. Затухание
// 0.85 — общепринятое; итераций хватает двадцати, дальше порядок верхушки
// не меняется.
func pageRank(g *graph.Graph, ents []graph.Entity, conf map[edgeKey]int, iters, top int) {
	idx := make(map[uint32]int, len(ents))
	for i, e := range ents {
		idx[e.ID] = i
	}
	out := make([][]int, len(ents))
	deg := make([]int, len(ents))
	for k := range conf {
		a, okA := idx[k.src]
		b, okB := idx[k.dst]
		if !okA || !okB || a == b {
			continue
		}
		out[a] = append(out[a], b)
		out[b] = append(out[b], a)
		deg[a]++
		deg[b]++
	}

	n := len(ents)
	if n == 0 {
		return
	}
	rank := make([]float64, n)
	next := make([]float64, n)
	for i := range rank {
		rank[i] = 1 / float64(n)
	}
	const damp = 0.85
	for it := 0; it < iters; it++ {
		var sink float64
		for i := range next {
			next[i] = 0
		}
		for i, links := range out {
			if len(links) == 0 {
				sink += rank[i]
				continue
			}
			share := rank[i] / float64(len(links))
			for _, j := range links {
				next[j] += share
			}
		}
		base := (1-damp)/float64(n) + damp*sink/float64(n)
		for i := range next {
			next[i] = base + damp*next[i]
		}
		rank, next = next, rank
	}

	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return rank[order[i]] > rank[order[j]] })

	fmt.Printf("\nA4. PageRank по %d понятиям (%d итераций)\n", n, iters)
	fmt.Printf("  %-46s %-9s %-8s %s\n", "понятие", "ранг", "связей", "упоминаний")
	for i := 0; i < top && i < len(order); i++ {
		k := order[i]
		fmt.Printf("  %-46s %.6f  %-8d %d\n",
			cut(ents[k].Name, 44), rank[k], deg[k], ents[k].Count)
	}

	// Совпадает ли верхушка ранга с верхушкой по числу связей: если да, поле
	// ранга не нужно — хватит степени, которая уже есть.
	byDeg := make([]int, n)
	copy(byDeg, order)
	sort.Slice(byDeg, func(i, j int) bool { return deg[byDeg[i]] > deg[byDeg[j]] })
	inDeg := make(map[int]bool, top)
	for i := 0; i < top && i < len(byDeg); i++ {
		inDeg[byDeg[i]] = true
	}
	same := 0
	for i := 0; i < top && i < len(order); i++ {
		if inDeg[order[i]] {
			same++
		}
	}
	fmt.Printf("  верхушка %d: совпадает со списком по числу связей на %d из %d\n",
		top, same, top)
	if same*10 >= top*9 {
		fmt.Println("  ранг почти повторяет степень — отдельное поле ранга смысла не даёт")
	} else {
		fmt.Println("  ранг и степень расходятся — ранг несёт свои сведения, поле стоит завести")
	}
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// weightExperiment — опыт с порогом веса связи (этап 101, Г2).
//
// «Neo4j: The Definitive Guide» (2025, стр. 368) перед поиском сообществ строит
// граф со-встречаемости с порогом, а не считает темы по всем совпадениям подряд.
// У нас порога нет, и связь из одного куска весит в Louvain столько же, сколько
// подтверждённая десятью. Здесь считаются три разбиения — без порога, w≥2, w≥3, —
// и видно, чем платится тишина: сколько понятий выпадает из тем совсем.
//
// Ничего не пишет на диск: рабочее разбиение не трогается.
func weightExperiment(g *graph.Graph) {
	fmt.Println("\nГ2. Разбиение с порогом веса связи")
	fmt.Printf("  %-20s %10s %10s %8s %11s %9s %8s %10s %9s\n",
		"порог", "понятий", "связей", "тем", "из одного", "крупнейшая", "медиана", "пар срезано", "понятий")

	show := func(label string, r graph.PartitionExperiment) {
		fmt.Printf("  %-20s %10d %10d %8d %11d %9d %8d %10d %9d\n",
			label, r.Nodes, r.Edges, r.Themes, r.Singleton, r.Largest, r.Median, r.CutPairs, r.CutNodes)
	}

	base := g.ExperimentPartition(graph.PartitionOpts{})
	show("без порога", base)
	for _, w := range []float64{2, 3} {
		show(fmt.Sprintf("вес ≥ %.0f", w), g.ExperimentPartition(graph.PartitionOpts{MinWeight: w}))
	}
	// Мягкий вариант: одиночное подтверждение не выбрасывается, а весит меньше.
	for _, k := range []float64{0.5, 0.25} {
		show(fmt.Sprintf("одиночные ×%.2f", k), g.ExperimentPartition(graph.PartitionOpts{OnceFactor: k}))
	}

	st := g.Structure()
	fmt.Printf("\n  строение графа: пар связей %d, из них на одном подтверждении %d (%d%%)\n",
		st.Pairs, st.PairsOnce, st.OnceShare())
	fmt.Printf("  понятий со связями %d, без связей %d (%d%%); наибольшая связная часть %d (%d%%), частей %d\n",
		st.Nodes, st.Isolated, st.IsolatedShare(), st.Largest, st.LargestShare(), st.Parts)
}

// hubStats — распределение степеней и цена нынешнего порога хаба (этап 101, Г3).
//
// «Knowledge Graphs and LLMs in Action» (2025, стр. 226–227) определяет хабы
// не числом связей, а верхушкой распределения: топ-350 узлов по степени, и путь
// через любой из них отбраковывается. У нас порог абсолютный — 500 связей
// (`ChainHubLimit`), и вопрос ровно один: какой доле верхушки он отвечает
// сегодня и уплывает ли эта доля по мере роста графа.
func hubStats(g *graph.Graph) {
	ents := g.Entities().Live()
	deg := make([]int, 0, len(ents))
	over := 0
	for _, e := range ents {
		d := len(g.Edges().Neighbors(e.ID))
		deg = append(deg, d)
		if d >= 500 {
			over++
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(deg)))

	fmt.Printf("\nГ3. Распределение степеней: понятий %d\n", len(deg))
	if len(deg) == 0 {
		return
	}
	fmt.Printf("  нынешний порог хаба 500 связей: %d понятий (%.3f%% верхушки)\n",
		over, 100*float64(over)/float64(len(deg)))

	fmt.Println("\n  доля верхушки → какой порог связей ей отвечает")
	for _, share := range []float64{0.0005, 0.001, 0.002, 0.005, 0.01} {
		n := int(float64(len(deg)) * share)
		if n < 1 {
			n = 1
		}
		fmt.Printf("    верхние %6.2f%% (%6d понятий) → %d связей и больше\n",
			100*share, n, deg[n-1])
	}
	// Приём книги: фиксированное число верхних узлов.
	for _, n := range []int{350, 1000} {
		if n <= len(deg) {
			fmt.Printf("    верхние %d понятий (как в книге) → %d связей и больше\n", n, deg[n-1])
		}
	}
	fmt.Printf("\n  верхушка: %v\n", deg[:min(10, len(deg))])
	fmt.Printf("  медиана степени %d, среднее %.1f\n", deg[len(deg)/2], mean(deg))
}

func mean(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0
	for _, x := range xs {
		s += x
	}
	return float64(s) / float64(len(xs))
}

// orphanStats — откуда берутся понятия без единой связи (этап 101).
//
// Число (4% графа) нашла строка строения в докторе; здесь оно разбирается
// на причины: не назвала ли модель отношений вовсе, или связи были и сгорели
// при склейке двойников, свернувшись в петли.
func orphanStats(g *graph.Graph, sample int) {
	o := g.Orphans(sample)
	fmt.Printf("\nПонятия без единой связи: %d\n", o.Total)
	if o.Total == 0 {
		return
	}
	pct := func(n int) float64 { return 100 * float64(n) / float64(o.Total) }
	fmt.Printf("  ни одной записи связи вообще (модель не назвала отношений): %d (%.1f%%)\n",
		o.NoRawEdges, pct(o.NoRawEdges))
	fmt.Printf("  записи связей есть, но после склейки все стали петлями:      %d (%.1f%%)\n",
		o.LostToMerge, pct(o.LostToMerge))
	fmt.Printf("  сами кого-то поглотили при склейке:                          %d (%.1f%%)\n",
		o.Survivors, pct(o.Survivors))
	fmt.Printf("  встречались в кусках: %d (%.1f%%), из них ровно один раз: %d (%.1f%%)\n",
		o.Mentioned, pct(o.Mentioned), o.SingleMention, pct(o.SingleMention))
	fmt.Printf("  ПУСТЫЕ УЗЛЫ (ни связей, ни упоминаний): %d (%.1f%%) — смысловой вход их не предлагает\n",
		o.Empty, pct(o.Empty))

	fmt.Println("\n  по типам:")
	types := make([]string, 0, len(o.ByType))
	for k := range o.ByType {
		types = append(types, k)
	}
	sort.Slice(types, func(i, j int) bool { return o.ByType[types[i]] > o.ByType[types[j]] })
	for _, k := range types {
		name := k
		if name == "" {
			name = "(без типа)"
		}
		fmt.Printf("    %-16s %6d (%.1f%%)\n", name, o.ByType[k], pct(o.ByType[k]))
	}

	fmt.Println("\n  самые упоминаемые из них:")
	fmt.Printf("    %6s %8s %6s  %s\n", "упом.", "записей", "тип", "имя")
	for _, e := range o.Sample {
		fmt.Printf("    %6d %8d %6s  %s\n", e.Count, e.RawEdges, e.Type, e.Name)
	}
}
