package ui

import (
	"io"

	"fmt"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Команды /graph — граф понятий поверх коллекции книг.
//
// Сборки здесь нет намеренно: она занимает карту на часы и запускается
// отдельным запуском без интерфейса — ollchat --graph-build. Из чата доступно
// только то, что стоит миллисекунды: состояние и переключатель подмешивания.

const graphHelp = `Граф понятий (GraphRAG):
  /graph                состояние графа выбранной коллекции
  /graph auto on|off    подмешивать карту понятий к каждому вопросу
  /graph status [имя]   то же состояние по названной коллекции
  /graph find <вопрос>  искать по графу и книгам, не спрашивая модель (то же /search)
  /graph use <имя>      с каким графом работать: пусто или «-» — рабочий,
                        иное имя — опытный рядом (lab → каталог graph-lab)

  /graph communities [коллекция]  темы графа: размеры, названия, описания
  /graph check [коллекция]        целостность и привязка к коллекции
  /graph pack <файл.tar> [колл.]  коллекция с графом в переносимый архив
  /graph archive [коллекция]      снять архив коллекции с графом в graph.archive_dir
                                  (в фоне; сам снимается раз в graph.archive_every, ⛁ в строке состояния)
  /graph archives [коллекция]     какие архивы есть
  /graph review [--judge] [колл.] разобрать пары, в которых машина усомнилась
                                  (связывание при сборке, ночные двойники): y — одно
                                  и то же, n — разные; --judge — проверить «ДА» арбитра
  /graph rm [коллекция] точно     удалить граф, книги не трогая

  Собирается отдельным запуском, потому что занимает карту на часы:
    ollchat --graph-build <коллекция> [--folder AI]
    ollchat --graph-communities <коллекция>   разметить темы (секунды)
    ollchat --graph-summaries <коллекция>     описать темы моделью (часы)
  Поиск руками:
    ollchat --graph-find "вопрос"
  Восстановить коллекцию с графом из архива (закрыв ollchat и ollmcp):
    ollchat --graph-restore <файл.tar.gz>`

// graphSubs — подкоманды /graph. Таблицей по той же причине, что и у /kb:
// по ней собирается перечень известных в сообщении об ошибке, и по ней же
// проверка стережёт расхождение меню с разбором.
var graphSubs = []subCommand{
	{names: []string{"help", "?"}, run: func(m *Model, _ string) tea.Cmd {
		m.addBlock(block{kind: blockNotice, text: graphHelp})
		return nil
	}},
	{names: []string{"auto"}, run: (*Model).graphAutoCmd},
	// /graph off — то же, что /graph auto off: так оно названо в меню.
	{names: []string{"off", "выкл"}, run: func(m *Model, _ string) tea.Cmd {
		return m.graphAutoCmd("off")
	}},
	{names: []string{"tune", "отбор"}, run: (*Model).graphTuneCmd},
	// /graph find — та же команда, что /search: одно ядро, один вид выдачи.
	// Раньше она была объявлена в меню, но не разобрана вовсе и отвечала
	// «неизвестная подкоманда».
	{names: []string{"find", "найти"}, run: (*Model).searchCmd},
	{names: []string{"use", "взять"}, run: (*Model).graphUseCmd},
	{names: []string{"status", "стат"}, run: (*Model).graphStatus},
	{names: []string{"communities", "сообщества", "темы"}, run: (*Model).graphCommunities},
	{names: []string{"check", "проверить"}, run: (*Model).graphCheck},
	{names: []string{"pack", "упаковать"}, run: (*Model).graphPack},
	{names: []string{"archive", "архив"}, run: (*Model).graphArchiveCmd},
	{names: []string{"archives", "архивы"}, run: (*Model).graphArchivesCmd},
	{names: []string{"review", "разбор"}, run: (*Model).graphReviewCmd},
	{names: []string{"rm", "remove", "удалить"}, run: (*Model).graphRemove},
	{names: []string{"build", "собрать"}, run: func(m *Model, rest string) tea.Cmd {
		// Отказ с готовой командой: человек уже сказал, чего хочет, и отсылать
		// его читать справку — лишний шаг.
		m.addBlock(block{kind: blockNotice, text: "сборка занимает карту на часы, поэтому идёт " +
			"отдельным запуском:\n  ollchat --graph-build " + m.graphCollName(rest) +
			"\nв tmux, чтобы обрыв связи её не убил"})
		return nil
	}},
}

// graphUseCmd — /graph use <имя>: с каким графом работать дальше.
//
// Графов над одной библиотекой может быть несколько: рабочий в каталоге `graph`
// и опытные рядом (`lab` → `graph-lab`). Книги у них общие, отличается только
// извлечённое. Переключение закрывает открытый граф: держать в памяти два
// по 330 МБ незачем, а следующий вопрос откроет нужный сам.
func (m *Model) graphUseCmd(rest string) tea.Cmd {
	name := strings.TrimSpace(rest)
	if name == "-" || name == "рабочий" {
		name = ""
	}
	if strings.EqualFold(name, "?") || name == "help" {
		m.addBlock(block{kind: blockNotice, text: "/graph use <имя> — взять другой граф\n" +
			"  /graph use lab   опытный граф в каталоге graph-lab\n" +
			"  /graph use -     вернуться к рабочему\n" +
			"сейчас: " + graphNameHuman(m.cfg.Graph.Name)})
		return nil
	}
	if !graph.ValidName(name) {
		m.addBlock(block{kind: blockError, text: "недопустимое имя графа: строчные латинские " +
			"буквы, цифры и дефис, до 32 знаков"})
		return nil
	}
	// Меняются и каталог графа, и настройки его сборки разом: иначе опытный
	// граф читался бы из своего каталога, а собирался настройками рабочего.
	if err := m.cfg.UseGraph(name); err != nil {
		m.fail("/graph use", err)
		return nil
	}
	m.closeGraph()
	m.addBlock(block{kind: blockNotice, text: "граф: " + graphNameHuman(name) +
		" (каталог " + graph.DirFor(m.cfg.Graph.Name) + ")\nоткроется при следующем вопросе"})
	return m.checkGraphHealthCmd()
}

// graphNameHuman — как назвать граф человеку.
func graphNameHuman(name string) string {
	if name == "" {
		return "рабочий"
	}
	return name
}

// graphCommand разбирает подкоманду /graph.
func (m *Model) graphCommand(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return m.graphStatus("")
	}
	rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))

	if cmd, ok := runSub(graphSubs, m, strings.ToLower(fields[0]), rest); ok {
		return cmd
	}
	m.addBlock(block{kind: blockError, text: "неизвестная подкоманда: /graph " + fields[0] +
		"\nизвестные: " + strings.Join(subNames(graphSubs), ", ") + "\n" + graphHelp})
	return nil
}

