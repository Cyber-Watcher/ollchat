package ui

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	kmaint "github.com/Cyber-Watcher/ollchat/internal/kb/maint"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// Команды /kb — управление базой знаний по книгам.
//
// Индексацию запускает только пользователь. У модели такого инструмента нет
// и не будет: иначе она сможет занять машину на часы и, через список корней,
// прочитать всё, до чего дотянется.

const kbHelp = `База знаний по книгам:
  /kb                          состояние: коллекции, книги, размер
  /kb new <имя> [описание]     создать коллекцию
  /kb add <имя> <путь>…        добавить книги из каталога и проиндексировать
  /kb sync <имя>               сверить с диском: новые добавить, пропавшие убрать
  /kb list [<имя>]             коллекции или книги в коллекции
  /kb search <запрос>          поиск своими глазами
  /kb use <имя|off>            коллекция по умолчанию для поиска моделью
  /kb auto on|off              подмешивать найденное перед каждым вопросом
  /kb style                    как модель отвечает по книгам: действующая политика
  /kb stop                     остановить индексацию (то же, что Esc)
  /kb rm <имя> [--book <путь>] удалить коллекцию или одну книгу
  /kb embed <имя> [--dry-run] [--recount]
                               посчитать смыслы: поиск начнёт понимать запрос,
                               а не только слова; --dry-run сперва оценит работу
  /kb merge <имя>              уплотнить: выбросить удалённое, слить сегменты
  /kb stats <имя>              подробности: книги, куски, термы, размеры
  /kb doctor [<имя>]           что не так: пропавшие книги, сканы, дубликаты
  /kb years <имя> [--recount]  проставить книгам год издания (без переиндексации)
  /kb reindex <имя> <путь>…    перечитать книги заново (правила разбора изменились)`

// subCommand — одна подкоманда: все её написания и что она делает.
//
// Таблицей, а не switch: по ней собирается перечень известных подкоманд
// в сообщении об ошибке, и по ней же проверка стережёт расхождение меню
// с разбором. При switch список известных пришлось бы писать руками вторым
// местом — и он разошёлся бы в первую же правку.
type subCommand struct {
	names []string
	run   func(m *Model, rest string) tea.Cmd
}

// runSub находит подкоманду по написанию.
func runSub(subs []subCommand, m *Model, sub, rest string) (tea.Cmd, bool) {
	for _, s := range subs {
		for _, n := range s.names {
			if n == sub {
				return s.run(m, rest), true
			}
		}
	}
	return nil, false
}

// subNames перечисляет первые написания подкоманд — для сообщения об ошибке
// и для проверки согласия меню с разбором.
func subNames(subs []subCommand) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.names[0])
	}
	return out
}

// kbSubs — подкоманды /kb.
var kbSubs = []subCommand{
	{names: []string{"help", "?"}, run: func(m *Model, _ string) tea.Cmd {
		m.addBlock(block{kind: blockNotice, text: kbHelp})
		return nil
	}},
	{names: []string{"new"}, run: (*Model).kbNew},
	{names: []string{"add"}, run: (*Model).kbAdd},
	{names: []string{"sync"}, run: (*Model).kbSync},
	{names: []string{"list", "ls"}, run: (*Model).kbList},
	{names: []string{"tune", "отбор"}, run: (*Model).kbTuneCmd},
	{names: []string{"search", "find"}, run: (*Model).kbSearch},
	{names: []string{"use"}, run: (*Model).kbUseCmd},
	{names: []string{"auto"}, run: (*Model).kbAutoCmd},
	// /kb off — то же, что /kb auto off. Обещано в меню, и человек, увидев
	// его там, набирает именно так, а не «auto off».
	{names: []string{"off", "выкл"}, run: func(m *Model, _ string) tea.Cmd {
		return m.kbAutoCmd("off")
	}},
	{names: []string{"stop"}, run: func(m *Model, _ string) tea.Cmd {
		if m.job == nil {
			m.addBlock(block{kind: blockNotice, text: "сейчас ничего не индексируется"})
			return nil
		}
		m.stopJob("остановлено")
		return nil
	}},
	{names: []string{"rm", "remove", "drop"}, run: (*Model).kbRemove},
	{names: []string{"years", "годы"}, run: (*Model).kbYears},
	{names: []string{"reindex", "перечитать"}, run: (*Model).kbReindex},
	{names: []string{"style", "стиль"}, run: func(m *Model, _ string) tea.Cmd { return m.kbStyleCmd() }},
	{names: []string{"embed", "векторы"}, run: (*Model).kbEmbed},
	{names: []string{"merge", "compact"}, run: (*Model).kbMerge},
	{names: []string{"stats"}, run: (*Model).kbStats},
	{names: []string{"doctor"}, run: (*Model).kbDoctor},
}

