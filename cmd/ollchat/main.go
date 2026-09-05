// Команда ollchat — TUI-клиент и агент для серверов Ollama.
package main

import (
	"context"
	"flag"
	"fmt"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	kmaint "github.com/Cyber-Watcher/ollchat/internal/kb/maint"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/buildinfo"
	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/confluence"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
	"github.com/Cyber-Watcher/ollchat/internal/ui"
)

// version — версия приложения. Выпуск подставляет сюда тег ключом
// -ldflags "-X main.version=v0.1.5" (см. scripts/build-dist.sh); сборка руками
// оставляет умолчание, и тогда приметы берутся из самого бинаря — как именно,
// разобрано в internal/buildinfo.
var version = buildinfo.Unknown

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ollchat: "+err.Error())
		os.Exit(1)
	}
}

// cliFlags — все ключи командной строки. Раньше 131 ключ жил локальными
// переменными внутри run() на 578 строк (этап 91, R4.9).
type cliFlags struct {
	cfgPath               *string
	initConfig            *bool
	serverName            *string
	modelName             *string
	workDir               *string
	modeFlag              *string
	showVer               *bool
	stepsFile             *string
	kbIndex               *string
	kbSync                *string
	kbMerge               *string
	kbMergeF              *bool
	kbYes                 *bool
	kbEmbed               *string
	kbRebase              *string
	kbRebaseFrom          *string
	kbRebaseTo            *string
	kbRefresh             *string
	kbDry                 *bool
	kbList                *bool
	kbDoctor              *string
	kbQuick               *bool
	kbYears               *string
	kbReindex             *string
	kbRecnt               *bool
	graphBuild            *string
	graphFolder           *string
	graphEntryEval        *string
	graphEntryColl        *string
	graphEntryLimit       *int
	graphEntryShow        *int
	graphEntryEnts        *int
	graphGroups           *string
	graphName             *string
	graphKind             *string
	graphNote             *string
	graphLimit            *int
	graphWorkers          *int
	graphRedoEmpty        *bool
	graphLinkNew          *bool
	graphLog              *string
	graphNewModel         *bool
	graphNewPrompt        *bool
	graphStatus           *string
	graphComm             *string
	graphSum              *string
	graphSumMin           *int
	kbEvalGen             *string
	kbEvalGenN            *int
	kbSeed                *int64
	kbSample              *string
	kbEval                *string
	kbEvalColl            *string
	kbEvalK               *int
	kbEvalWeight          *float64
	kbEvalTable           *float64
	kbEvalRRFK            *float64
	kbEvalOnly            *string
	kbEvalRerank          *bool
	kbEvalCands           *int
	kbEvalSnippet         *bool
	graphEmbedRecount     *bool
	graphFindings         *string
	graphFindingsRating   *int
	graphFindingsMembers  *int
	graphFindingsRedo     *bool
	graphFindingsDry      *bool
	graphFreshComm        *bool
	graphCarrySim         *float64
	graphBench            *string
	graphBenchModels      *string
	graphBenchKeep        *string
	graphTune             *string
	graphTuneList         *string
	graphTuneShow         *int
	graphDrift            *string
	graphDriftSim         *float64
	graphDriftShow        *int
	graphMerge            *string
	graphMergeFile        *string
	graphMergeLevel       *string
	graphMergeMinSame     *float64
	graphMergeDrop        *bool
	graphBook             *string
	graphBookName         *string
	graphGroupsBuild      *string
	graphGroupsFrom       *string
	graphGroupsMinCos     *float64
	graphGroupsMutual     *float64
	graphDropBook         *string
	graphRestoreBook      *bool
	graphDropApply        *bool
	graphCompact          *string
	graphCompactCheck     *bool
	graphCompactForce     *bool
	graphMergeDry         *bool
	graphResolve          *string
	graphResolveFull      *bool
	graphResolveMinCos    *float64
	graphResolveMinCosMut *float64
	graphResolveCross     *bool
	graphResolveShow      *int
	graphResolveOut       *string
	graphEmbed            *string
	graphRecheck          *string
	graphRecheckN         *int
	graphDoctor           *string
	graphArchive          *string
	graphArchives         *string
	graphRestore          *string
	graphRestoreYes       *bool
	graphFind             *string
	graphColl             *string
	graphJSON             *bool
	askQ                  *string
	askStdin              *bool
	askFile               *string
	askJSON               *bool
	askRep                *int
	askMixK               *string
	askShow               *bool
	askTools              *bool
	askTemp               *float64
	askSeed               *int
	askCtx                *string
	askThink              *string
	askColl               *string
	serveAddr             *string
	serveMCP              *bool
	askSense              *float64
	askPool               *int
	askEntities           *int
	askNeighbors          *int
	askTopK               *int
	askPerBook            *int
	askMinCos             *float64
	askSemW               *float64
}