// graphCollName — имя коллекции для подсказки: названное или выбранное.
func (m *Model) graphCollName(arg string) string {
	if n := strings.Fields(arg); len(n) > 0 {
		return n[0]
	}
	if m.kb.use != "" {
		return m.kb.use
	}
	return "<коллекция>"
}

// graphAutoCmd включает и выключает подмешивание карты понятий.
func (m *Model) graphAutoCmd(arg string) tea.Cmd {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "вкл":
		if m.kb.use == "" {
			m.addBlock(block{kind: blockError, text: "сперва выберите коллекцию: /kb use <имя>"})
			return nil
		}
		m.gr.autoOn = true
		m.addBlock(block{kind: blockNotice, text: "карта понятий подмешивается к вопросам, " +
			"связанным с графом; на остальные вопросы не тратится ничего"})
	case "off", "выкл":
		m.gr.autoOn = false
		m.addBlock(block{kind: blockNotice, text: "подмешивание карты понятий выключено"})
	case "":
		if m.gr.autoOn {
			m.addBlock(block{kind: blockNotice, text: "подмешивание карты понятий включено (/graph auto off — выключить)"})
		} else {
			m.addBlock(block{kind: blockNotice, text: "подмешивание карты понятий выключено (/graph auto on — включить)"})
		}
	default:
		m.addBlock(block{kind: blockError, text: "надо /graph auto on или /graph auto off"})
	}
	return nil
}

// graphStatus показывает, что в графе есть и чего в нём ещё нет.
func (m *Model) graphStatus(arg string) tea.Cmd {
	coll, err := m.kbCollection(strings.TrimSpace(arg))
	if err != nil {
		m.fail("/graph status", err)
		return nil
	}

	// **Открытие графа идёт в фоне, а не здесь.** Реестр понятий вырос
	// до 90 МБ, и его разбор занимает восемь секунд. Раньше это делалось
	// прямо в Update: интерфейс замирал, нажатия копились, и человек жал
	// Enter несколько раз, решив, что команда не сработала. Кэш `graphOf`
	// не спасает — во время сборки отметка файлов меняется постоянно,
	// и граф переоткрывается на каждый вызов.
	//
	// В горутину уходят только путь и число кусков, а не сама коллекция:
	// делить объект между потоками ради одной строки отчёта незачем.
	dir, chunks, name := coll.Dir(), coll.ChunkCount(), coll.Name()
	auto := m.gr.autoOn
	rules := m.cfg.Graph.Rules()

	m.addBlock(block{kind: blockNotice, text: "считаю состояние графа…"})
	return func() tea.Msg {
		g, err := graph.Open(dir, chunks, rules)
		if err != nil {
			return noticeMsg{text: fmt.Sprintf(
				"у коллекции %s графа нет.\nСобрать: ollchat --graph-build %s [--folder AI]",
				name, name)}
		}
		defer g.Close()
		return noticeMsg{text: graphStatusText(g, name, chunks, auto)}
	}
}