// kbCommand разбирает подкоманду /kb.
func (m *Model) kbCommand(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return m.kbStatus()
	}
	sub := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))

	if cmd, ok := runSub(kbSubs, m, sub, rest); ok {
		return cmd
	}
	m.addBlock(block{kind: blockError, text: "неизвестная подкоманда " + sub +
		"\nизвестные: " + strings.Join(subNames(kbSubs), ", ") + "\nподробности — /kb help"})
	return nil
}

// kbStatus показывает общее состояние базы.
func (m *Model) kbStatus() tea.Cmd {
	base, dir, use, picked, auto := m.kb.base, m.cfg.KB.Dir, m.kb.use, m.kb.picked, m.kb.autoOn
	// Открытие каждой коллекции — секунды на большой библиотеке; в фоне (этап 91, R6.5).
	return func() tea.Msg {
		text, err := kbStatusText(base, dir, use, picked, auto)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/kb status: %w", err)}
		}
		return noticeMsg{text: text}
	}
}

// kbStatusText — сводка по базе: коллекции, размеры, что выбрано для модели.
func kbStatusText(base *kb.Base, dir, use string, picked, auto bool) (string, error) {
	names, err := base.Names()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "База знаний: %s\n", dir)
	if len(names) == 0 {
		b.WriteString("\nКоллекций пока нет. Создать и наполнить:\n" +
			"  /kb add go /путь/к/книгам\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "\n%-16s %7s %8s %9s\n", "коллекция", "книг", "кусков", "размер")
	for _, name := range names {
		c, err := base.Open(name)
		if err != nil {
			fmt.Fprintf(&b, "%-16s  — %s\n", name, err.Error())
			continue
		}
		st := c.Stats()
		mark := " "
		if name == use {
			mark = "▸"
		}
		fmt.Fprintf(&b, "%s%-15s %7d %8d %8.1fМБ\n", mark, name, st.Indexed, st.Chunks, float64(st.Bytes)/1e6)
	}
	if use != "" {
		fmt.Fprintf(&b, "\nМодель ищет в коллекции %q", use)
		if picked {
			b.WriteString(" (выбрана сама: она в базе одна)")
		}
		if auto {
			b.WriteString("; найденное подмешивается перед каждым вопросом (/kb auto off — выключить)")
		}
		b.WriteString(".")
	} else {
		b.WriteString("\nКоллекция для модели не выбрана: /kb use <имя>")
	}
	return b.String(), nil
}

func (m *Model) kbNew(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockError, text: "использование: /kb new <имя> [описание]"})
		return nil
	}
	name := fields[0]
	desc := strings.TrimSpace(strings.TrimPrefix(arg, name))
	if _, err := m.kb.base.Create(name, desc); err != nil {
		m.fail("/kb new", err)
		return nil
	}
	m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
		"коллекция %q создана — наполнить: /kb add %s /путь/к/книгам", name, name)})
	return nil
}

// kbAdd добавляет книги: это долгая работа, поэтому она уходит в фон.
func (m *Model) kbAdd(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		m.addBlock(block{kind: blockError, text: "использование: /kb add <имя> <путь>…"})
		return nil
	}
	name := fields[0]
	paths := fields[1:]

	allowed, err := m.kbPathsAllowed(paths)
	if err != nil {
		m.fail("/kb add", err)
		return nil
	}

	coll, err := m.kb.base.Open(name)
	if err != nil {
		// Коллекции нет — заводим её сразу, чтобы не заставлять делать это
		// отдельной командой.
		coll, err = m.kb.base.Create(name, "")
		if err != nil {
			m.fail("/kb add", err)
			return nil
		}
	}
	if err := coll.AddRoots(allowed); err != nil {
		m.fail("/kb add", err)
		return nil
	}
	opt := kb.IndexOpts{
		Workers:  m.cfg.KB.Workers,
		MaxBytes: int64(m.cfg.KB.MaxBookMB) << 20,
	}
	_ = m.logger.Write(chatlog.KindSystem, fmt.Sprintf("Индексация коллекции %s: %v", name, allowed))

	title := fmt.Sprintf("индексация коллекции %s", name)
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		_, err := coll.Add(ctx, allowed, opt, report)
		return err
	})
}

