package maint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Doctor — состояние графа одним взглядом и что с ним делать.
//
// **Зачем отдельно от --graph-status.** Статус отвечает «сколько чего»,
// а доктор — «что не в порядке и какой командой это чинится». Разница
// не косметическая: 02.09.2026 обзор тем работал по трети графа и молчал
// об этом. Числа, по которым это видно, были доступны и раньше — понятий
// в графе и понятий, попавших в темы, — но лежали в разных командах, и
// сопоставить их никто не догадался.
//
// Ни одна проверка здесь не занимает карту и не пересчитывает разбиение:
// доктор должен отвечать за секунды, иначе его перестанут звать.
// Doctor печатает отчёт в stdout, ход работы — в stderr.
func Doctor(stdout io.Writer, cfg *config.Config, name string) error {
	return DoctorTo(stdout, os.Stderr, cfg, name)
}

// Числа выборки провенанса. Три тысячи — замер 16.09.2026: секунды чтения,
// и одна битая ссылка на тысячу дала бы в выборке три. Зерно постоянное,
// чтобы два запуска доктора подряд давали одни числа и разница означала
// изменение графа, а не другую выборку.
const (
	provenanceSample = 3000
	provenanceSeed   = 20260916
)

// DoctorTo — то же с явным стоком для хода работы: интерфейсу, который рисует
// экран сам, нужен io.Discard, иначе строки хода лягут поверх ленты.
func DoctorTo(stdout, progress io.Writer, cfg *config.Config, name string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	if name == "" {
		name = cfg.KB.Default
	}
	if name == "" {
		names, err := base.Names()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("в базе знаний нет коллекций")
		}
		name = names[0]
	}

	coll, err := base.Open(name)
	if err != nil {
		return graphNeedsLocalFiles(cfg, name, err)
	}
	chunks := coll.ChunkCount()

	// Ход работы — обязателен, а не украшение.
	//
	// Замер 02.09.2026: доктор идёт 44 секунды и берёт 1.5 ГБ, и всё это время
	// молчал. Молчание длинной проверки неотличимо от зависания — эта ошибка
	// уже записана в project_kb.md, и здесь я наступил на неё снова.
	//
	// Пишем в поток ошибок: вывод доктора читают и глазами, и скриптом,
	// и ход работы не должен попадать во второй.
	stage := newDoctorStage(progress)
	stage.say("открываю граф — на большом это полминуты")
	g, err := graph.Open(coll.Dir(), chunks, cfg.Graph.Rules())
	if err != nil {
		stage.done()
		return err
	}
	defer g.Close()
	stage.say("читаю разметку тем")

	st := g.Stats(chunks)
	cst := coll.Stats()
	ms := deadMarkStats(coll, g)
	// Разбор считается по кускам ЖИВЫХ книг, а не по числу отметок в журнале:
	// там лежат и отметки удалённых книг. До 07.10.2026 заголовок брал
	// st.Covered — все отметки — и делил их на все куски хранилища вместе
	// с удалёнными. На проверочной коллекции (у живой книги разобраны 3 куска
	// из 10, у удалённых книг 64 отметки) выходило «разобрано кусков 67 из 34
	// (197%), осталось 0» — и ни одного совета разобрать остаток.
	// Каталоги считаются тем же проходом.
	stage.say("считаю разбор по кускам коллекции")
	cov, err := liveCoverage(coll, g, cfg.KB.Roots, stage)
	if err != nil {
		stage.done()
		return err
	}
	// Журналы на сдвиг записей — по сырым файлам: доктор иначе видит только
	// принятые записи, а чтение журналов принимает любые (journalshift.go).
	// Реестр — все записи коллекции, с удалёнными: отметка удалённой книги —
	// законный след, а не мусор сдвига.
	stage.say("проверяю журналы графа на сдвиг записей")
	inRegistry := map[uint32]bool{}
	for _, b := range coll.Books() {
		inRegistry[b.ID] = true
	}
	shift, shiftErr := g.JournalShift(func(doc uint32) bool { return inRegistry[doc] })

	stage.done()
	fmt.Fprintf(stdout, "коллекция %s · граф\n", name)
	// Живых понятий, а не записей реестра: склейка двойников лежит отдельным
	// журналом и снимается, поэтому запись из реестра не исчезает. Показывать
	// одно число вместо двух нельзя — после склейки 2693 двойников доктор
	// уверял, что понятий по-прежнему 161 239.
	if st.Merged > 0 {
		fmt.Fprintf(stdout, "  понятий %d (в реестре %d, поглощено склейкой %d), связей %d, упоминаний %d\n",
			st.Live(), st.Entities, st.Merged, st.Edges, st.Mentions)
	} else {
		fmt.Fprintf(stdout, "  понятий %d, связей %d, упоминаний %d\n", st.Entities, st.Edges, st.Mentions)
	}

	// 1. Разбор кусков.
	pct := 0
	if cov.total > 0 {
		pct = 100 * cov.marked() / cov.total
	}
	fmt.Fprintf(stdout, "  разобрано кусков %d из %d (%d%%), осталось %d\n", cov.marked(), cov.total, pct, cov.pending)
	if cov.skipped > 0 || cov.empty > 0 || cov.service > 0 {
		fmt.Fprintf(stdout, "    с понятиями %d, пустых %d, не разобрала модель %d, служебных %d\n",
			cov.done, cov.empty, cov.skipped, cov.service)
		fmt.Fprintf(stdout, "      «служебных» — оглавления и списки литературы, модели не показывались\n")
	}
	if cov.again > 0 {
		fmt.Fprintf(stdout, "    из разобранных сборка возьмёт снова %d — «служебные» без признака: "+
			"забыты чисткой графа или признак снят; они же в «осталось»\n", cov.again)
	}
	if ms.Gone > 0 {
		fmt.Fprintf(stdout, "    отметок книг, которых в коллекции НЕТ: %d (в %d книгах) — "+
			"в счёт выше не входят\n", ms.Gone, ms.GoneBooks)
	}
	if shiftErr != nil {
		fmt.Fprintf(stdout, "  журналы: проверка на сдвиг записей не удалась — %v\n", shiftErr)
	} else {
		printJournalShift(stdout, shift)
	}

	fmt.Fprintf(stdout, "  %s\n", g.PromptLine())

	// 2. Векторы понятий графа.
	var needGraphEmbed bool
	if info := g.VectorsInfo(); info.Ready {
		if info.Count >= st.Entities {
			fmt.Fprintf(stdout, "  векторы понятий: посчитаны все %d (%s)\n", info.Count, info.Model)
		} else {
			needGraphEmbed = true
			fmt.Fprintf(stdout, "  векторы понятий: %d из %d — %d понятий не находятся по смыслу\n",
				info.Count, st.Entities, st.Entities-info.Count)
		}
	} else if st.Entities > 0 {
		needGraphEmbed = true
		if p := g.VectorsProblem(); p != "" {
			fmt.Fprintf(stdout, "  векторы понятий на диске есть, но не приняты: %s\n", p)
		} else {
			fmt.Fprintln(stdout, "  векторы понятий не считались — смыслового входа в граф нет")
		}
	}
	// Индекс троек — надстройка по желанию: печатается, только когда он есть
	// или включён вход по нему без индекса.
	if ei := g.EdgeVectorsInfo(); ei.Ready {
		fmt.Fprintf(stdout, "  индекс троек: %d связей (%s)\n", ei.Count, ei.Model)
	} else if ei.Problem != "" {
		fmt.Fprintf(stdout, "  индекс троек на диске есть, но не принят: %s\n", ei.Problem)
	} else if g.Rules().TripleLimit > 0 {
		fmt.Fprintln(stdout, "  вход по тройкам включён (triple_limit), а индекса нет — посчитать: --graph-embed-edges")
	}
	// Квота без самого входа — настройка, которая молча не делает ничего:
	// места резервируются для того, кто за ними не придёт.
	if r := g.Rules(); r.TripleQuota > 0 && r.TripleLimit == 0 {
		fmt.Fprintln(stdout, "  мест входа отдано тройкам (triple_quota), но сам вход выключен (triple_limit = 0) — настройка не делает ничего")
	}
	if p := g.Descriptions().Problem(); p != "" {
		fmt.Fprintf(stdout, "  описания понятий: %s\n", p)
	}
	// Карта книг: переживёт ли граф переиндексацию коллекции.
	books := knownBooks(coll)
	if mapped, same, moved, unknown, reread, err := graph.BookMapReport(g.Dir(), books); err != nil {
		fmt.Fprintf(stdout, "  карта книг графа: НЕТ (%v) — граф не переживёт переиндексацию коллекции; записать: --graph-record-books\n",
			err)
	} else {
		// Четыре слагаемых, а не три: без «перечитано» строка не сходилась
		// с общим числом книг (этап 110, А0.3).
		fmt.Fprintf(stdout, "  карта книг графа: %d книг, на своих номерах %d, переехало %d, перечитано %d, в коллекции больше нет %d",
			mapped, same, moved, reread, unknown)
		if same+moved+reread+unknown != mapped {
			fmt.Fprintf(stdout, " (не сходится: %d+%d+%d+%d≠%d)", same, moved, reread, unknown, mapped)
		}
		if moved > 0 {
			fmt.Fprintf(stdout, " — НУМЕРАЦИЯ СМЕНИЛАСЬ: ollchat --graph-rebase-books %s --kb-dry-run", name)
		}
		fmt.Fprintln(stdout)
	}
	if all := coll.MatchingDocs(kb.ChunkFilter{}); len(books) < len(all) {
		fmt.Fprintf(stdout, "  книг без хеша содержимого: %d из %d — ollchat --kb-hash %s (карта книг без них неполна)\n",
			len(all)-len(books), len(all), name)
	}

	// 3. Векторы кусков самой коллекции.
	needKBEmbed := cst.Vectors < cst.Chunks
	switch {
	case cst.Vectors == 0 && cst.Chunks > 0:
		fmt.Fprintln(stdout, "  векторы кусков: не считались — смысловой поиск по книгам выключен")
	case needKBEmbed:
		fmt.Fprintf(stdout, "  векторы кусков: %d из %d — %d кусков находятся только по словам\n",
			cst.Vectors, cst.Chunks, cst.Chunks-cst.Vectors)
	default:
		fmt.Fprintf(stdout, "  векторы кусков: посчитаны все %d\n", cst.Chunks)
	}

	// 4. Темы. Главное здесь — понятия, не попавшие ни в одну тему: обзор тем
	// их не видит, и молча.
	var needCommunities, needSummaries bool
	// Схема 2: журнал синонимов с источником. У рабочего графа его нет,
	// и раздел не печатается вовсе — молчание значит «формат 1», а не «пусто».
	if ar, ok := g.AliasReportOf(5); ok {
		if ar.Records == 0 {
			fmt.Fprintln(stdout, "  синонимы (схема 2): журнал пуст — ни одного синонима не извлечено")
		} else {
			fmt.Fprintf(stdout, "  синонимы (схема 2): вхождений %d в %d кусках, пар понятие–синоним %d у %d понятий\n",
				ar.Records, ar.Chunks, ar.Pairs, ar.Entities)
			fmt.Fprintf(stdout, "    переводов %d, аббревиатур %d, иных написаний %d; совпадают с именем другого понятия %d\n",
				ar.Translations, ar.Acronyms, ar.Other, ar.Clashes)
			fmt.Fprintln(stdout, "    каждое вхождение по устройству схемы найдено в тексте своего куска")
			for _, t := range ar.Top {
				mark := ""
				if t.Clash {
					mark = "  ← чужое имя"
				}
				fmt.Fprintf(stdout, "      %5d × %s ← %s%s\n", t.Count, t.Entity, t.Alias, mark)
			}
		}
	}

	// Связывания при сборке (--graph-link-new): решений и очередь человеку.
	if l := g.Links(); l != nil && (l.Queued() > 0 || len(l.Judged()) > 0) {
		fmt.Fprintf(stdout, "  связывания: решений арбитра «ДА» %d, ждут человека %d (links.jsonl)\n",
			len(l.Judged()), l.Queued())
		if l.Queued() > 0 {
			fmt.Fprintln(stdout, "    разобрать глазами: /graph review в чате")
		}
	}

	comms, cerr := g.LoadCommunities()

	// Строение графа: связность целиком, одиночки, опора связей (этап 101, Г5).
	// Меры взяты у «Knowledge Graphs and LLMs in Action» (2025, стр. 124).
	// Связность тем считается тем же обходом — матрица смежности строится один раз.
	stage.say("меряю строение графа")
	shape, conn := g.Shape(comms)
	stage.done()
	fmt.Fprintf(stdout, "  строение: понятий со связями %d, без связей %d (%d%%)\n",
		shape.Nodes, shape.Isolated, shape.IsolatedShare())
	if shape.Isolated > 0 {
		fmt.Fprintln(stdout, "    понятие без связей не попадёт в тему никогда: разбиение считается по связям")
		// Откуда одиночки: без записей связей (пустое извлечение), связи
		// выродились в петли при склейке, пустые узлы после чистки (этап 101, Г6).
		orph := g.Orphans(0)
		fmt.Fprintf(stdout, "    из них модель не назвала ни одной связи у %d, связи ушли в склейку у %d, пустых узлов %d, с одним упоминанием %d\n",
			orph.NoRawEdges, orph.LostToMerge, orph.Empty, orph.SingleMention)
	}
	if shape.Nodes > 0 {
		fmt.Fprintf(stdout, "    наибольшая связная часть %d понятий (%d%%), всего частей %d\n",
			shape.Largest, shape.LargestShare(), shape.Parts)
		fmt.Fprintf(stdout, "    связей различных %d, из них на одном подтверждении %d (%d%%)\n",
			shape.Pairs, shape.PairsOnce, shape.OnceShare())
		// По источникам, а не по кускам (этап 101, Г9): соседние куски одной
		// книги перекрываются, и фраза из перекрытия подтверждает связь дважды.
		corr := g.Corroboration()
		fmt.Fprintf(stdout, "    по источникам: с одним источником %d%% (соседние куски одной книги — один источник), копий из перекрытия среди подтверждений %d%%\n",
			corr.SingleOriginShare(), corr.InflationShare())
		// Целостность провенанса — выборкой, а не по всем связям: их 1,6 млн,
		// и у каждой надо прочитать кусок. «Agentic RAG Systems» (Norman, 2026,
		// стр. 129) держит эту метрику в обязательных: граф, который тихо
		// портится, даёт поиск, «постепенно становящийся неверным, причём
		// ничто не выглядит поломанным». До 16.09.2026 доктор считал ЧИСЛО
		// подтверждений, но не проверял, существует ли кусок за ними.
		if prov := g.Provenance(coll, provenanceSample, provenanceSeed); prov.Checked > 0 {
			fmt.Fprintf(stdout, "    провенанс (выборка %d связей): кусок-источник найден у %d, НЕ найден у %d (%.2f%%)\n",
				prov.Checked, prov.Checked-prov.Missing, prov.Missing, 100*prov.Bad())
			fmt.Fprintf(stdout, "      имена связи в куске: оба %d%%, одно %d%%, ни одного %d%% (последнее — вероятная ошибка извлечения)\n",
				100*prov.Both/prov.Checked, 100*prov.One/prov.Checked, 100*prov.None/prov.Checked)
			if prov.Missing > 0 {
				fmt.Fprintf(stdout, "      ВНИМАНИЕ: выдержку по таким связям показать нечем. Примеры:\n")
				for _, x := range prov.Examples {
					fmt.Fprintf(stdout, "        %s\n", x)
				}
			}
		}
		if comms != nil && comms.ByOrigins != cfg.Graph.Rules().WeightsByOrigins {
			fmt.Fprintf(stdout, "    ВНИМАНИЕ: разбиение тем считано на весах %s, а настройка graph.weights_by_origins = %v — пересчитать: ollchat --graph-communities %s\n",
				weightsWord(comms.ByOrigins), cfg.Graph.Rules().WeightsByOrigins, name)
		}
		if shape.HubLimit > 0 {
			fmt.Fprintf(stdout, "    хабов (от %d связей): %d (%.3f%% верхушки) — через них не идут цепочки\n",
				shape.HubLimit, shape.Hubs, shape.HubShare())
		}
	}

	// Битая разметка — не «темы не размечены»: совет пересчитать поверх неё
	// выбросил бы описания тем, а битый файл ушёл бы в копию поверх прежней,
	// ещё целой (см. brokenCommunities). До 07.10.2026 доктор их не различал.
	var brokenTopics error
	switch {
	case cerr != nil:
		brokenTopics = brokenCommunities(g, cerr)
		fmt.Fprintf(stdout, "  темы: разметка НЕ ЧИТАЕТСЯ — %v\n", cerr)
	case comms == nil || len(comms.List) == 0:
		needCommunities = true
		fmt.Fprintln(stdout, "  темы: не размечены — обзор тем работать не будет")
	default:
		var lvl0, cand, described int
		inTopic := make(map[uint32]bool)
		for _, c := range comms.List {
			if c.Level != 0 {
				continue
			}
			lvl0++
			for _, m := range c.Members {
				inTopic[m] = true
			}
			if len(c.Members) >= 5 { // то же правило, что у --graph-summaries
				cand++
				if strings.TrimSpace(c.Summary) != "" {
					described++
				}
			}
		}
		// Live, а не All: поглощённое склейкой понятие не самостоятельный узел,
		// в темах его быть и не должно. Обход по All считал каждую склейку
		// «понятием вне тем» — после склейки 15 311 пар число подскочило
		// с 17 762 до 33 059 (11%), хотя живых вне тем осталось столько же
		// (12 530, все без связей). На этом же числе стоит рекомендация
		// «пора пересчитать разметку», то есть дефект не только пугал,
		// но и звал пересчитывать без повода (найдено 15.09.2026).
		uncovered := countUncovered(g.Entities().Live(), inTopic)
		if comms.Algorithm == graph.AlgoLeiden {
			fmt.Fprintf(stdout, "  разбиение: Лейден, уровней в лестнице %d (на диске два)\n", comms.LeidenLevels)
		}
		fmt.Fprintf(stdout, "  темы: %d (нижнего уровня %d), с описанием %d из %d кандидатов\n",
			len(comms.List), lvl0, described, cand)
		if comms.Entities > 0 && st.Entities > comms.Entities {
			fmt.Fprintf(stdout, "    разбиение считалось при %d понятиях, сейчас их %d\n",
				comms.Entities, st.Entities)
		}
		if uncovered > 0 {
			// Делим на ЖИВЫЕ понятия, а не на записи реестра: в числителе
			// живые (`Entities().Live()`), и делить их на реестр с поглощёнными
			// склейкой значит занижать долю — 24.09.2026 это давало 3 % вместо
			// 4 % и отодвигало порог пересчёта. Та же ловушка, что в числителе
			// 15.09.2026, только с другой стороны дроби.
			live := st.Live()
			share := 100 * uncovered / max(live, 1)
			fmt.Fprintf(stdout, "    понятий вне тем: %d (%d%%) — обзор тем их не видит\n", uncovered, share)
			needCommunities = repartitionDue(uncovered, live)
		}
		if described < cand {
			needSummaries = true
		}
		// Связность тем: Louvain не гарантирует, что тема — одно целое, и
		// описание темы из двух несвязных половин описывает две разные вещи.
		// Посчитана выше, вместе со строением графа.
		if conn.Disconnected > 0 {
			fmt.Fprintf(stdout, "    несвязных тем: %d из %d (%d%%), частей в них %d, самая рваная — на %d\n",
				conn.Disconnected, conn.Communities, conn.Share(), conn.Parts, conn.Largest)
		} else if conn.Communities > 0 {
			fmt.Fprintln(stdout, "    все темы связны")
		}
	}

	// 5. Что делать. Порядок важен: сперва разбор, потом разметка, потом описания.
	fmt.Fprintln(stdout, "\nчто сделать:")
	var n int
	step := func(cmd, why string) {
		n++
		fmt.Fprintf(stdout, "  %d. %s\n     %s\n", n, cmd, why)
	}
	if g.Locked() {
		fmt.Fprintln(stdout, "  сейчас идёт сборка — советы ниже выполняйте после её остановки")
	}
	// Сдвиг журналов — первым: любой следующий шаг (сборка, чистка,
	// уплотнение) пишет поверх испорченного и запутывает разбор. Без шага
	// доктор сказал бы «всё в порядке» под строкой со сдвигом.
	if shiftErr != nil {
		step(fmt.Sprintf("ollchat --graph-doctor %s", name),
			"журналы графа на сдвиг записей не проверены (см. «журналы» выше) — повторить, а до того граф не править")
	}
	if shift.Bad() > 0 {
		step(fmt.Sprintf("ollchat --graph-archive %s — и больше ничего: граф не править", name),
			"в журналах записи с недопустимыми полями — они прочитаны со сдвигом (см. «журналы» выше); "+
				"снять архив и разобрать с владельцем, до того ни сборки, ни чистки, ни уплотнения")
	}
	// Без отметки во время сборки — её же хвост: отметки ещё в её буфере.
	if shift.Unmarked() > 0 && shift.Bad() == 0 && !g.Locked() {
		step(fmt.Sprintf("ollchat --graph-archive %s — и разобрать с владельцем, граф не править", name),
			"упоминания или связи из кусков без отметки разбора (см. «журналы» выше): след последнего жёсткого обрыва "+
				"или сдвиг на 4 и 8 байт, которого поля записи не выдают; что из двух — решает место первой и доля после неё")
	}
	if cov.pending > 0 {
		// Называем настоящие каталоги, а не «<каталог>»: ключ --graph-folder
		// отбирает книги по куску пути **внутри библиотеки**, и человеку
		// неоткуда узнать, какие пути там есть, кроме как заглянув на диск.
		printCoverage(stdout, cov.folders)
		// В советах — только каталоги с остатком: закрытые видны в таблице,
		// и «… и ещё N каталогов» не должно считать их работой.
		var left []folderPending
		for _, f := range cov.folders {
			if f.pending > 0 {
				left = append(left, f)
			}
		}
		var more int
		// В советах список обрезаем: за одну ночь берут один каталог, а вся
		// картина уже показана таблицей выше.
		if len(left) > 6 {
			more = len(left) - 6
			left = left[:6]
		}
		if len(left) > 0 {
			// Одним шагом со списком каталогов, а не пятью одинаковыми советами:
			// объяснение у них общее, а разное только имя и число.
			n++
			fmt.Fprintf(stdout, "\n  %d. разобрать оставшееся — по каталогу за раз:\n", n)
			for _, f := range left {
				fmt.Fprintf(stdout, "     %s\n", buildAdvice(name, f))
			}
			if more > 0 {
				fmt.Fprintf(stdout, "     … и ещё %s, все видны в таблице выше\n",
					plural(more, "каталог", "каталога", "каталогов"))
			}
			fmt.Fprintln(stdout, "     разбор — единственный шаг, который нельзя доделать задним числом дёшево")
		} else {
			step(fmt.Sprintf("ollchat --graph-build %s", name),
				fmt.Sprintf("разобрать оставшиеся %d кусков", cov.pending))
		}
	}
	if brokenTopics != nil {
		n++
		fmt.Fprintf(stdout, "  %d. разобраться с файлом разметки тем, прежде чем что-либо пересчитывать:\n", n)
		for _, l := range strings.Split(brokenTopics.Error(), "\n")[1:] {
			fmt.Fprintf(stdout, "     %s\n", l)
		}
	}
	if needCommunities {
		step(fmt.Sprintf("ollchat --graph-drift %s && ollchat --graph-communities %s", name, name),
			"пересчитать разметку тем: сперва посмотреть, насколько она разошлась, потом пересчитать (процессор, секунды)")
	}
	if needSummaries {
		step(fmt.Sprintf("ollchat --graph-summaries %s", name),
			"описать темы без описания — без него обзор знает тему по имени, но не по содержанию (карта)")
	}
	if needGraphEmbed {
		step(fmt.Sprintf("ollchat --graph-embed %s", name),
			"досчитать векторы понятий — иначе новые понятия находятся только точным написанием (карта, минуты)")
	}
	if needKBEmbed && cst.Chunks > 0 {
		step(fmt.Sprintf("ollchat --kb-embed %s", name),
			"досчитать векторы кусков — иначе новые книги ищутся только по словам (карта)")
	}
	if n == 0 {
		fmt.Fprintln(stdout, "  ничего — граф, темы и векторы в порядке")
	}
	return nil
}

