package maint

import (
	"fmt"
	"io"
	"os"
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

// DoctorTo — то же с явным стоком для хода работы: интерфейсу, который рисует
// экран сам, нужен io.Discard, иначе строки хода лягут поверх ленты.
// Числа выборки провенанса. Три тысячи — замер 16.09.2026: секунды чтения,
// и одна битая ссылка на тысячу дала бы в выборке три. Зерно постоянное,
// чтобы два запуска доктора подряд давали одни числа и разница означала
// изменение графа, а не другую выборку.
const (
	provenanceSample = 3000
	provenanceSeed   = 20260916
)

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
	done, empty, skipped := g.Progress().Counts()

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
	if chunks > 0 {
		pct = 100 * st.Covered / chunks
	}
	fmt.Fprintf(stdout, "  разобрано кусков %d из %d (%d%%), осталось %d\n", st.Covered, chunks, pct, st.Pending)
	if skipped > 0 || empty > 0 {
		fmt.Fprintf(stdout, "    с понятиями %d, пустых %d, пропущено %d\n", done, empty, skipped)
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

	switch {
	case cerr != nil || comms == nil || len(comms.List) == 0:
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
		fmt.Fprintf(stdout, "  темы: %d (нижнего уровня %d), с описанием %d из %d кандидатов\n",
			len(comms.List), lvl0, described, cand)
		if comms.Entities > 0 && st.Entities > comms.Entities {
			fmt.Fprintf(stdout, "    разбиение считалось при %d понятиях, сейчас их %d\n",
				comms.Entities, st.Entities)
		}
		if uncovered > 0 {
			share := 100 * uncovered / max(st.Entities, 1)
			fmt.Fprintf(stdout, "    понятий вне тем: %d (%d%%) — обзор тем их не видит\n", uncovered, share)
			needCommunities = repartitionDue(uncovered, st.Entities)
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
	if st.Pending > 0 {
		// Называем настоящие каталоги, а не «<каталог>»: ключ --graph-folder
		// отбирает книги по куску пути **внутри библиотеки**, и человеку
		// неоткуда узнать, какие пути там есть, кроме как заглянув на диск.
		stage.say("считаю покрытие по каталогам")
		all := pendingByFolder(coll, g, cfg.KB.Roots, chunks, stage)
		stage.done()
		printCoverage(stdout, all)
		left := all
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
				if f.pending == 0 {
					continue
				}
				fmt.Fprintf(stdout, "     ollchat --graph-build %s --graph-folder %s   (%d кусков)\n",
					name, f.folder, f.pending)
			}
			if more > 0 {
				fmt.Fprintf(stdout, "     … и ещё %s, все видны в таблице выше\n",
					plural(more, "каталог", "каталога", "каталогов"))
			}
			fmt.Fprintln(stdout, "     разбор — единственный шаг, который нельзя доделать задним числом дёшево")
		} else {
			step(fmt.Sprintf("ollchat --graph-build %s", name),
				fmt.Sprintf("разобрать оставшиеся %d кусков", st.Pending))
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
func repartitionDue(uncovered, entities int) bool {
	if entities <= 0 || uncovered <= 0 {
		return false
	}
	return 100*uncovered/entities >= 10
}

// folderPending — сколько кусков каталога библиотеки ещё не разобрано.
type folderPending struct {
	folder  string
	done    int
	pending int
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
	var td, tl int
	for _, r := range rows {
		total := r.done + r.pending
		if total == 0 {
			continue
		}
		mark := ""
		if r.pending == 0 {
			mark = "  ЗАКРЫТ"
		}
		fmt.Fprintf(stdout, "    %-26s %10d %10d %10d   %5.1f%%%s\n",
			r.folder, r.done, r.pending, total, float64(r.done)/float64(total)*100, mark)
		td += r.done
		tl += r.pending
	}
	if td+tl > 0 {
		fmt.Fprintf(stdout, "    %-26s %10d %10d %10d   %5.1f%%\n", "итого по каталогам",
			td, tl, td+tl, float64(td)/float64(td+tl)*100)
	}
}

// pendingByFolder разносит неразобранные куски по каталогам верхнего уровня.
//
// Каталог здесь — **не место на диске, где лежит граф**, а верхняя папка
// библиотеки: `/AI`, `/Infosec`, `/DevOps`. Ключ `--graph-folder` отбирает книги
// по куску пути, и это единственный способ собирать граф по частям, а не всю
// библиотеку разом.
//
// Один проход по кускам; каталоги с полностью разобранным содержимым молчат.
func pendingByFolder(coll *kb.Collection, g *graph.Graph, roots []string,
	total int, stage *doctorStage) []folderPending {

	done := map[string]int{}
	left := map[string]int{}
	var seen int
	_ = coll.EachChunkRef(kb.ChunkFilter{}, func(r kb.ChunkRef) error {
		seen++
		// Полоса обновляется не на каждом куске: их полмиллиона, и вывод
		// стоил бы дороже самой работы.
		if stage != nil && seen%20000 == 0 {
			stage.progress("остаток по каталогам", seen, total)
		}
		f := topFolder(r.Book.Path, roots)
		if f == "" {
			f = "(корень библиотеки)"
		}
		if g.Progress().Done(graph.ChunkKey{Doc: r.Doc, Ord: r.Ord}) {
			done[f]++
			return nil
		}
		left[f]++
		return nil
	})
	out := make([]folderPending, 0, len(left)+len(done))
	for f, n := range left {
		out = append(out, folderPending{folder: f, done: done[f], pending: n})
	}
	// Каталоги без остатка — закрытые. В советах им места нет, а в таблице
	// покрытия они и есть главное: закрытый каталог — это сделанная работа.
	for f, n := range done {
		if _, hasLeft := left[f]; !hasLeft {
			out = append(out, folderPending{folder: f, done: n})
		}
	}
	// От большего к меньшему: сперва то, что займёт карту надолго.
	sort.Slice(out, func(i, j int) bool { return out[i].pending > out[j].pending })
	return out
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

// topFolder — верхняя папка книги относительно корня библиотеки.
func topFolder(path string, roots []string) string {
	for _, root := range roots {
		if root == "" || !strings.HasPrefix(path, root) {
			continue
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(path, root), "/")
		if i := strings.Index(rest, "/"); i > 0 {
			return "/" + rest[:i]
		}
		return "" // книга лежит в самом корне, отбирать по каталогу нечего
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