func (m *Model) kbSync(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	if name == "" {
		name = m.kb.use
	}
	if name == "" {
		m.addBlock(block{kind: blockError, text: "использование: /kb sync <имя>"})
		return nil
	}
	coll, err := m.kb.base.Open(name)
	if err != nil {
		m.fail("/kb sync", err)
		return nil
	}
	title := fmt.Sprintf("сверка коллекции %s", name)
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		_, err := coll.Sync(ctx, report)
		return err
	})
}

// kbReindex перечитывает названные книги заново.
//
// Отдельно от /kb sync намеренно: sync смотрит на размер и время файла,
// а здесь книга не менялась — изменились правила разбора.
func (m *Model) kbReindex(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		m.addBlock(block{kind: blockError, text: "использование: /kb reindex <коллекция> <путь к книге>…"})
		return nil
	}
	name, paths := fields[0], fields[1:]
	coll, err := m.kb.base.Open(name)
	if err != nil {
		m.fail("/kb reindex", err)
		return nil
	}
	paths, err = m.kbPathsAllowed(paths)
	if err != nil {
		m.fail("/kb reindex", err)
		return nil
	}
	opt := kb.IndexOpts{Workers: m.cfg.KB.Workers, MaxBytes: int64(m.cfg.KB.MaxBookMB) * 1024 * 1024}
	title := fmt.Sprintf("перечитываю книги коллекции %s: %d", name, len(paths))
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		_, err := coll.Reindex(ctx, paths, opt, report)
		return err
	})
}

// kbYears проставляет книгам год издания.
//
// Отдельной командой, а не при каждом запуске: у собранных до появления этого
// поля книг года нет, а переиндексировать библиотеку ради одного числа — часы
// работы. Проход дешёвый: 94% книг получают год из имени файла, ничего
// не открывая, и лишь остальные читаются на пять первых страниц.
func (m *Model) kbYears(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	force := false
	name := ""
	for _, f := range fields {
		switch f {
		case "--recount", "--force":
			force = true
		default:
			if name == "" {
				name = f
			}
		}
	}
	if name == "" {
		name = m.kb.use
	}
	if name == "" {
		m.addBlock(block{kind: blockError, text: "использование: /kb years <имя> [--recount]"})
		return nil
	}
	coll, err := m.kb.base.Open(name)
	if err != nil {
		m.fail("/kb years", err)
		return nil
	}
	maxBytes := int64(m.cfg.KB.MaxBookMB) * 1024 * 1024
	title := fmt.Sprintf("годы изданий коллекции %s", name)
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		res, err := coll.RefreshYears(ctx, maxBytes, force, func(p kb.YearsProgress) {
			report(kb.Progress{
				Phase: "годы", Collection: name,
				DocsDone: p.Done, DocsTotal: p.Total,
				Added: p.Found, Skipped: p.Missing, Current: p.Book,
			})
		})
		if err != nil {
			return err
		}
		report(kb.Progress{
			Phase: "годы", Collection: name,
			DocsDone: res.Done, DocsTotal: res.Total,
			Added: res.Found, Skipped: res.Missing, Done: true,
		})
		return nil
	})
}

func (m *Model) kbList(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	if name == "" {
		return m.kbStatus()
	}
	base := m.kb.base
	return func() tea.Msg {
		coll, err := base.Open(name)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/kb list: %w", err)}
		}
		return noticeMsg{text: kbListText(name, coll)}
	}
}