// parseFlags объявляет и разбирает ключи.
func parseFlags() *cliFlags {
	f := &cliFlags{}
	f.cfgPath = flag.String("c", "", "путь к файлу настроек (по умолчанию "+config.DefaultPath()+")")
	f.initConfig = flag.Bool("init-config", false, "создать файл настроек с комментариями и выйти")
	f.serverName = flag.String("server", "", "имя сервера из конфига (перекрывает general.default_server)")
	f.modelName = flag.String("model", "", "модель на сервере (перекрывает настройку сервера)")
	f.workDir = flag.String("cwd", "", "рабочий каталог — корень песочницы (перекрывает sandbox.root)")
	f.modeFlag = flag.String("mode", "", "режим подтверждений: safe, auto-edit или noask")
	f.showVer = flag.Bool("version", false, "показать версию и выйти")
	f.stepsFile = flag.String("steps-file", "", "куда писать журнал шагов этого запуска (перекрывает log.steps_file_pattern; для замеров)")

	f.kbIndex = flag.String("kb-index", "", "собрать коллекцию книг без запуска интерфейса: --kb-index go /путь")
	f.kbSync = flag.String("kb-sync", "", "сверить коллекцию с диском без запуска интерфейса")
	f.kbMerge = flag.String("kb-merge", "", "уплотнить коллекцию: выбросить удалённое, слить сегменты")
	f.kbMergeF = flag.Bool("kb-merge-force", false,
		"с --kb-merge: уплотнить, даже если по коллекции собран граф;\n"+
			"граф после этого не откроется, и собирать его придётся заново")
	f.kbYes = flag.Bool("kb-yes", false,
		"с --kb-merge: не спрашивать подтверждений (для скриптов);\n"+
			"без него уплотнение дважды переспрашивает и требует ответа ДА")
	f.kbEmbed = flag.String("kb-embed", "", "посчитать векторы(смыслы) коллекции (эмбеддинги) без запуска интерфейса")
	f.kbRebase = flag.String("kb-rebase", "",
		"переписать корень коллекции при переносе: --kb-rebase books --kb-rebase-to /новый/путь")
	f.kbRebaseFrom = flag.String("kb-rebase-from", "",
		"с --kb-rebase: старый корень (пусто — взять из паспорта, если он один)")
	f.kbRebaseTo = flag.String("kb-rebase-to", "",
		"с --kb-rebase: новый корень, где книги лежат теперь")
	f.kbRefresh = flag.String("kb-refresh", "",
		"долить новое и сразу досчитать векторы(смыслы): --kb-refresh projectdocs")
	f.kbDry = flag.Bool("kb-dry-run", false, dryRunFlagHelp)
	f.kbList = flag.Bool("kb-list", false, "показать коллекции базы знаний и выйти")
	f.kbDoctor = flag.String("kb-doctor", "", "проверить коллекцию: пропавшие книги, сканы, повторы (\"all\" — все)")
	f.kbQuick = flag.Bool("kb-quick", false, "с --kb-doctor: без сверки книг по содержимому — быстрее, но повторы не найдутся")
	f.kbYears = flag.String("kb-years", "", "проставить книгам коллекции год издания")
	f.kbReindex = flag.String("kb-reindex", "", "перечитать книги коллекции заново: --kb-reindex books <путь>…")
	f.kbRecnt = flag.Bool("kb-recount", false, "с --kb-years: перечитать год и там, где он уже стоит")

	f.graphBuild = flag.String("graph-build", "", "собрать граф понятий по коллекции: --graph-build books")
	f.graphFolder = flag.String("graph-folder", "", "с --graph-build и --graph-status: только книги, чей путь содержит эту строку")
	f.graphEntryEval = flag.String("graph-entry-eval", "",
		"замерить вход в граф по набору вопросов: --graph-entry-eval вопросы.toml")
	f.graphEntryColl = flag.String("graph-entry-eval-collection", "",
		"с --graph-entry-eval: коллекция (пусто — kb.default)")
	f.graphEntryLimit = flag.Int("graph-entry-eval-limit", 0,
		"с --graph-entry-eval: взять только первые N вопросов")
	f.graphEntryShow = flag.Int("graph-entry-eval-show", 10,
		"с --graph-entry-eval: сколько худших случаев показать")
	f.graphEntryEnts = flag.Int("graph-entry-eval-entities", 0,
		"с --graph-entry-eval: сколько мест в карте понятий (0 — как в mix.entities)")
	f.graphGroups = flag.String("graph-groups", "",
		"режим групп понятий на этот запуск: union, expand или off (пусто — как в конфиге)")
	f.graphName = flag.String("graph-name", "", "с каким графом работать: пусто — рабочий, иначе опытный рядом (lab → graph-lab)")
	f.graphKind = flag.String("graph-kind", "", "при создании графа: production или experimental (умолчание — production)")
	f.graphNote = flag.String("graph-note", "", "при создании графа: чем он отличается — схема, промпт, правила")
	f.graphLimit = flag.Int("graph-limit", 0, "с --graph-build: сколько кусков разобрать за заход (замер скорости)")
	f.graphWorkers = flag.Int("graph-workers", 0, "с --graph-build: сколько кусков разбирать одновременно")
	f.graphRedoEmpty = flag.Bool("graph-redo-empty", false,
		"с --graph-build: перепройти куски, помеченные пустыми")
	f.graphLog = flag.String("graph-log", "",
		"с --graph-build: дозаписывать ход в этот файл строками с отметкой времени")
	f.graphNewModel = flag.Bool("graph-allow-model-change", false,
		"с --graph-build: досбирать граф моделью, отличной от той, которой он начат;\n"+
			"по умолчанию это отказ — смешанный граф выглядит исправным и не чинится")
	f.graphNewPrompt = flag.Bool("graph-allow-prompt-change", false,
		"с --graph-build: досбирать граф промптом, отличным от записанного в паспорте;\n"+
			"по умолчанию это отказ — граф двумя схемами выглядит исправным и не чинится")
	f.graphStatus = flag.String("graph-status", "", "показать состояние графа коллекции (\"all\" — всех)")
	f.graphComm = flag.String("graph-communities", "",
		"разбить граф коллекции на сообщества: --graph-communities books")
	f.graphSum = flag.String("graph-summaries", "",
		"написать моделью резюме сообществ графа: --graph-summaries books")
	f.graphSumMin = flag.Int("graph-summaries-min", 0,
		"с --graph-summaries: не описывать сообщества меньше стольких понятий (по умолчанию 5)")
	f.kbEvalGen = flag.String("kb-eval-gen", "",
		"собрать замерный набор: слепая выборка кусков и вопрос к каждому")
	f.kbEvalGenN = flag.Int("kb-eval-gen-n", 200,
		"с --kb-eval-gen: сколько кусков отобрать")
	f.kbSeed = flag.Int64("kb-seed", 0,
		"зерно случайности для выборки; 0 — взять текущее время и напечатать его")
	f.kbSample = flag.String("kb-sample", "",
		"слепая выборка кусков коллекции в JSON: --kb-sample books")
	f.kbEval = flag.String("kb-eval", "",
		"замерить качество поиска по набору вопросов: --kb-eval набор.toml")
	f.kbEvalColl = flag.String("kb-eval-collection", "",
		"с --kb-eval: коллекция, по которой мерить (по умолчанию kb.default)")
	f.kbEvalK = flag.Int("kb-eval-k", 0,
		"с --kb-eval: сколько первых кусков смотреть (по умолчанию 10)")
	f.kbEvalWeight = flag.Float64("kb-eval-weight", 0,
		"с --kb-eval: вес смыслового списка при слиянии (0 — как в работе)")
	f.kbEvalTable = flag.Float64("kb-eval-table-boost", 0,
		"с --kb-eval: надбавка кускам-таблицам (0 — как в работе, 1 — без надбавки)")
	f.kbEvalRRFK = flag.Float64("kb-eval-rrfk", 0,
		"с --kb-eval: постоянная слияния k (0 — как в работе)")
	f.kbEvalOnly = flag.String("kb-eval-only", "",
		"с --kb-eval: мерить только один режим — слова, векторы(смыслы) или слияние")
	f.kbEvalRerank = flag.Bool("kb-eval-rerank", false,
		"с --kb-eval: добавить вторую ступень — переранжирование кросс-энкодером")
	f.kbEvalCands = flag.Int("kb-eval-candidates", 0,
		"с --kb-eval-rerank: сколько кусков отдавать второй ступени (по умолчанию 40)")
	f.kbEvalSnippet = flag.Bool("kb-eval-snippet", false,
		"с --kb-eval-rerank: подавать выдержки вместо кусков целиком")
	f.graphEmbedRecount = flag.Bool("graph-embed-recount", false,
		"с --graph-embed: пересчитать все векторы понятий, а не только новые")
	f.graphFindings = flag.String("graph-findings", "",
		"написать разбор важных тем графа: --graph-findings books")
	f.graphFindingsRating = flag.Int("graph-findings-min-rating", 0,
		"с --graph-findings: брать темы с оценкой не ниже (по умолчанию 8)")
	f.graphFindingsMembers = flag.Int("graph-findings-min-members", 0,
		"с --graph-findings: брать темы не меньше стольких понятий (по умолчанию 20)")
	f.graphFindingsRedo = flag.Bool("graph-findings-redo", false,
		"с --graph-findings: пересчитать и уже разобранные темы")
	f.graphFindingsDry = flag.Bool("graph-findings-dry", false,
		"с --graph-findings: только сказать, сколько тем подходит, и выйти")
	f.graphFreshComm = flag.Bool("graph-communities-fresh", false,
		"с --graph-communities: считать начисто, не перенося описания прежних тем")
	f.graphCarrySim = flag.Float64("graph-carry-similarity", 0,
		"с --graph-communities: с какой доли общих понятий переносить описание (0 — 0.7)")
	f.graphBench = flag.String("graph-bench", "",
		"сравнить модели извлечения на одних кусках: --graph-bench books")
	f.graphBenchModels = flag.String("graph-bench-models", "",
		"с --graph-bench: модели через запятую (по умолчанию graph.model и graph.summary_model)")
	f.graphBenchKeep = flag.String("graph-bench-keep", "",
		"с --graph-bench: не удалять временные графы, а сложить их в этот каталог")
	f.graphTune = flag.String("graph-tune", "",
		"подобрать разрешение разбиения: --graph-tune books")
	f.graphTuneList = flag.String("graph-tune-resolutions", "",
		"с --graph-tune: какие значения перебрать, через запятую (по умолчанию 1,3,5,8)")
	f.graphTuneShow = flag.Int("graph-tune-show", 3,
		"с --graph-tune: сколько крупнейших тем показать составом (0 — не показывать)")
	f.graphDrift = flag.String("graph-drift", "",
		"проверить, пора ли пересчитывать сообщества: --graph-drift books")
	f.graphDriftSim = flag.Float64("graph-drift-similarity", 0,
		"с --graph-drift: с какого совпадения состава тема считается прежней (0 — 0.7)")
	f.graphDriftShow = flag.Int("graph-drift-show", 0,
		"с --graph-drift: показать столько сильнее всего перекроившихся тем")
	f.graphMerge = flag.String("graph-merge", "",
		"склеить двойников по разбору: --graph-merge books --graph-merge-file verdicts.tsv")
	f.graphMergeFile = flag.String("graph-merge-file", "",
		"с --graph-merge: файл разбора (verdicts.tsv); без него — показать уже склеенное")
	f.graphMergeLevel = flag.String("graph-merge-level", "strict",
		"с --graph-merge: строгость отбора — strict, alias, vector, mixed, soft, all-yes")
	f.graphMergeMinSame = flag.Float64("graph-merge-min-cos-same", 0,
		"с --graph-merge: отдельный порог близости для пар внутри одного языка (0 — не применять)")
	f.graphMergeDrop = flag.Bool("graph-merge-drop", false,
		"с --graph-merge: снять все склейки, вернув граф в прежний вид")
	f.graphBook = flag.String("graph-book", "",
		"показать вклад книги в граф: --graph-book books --graph-book-name <часть имени>")
	f.graphBookName = flag.String("graph-book-name", "",
		"с --graph-book: часть имени или пути книги")
	f.graphGroupsBuild = flag.String("graph-groups-build", "",
		"собрать группы понятий: --graph-groups-build books --from merges|resolve|both")
	f.graphGroupsFrom = flag.String("from", "both",
		"с --graph-groups-build: источник — merges (склейки), resolve (вычислить), both")
	f.graphGroupsMinCos = flag.Float64("graph-groups-min-cos", 0,
		"с --graph-groups-build --from resolve: порог близости пар (0 — 0.90)")
	f.graphGroupsMutual = flag.Float64("graph-groups-min-cos-mutual", 0,
		"с --graph-groups-build --from resolve: порог для взаимных синонимов (0 — 0.70)")
	f.graphDropBook = flag.String("graph-drop-book", "",
		"скрыть вклад книги из графа: --graph-drop-book books --graph-book-name <часть имени> --apply")
	f.graphRestoreBook = flag.Bool("graph-restore-book", false,
		"с --graph-drop-book: наоборот, вернуть ранее скрытую книгу")
	f.graphDropApply = flag.Bool("apply", false,
		"с --graph-drop-book: применить (без него — сухой прогон)")
	f.graphCompact = flag.String("graph-compact", "",
		"уплотнить реестр понятий графа: --graph-compact books")
	f.graphCompactCheck = flag.Bool("graph-compact-check", false,
		"с --graph-compact: только сличить словари и рассказать, ничего не подменяя")
	f.graphCompactForce = flag.Bool("graph-compact-force", false,
		"с --graph-compact: подменить реестр даже при расхождении словарей")
	f.graphMergeDry = flag.Bool("graph-merge-dry", false,
		"с --graph-merge: только показать, что склеится, ничего не записывая")
	f.graphResolve = flag.String("graph-resolve", "",
		"показать двойников среди понятий графа, ничего не меняя: --graph-resolve books")
	f.graphResolveFull = flag.Bool("graph-resolve-full", false,
		"с --graph-resolve: перебрать все пары понятий, а не только связанные синонимом (минуты процессора)")
	f.graphResolveMinCos = flag.Float64("graph-resolve-min-cos", 0,
		"с --graph-resolve: ниже этой близости пара не показывается (0 — 0.90)")
	f.graphResolveMinCosMut = flag.Float64("graph-resolve-min-cos-mutual", 0,
		"с --graph-resolve: свой порог для пар со взаимным синонимом (0 — 0.80)")
	f.graphResolveCross = flag.Bool("graph-resolve-cross", false,
		"с --graph-resolve: только пары через границу алфавита")
	f.graphResolveShow = flag.Int("graph-resolve-show", 0,
		"с --graph-resolve: сколько пар показать (0 — 40)")
	f.graphResolveOut = flag.String("graph-resolve-out", "",
		"с --graph-resolve: выписать все пары в файл TSV")
	f.graphEmbed = flag.String("graph-embed", "",
		"посчитать векторы понятий графа — смысловой вход: --graph-embed books")
	f.graphRecheck = flag.String("graph-recheck", "",
		"передоописать моделью извлечения самые рыхлые темы: --graph-recheck books")
	f.graphRecheckN = flag.Int("graph-recheck-count", 0,
		"с --graph-recheck: сколько тем пересмотреть (по умолчанию 50)")
	f.graphArchive = flag.String("graph-archive", "",
		"снять архив коллекции с графом в graph.archive_dir: --graph-archive books")
	f.graphArchives = flag.String("graph-archives", "",
		"перечислить архивы коллекции (all — всех): --graph-archives books")
	f.graphRestore = flag.String("graph-restore", "",
		"восстановить коллекцию с графом из архива, подменив нынешнюю: --graph-restore books-20260904-111305.tar.gz")
	f.graphRestoreYes = flag.Bool("graph-restore-yes", false,
		"с --graph-restore: не спрашивать подтверждения (для скриптов)")
	f.graphLinkNew = flag.Bool("graph-link-new", false,
		"с --graph-build: связывать новые имена с существующими понятиями по вектору и арбитру (опытный граф)")
	f.graphDoctor = flag.String("graph-doctor", "",
		"проверить граф и сказать, какими командами привести его в порядок: --graph-doctor books")
	f.graphFind = flag.String("graph-find", "", "искать по графу понятий: --graph-find \"как связаны X и Y\"")
	f.graphColl = flag.String("graph-collection", "", "с --graph-find: в какой коллекции искать")
	f.graphJSON = flag.Bool("graph-json", false, "с --graph-find: выдать разбираемый JSON")

	// ── Спросить модель из командной строки ──────────────────────────
	//
	// Ради замеров: числа отбора подбираются только прогоном, а прогон
	// должен идти без интерфейса и без временных скриптов вокруг него.
	f.askQ = flag.String("ask", "", "спросить модель и напечатать ответ: --ask \"вопрос\"")
	f.askStdin = flag.Bool("ask-stdin", false, "с --ask: вопрос читается со стандартного ввода")
	f.askFile = flag.String("questions", "", "файл с вопросами, по одному в строке — спросить каждый")
	f.askJSON = flag.Bool("json", false, "с --ask: строка JSON на ответ (вопрос, ответ, счётчики, настройки)")
	f.askRep = flag.Int("repeat", 0, "с --ask: повторить каждый вопрос N раз — видно разброс от сэмплирования")
	f.askMixK = flag.String("mix", "", "что подмешивать: off, graph, books, all (по умолчанию как в конфиге)")
	f.askShow = flag.Bool("show-mix", false, "с --ask: напечатать подмешанное вместе с ответом")
	f.askTools = flag.Bool("tools", false, "с --ask: разрешить модели инструменты (действия с подтверждением отклоняются)")
	f.askTemp = flag.Float64("temperature", -1, "с --ask: температура (по умолчанию 0 — ради повторяемости)")
	f.askSeed = flag.Int("seed", -1, "с --ask: зерно генератора — тот же ответ на тот же вопрос")
	f.askCtx = flag.String("num-ctx", "", "с --ask: окно контекста на запрос, например 32k")
	f.askThink = flag.String("think", "", "с --ask: рассуждения модели on или off")
	f.askColl = flag.String("kb-use", "", "с --ask: коллекция базы знаний")

	// Служба знаний: тот же бинарь раздаёт собранную библиотеку по сети.
	f.serveAddr = flag.String("serve", "", "поднять службу знаний: --serve 0.0.0.0:8377")
	f.serveMCP = flag.Bool("mcp", false, "с --serve: отдавать и протокол MCP на том же порту")

	// Числа отбора: те же, что в конфиге и в командах /graph tune, /kb tune.
	f.askSense = flag.Float64("graph-sense", -1, "вес уместности связи против подтверждённости книгами (0 — не пересортировывать)")
	f.askPool = flag.Int("graph-pool", 0, "во сколько раз шире показанного брать пул для пересортировки связей")
	f.askEntities = flag.Int("mix-entities", 0, "сколько понятий брать в карту, подмешиваемую к вопросу")
	f.askNeighbors = flag.Int("mix-neighbors", 0, "сколько связей показывать у каждого понятия карты")
	f.askTopK = flag.Int("kb-topk", 0, "сколько фрагментов книг возвращать")
	f.askPerBook = flag.Int("kb-max-per-book", 0, "не больше стольких фрагментов из одной книги")
	f.askMinCos = flag.Float64("kb-min-cosine", -1, "порог смысловой близости фрагмента")
	f.askSemW = flag.Float64("kb-semantic-weight", -1, "вес смысла против совпадения слов")
	flag.Usage = usage
	flag.Parse()
	return f
}