// printJournalShift печатает раздел «журналы»: сдвиг записей двоичных
// журналов графа (graph.JournalShift).
//
// Строки до « — » сверяет второй прибор (ollscripts/graphdoctorcheck.py)
// своим разбором тех же файлов, поэтому их вид менять только вместе с ним.
// Первая строка печатается всегда, и с нулём: молчание о проверке
// неотличимо от непроведённой проверки. Сдвигом называется только первый
// счёт — недопустимые поля; куски без отметки, книги не из реестра и
// нецелый хвост печатаются рядом, но законные причины у них есть.
func printJournalShift(stdout io.Writer, r graph.JournalShiftReport) {
	fmt.Fprintf(stdout, "  журналы: записей с недопустимыми полями %d (mentions.log %d, edges.log %d, progress.log %d)\n",
		r.Bad(), r.Mentions.Bad, r.Edges.Bad, r.Progress.Bad)
	for _, c := range r.All() {
		if c.Bad == 0 {
			continue
		}
		fmt.Fprintf(stdout, "    ВНИМАНИЕ: %s читается со сдвигом: первая недопустимая запись на смещении %d байт, "+
			"последняя на %d; после первой недопустимых %d из %d (%d%%)\n",
			c.File, c.BadFirst, c.BadLast, c.Bad, c.BadAfter, 100*c.Bad/max(c.BadAfter, 1))
	}
	if r.Bad() > 0 {
		fmt.Fprintln(stdout, "      обрыв — на этом месте или до одной записи раньше: у первой сдвинутой записи поля бывают целы")
	}
	fmt.Fprintf(stdout, "  журналы: записей из кусков без отметки разбора %d (mentions.log %d, edges.log %d)\n",
		r.Unmarked(), r.Mentions.Unmarked, r.Edges.Unmarked)
	for _, c := range []graph.JournalCheck{r.Mentions, r.Edges} {
		if c.Unmarked == 0 {
			continue
		}
		fmt.Fprintf(stdout, "    %s: первая на смещении %d байт, после неё таких %d из %d (%d%%)\n",
			c.File, c.UnmarkedFirst, c.Unmarked, c.UnmarkedAfter, 100*c.Unmarked/max(c.UnmarkedAfter, 1))
	}
	if r.Unmarked() > 0 {
		fmt.Fprintln(stdout, "      законно так бывает на хвосте идущей сборки, на кусках последнего жёсткого обрыва и при сдвинутых отметках;")
		fmt.Fprintln(stdout, "      сплошь после одного места — сдвиг упоминаний на 4 или 8 байт: поля записи его не выдают")
	}
	if r.Unknown() > 0 {
		fmt.Fprintf(stdout, "  журналы: записей с книгой не из реестра %d (mentions.log %d, edges.log %d, progress.log %d) — "+
			"след перечитанных книг и уплотнённой коллекции, сам по себе не сдвиг\n",
			r.Unknown(), r.Mentions.Unknown, r.Edges.Unknown, r.Progress.Unknown)
	}
	for _, c := range r.All() {
		if c.Tail > 0 {
			fmt.Fprintf(stdout, "  журналы: нецелый хвост %s %d байт — оборванная запись: срежется перед следующей дозаписью, "+
				"чтению не мешает (во время сборки — её недописанная запись)\n", c.File, c.Tail)
		}
	}
}