// kbListText — книги коллекции по алфавиту с годом, объёмом и числом кусков.
func kbListText(name string, coll *kb.Collection) string {
	books := coll.Books()
	sort.Slice(books, func(i, j int) bool { return books[i].Title < books[j].Title })

	var b strings.Builder
	fmt.Fprintf(&b, "Коллекция %s: книг %d\n\n", name, len(books))
	for _, bk := range books {
		title := bk.Title
		if title == "" {
			title = filepath.Base(bk.Path)
		}
		switch bk.Kind {
		case kb.BookOK:
			year := "    —"
			if bk.Year > 0 {
				year = fmt.Sprintf(" %4d", bk.Year)
			}
			fmt.Fprintf(&b, "  %-50.50s %s %4d %s, кусков %d\n",
				title, year, bk.Units, unitShort(bk.UnitWord), bk.Chunks)
		default:
			fmt.Fprintf(&b, "  %-56.56s ✘ %s\n", title, bk.Err)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func unitShort(unit string) string {
	if unit == "разделов" {
		return "разд."
	}
	return "стр."
}

// kbSearch — поиск глазами пользователя, без участия модели.
func (m *Model) kbSearch(arg string) tea.Cmd {
	query := strings.TrimSpace(arg)
	name := m.kb.use
	// «-c имя» в конце запроса выбирает коллекцию.
	if i := strings.LastIndex(query, " -c "); i > 0 {
		name = strings.TrimSpace(query[i+4:])
		query = strings.TrimSpace(query[:i])
	}
	if query == "" {
		m.addBlock(block{kind: blockError, text: "использование: /kb search [-f] [-r] [N] <запрос> [-c коллекция]"})
		return nil
	}
	// Тот же ход и тот же вид выдачи, что у /search, только без входа
	// в граф (этап 91, R2.7): раньше здесь был свой поиск одной ступенью.
	return m.searchCmdFor(query, name, false, "kb", "/kb search")
}

func (m *Model) kbUseCmd(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	if name == "" {
		if m.kb.use == "" {
			m.addBlock(block{kind: blockNotice, text: "коллекция не выбрана: /kb use <имя>"})
		} else {
			m.addBlock(block{kind: blockNotice, text: "выбрана коллекция " + m.kb.use})
		}
		return nil
	}
	if name == "off" || name == "выкл" {
		m.kb.use = ""
		m.kb.coll = nil
		m.closeGraph()
		m.addBlock(block{kind: blockNotice, text: "коллекция снята: модель больше не ищет по книгам"})
		return nil
	}
	coll, err := m.kb.base.Open(name)
	if err != nil {
		m.fail("/kb use", err)
		return nil
	}
	m.kb.use = name
	m.kb.coll = coll
	m.closeGraph() // граф прежней коллекции больше не при чём
	st := coll.Stats()
	text := fmt.Sprintf("модель будет искать в коллекции %s (книг %d, кусков %d)", name, st.Indexed, st.Chunks)
	if st.Stale {
		text += "\n  правила разбора изменились с прошлой сборки — стоит пересобрать: /kb add " + name + " <путь>"
	}
	m.addBlock(block{kind: blockNotice, text: text})
	return nil
}

func (m *Model) kbAutoCmd(arg string) tea.Cmd {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "вкл":
		if m.kb.use == "" {
			m.addBlock(block{kind: blockError, text: "сначала выберите коллекцию: /kb use <имя>"})
			return nil
		}
		m.kb.autoOn = true
		m.addBlock(block{kind: blockNotice, text: "найденное в книгах будет подмешиваться перед каждым вопросом"})
	case "off", "выкл":
		m.kb.autoOn = false
		m.addBlock(block{kind: blockNotice, text: "подмешивание выключено: модель ищет сама, инструментом"})
	default:
		state := "выключено"
		if m.kb.autoOn {
			state = "включено"
		}
		m.addBlock(block{kind: blockNotice, text: "подмешивание " + state + " (/kb auto on|off)"})
	}
	return nil
}

func (m *Model) kbRemove(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockError, text: "использование: /kb rm <имя> [--book <путь>]"})
		return nil
	}
	name := fields[0]
	if len(fields) >= 3 && fields[1] == "--book" {
		coll, err := m.kb.base.Open(name)
		if err != nil {
			m.fail("/kb rm", err)
			return nil
		}
		path := strings.TrimSpace(strings.TrimPrefix(arg, name+" --book"))
		if err := coll.Forget(path); err != nil {
			m.fail("/kb rm", err)
			return nil
		}
		m.addBlock(block{kind: blockNotice, text: "книга убрана из выдачи: " + path})
		return nil
	}
	if err := m.kb.base.Remove(name); err != nil {
		m.fail("/kb rm", err)
		return nil
	}
	if m.kb.use == name {
		m.kb.use, m.kb.coll = "", nil
		m.closeGraph()
	}
	m.addBlock(block{kind: blockNotice, text: "коллекция " + name + " удалена"})
	return nil
}

func (m *Model) kbStats(arg string) tea.Cmd {
	name, err := m.resolveKBName(arg)
	if err != nil {
		m.fail("/kb stats", err)
		return nil
	}
	base, kbCfg := m.kb.base, m.cfg.KB
	return func() tea.Msg {
		coll, err := base.Open(name)
		if err != nil {
			return errorMsg{err: fmt.Errorf("/kb stats: %w", err)}
		}
		return noticeMsg{text: kbStatsText(coll, kbCfg)}
	}
}