// dispatchCLI выполняет безголовую команду, если её попросили ключом.
// Возвращает true, когда команда была: интерфейс тогда не запускается.
func dispatchCLI(cfg *config.Config, f *cliFlags) (bool, error) {
	switch {
	case *f.kbList:
		return true, kmaint.List(os.Stdout, cfg)
	case *f.kbDoctor != "":
		return true, kmaint.Doctor(os.Stdout, cfg, *f.kbDoctor, *f.kbQuick)
	case *f.kbIndex != "":
		return true, kmaint.Index(os.Stdout, cfg, *f.kbIndex, flag.Args(), false, *f.kbDry)
	case *f.kbSync != "":
		return true, kmaint.Index(os.Stdout, cfg, *f.kbSync, nil, true, *f.kbDry)
	case *f.kbReindex != "":
		return true, kmaint.Reindex(os.Stdout, cfg, *f.kbReindex, flag.Args())
	case *f.kbYears != "":
		return true, kmaint.Years(os.Stdout, cfg, *f.kbYears, *f.kbRecnt)
	case *f.kbMerge != "":
		return true, kmaint.Merge(os.Stdout, cfg, *f.kbMerge, *f.kbMergeF, *f.kbYes, *f.kbDry)
	case *f.kbRebase != "":
		if strings.TrimSpace(*f.kbRebaseTo) == "" {
			return true, fmt.Errorf("укажите новый корень: --kb-rebase-to /новый/путь")
		}
		return true, kmaint.Rebase(os.Stdout, cfg, *f.kbRebase, *f.kbRebaseFrom, *f.kbRebaseTo, *f.kbDry)
	case *f.kbRefresh != "":
		return true, kmaint.Refresh(os.Stdout, cfg, *f.kbRefresh, *f.kbDry)
	case *f.kbEmbed != "":
		return true, kmaint.Embed(os.Stdout, cfg, *f.kbEmbed, *f.kbDry)
	case *f.graphBuild != "":
		return true, gmaint.Build(os.Stdout, cfg, *f.graphBuild, *f.graphFolder, *f.graphLimit, *f.graphWorkers,
			*f.graphNewModel, *f.graphNewPrompt, *f.graphRedoEmpty, *f.graphLinkNew, *f.graphLog, *f.graphKind, *f.graphNote)
	case *f.graphDoctor != "":
		return true, gmaint.Doctor(os.Stdout, cfg, *f.graphDoctor)
	case *f.graphArchive != "":
		return true, gmaint.Archive(os.Stdout, cfg, *f.graphArchive)
	case *f.graphArchives != "":
		return true, gmaint.Archives(os.Stdout, cfg, *f.graphArchives)
	case *f.graphRestore != "":
		return true, gmaint.Restore(os.Stdout, cfg, *f.graphRestore, *f.graphRestoreYes)
	case *f.graphFind != "":
		// Ключи отбора действуют и здесь: смотреть выдачу при разных числах
		// нужно ровно так же, как ответы.
		if *f.askSense >= 0 {
			cfg.Graph.NeighborSenseWeight = *f.askSense
		}
		if *f.askPool > 0 {
			cfg.Graph.NeighborPool = *f.askPool
		}
		return true, gmaint.Find(os.Stdout, cfg, *f.graphColl, *f.graphFind, *f.graphJSON)
	case *f.graphSum != "":
		return true, gmaint.Summaries(os.Stdout, cfg, *f.graphSum, *f.graphSumMin)
	case *f.kbEvalGen != "":
		coll := *f.kbEvalColl
		if coll == "" {
			coll = cfg.KB.Default
		}
		return true, kmaint.EvalGen(os.Stdout, cfg, coll, *f.kbEvalGen, *f.kbEvalGenN, *f.kbSeed, cfg.Graph.SummaryWorkers)
	case *f.kbSample != "":
		return true, kmaint.Sample(os.Stdout, cfg, *f.kbSample, *f.kbEvalGenN, *f.kbSeed)
	case *f.kbEval != "":
		coll := *f.kbEvalColl
		if coll == "" {
			coll = cfg.KB.Default
		}
		return true, kmaint.Eval(os.Stdout, cfg, coll, *f.kbEval, *f.kbEvalK, *f.kbEvalWeight, *f.kbEvalRRFK, *f.kbEvalTable,
			*f.kbEvalOnly, *f.kbEvalRerank, *f.kbEvalCands, *f.kbEvalSnippet)
	case *f.graphFindings != "":
		return true, gmaint.Findings(os.Stdout, cfg, *f.graphFindings, *f.graphFindingsRating,
			*f.graphFindingsMembers, *f.graphFindingsRedo, *f.graphFindingsDry)
	case *f.graphBench != "":
		models := *f.graphBenchModels
		if models == "" {
			models = cfg.Graph.Model + "," + cfg.Graph.SummaryModel
		}
		return true, gmaint.Bench(os.Stdout, cfg, *f.graphBench, models, *f.graphFolder,
			*f.graphLimit, *f.graphWorkers, *f.graphBenchKeep)
	case *f.graphTune != "":
		return true, gmaint.Tune(os.Stdout, cfg, *f.graphTune, *f.graphTuneList, *f.graphTuneShow)
	case *f.graphDrift != "":
		return true, gmaint.Drift(os.Stdout, cfg, *f.graphDrift, *f.graphDriftSim, *f.graphDriftShow)
	case *f.graphEntryEval != "":
		return true, gmaint.EntryEval(os.Stdout, cfg, *f.graphEntryEval, gmaint.EntryEvalOpts{
			Collection: *f.graphEntryColl, Limit: *f.graphEntryLimit, Show: *f.graphEntryShow,
			Entities: *f.graphEntryEnts})

	case *f.graphBook != "":
		return true, gmaint.Book(os.Stdout, cfg, *f.graphBook, *f.graphBookName)

	case *f.graphGroupsBuild != "":
		return true, gmaint.GroupsBuild(os.Stdout, cfg, *f.graphGroupsBuild, *f.graphGroupsFrom,
			*f.graphGroupsMinCos, *f.graphGroupsMutual)

	case *f.graphDropBook != "":
		return true, gmaint.DropBook(os.Stdout, cfg, *f.graphDropBook, *f.graphBookName, *f.graphRestoreBook, *f.graphDropApply)

	case *f.graphCompact != "":
		return true, gmaint.Compact(os.Stdout, cfg, *f.graphCompact, *f.graphCompactCheck, *f.graphCompactForce)

	case *f.graphMerge != "":
		return true, gmaint.Merge(os.Stdout, cfg, *f.graphMerge, *f.graphMergeFile, *f.graphMergeLevel,
			*f.graphMergeMinSame, *f.graphMergeDrop, *f.graphMergeDry)
	case *f.graphResolve != "":
		return true, gmaint.Resolve(os.Stdout, cfg, *f.graphResolve, *f.graphResolveMinCos, *f.graphResolveMinCosMut, *f.graphResolveFull,
			*f.graphResolveCross, *f.graphResolveShow, *f.graphResolveOut)
	case *f.graphEmbed != "":
		return true, gmaint.Embed(os.Stdout, cfg, *f.graphEmbed, *f.graphEmbedRecount)
	case *f.graphRecheck != "":
		return true, gmaint.Recheck(os.Stdout, cfg, *f.graphRecheck, *f.graphRecheckN, 0)
	case *f.graphComm != "":
		return true, gmaint.Communities(os.Stdout, cfg, *f.graphComm, *f.graphFreshComm, *f.graphCarrySim)
	case *f.graphStatus != "":
		name := *f.graphStatus
		if name == "all" || name == "все" {
			name = ""
		}
		return true, gmaint.Status(os.Stdout, cfg, name, *f.graphFolder)
	}
	return false, nil
}