// repartitionDue — пора ли пересчитывать разметку тем.
//
// Судим по доле понятий, не попавших ни в одну тему, а не по доле тем, которые
// «перекроились бы». Причина в замере 02.09.2026: доля перекроившихся тем была
// 61%, и её легко принять за обычную возню разбиения, — а вот 101 тысяча понятий
// из 161 вне всяких тем означала, что обзор работает по трети графа и молчит
// об этом.
//
// Порог — десятая часть. Ниже неё пересчёт стоит дороже пользы: разметка
// секундная, но следом идут описания тем, а это часы карты.
const repartitionThreshold = 10

func repartitionDue(uncovered, entities int) bool {
	if entities <= 0 || uncovered <= 0 {
		return false
	}
	return 100*uncovered/entities >= repartitionThreshold
}

// folderPending — разбор кусков одного каталога библиотеки.
type folderPending struct {
	folder  string
	done    int // с отметкой любого вида
	pending int // сборка ещё возьмёт (graph.WillTake)
	total   int // всего кусков живых книг каталога
}

// printCoverage печатает покрытие графом по каталогам библиотеки.
//
// Таблица нужна затем, что список «сколько осталось» отвечает не на тот вопрос.
// Человек планирует ночь и спрашивает: какой каталог закрыт целиком, какой
// начат, а какой не тронут вовсе. 02.09.2026 на этом обожглись: по списку
// остатков казалось, что /Monitoring почти готов (7388 из библиотеки в полмиллиона),
// а на деле в нём разобрано 925 кусков из 8313 — одиннадцать процентов.
// И наоборот, /Infosec с его 6538 оставшимися был готов на 80% и закрывался
// за одну ночь, но в список крупнейших не попадал и был не виден вовсе.
func printCoverage(stdout io.Writer, rows []folderPending) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintln(stdout, "\n  покрытие по каталогам библиотеки:")
	fmt.Fprintf(stdout, "    %-26s %10s %10s %10s  %s\n", "каталог", "разобрано", "осталось", "всего", "покрытие")
	var td, tl, tt int
	for _, r := range rows {
		if r.total == 0 {
			continue
		}
		mark := ""
		if r.pending == 0 {
			mark = "  ЗАКРЫТ"
		}
		fmt.Fprintf(stdout, "    %-26s %10d %10d %10d   %5.1f%%%s\n",
			r.folder, r.done, r.pending, r.total, float64(r.done)/float64(r.total)*100, mark)
		td += r.done
		tl += r.pending
		tt += r.total
	}
	if tt > 0 {
		fmt.Fprintf(stdout, "    %-26s %10d %10d %10d   %5.1f%%\n", "итого по каталогам",
			td, tl, tt, float64(td)/float64(tt)*100)
	}
}