// kbStatsText — подробности одной коллекции: книги, куски, векторы, каталоги.
func kbStatsText(coll *kb.Collection, kbCfg config.KB) string {
	st := coll.Stats()
	meta := coll.Meta()
	var b strings.Builder
	fmt.Fprintf(&b, "Коллекция %s\n", coll.Name())
	if meta.Description != "" {
		fmt.Fprintf(&b, "  %s\n", meta.Description)
	}
	fmt.Fprintf(&b, "  книг: %d (с текстом %d, сканов %d, со сбоями %d, убрано %d)\n",
		st.Books, st.Indexed, st.Scans, st.Broken, st.Deleted)
	fmt.Fprintf(&b, "  кусков: %d, термов: %d, сегментов: %d\n", st.Chunks, st.Terms, st.Segments)
	fmt.Fprintf(&b, "  на диске: %.1f МБ\n", float64(st.Bytes)/1e6)
	fmt.Fprintf(&b, "  правила разбора: %s\n", st.Analyzer)
	if st.Stale {
		b.WriteString("  ВНИМАНИЕ: правила изменились с прошлой сборки, стоит пересобрать\n")
	}
	if len(meta.Roots) > 0 {
		fmt.Fprintf(&b, "  собрана из: %s\n", strings.Join(meta.Roots, ", "))
	}
	if len(kbCfg.Roots) > 0 {
		// Разные вещи: откуда собрана коллекция и откуда вообще разрешено брать
		// книги. Раньше обе назывались «каталоги», и это путало.
		fmt.Fprintf(&b, "  разрешено:  %s\n", strings.Join(kbCfg.Roots, ", "))
	}
	b.WriteString(folderBreakdown(coll.Breakdown()))
	switch {
	case kbCfg.EmbedModel == "":
		b.WriteString("  векторы(смыслы): не настроены (kb.embed_model пуст) — поиск идёт только по словам\n")
	case st.Vectors == 0:
		fmt.Fprintf(&b, "  векторы(смыслы): не посчитаны — /kb embed %s\n", coll.Name())
	case st.Vectors < st.Chunks:
		fmt.Fprintf(&b, "  векторы(смыслы): %d%% (%d из %d), модель %s, размерность %d — досчитать: /kb embed %s\n",
			st.Vectors*100/st.Chunks, st.Vectors, st.Chunks, st.VecModel, st.VecDim, coll.Name())
	default:
		fmt.Fprintf(&b, "  векторы(смыслы): посчитаны целиком, модель %s, размерность %d\n", st.VecModel, st.VecDim)
	}
	if st.Vectors > 0 && st.VecModel != kbCfg.EmbedModel && kbCfg.EmbedModel != "" {
		fmt.Fprintf(&b, "  ВНИМАНИЕ: векторы(смыслы) посчитаны моделью %s, а в настройках %s — /kb embed %s --recount\n",
			st.VecModel, kbCfg.EmbedModel, coll.Name())
	}
	if need, why := coll.NeedsMerge(); need {
		fmt.Fprintf(&b, "  %s\n", why)
	}
	return strings.TrimRight(b.String(), "\n")
}

// kbEmbed считает смыслы коллекции.
//
// Работа долгая и идёт на чужом сервере, поэтому по умолчанию сперва
// предлагается оценка: сколько кусков осталось, сколько это займёт времени
// и места. Оценка — настоящий замер на небольшой пробе, а не таблица:
// придумывать производительность чужого стенда нельзя.
func (m *Model) kbEmbed(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	var name string
	var dry, recount bool
	for _, f := range fields {
		switch f {
		case "--dry-run", "--оценка":
			dry = true
		case "--recount", "--заново":
			recount = true
		default:
			if name == "" {
				name = f
			}
		}
	}
	coll, err := m.kbCollection(name)
	if err != nil {
		m.fail("/kb embed", err)
		return nil
	}
	emb := m.embedder()
	if emb == nil {
		m.addBlock(block{kind: blockError, text: "смысловой поиск не настроен: задайте kb.embed_model " +
			"в файле настроек (например \"bge-m3\") и вытяните модель на сервер: ollama pull bge-m3"})
		return nil
	}
	opt := kb.EmbedOpts{Batch: m.cfg.KB.EmbedBatch, Workers: m.cfg.KB.EmbedWorkers, Recount: recount}

	if dry {
		return m.startJob("оценка работы по векторам(смыслам) "+coll.Name(), func(ctx context.Context, report func(kb.Progress)) error {
			res, err := coll.EstimateEmbed(ctx, emb, opt, 200)
			if err != nil {
				return err
			}
			if res.Added == 0 {
				report(kb.Progress{Done: true, Phase: "векторы(смыслы) уже посчитаны для всей коллекции"})
				return nil
			}
			report(kb.Progress{Done: true, Phase: fmt.Sprintf(
				"осталось кусков %d из %d · размерность %d · ориентировочно %s · на диске ещё %.0f МБ · модель %s",
				res.Added, res.Total, res.Dim, res.Elapsed.Round(time.Second),
				float64(res.Bytes)/1e6, res.Model)})
			return nil
		})
	}

	if e, ok := emb.(*kbembed.Embedder); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if w := e.Placement(ctx).Warning(e.Model()); w != "" {
			m.addBlock(block{kind: blockHint, text: w})
		}
		cancel()
	}
	return m.startJob("векторы(смыслы) коллекции "+coll.Name(), func(ctx context.Context, report func(kb.Progress)) error {
		res, err := coll.Embed(ctx, emb, opt, report)
		if err != nil {
			return err
		}
		if res.Canceled {
			report(kb.Progress{Done: true, Canceled: true, Phase: fmt.Sprintf(
				"прервано: посчитано %d из %d кусков, продолжить — та же команда",
				res.Covered, res.Total)})
			return nil
		}
		report(kb.Progress{Done: true, Phase: fmt.Sprintf(
			"векторы(смыслы) посчитаны: кусков %d из %d, размерность %d, на диске %.1f МБ, за %s",
			res.Covered, res.Total, res.Dim, float64(res.Bytes)/1e6, res.Elapsed.Round(time.Second))})
		return nil
	})
}