// graphStatusText собирает отчёт о состоянии графа.
//
// Отдельной функцией, потому что считается она в горутине, а к модели
// оттуда прикасаться нельзя.
func graphStatusText(g *graph.Graph, name string, chunks int, auto bool) string {
	st := g.Stats(chunks)
	done, empty, skipped := g.Progress().Counts()

	var b strings.Builder
	fmt.Fprintf(&b, "Граф коллекции %s\n", name)
	fmt.Fprintf(&b, "  понятий %d, связей %d, упоминаний %d\n", st.Entities, st.Edges, st.Mentions)
	fmt.Fprintf(&b, "  разобрано кусков %d из %d (осталось %d)\n", st.Covered, chunks, st.Pending)
	fmt.Fprintf(&b, "  из них с понятиями %d, пустых %d, пропущено %d\n", done, empty, skipped)
	if st.Model != "" {
		fmt.Fprintf(&b, "  модель извлечения: %s\n", st.Model)
	}
	fmt.Fprintf(&b, "  %s\n", g.PromptLine())
	if v := g.VectorsInfo(); v.Ready {
		if v.Count >= st.Entities {
			fmt.Fprintf(&b, "  векторы(смыслы) понятий: посчитаны все %d (%s)\n", v.Count, v.Model)
		} else {
			fmt.Fprintf(&b, "  векторы(смыслы) понятий: посчитано %d из %d — %d не находятся по смыслу"+
				" (ollchat --graph-embed %s)\n", v.Count, st.Entities, st.Entities-v.Count, name)
		}
	}
	if n := g.Merges().Count(); n > 0 {
		fmt.Fprintf(&b, "  склеено двойников: %d (снять — ollchat --graph-merge %s --graph-merge-drop)\n",
			n, name)
	}
	if g.Locked() {
		b.WriteString("  идёт сборка\n")
	}
	if auto {
		b.WriteString("  карта понятий подмешивается к вопросам (/graph auto off — выключить)")
	} else {
		b.WriteString("  подмешивание выключено (/graph auto on — включить)")
	}
	return b.String()
}

// graphCommunities показывает темы графа: их размеры, названия и описания.
func (m *Model) graphCommunities(arg string) tea.Cmd {
	name, err := m.resolveKBName(arg)
	if err != nil {
		m.fail("/graph communities", err)
		return nil
	}
	base, rules := m.kb.base, m.cfg.Graph.Rules()
	return func() tea.Msg {
		coll, err := base.Open(name)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph communities: %w", err)}
		}
		g, err := graph.Open(coll.Dir(), coll.ChunkCount(), rules)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph communities: граф коллекции %s: %w", name, err)}
		}
		defer g.Close()
		comms, err := g.LoadCommunities()
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph communities: %w", err)}
		}
		return noticeMsg{text: graphCommunitiesText(name, comms)}
	}
}