// chunkCoverage — разбор кусков живых книг коллекции: сколько всего, сколько
// с отметкой (по видам), сколько сборка ещё возьмёт, и то же по каталогам.
type chunkCoverage struct {
	total   int // кусков живых книг
	pending int // сборка ещё возьмёт (graph.WillTake)
	// Отметки по видам — у кусков живых книг; отметки удалённых книг сюда
	// не попадают, их считает deadMarkStats.
	done, empty, skipped, service int
	// again — с отметкой, но сборка возьмёт их снова: «служебный» без
	// признака оглавления (кусок забыт --graph-forget-chunks или признак
	// снят). Они и в «разобрано», и в «осталось» — так считают и
	// --graph-status, и --graph-pending.
	again   int
	folders []folderPending
}

// marked — сколько кусков с отметкой любого вида: «разобрано».
func (c chunkCoverage) marked() int { return c.done + c.empty + c.skipped + c.service }

// liveCoverage считает разбор кусков коллекции одним проходом — по тем же
// правилам, что graph.BooksProgress (`--graph-status --graph-folder`)
// и graph.PendingChunks (`--graph-pending`): «разобрано» — отметка любого
// вида, «осталось» — что сборка возьмёт (graph.WillTake); неотмеченный
// служебный кусок не считается ни туда, ни сюда — сборка его пометит без
// модели. Обход идёт по кускам живых книг, поэтому отметки удалённых книг
// в счёт не попадают. Совпадение трёх приборов проверяет тест
// TestDoctorCountsAgreeWithStatusAndPending.
//
// Каталог здесь — **не место на диске, где лежит граф**, а верхняя папка
// библиотеки: `/AI`, `/Infosec`, `/DevOps`. Ключ `--graph-folder` отбирает книги
// по каталогу, и это единственный способ собирать граф по частям, а не всю
// библиотеку разом. roots пусты — каталогов не различаем.
func liveCoverage(coll *kb.Collection, g *graph.Graph, roots []string, stage *doctorStage) (chunkCoverage, error) {
	var cov chunkCoverage
	by := map[string]*folderPending{}
	total := coll.ChunkCount()
	var seen int
	err := coll.EachChunkRef(kb.ChunkFilter{}, func(r kb.ChunkRef) error {
		seen++
		// Полоса обновляется не на каждом куске: их полмиллиона, и вывод
		// стоил бы дороже самой работы.
		if stage != nil && seen%20000 == 0 {
			stage.progress("разбор по кускам", seen, total)
		}
		f := TopFolder(r.Book.Path, roots)
		if f == "" {
			f = rootFolderLabel
		}
		fp := by[f]
		if fp == nil {
			fp = &folderPending{folder: f}
			by[f] = fp
		}
		fp.total++
		cov.total++
		mark, ok := g.Progress().MarkOf(graph.ChunkKey{Doc: r.Doc, Ord: r.Ord})
		known := true
		switch {
		case !ok:
			known = false
		case mark == graph.MarkDone:
			cov.done++
		case mark == graph.MarkEmpty:
			cov.empty++
		case mark == graph.MarkSkipped:
			cov.skipped++
		case mark == graph.MarkService:
			cov.service++
		default:
			known = false // признака неизвестного вида BooksProgress тоже не считает
		}
		if known {
			fp.done++
		}
		if g.WillTake(r) {
			fp.pending++
			cov.pending++
			if known {
				cov.again++
			}
		}
		return nil
	})
	if err != nil {
		return cov, err
	}
	cov.folders = make([]folderPending, 0, len(by))
	for _, fp := range by {
		cov.folders = append(cov.folders, *fp)
	}
	// От большего к меньшему: сперва то, что займёт карту надолго.
	// При равном остатке — по имени каталога: список собран обходом карт,
	// и закрытые каталоги (остаток 0) менялись местами от запуска к запуску
	// (поймано 03.10.2026 сравнением вывода двух прогонов на одном снимке).
	// Закрытые каталоги в советах не нужны, а в таблице покрытия они и есть
	// главное: закрытый каталог — это сделанная работа.
	sort.Slice(cov.folders, func(i, j int) bool {
		a, b := cov.folders[i], cov.folders[j]
		if a.pending != b.pending {
			return a.pending > b.pending
		}
		return a.folder < b.folder
	})
	return cov, nil
}