// kbMerge уплотняет коллекцию: физически выбрасывает куски удалённых книг
// и сливает сегменты в один.
//
// Задача долгая — на коллекции в четверть миллиона кусков это переписывание
// всего хранилища, — поэтому идёт через тот же механизм фоновой работы, что
// и индексация: с прогрессом, с остановкой по Esc.
func (m *Model) kbMerge(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	// «force» последним словом — осознанный обход отказа при собранном графе.
	force := false
	if f := strings.Fields(name); len(f) > 0 && (f[len(f)-1] == "force" || f[len(f)-1] == "--force") {
		force = true
		name = strings.TrimSpace(strings.Join(f[:len(f)-1], " "))
	}
	if name == "" {
		name = m.kb.use
	}
	coll, err := m.kbCollection(name)
	if err != nil {
		m.fail("/kb merge", err)
		return nil
	}
	title := fmt.Sprintf("уплотнение коллекции %s", coll.Name())
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		res, err := coll.Merge(ctx, kb.MergeOpts{Force: force}, report)
		if err != nil {
			return err
		}
		if res.Canceled {
			report(kb.Progress{Done: true, Canceled: true,
				Phase: "уплотнение прервано, коллекция осталась прежней"})
			return nil
		}
		report(kb.Progress{Done: true, Phase: fmt.Sprintf(
			"уплотнено: кусков %d → %d, сегментов %d → %d, на диске %.1f → %.1f МБ, за %s",
			res.ChunksBefore, res.ChunksAfter, res.SegmentsBefore, res.SegmentsAfter,
			float64(res.BytesBefore)/1e6, float64(res.BytesAfter)/1e6,
			res.Elapsed.Round(time.Second))})
		return nil
	})
}

// kbPendingMsg — что стоит сказать о базе знаний при запуске.
type kbPendingMsg struct{ text string }