func run() error {
	f := parseFlags()

	if *f.showVer {
		fmt.Println("ollchat " + buildinfo.Describe(version))
		return nil
	}

	path := *f.cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	path = config.ExpandPath(path)

	if *f.initConfig {
		if err := config.WriteTemplate(path); err != nil {
			return err
		}
		fmt.Println("Файл настроек создан: " + path)
		fmt.Println("Отредактируйте адреса серверов и запустите ollchat.")
		return nil
	}

	cfg, exists, err := config.Load(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("файл настроек %s не найден.\nСоздайте его командой: ollchat --init-config", path)
	}

	if *f.stepsFile != "" {
		cfg.Log.StepsFile = *f.stepsFile
	}

	// Какой граф открывать — решается здесь, до первого открытия: ключ сильнее
	// настройки. Дальше по коду граф берётся по выбранному имени, и отдельно
	// протаскивать его через два десятка вызовов не нужно.
	selected := cfg.Graph.Name
	if *f.graphName != "" {
		selected = *f.graphName
	}
	// UseGraph выбирает и каталог графа, и его настройки сборки: раздел
	// [graph.<имя>] перекрывает общий [graph] по полям.
	if err := cfg.UseGraph(selected); err != nil {
		return err
	}
	if *f.graphGroups != "" {
		cfg.Graph.Groups = *f.graphGroups
	}
	if selected != "" {
		fmt.Fprintf(os.Stderr, "граф: %s (каталог %s)\n", selected, graph.DirFor(selected))
	}

	// Действия с базой знаний идут без интерфейса: обход тысяч книг занимает
	// часы, и держать ради него открытое окно незачем.
	if done, err := dispatchCLI(cfg, f); done {
		return err
	}

	// Рабочий каталог: флаг важнее конфига.
	root := cfg.Sandbox.Root
	if *f.workDir != "" {
		root = *f.workDir
	}
	sandbox, err := permissions.NewSandbox(root, cfg.Sandbox.AllowOutside,
		cfg.Sandbox.FollowSymlinks, cfg.Sandbox.MaxFileKB)
	if err != nil {
		return err
	}
	sandbox.SetMaxPDFMB(cfg.Sandbox.MaxPDFMB)

	ruleSet, err := permissions.Compile(cfg.Permissions.Allow, cfg.Permissions.Ask,
		cfg.Permissions.Deny, sandbox.Root())
	if err != nil {
		return err
	}

	mode := cfg.General.Mode
	if *f.modeFlag != "" {
		mode = *f.modeFlag
	}
	guard := permissions.NewGuard(ruleSet, sandbox, mode)
	if err := guard.SetMode(mode); err != nil {
		return err
	}

	// База знаний открывается заранее: инструменты поиска получают её саму,
	// а не путь, и файловой системы не касаются вовсе.
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return fmt.Errorf("база знаний: %w", err)
	}
	defer base.Close()

	// Сервер выбирается раньше набора инструментов: пустой kb.embed_url
	// означает «тот же сервер, что для чата», и его адрес нужен уже здесь.
	// Выбор сервера: флаг важнее конфига.
	srvName := cfg.General.DefaultServer
	if *f.serverName != "" {
		srvName = *f.serverName
	}
	srv, ok := cfg.ServerByName(srvName)
	if !ok {
		return fmt.Errorf("сервер %q не описан в файле настроек %s (доступны: %v)",
			srvName, path, cfg.ServerNames())
	}

	model := srv.Model
	if *f.modelName != "" {
		model = *f.modelName
	}

	// Токен Confluence на сеанс: приходит командой /confluencetoken и главнее
	// файла с переменной окружения. Держится в памяти, на диск не попадает.
	cfSession := &confluence.Session{}

	// Граф держится открытым между вызовами инструментов: в одном обмене
	// модель зовёт graph_search, graph_neighbors и graph_overview подряд,
	// и каждый вызов открывал граф заново — 11.7 с на коллекции books
	// (замер 28.08.2026). Срок простоя короткий: открытый граф стоит
	// гигабайт памяти, и держать его весь сеанс ради вопроса раз в час незачем.
	// Общая библиотека организации, если она настроена. Пустой адрес —
	// работаем с файлами, как раньше; ошибка здесь означает недоступную службу
	// или неверный ключ, и узнать об этом надо при запуске, а не на первом
	// вопросе человека.
	library, err := kbremote.FromConfig(context.Background(), cfg.KB)
	if err != nil {
		return fmt.Errorf("общая библиотека: %w", err)
	}

	// Живые числа отбора: один экземпляр на реестр инструментов и на интерфейс,
	// иначе крутилка /graph tune меняла бы поиск в диалоге, а модель искала бы
	// по-старому.
	live := tools.NewLive(gmaint.NeighborRank(cfg),
		cfg.KB.TopK, cfg.KB.MaxPerBook, cfg.KB.MinCosine, cfg.KB.SemanticWeight)

	// Кэш открытых графов один на программу: у инструментов модели и у поиска
	// в интерфейсе он общий, иначе в памяти жили бы два одинаковых графа.
	graphCache := gmaint.CacheFor(cfg, 5*time.Minute)

	registry, err := tools.NewRegistry(cfg.Agent.Tools, tools.Options{
		GraphRules:           cfg.Graph.Rules(),
		KBTableBoost:         cfg.KB.TableBoost,
		KBAbstainGap:         cfg.KB.AbstainGap,
		KBAbstainScore:       cfg.KB.AbstainScore,
		Live:                 live,
		Library:              libraryOrNil(library),
		Sandbox:              sandbox,
		BashTimeout:          cfg.Agent.BashTimeoutDuration(),
		MaxOutputKB:          cfg.Agent.MaxOutputKB,
		KB:                   base,
		KBDir:                cfg.KB.Dir,
		KBDefault:            cfg.KB.Default,
		GraphCache:           graphCache,
		GraphNeighbors:       gmaint.NeighborRank(cfg),
		GraphMinRating:       cfg.Graph.MinRating,
		GraphRelationSnippet: cfg.Graph.RelationSnippet,
		Reranker:             kbrerank.New(cfg.KB.RerankOptions()),
		RerankOpts: kb.RerankOpts{
			Candidates: cfg.KB.RerankCandidates,
			Snippet:    cfg.KB.RerankSnippet,
		},
		KBTopK:         cfg.KB.TopK,
		KBMaxPerBook:   cfg.KB.MaxPerBook,
		Semantic:       cfg.KB.Semantic,
		QueryTimeout:   cfg.KB.QueryTimeoutDuration(),
		MinCosine:      cfg.KB.MinCosine,
		SemanticWeight: cfg.KB.SemanticWeight,
		AnswerStyle:    cfg.KB.AnswerStyle,
		SearxURL:       cfg.Web.SearxngURL,
		SearxTimeout:   cfg.Web.TimeoutDuration(),
		ConfluenceURL:  cfg.Confluence.URL,
		ConfluenceToken: confluence.Resolver(cfSession, config.ExpandPath(cfg.Confluence.TokenFile),
			cfg.Confluence.TokenCmd, cfg.Confluence.TokenEnv),
		ConfluenceTimeout: cfg.Confluence.TimeoutDuration(),
		Embedder:          kbembed.New(cfg.KB.EmbedOptions(), srv.URL, srv.TimeoutDuration(), srv.Headers),
	})
	if err != nil {
		return err
	}

	logPattern, err := cfg.Log.NamePattern()
	if err != nil {
		return fmt.Errorf("log.file_pattern: %w", err)
	}
	logger := chatlog.NewFromPattern(cfg.Log.Dir, logPattern, time.Now(), cfg.Log.Enabled)
	defer logger.Close()
	stepsPattern, err := cfg.Log.StepsPattern()
	if err != nil {
		return fmt.Errorf("log.steps_file_pattern: %w", err)
	}
	steps := steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollchat", cfg.Log.Enabled)
	defer steps.Close()
	if err := logger.WriteSessionHeader(srv.Name, srv.URL, model); err != nil {
		fmt.Fprintln(os.Stderr, "предупреждение: не удалось писать журнал: "+err.Error())
	}

	store := session.NewStore(sessionDir())

	// Безголовый режим: спросить модель и напечатать ответ. Стоит здесь,
	// потому что ему нужны и сервер с моделью, и — при --tools — тот же реестр
	// инструментов и те же правила, что у диалога.
	if *f.askQ != "" || *f.askStdin || *f.askFile != "" {
		numCtx := 0
		if strings.TrimSpace(*f.askCtx) != "" {
			n, err := config.ParseTokens(*f.askCtx)
			if err != nil {
				return fmt.Errorf("--num-ctx: %w", err)
			}
			numCtx = n
		}
		return runAsk(cfg, srv, model, askOpts{
			Question:  *f.askQ,
			Questions: *f.askFile,
			Stdin:     *f.askStdin,
			Repeat:    *f.askRep,
			JSON:      *f.askJSON,
			ShowMix:   *f.askShow,

			Temperature: *f.askTemp, HasTemp: *f.askTemp >= 0,
			Seed: *f.askSeed, HasSeed: *f.askSeed >= 0,
			NumCtx: numCtx, Think: *f.askThink, Tools: *f.askTools,

			Mix: *f.askMixK, Collection: *f.askColl,
			Entities: *f.askEntities, Neighbors: *f.askNeighbors,
			SenseWeight: *f.askSense, HasSense: *f.askSense >= 0, Pool: *f.askPool,
			TopK: *f.askTopK, MaxPerBook: *f.askPerBook,
			MinCosine: *f.askMinCos, HasMinCos: *f.askMinCos >= 0,
			SemanticWeight: *f.askSemW, HasSemWeight: *f.askSemW >= 0,

			registry: registry, guard: guard,
		})
	}

	// Служба знаний. Стоит здесь, потому что ей нужны и библиотека, и тот же
	// реестр инструментов, что у диалога: клиент обязан получить ровно ту
	// выдачу, которую увидел бы, работая с файлами.
	if *f.serveAddr != "" {
		return runServe(cfg, *f.serveAddr, *f.serveMCP, registry, base, gmaint.CacheFor(cfg, time.Hour))
	}

	m := ui.New(cfg, guard, registry, logger, steps, store, srv, model)
	m.SetLive(live)             // тот же экземпляр, что у инструментов
	m.SetGraphCache(graphCache) // и тот же кэш графов
	m.SetConfluence(cfSession)
	// Полноэкранный режим и режим мыши в Bubble Tea v2 задаются самим экраном
	// (полями AltScreen и MouseMode в Model.View), а не настройками программы:
	// мышь должна переключаться на лету, иначе текст ленты не выделить.
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