// rootFolderLabel — строка таблицы для книг, лежащих прямо в корне
// библиотеки (или вне известных корней): каталога, по которому их отобрать,
// у них нет.
const rootFolderLabel = "(корень библиотеки)"

// buildAdvice — команда разбора остатка одного каталога, готовая к вставке
// в терминал.
//
// До 07.10.2026 для книг в корне библиотеки печаталось
// `--graph-folder (корень библиотеки)` — синтаксическая ошибка bash, а имя
// каталога с пробелом разваливалось на два довода. Отобрать книги корня
// ключом каталога нельзя, поэтому для них совет — сборка без отбора, и это
// сказано прямо: она возьмёт и остаток остальных каталогов.
func buildAdvice(name string, f folderPending) string {
	if f.folder == rootFolderLabel {
		return fmt.Sprintf("ollchat --graph-build %s   (%d кусков книг прямо в корне библиотеки; "+
			"отбора по каталогу у них нет — сборка без отбора возьмёт и остальной остаток)", name, f.pending)
	}
	return fmt.Sprintf("ollchat --graph-build %s --graph-folder %s   (%d кусков)",
		name, shellQuote(f.folder), f.pending)
}

// deadMarkStats — отметки разбора ПО ВИДАМ, отдельно по книгам, которых
// в коллекции больше нет.
//
// Прежнее одно число «пропущено» складывало потерю (модель не дала
// разбираемого ответа) с нормой работы (служебный кусок), да ещё и со следами
// удалённых книг — и раздувалось вдвое (этап 110, А0). LiveBooks, а НЕ Books:
// второй отдаёт реестр как есть, вместе с помеченными удалёнными (его
// докстринг прямо про это), и удалённая книга считалась бы живой. Поймано
// 27.09.2026 сверкой с разбором progress.log: доктор печатал «не разобрала
// модель 1362» при настоящих 47, потому что 11 802 отметки удалённых книг
// попадали в счёт живых.
func deadMarkStats(coll *kb.Collection, g *graph.Graph) graph.MarkStats {
	aliveBooks := map[uint32]bool{}
	for _, b := range coll.LiveBooks() {
		aliveBooks[b.ID] = true
	}
	return g.Progress().Stats(func(doc uint32) bool { return aliveBooks[doc] })
}