// kbPendingCmd сверяет состояние коллекций и сообщает, что с ними не так:
// в папках появились книги, или у книг нет смыслов.
//
// Только сообщает. Ни индексация, ни векторизация сама не запускается: это
// минуты работы и чужое решение — а на общем сервере ещё и чужая видеопамять.
func (m *Model) kbPendingCmd() tea.Cmd {
	if m.kb.base == nil {
		return nil
	}
	checkFolders := m.cfg.KB.SyncOnStart
	embedModel := m.cfg.KB.EmbedModel
	if !checkFolders && embedModel == "" {
		return nil
	}
	base := m.kb.base
	// Коллекция, в которой ищет модель. Про неё сказать стоит; про остальные —
	// только если счёт смыслов был начат и брошен.
	inUse := m.kb.use
	return func() tea.Msg {
		names, err := base.Names()
		if err != nil || len(names) == 0 {
			return nil
		}
		var lines []string
		for _, n := range names {
			coll, err := base.Open(n)
			if err != nil {
				continue
			}
			if checkFolders {
				if ch, err := coll.Pending(); err == nil && ch.Any() {
					parts := make([]string, 0, 2)
					if len(ch.Files) > 0 {
						parts = append(parts, fmt.Sprintf("новых книг %d", len(ch.Files)))
					}
					if len(ch.Changed) > 0 {
						parts = append(parts, fmt.Sprintf("изменилось %d", len(ch.Changed)))
					}
					if ch.Missing > 0 {
						parts = append(parts, fmt.Sprintf("пропало %d", ch.Missing))
					}
					lines = append(lines, fmt.Sprintf("  %s: %s — /kb sync %s", n, strings.Join(parts, ", "), n))
				}
			}
			if embedModel != "" {
				// Ноль процентов — это не «работа брошена», а «смысловой поиск
				// для этой коллекции не нужен»: у документации проекта ищут
				// точные слова, и векторы там ни к чему. Напоминать о нуле
				// имеет смысл только про ту коллекцию, в которой модель ищет;
				// прочие иначе ругались бы вечно и без всякой пользы.
				// Начатый и брошенный счёт (1..99%) — другое дело: это
				// незаконченная работа, и сказать о ней надо про любую.
				cov := coll.Coverage(embedModel)
				if cov.Total > 0 && !cov.Full() && shouldWarnCoverage(cov.Percent(), n, inUse) {
					lines = append(lines, fmt.Sprintf("  %s: векторы(смыслы) посчитаны для %d%% кусков — /kb embed %s",
						n, cov.Percent(), n))
				}
			}
		}
		if len(lines) == 0 {
			return nil
		}
		return kbPendingMsg{text: "База знаний:\n" + strings.Join(lines, "\n")}
	}
}

// embedder возвращает то, чем считать смыслы, или nil.
//
// Пересобирается при смене сервера: пустой kb.embed_url означает «тот сервер,
// что выбран для чата», и после Ctrl+S это уже другой адрес.
func (m *Model) embedder() kb.Embedder {
	if m.cfg.KB.EmbedModel == "" {
		return nil
	}
	url := m.cfg.KB.EmbedURL
	if url == "" {
		url = m.server.URL
	}
	if m.kb.emb == nil || m.kb.embURL != url {
		m.kb.emb = kbembed.New(m.cfg.KB.EmbedOptions(), m.server.URL, m.server.TimeoutDuration(), m.server.Headers)
		m.kb.embURL = url
	}
	if m.kb.emb == nil {
		return nil
	}
	return m.kb.emb
}

// folderBreakdown показывает состав коллекции по подпапкам.
//
// «Книг: 198» само по себе не говорит ничего: от того, что внутри шесть книг
// по Go и шестьдесят шесть по C#, прямо зависит, чего ждать от поиска.
func folderBreakdown(fs []kb.FolderStat) string {
	if len(fs) < 2 {
		return "" // разбивать нечего
	}
	const show = 12
	var b strings.Builder
	b.WriteString("  состав:\n")
	hidden, hiddenBooks := 0, 0
	for i, f := range fs {
		if i >= show {
			hidden++
			hiddenBooks += f.Books
			continue
		}
		name := f.Folder
		if name == "." {
			name = "(в корне)"
		}
		fmt.Fprintf(&b, "    %-28.28s %4d книг", name, f.Books)
		if f.Chunks > 0 {
			fmt.Fprintf(&b, ", кусков %d", f.Chunks)
		}
		if f.Scans > 0 {
			fmt.Fprintf(&b, ", без текста %d", f.Scans)
		}
		b.WriteString("\n")
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "    …и ещё %d папок, книг в них %d\n", hidden, hiddenBooks)
	}
	return b.String()
}

// kbCollection открывает коллекцию по имени или выбранную по умолчанию.
func (m *Model) kbCollection(name string) (*kb.Collection, error) {
	name, err := m.resolveKBName(name)
	if err != nil {
		return nil, err
	}
	return m.kb.base.Open(name)
}

// resolveKBName — какую коллекцию имеют в виду: названную, выбранную или
// единственную. Дёшево (список каталогов); само открытие коллекции — секунды
// на большой библиотеке, и ему место в фоне.
func (m *Model) resolveKBName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = m.kb.use
	}
	if name == "" {
		names, err := m.kb.base.Names()
		if err != nil {
			return "", err
		}
		if len(names) == 1 {
			name = names[0]
		} else if len(names) == 0 {
			return "", fmt.Errorf("коллекций пока нет: /kb add <имя> /путь/к/книгам")
		} else {
			return "", fmt.Errorf("коллекция не выбрана: /kb use <имя> (есть: %s)", strings.Join(names, ", "))
		}
	}
	return name, nil
}