// graphCommunitiesText — темы графа: размеры, названия, сколько описано.
func graphCommunitiesText(name string, comms *graph.Communities) string {
	if comms == nil {
		return "сообщества не размечены:\n  ollchat --graph-communities " +
			name + "\nразметка занимает секунды и карту не трогает"
	}
	lvl0 := comms.Level(0)
	sort.Slice(lvl0, func(i, j int) bool {
		return len(lvl0[i].Members) > len(lvl0[j].Members)
	})
	described := 0
	for _, c := range lvl0 {
		if c.Title != "" {
			described++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Темы графа коллекции %s: %d, из них с описанием %d\n\n",
		name, len(lvl0), described)
	shown := 0
	for _, c := range lvl0 {
		if c.Title == "" {
			continue
		}
		fmt.Fprintf(&b, "  %-42.42s понятий %d\n", c.Title, len(c.Members))
		if shown++; shown >= 25 {
			break
		}
	}
	if described == 0 {
		fmt.Fprintf(&b, "  описаний нет: ollchat --graph-summaries %s\n", name)
	} else if described > shown {
		fmt.Fprintf(&b, "\n  …и ещё %d описанных тем\n", described-shown)
	}
	return strings.TrimRight(b.String(), "\n")
}

// graphCheck — тот же доктор, что и `ollchat --graph-doctor`: покрытие,
// векторы, промпт, темы, советы. Одно ядро на команду и интерфейс (этап 91,
// R4.2); раньше здесь была своя укороченная проверка. Идёт в фоне: доктор
// открывает граф, а это секунды.
func (m *Model) graphCheck(arg string) tea.Cmd {
	coll, err := m.kbCollection(arg)
	if err != nil {
		m.fail("/graph check", err)
		return nil
	}
	name, cfg := coll.Name(), m.cfg
	m.addBlock(block{kind: blockNotice, text: "проверяю граф коллекции " + name + "…"})
	return func() tea.Msg {
		var b strings.Builder
		if err := gmaint.DoctorTo(&b, io.Discard, cfg, name); err != nil {
			return noticeMsg{text: "доктор графа: " + err.Error()}
		}
		return noticeMsg{text: strings.TrimRight(b.String(), "\n")}
	}
}

// graphPack складывает коллекцию с графом в переносимый архив.
func (m *Model) graphPack(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockError, text: "нужно имя архива: /graph pack <файл.tar> [коллекция]"})
		return nil
	}
	// Архив пишется в песочницу по тем же правилам, что и всё остальное:
	// коллекция — личные книги, и раскладывать их куда попало нельзя.
	path, err := m.guard.Sandbox().Resolve(fields[0])
	if err != nil {
		m.fail("/graph pack", err)
		return nil
	}
	name, err := m.resolveKBName(strings.Join(fields[1:], " "))
	if err != nil {
		m.fail("/graph pack", err)
		return nil
	}
	base, rules := m.kb.base, m.cfg.Graph.Rules()
	m.addBlock(block{kind: blockNotice, text: "упаковываю коллекцию " + name + "…"})
	// Архив — мегабайты чтения и записи; в цикле событий им не место (этап 91, R6.3).
	return func() tea.Msg {
		coll, err := base.Open(name)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph pack: %w", err)}
		}
		if _, err := graph.Open(coll.Dir(), coll.ChunkCount(), rules); err != nil {
			return errorMsg{err: fmt.Errorf("/graph pack: граф коллекции %s: %w", name, err)}
		}
		res, err := graph.Pack(coll.Dir(), path)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph pack: упаковать не вышло: %w", err)}
		}
		return noticeMsg{text: fmt.Sprintf(
			"коллекция %s упакована в %s\n  файлов %d, прочитано %.1f МБ, в архиве %.1f МБ\n"+
				"  распаковать на другой машине в каталог коллекций и открыть — пересборка не нужна",
			name, path, res.Files, float64(res.Bytes)/1e6, float64(res.Wrote)/1e6)}
	}
}

// graphRemove удаляет граф коллекции.
//
// Со словом-подтверждением, а не сразу: пересборка стоит часов работы
// видеокарты, и «случайно удалил» здесь означает потерянный день.
func (m *Model) graphRemove(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	sure := false
	for i, f := range fields {
		if strings.EqualFold(f, "точно") || strings.EqualFold(f, "confirm") {
			sure = true
			fields = append(fields[:i], fields[i+1:]...)
			break
		}
	}
	name, err := m.resolveKBName(strings.Join(fields, " "))
	if err != nil {
		m.fail("/graph rm", err)
		return nil
	}
	base, rules, graphName := m.kb.base, m.cfg.Graph.Rules(), m.cfg.Graph.Name
	return func() tea.Msg {
		coll, err := base.Open(name)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph rm: %w", err)}
		}
		g, err := graph.Open(coll.Dir(), coll.ChunkCount(), rules)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph rm: граф коллекции %s: %w", name, err)}
		}
		st := g.Check(coll.ChunkCount())
		g.Close()
		if !sure {
			return noticeMsg{text: fmt.Sprintf(
				"будет удалён граф коллекции %s: понятий %d, связей %d, разобрано кусков %d.\n"+
					"Пересборка займёт часы работы видеокарты.\n"+
					"Если решение обдуманное:  /graph rm %s точно",
				name, st.Entities, st.Edges, st.Parsed, name)}
		}
		size, err := graph.Remove(coll.Dir(), graphName)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/graph rm: %w", err)}
		}
		return graphRemovedMsg{name: name, size: size}
	}
}

// openGraphFor открывает граф названной или выбранной коллекции.
func (m *Model) openGraphFor(arg string) (*graph.Graph, *kb.Collection, error) {
	name := strings.TrimSpace(arg)
	if name == "" {
		name = m.kb.use
	}
	if name == "" {
		return nil, nil, fmt.Errorf("коллекция не выбрана: /kb use <имя>")
	}
	coll, err := m.kbCollection(name)
	if err != nil {
		return nil, nil, err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), m.cfg.Graph.Rules())
	if err != nil {
		return nil, nil, fmt.Errorf("граф коллекции %s: %w", name, err)
	}
	return g, coll, nil
}