// plural склоняет существительное по числу — по-русски, без «каталог(а)».
func plural(n int, one, few, many string) string {
	word := many
	switch mod100 := n % 100; {
	case mod100 >= 11 && mod100 <= 14:
	default:
		switch n % 10 {
		case 1:
			word = one
		case 2, 3, 4:
			word = few
		}
	}
	return fmt.Sprintf("%d %s", n, word)
}

// TopFolder — верхняя папка книги относительно корня библиотеки: «/AI»,
// «/DevOps». Пусто — книга лежит прямо в корне (отбирать по каталогу нечего)
// или вне всех корней. Одно правило на доктора графа и замеры
// (internal/graph/stats): до 07.10.2026 у них было по своей копии, и правка
// 20.09 попала только в одну.
//
// **Корень сравнивается по границе каталога.** Прежние копии сравнивали
// подстрокой, и корень /data/lib находил книги из /data/library — каталогом
// такой книги становился «/rary», которого нет.
//
// **Из подходящих корней берётся самый короткий.** У коллекции lab корень —
// сам <корень библиотеки>/Раздел, и по нему каталог книги вырождался
// в корень (20.09.2026: 100 % «чужих» у lab); корни библиотеки короче корней
// коллекции — их и надо резать.
func TopFolder(path string, roots []string) string {
	best := ""
	for _, root := range roots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		inside := path == root || strings.HasPrefix(path, root+"/")
		if root == "/" {
			inside = strings.HasPrefix(path, "/")
		}
		if inside && (best == "" || len(root) < len(best)) {
			best = root
		}
	}
	if best == "" {
		return ""
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(path, best), "/")
	if i := strings.Index(rest, "/"); i > 0 {
		return "/" + rest[:i]
	}
	return ""
}