// kbPathsAllowed проверяет, что пути лежат внутри разрешённых корней kb.roots.
//
// Правило одно на интерфейс и на командную строку — kb/maint.AllowedPaths
// (этап 91, R4.8): до того здесь жила своя копия с другим раскрытием «~».
func (m *Model) kbPathsAllowed(paths []string) ([]string, error) {
	return kmaint.AllowedPaths(m.cfg.KB.Roots, paths)
}

// kbDoctor показывает, что в коллекции не так.
//
// Сам отчёт собирает `kb.Doctor` — тот же, что отдаёт `ollchat --kb-doctor`.
// Два текста об одном разошлись бы в первой же правке, и человек получал бы
// разные ответы на один вопрос в зависимости от того, откуда спросил.
//
// Сверка по содержимому читает файлы и потому идёт в фоне: держать ленту
// на секундном ожидании нельзя.
//
// Отчёт кладётся в ленту как **готовый текст** (blockRaw), а не как заметка.
// Заметка переносится по ширине окна через `wrap`, а тот собирает строку из
// `strings.Fields` — и отступы пропадают. Для отчёта это не мелочь: он весь
// построен на них, каталог стоит над именем книги со сдвигом вправо, и без
// отступов список сливается в кашу.
func (m *Model) kbDoctor(arg string) tea.Cmd {
	coll, err := m.kbCollection(strings.TrimSpace(arg))
	if err != nil {
		m.fail("/kb doctor", err)
		return nil
	}
	m.statusMsg = "проверяю коллекцию…"
	return func() tea.Msg {
		var inGraph kb.InGraph
		if g := m.graphOf(coll); g != nil {
			inGraph = g.CoversDoc
		}
		return rawMsg{text: kb.Doctor(coll, kb.DoctorOpts{
			Deep:    true,
			InGraph: inGraph,
			Paint:   func(s string) string { return styBook.Render(s) },
		})}
	}
}

// shouldWarnCoverage решает, стоит ли напоминать о несчитанных смыслах.
//
// Ноль процентов — это не «работа брошена», а «смысловой поиск для этой
// коллекции не нужен»: у документации проекта ищут точные слова, и векторы
// там ни к чему. Про нуль имеет смысл сказать только о той коллекции,
// в которой модель ищет; прочие иначе ругались бы при каждом запуске вечно.
// Начатый и брошенный счёт — другое дело: это незаконченная работа, и сказать
// о ней надо про любую коллекцию.
func shouldWarnCoverage(percent int, coll, inUse string) bool {
	if percent >= 100 {
		return false
	}
	if percent == 0 {
		return coll == inUse
	}
	return true
}

// kbStyleCmd показывает действующую политику ответа по книгам.
//
// Зачем отдельная команда. Политику можно заменить своей через kb.answer_style,
// но до этой команды её негде было **прочитать**: в конфиге по умолчанию пусто,
// в комментарии рядом — сокращённый пример, а сам текст лежал константой
// в исходнике. Правя вслепую, легко выбросить требование, о котором не знал:
// каждое слово там появилось после конкретной беды — «Название» после ссылок
// одними фамилиями, «перевод» после английских цитат в русском ответе.
//
// Поэтому команда печатает текст целиком и говорит, откуда он взят.
func (m *Model) kbStyleCmd() tea.Cmd {
	own := strings.TrimSpace(m.cfg.KB.AnswerStyle)
	style := kb.AnswerStyle(m.cfg.KB.AnswerStyle)

	var b strings.Builder
	if own != "" {
		b.WriteString("Политика ответа по книгам — ваша, из kb.answer_style:\n\n")
	} else {
		b.WriteString("Политика ответа по книгам — встроенная " +
			"(kb.answer_style пуст):\n\n")
	}
	// По абзацам: сплошной строкой в четыреста слов это нечитаемо.
	for _, part := range strings.Split(style, "\n") {
		if p := strings.TrimSpace(part); p != "" {
			b.WriteString("  " + p + "\n")
		}
	}
	b.WriteString("\nУходит модели в трёх местах: в описании kb_search, " +
		"в подписи под его выдачей и в шапке подмешивания.\n")
	if own == "" {
		b.WriteString("Чтобы заменить своей — скопируйте текст выше в kb.answer_style " +
			"файла настроек и правьте. Пересборка не нужна.\n")
		b.WriteString("Осторожно: образец ссылки попадает в ответ дословно, " +
			"а требование ссылаться надо уравновешивать требованием объяснять.")
	} else {
		b.WriteString("Вернуть встроенную — очистите kb.answer_style.")
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(b.String(), "\n")})
	return nil
}
