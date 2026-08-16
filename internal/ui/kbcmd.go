package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/itpro/ollchat/internal/chatlog"
	"github.com/itpro/ollchat/internal/kb"
	"github.com/itpro/ollchat/internal/kbembed"
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
  /kb stop                     остановить индексацию (то же, что Esc)
  /kb rm <имя> [--book <путь>] удалить коллекцию или одну книгу
  /kb embed <имя> [--dry-run] [--recount]
                               посчитать смыслы: поиск начнёт понимать запрос,
                               а не только слова; --dry-run сперва оценит работу
  /kb merge <имя>              уплотнить: выбросить удалённое, слить сегменты
  /kb stats <имя>              подробности: книги, куски, термы, размеры
  /kb doctor [<имя>]           что не так: пропавшие книги, сканы, дубликаты`

// kbCommand разбирает подкоманду /kb.
func (m *Model) kbCommand(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return m.kbStatus()
	}
	sub := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))

	switch sub {
	case "help", "?":
		m.addBlock(block{kind: blockNotice, text: kbHelp})
		return nil
	case "new":
		return m.kbNew(rest)
	case "add":
		return m.kbAdd(rest)
	case "sync":
		return m.kbSync(rest)
	case "list", "ls":
		return m.kbList(rest)
	case "search", "find":
		return m.kbSearch(rest)
	case "use":
		return m.kbUseCmd(rest)
	case "auto":
		return m.kbAutoCmd(rest)
	case "stop":
		if m.job == nil {
			m.addBlock(block{kind: blockNotice, text: "сейчас ничего не индексируется"})
			return nil
		}
		m.stopJob("остановлено")
		return nil
	case "rm", "remove", "drop":
		return m.kbRemove(rest)
	case "embed", "смыслы":
		return m.kbEmbed(rest)
	case "merge", "compact":
		return m.kbMerge(rest)
	case "stats":
		return m.kbStats(rest)
	case "doctor":
		return m.kbDoctor(rest)
	}
	m.addBlock(block{kind: blockError, text: "неизвестная подкоманда " + sub + " — /kb help"})
	return nil
}

// kbStatus показывает общее состояние базы.
func (m *Model) kbStatus() tea.Cmd {
	names, err := m.kbBase.Names()
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "База знаний: %s\n", m.cfg.KB.Dir)
	if len(names) == 0 {
		b.WriteString("\nКоллекций пока нет. Создать и наполнить:\n" +
			"  /kb add go /путь/к/книгам\n")
		m.addBlock(block{kind: blockNotice, text: b.String()})
		return nil
	}
	fmt.Fprintf(&b, "\n%-16s %7s %8s %9s\n", "коллекция", "книг", "кусков", "размер")
	for _, name := range names {
		c, err := m.kbBase.Open(name)
		if err != nil {
			fmt.Fprintf(&b, "%-16s  — %s\n", name, err.Error())
			continue
		}
		st := c.Stats()
		mark := " "
		if name == m.kbUse {
			mark = "▸"
		}
		fmt.Fprintf(&b, "%s%-15s %7d %8d %8.1fМБ\n", mark, name, st.Indexed, st.Chunks, float64(st.Bytes)/1e6)
	}
	if m.kbUse != "" {
		fmt.Fprintf(&b, "\nМодель ищет в коллекции %q", m.kbUse)
		if m.kbAutoOn {
			b.WriteString("; найденное подмешивается перед каждым вопросом (/kb auto off — выключить)")
		}
		b.WriteString(".")
	} else {
		b.WriteString("\nКоллекция для модели не выбрана: /kb use <имя>")
	}
	m.addBlock(block{kind: blockNotice, text: b.String()})
	return nil
}

func (m *Model) kbNew(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockError, text: "использование: /kb new <имя> [описание]"})
		return nil
	}
	name := fields[0]
	desc := strings.TrimSpace(strings.TrimPrefix(arg, name))
	if _, err := m.kbBase.Create(name, desc); err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
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
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}

	coll, err := m.kbBase.Open(name)
	if err != nil {
		// Коллекции нет — заводим её сразу, чтобы не заставлять делать это
		// отдельной командой.
		coll, err = m.kbBase.Create(name, "")
		if err != nil {
			m.addBlock(block{kind: blockError, text: err.Error()})
			return nil
		}
	}
	if err := coll.AddRoots(allowed); err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
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
		name = m.kbUse
	}
	if name == "" {
		m.addBlock(block{kind: blockError, text: "использование: /kb sync <имя>"})
		return nil
	}
	coll, err := m.kbBase.Open(name)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	title := fmt.Sprintf("сверка коллекции %s", name)
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		_, err := coll.Sync(ctx, report)
		return err
	})
}

func (m *Model) kbList(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	if name == "" {
		return m.kbStatus()
	}
	coll, err := m.kbBase.Open(name)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
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
			fmt.Fprintf(&b, "  %-56.56s %4d %s, кусков %d\n", title, bk.Units, unitShort(bk.UnitWord), bk.Chunks)
		default:
			fmt.Fprintf(&b, "  %-56.56s ✘ %s\n", title, bk.Err)
		}
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(b.String(), "\n")})
	return nil
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
	name := m.kbUse
	// «-c имя» в конце запроса выбирает коллекцию.
	if i := strings.LastIndex(query, " -c "); i > 0 {
		name = strings.TrimSpace(query[i+4:])
		query = strings.TrimSpace(query[:i])
	}
	if query == "" {
		m.addBlock(block{kind: blockError, text: "использование: /kb search <запрос> [-c коллекция]"})
		return nil
	}
	coll, err := m.kbCollection(name)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	opt := kb.DefaultSearchOpts()
	opt.TopK = m.cfg.KB.TopK
	opt.MaxPerDoc = m.cfg.KB.MaxPerBook
	opt.Semantic = m.cfg.KB.Semantic
	opt.MinCosine = m.cfg.KB.MinCosine
	opt.SemanticWeight = m.cfg.KB.SemanticWeight
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hits, err := coll.SearchWith(ctx, query, opt, m.embedder())
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	if note := coll.SearchNote(); note != "" {
		m.addBlock(block{kind: blockHint, text: note})
	}
	if len(hits) == 0 {
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"по запросу %q в коллекции %s ничего не нашлось", query, coll.Name())})
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Найдено в коллекции %s: %d\n", coll.Name(), len(hits))
	for i, h := range hits {
		fmt.Fprintf(&b, "\n[%d] %s · %s %d · id=%s\n%s\n",
			i+1, h.Book, h.Unit, h.UnitFrom, h.ID, strings.TrimSpace(h.Snippet))
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(b.String(), "\n")})
	return nil
}

func (m *Model) kbUseCmd(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	if name == "" {
		if m.kbUse == "" {
			m.addBlock(block{kind: blockNotice, text: "коллекция не выбрана: /kb use <имя>"})
		} else {
			m.addBlock(block{kind: blockNotice, text: "выбрана коллекция " + m.kbUse})
		}
		return nil
	}
	if name == "off" || name == "выкл" {
		m.kbUse = ""
		m.kbColl = nil
		m.addBlock(block{kind: blockNotice, text: "коллекция снята: модель больше не ищет по книгам"})
		return nil
	}
	coll, err := m.kbBase.Open(name)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	m.kbUse = name
	m.kbColl = coll
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
		if m.kbUse == "" {
			m.addBlock(block{kind: blockError, text: "сначала выберите коллекцию: /kb use <имя>"})
			return nil
		}
		m.kbAutoOn = true
		m.addBlock(block{kind: blockNotice, text: "найденное в книгах будет подмешиваться перед каждым вопросом"})
	case "off", "выкл":
		m.kbAutoOn = false
		m.addBlock(block{kind: blockNotice, text: "подмешивание выключено: модель ищет сама, инструментом"})
	default:
		state := "выключено"
		if m.kbAutoOn {
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
		coll, err := m.kbBase.Open(name)
		if err != nil {
			m.addBlock(block{kind: blockError, text: err.Error()})
			return nil
		}
		path := strings.TrimSpace(strings.TrimPrefix(arg, name+" --book"))
		if err := coll.Forget(path); err != nil {
			m.addBlock(block{kind: blockError, text: err.Error()})
			return nil
		}
		m.addBlock(block{kind: blockNotice, text: "книга убрана из выдачи: " + path})
		return nil
	}
	if err := m.kbBase.Remove(name); err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	if m.kbUse == name {
		m.kbUse, m.kbColl = "", nil
	}
	m.addBlock(block{kind: blockNotice, text: "коллекция " + name + " удалена"})
	return nil
}

func (m *Model) kbStats(arg string) tea.Cmd {
	name := strings.TrimSpace(arg)
	if name == "" {
		name = m.kbUse
	}
	coll, err := m.kbCollection(name)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
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
	if len(m.cfg.KB.Roots) > 0 {
		// Разные вещи: откуда собрана коллекция и откуда вообще разрешено брать
		// книги. Раньше обе назывались «каталоги», и это путало.
		fmt.Fprintf(&b, "  разрешено:  %s\n", strings.Join(m.cfg.KB.Roots, ", "))
	}
	b.WriteString(folderBreakdown(coll.Breakdown()))
	switch {
	case m.cfg.KB.EmbedModel == "":
		b.WriteString("  смыслы: не настроены (kb.embed_model пуст) — поиск идёт только по словам\n")
	case st.Vectors == 0:
		fmt.Fprintf(&b, "  смыслы: не посчитаны — /kb embed %s\n", coll.Name())
	case st.Vectors < st.Chunks:
		fmt.Fprintf(&b, "  смыслы: %d%% (%d из %d), модель %s, размерность %d — досчитать: /kb embed %s\n",
			st.Vectors*100/st.Chunks, st.Vectors, st.Chunks, st.VecModel, st.VecDim, coll.Name())
	default:
		fmt.Fprintf(&b, "  смыслы: посчитаны целиком, модель %s, размерность %d\n", st.VecModel, st.VecDim)
	}
	if st.Vectors > 0 && st.VecModel != m.cfg.KB.EmbedModel && m.cfg.KB.EmbedModel != "" {
		fmt.Fprintf(&b, "  ВНИМАНИЕ: смыслы посчитаны моделью %s, а в настройках %s — /kb embed %s --recount\n",
			st.VecModel, m.cfg.KB.EmbedModel, coll.Name())
	}
	if need, why := coll.NeedsMerge(); need {
		fmt.Fprintf(&b, "  %s\n", why)
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(b.String(), "\n")})
	return nil
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
		m.addBlock(block{kind: blockError, text: err.Error()})
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
		return m.startJob("оценка работы по смыслам "+coll.Name(), func(ctx context.Context, report func(kb.Progress)) error {
			res, err := coll.EstimateEmbed(ctx, emb, opt, 200)
			if err != nil {
				return err
			}
			if res.Added == 0 {
				report(kb.Progress{Done: true, Phase: "смыслы уже посчитаны для всей коллекции"})
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
	return m.startJob("смыслы коллекции "+coll.Name(), func(ctx context.Context, report func(kb.Progress)) error {
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
			"смыслы посчитаны: кусков %d из %d, размерность %d, на диске %.1f МБ, за %s",
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
	if name == "" {
		name = m.kbUse
	}
	coll, err := m.kbCollection(name)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	title := fmt.Sprintf("уплотнение коллекции %s", coll.Name())
	return m.startJob(title, func(ctx context.Context, report func(kb.Progress)) error {
		res, err := coll.Merge(ctx, report)
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
	if m.kbBase == nil {
		return nil
	}
	checkFolders := m.cfg.KB.SyncOnStart
	embedModel := m.cfg.KB.EmbedModel
	if !checkFolders && embedModel == "" {
		return nil
	}
	base := m.kbBase
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
					if ch.New > 0 {
						parts = append(parts, fmt.Sprintf("новых книг %d", ch.New))
					}
					if ch.Missing > 0 {
						parts = append(parts, fmt.Sprintf("пропало %d", ch.Missing))
					}
					lines = append(lines, fmt.Sprintf("  %s: %s — /kb sync %s", n, strings.Join(parts, ", "), n))
				}
			}
			if embedModel != "" {
				if cov := coll.Coverage(embedModel); cov.Total > 0 && !cov.Full() {
					lines = append(lines, fmt.Sprintf("  %s: смыслы посчитаны для %d%% кусков — /kb embed %s",
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
	if m.kbEmb == nil || m.kbEmbURL != url {
		m.kbEmb = kbembed.New(m.cfg, m.server.URL, m.server.TimeoutDuration(), m.server.Headers)
		m.kbEmbURL = url
	}
	if m.kbEmb == nil {
		return nil
	}
	return m.kbEmb
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
	if name == "" {
		name = m.kbUse
	}
	if name == "" {
		names, err := m.kbBase.Names()
		if err != nil {
			return nil, err
		}
		if len(names) == 1 {
			name = names[0]
		} else if len(names) == 0 {
			return nil, fmt.Errorf("коллекций пока нет: /kb add <имя> /путь/к/книгам")
		} else {
			return nil, fmt.Errorf("коллекция не выбрана: /kb use <имя> (есть: %s)", strings.Join(names, ", "))
		}
	}
	return m.kbBase.Open(name)
}

// kbPathsAllowed проверяет, что пути лежат внутри разрешённых корней.
//
// Песочница здесь не годится: она по построению отвергает всё вне рабочего
// каталога, а книги лежат в другом месте. Список корней — отдельное разрешение,
// которое пользователь даёт в файле настроек, и расширить его ни модель,
// ни команда не могут.
func (m *Model) kbPathsAllowed(paths []string) ([]string, error) {
	roots := m.cfg.KB.Roots
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(expandHome(p))
		if err != nil {
			return nil, err
		}
		if len(roots) == 0 {
			return nil, fmt.Errorf("в настройках не указано ни одного каталога с книгами.\n"+
				"Добавьте его в файл настроек и перезапустите:\n  [kb]\n  roots = [%q]", abs)
		}
		ok := false
		for _, r := range roots {
			if withinRoot(r, abs) {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("каталог %s вне разрешённых kb.roots:\n  %s", abs, strings.Join(roots, "\n  "))
		}
		out = append(out, abs)
	}
	return out, nil
}

// withinRoot сообщает, что путь лежит внутри корня.
func withinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// expandHome раскрывает «~» в начале пути.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// kbAutoContext ищет по книгам перед отправкой вопроса и возвращает подмешанный
// текст. Работает даже с моделями без поддержки инструментов.
func (m *Model) kbAutoContext(question string) (string, int) {
	if !m.kbAutoOn || m.kbUse == "" {
		return "", 0
	}
	coll, err := m.kbCollection(m.kbUse)
	if err != nil {
		return "", 0
	}
	opt := kb.DefaultSearchOpts()
	opt.TopK = m.cfg.KB.TopK
	opt.MaxPerDoc = m.cfg.KB.MaxPerBook
	opt.Semantic = m.cfg.KB.Semantic
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hits, err := coll.SearchWith(ctx, question, opt, m.embedder())
	if err != nil || len(hits) == 0 {
		return "", 0
	}
	var b strings.Builder
	b.WriteString("Выдержки из книг пользователя, относящиеся к вопросу. Это опора для ответа, " +
		"а не сам ответ.\n" + kb.AnswerStyle(m.cfg.KB.AnswerStyle) + "\n\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s · %s %d\n%s\n\n", i+1, h.Book, h.Unit, h.UnitFrom, strings.TrimSpace(h.Snippet))
	}
	return b.String(), len(hits)
}

// kbDoctor показывает, что в коллекции не так.
//
// Отчёт нужен потому, что тихие потери — худшее, что может быть с базой знаний:
// книга не прочиталась, скан не дал текста, файл переехал, а поиск просто
// ничего не находит и объяснить это нечем.
func (m *Model) kbDoctor(arg string) tea.Cmd {
	coll, err := m.kbCollection(strings.TrimSpace(arg))
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	books := coll.Books()
	st := coll.Stats()

	var gone, scans, broken []kb.BookRec
	byTitle := map[string][]kb.BookRec{}
	for _, b := range books {
		switch b.Kind {
		case kb.BookOK:
			if _, err := os.Stat(b.Path); err != nil {
				gone = append(gone, b)
			}
			byTitle[normalizeTitle(b.Title)] = append(byTitle[normalizeTitle(b.Title)], b)
		case kb.BookScan:
			scans = append(scans, b)
		case kb.BookBroken, kb.BookGarbled, kb.BookSkipped:
			broken = append(broken, b)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Проверка коллекции %s\n", coll.Name())

	if len(gone) > 0 {
		fmt.Fprintf(&b, "\nПропали с диска: %d\n", len(gone))
		for i, r := range gone {
			if i >= 5 {
				fmt.Fprintf(&b, "  …и ещё %d\n", len(gone)-5)
				break
			}
			fmt.Fprintf(&b, "  %s\n", r.Path)
		}
		b.WriteString("  Поиск по ним пока работает — текст лежит в базе. Убрать: /kb sync " + coll.Name() + "\n")
	}
	if len(scans) > 0 {
		fmt.Fprintf(&b, "\nСканы без текстового слоя: %d\n", len(scans))
		for i, r := range scans {
			if i >= 3 {
				break
			}
			fmt.Fprintf(&b, "  %s\n", filepath.Base(r.Path))
		}
		b.WriteString("  Текста в них нет; страницы можно показать модели с vision через /addimg.\n")
	}
	if len(broken) > 0 {
		fmt.Fprintf(&b, "\nНе прочитались: %d\n", len(broken))
		for i, r := range broken {
			if i >= 5 {
				fmt.Fprintf(&b, "  …и ещё %d\n", len(broken)-5)
				break
			}
			fmt.Fprintf(&b, "  %-46.46s %s\n", filepath.Base(r.Path), r.Err)
		}
	}

	// Разные издания одной книги: выдача от них не страдает (повторы
	// отсеиваются), но место занимают вдвойне.
	dups := 0
	for title, group := range byTitle {
		if title == "" || len(group) < 2 {
			continue
		}
		if dups == 0 {
			b.WriteString("\nПохоже на разные издания одной книги:\n")
		}
		if dups < 5 {
			fmt.Fprintf(&b, "  %s — %d экземпляра\n", group[0].Title, len(group))
		}
		dups++
	}
	if dups > 5 {
		fmt.Fprintf(&b, "  …и ещё %d\n", dups-5)
	}

	if st.Segments > 6 {
		fmt.Fprintf(&b, "\nСегментов %d: каждый добавляет свой проход при поиске.\n", st.Segments)
	}
	if st.Deleted > 0 {
		fmt.Fprintf(&b, "\nПомечено удалёнными: %d — место на диске они пока занимают.\n", st.Deleted)
	}
	if st.Stale {
		fmt.Fprintf(&b, "\nПравила разбора изменились (%s → %s): стоит пересобрать коллекцию.\n",
			st.Analyzer, kb.AnalyzerVersion)
	}
	if len(gone)+len(scans)+len(broken)+dups == 0 && !st.Stale {
		b.WriteString("\nВсё в порядке: пропавших книг, сканов и сбоев нет.\n")
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(b.String(), "\n")})
	return nil
}

// normalizeTitle приводит заголовок к виду, по которому узнаются разные издания
// одной книги: без регистра, цифр года и знаков.
func normalizeTitle(s string) string {
	var out []rune
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'а' && r <= 'я', r == ' ':
			out = append(out, r)
		}
	}
	return strings.Join(strings.Fields(string(out)), " ")
}