// doctorStage — строка хода работы доктора.
//
// В терминале строка перерисовывается на месте и стирается в конце: отчёт должен
// остаться чистым. В журнале (когда вывод перенаправлен) каждая стадия печатается
// отдельной строкой — стирать там нечего, а знать, на чём стоит, надо.
type doctorStage struct {
	w     io.Writer // куда писать ход; в интерфейсе — io.Discard
	start time.Time
	tty   bool
	shown bool
}

func newDoctorStage(w io.Writer) *doctorStage {
	tty := false
	if f, ok := w.(*os.File); ok {
		tty = isTTY(f)
	}
	return &doctorStage{w: w, start: time.Now(), tty: tty}
}

func (s *doctorStage) say(what string) {
	if s == nil {
		return
	}
	if s.tty {
		fmt.Fprintf(s.w, "\r\033[K%s… %s", what, humanSince(s.start))
		s.shown = true
		return
	}
	fmt.Fprintf(s.w, "%s…\n", what)
}

func (s *doctorStage) progress(what string, done, total int) {
	if s == nil || !s.tty || total <= 0 {
		return
	}
	pct := 100 * done / total
	fmt.Fprintf(s.w, "\r\033[K%s %s %d%% · %s", what, bar(pct), pct, humanSince(s.start))
	s.shown = true
}