// sessionDir возвращает каталог для сохранённых сессий.
// libraryOrNil отдаёт интерфейс или пустое значение.
//
// Прямое присваивание `*kbremote.Client` в поле интерфейса сделало бы его
// ненулевым даже при nil-указателе — известная ловушка Go, из-за которой
// проверка «библиотека настроена?» стала бы всегда истинной, а поиск ходил бы
// в никуда.
func libraryOrNil(c *kbremote.Client) kb.Library {
	if c == nil {
		return nil
	}
	return c
}

func sessionDir() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".local", "share", "ollchat", "sessions")
	}
	return filepath.Join(os.TempDir(), "ollchat-sessions")
}

// dryRunFlagHelp — описание ключа сухого прогона.
//
// Вынесено в постоянную, потому что это обещание пользователю: перечисленные
// здесь команды обязаны ничего не менять при --kb-dry-run. Проверяется тестом.
const dryRunFlagHelp = "только показать, что будет сделано: " +
	"с --kb-embed, --kb-sync, --kb-index, --kb-refresh, --kb-rebase"

func usage() {
	fmt.Fprintf(os.Stderr, `ollchat %s — TUI-клиент и агент для Ollama

Использование:
  ollchat [флаги]

Флаги:
`, version)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Примеры:
  ollchat --init-config           создать файл настроек
  ollchat                         запуск с сервером по умолчанию
  ollchat --ask "вопрос"          спросить модель и напечатать ответ (без интерфейса)
  ollchat --questions q.txt --json --mix graph --graph-sense 1.5 --repeat 3
                                  замер: каждый вопрос трижды при заданных числах отбора
  ollchat --server lab            запуск с указанным сервером
  ollchat --model qwen3.6:latest  запуск с указанной моделью
  ollchat --cwd ~/projects/app    песочница в другом каталоге

База знаний по книгам:
  ollchat --kb-index go /mnt/books/Go   собрать коллекцию (можно под nohup)
  ollchat --kb-sync go                  доиндексировать новое, убрать пропавшее
  ollchat --kb-years go                 проставить книгам год издания
  ollchat --kb-reindex go /путь/к/книге  перечитать книгу заново
  ollchat --kb-merge go                 уплотнить: выбросить удалённое с диска
  ollchat --kb-embed go --kb-dry-run    оценить работу по смыслам
  ollchat --kb-embed go                 посчитать смыслы (можно под nohup)
  ollchat --kb-refresh projectdocs      долить новое и сразу досчитать смыслы
  ollchat --kb-rebase books --kb-rebase-to /новый/путь --kb-dry-run
                                        перенос: переписать корень, не переиндексируя
  ollchat --graph-build books --graph-folder /AI/ --graph-limit 200
                                        собрать граф понятий по каталогу книг
  ollchat --graph-status books          состояние графа: понятия, связи, охват
  ollchat --graph-status books --graph-folder /Infosec/
                                        то же по одному каталогу: сколько осталось
  ollchat --graph-drift books           пора ли пересчитывать сообщества
  ollchat --graph-resolve books         двойники понятий: показать, ничего не меняя
  ollchat --graph-merge books --graph-merge-file verdicts.tsv
                                        склеить двойников (снимается --graph-merge-drop)
  ollchat --graph-find "как связаны RAG и граф знаний"
                                        поиск по графу: понятия, связи, цитаты
  ollchat --kb-eval-gen набор.toml --kb-seed 42
                                        собрать замерный набор для поиска
  ollchat --kb-eval набор.toml          замерить качество поиска
  ollchat --kb-list                     что уже собрано
  ollchat --kb-doctor go                проверка: пропавшие книги, сканы, повторы
  ollchat --kb-doctor all --kb-quick    то же по всем коллекциям, без сверки содержимого
`)
}