// done стирает строку хода, чтобы отчёт начинался с чистого места.
func (s *doctorStage) done() {
	if s == nil || !s.tty || !s.shown {
		return
	}
	fmt.Fprint(s.w, "\r\033[K")
	s.shown = false
}

// humanSince — сколько прошло, словами для человека.
func humanSince(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d с", int(d.Seconds()))
	}
	return fmt.Sprintf("%d мин %d с", int(d.Minutes()), int(d.Seconds())%60)
}

// weightsWord — чем считаны веса разбиения, словами.
func weightsWord(byOrigins bool) string {
	if byOrigins {
		return "по источникам"
	}
	return "по кускам"
}

// countUncovered — сколько понятий не попало ни в одну тему.
//
// **Принимает ЖИВЫЕ понятия** (`Entities().Live()`), а не весь реестр.
// Поглощённое склейкой понятие — не самостоятельный узел: его связи и
// упоминания отданы выжившему, и в темах ему быть нечего. Обход по `All()`
// считал каждую склейку «понятием вне тем»: после склейки 15 311 пар число
// подскочило с 17 762 до 33 059 (11% графа), хотя живых вне тем осталось
// столько же — 12 530, и все они без связей, то есть в тему попасть
// не могли никогда.
//
// Цена дефекта была не только в испуге: на этом же числе стоит
// `repartitionDue`, то есть доктор звал пересчитывать разметку после каждой
// крупной склейки без всякого повода (найдено 15.09.2026).
func countUncovered(live []graph.Entity, inTopic map[uint32]bool) int {
	n := 0
	for _, e := range live {
		if !inTopic[e.ID] {
			n++
		}
	}
	return n
}
