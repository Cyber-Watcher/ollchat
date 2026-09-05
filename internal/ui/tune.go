package ui

import (
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Крутилки отбора на сеанс.
//
// **Зачем.** Числа отбора — вес уместности связи, ширина пула, сколько
// фрагментов брать, порог косинуса — подбираются замером, а не рассуждением.
// До сих пор посмотреть «а если так?» стоило правки конфига и перезапуска,
// то есть минуты и потерянного диалога. Теперь это команда, и один и тот же
// вопрос можно прогнать при разных значениях подряд.
//
// **В конфиг не пишется.** Подобрал, посмотрел, вернул `reset`. Понравилось —
// переносите в файл настроек руками: значение, выбранное на трёх вопросах,
// в конфиг попадать не должно (замер 28.08.2026 на тридцати вопросах дал
// обратный ответ по сравнению с прогоном на ста).
//
// **Одни и те же числа у всех трёх дорог** — поиска командой, инструментов
// модели и подмешивания к вопросу. Иначе крутилка меняла бы одно, а отвечала
// бы модель по другому, и понять, что произошло, было бы нельзя.

// graphTuneCmd — /graph tune [что] [значение].
func (m *Model) graphTuneCmd(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockNotice, text: m.tuneReport()})
		return nil
	}

	what := strings.ToLower(fields[0])
	if what == "reset" || what == "сброс" {
		m.live.SetRank(graph.NeighborRank{
			SenseWeight: m.cfg.Graph.NeighborSenseWeight,
			Pool:        m.cfg.Graph.NeighborPool,
		})
		m.addBlock(block{kind: blockNotice, text: "отбор связей: значения из конфига возвращены\n" +
			m.tuneReport()})
		return nil
	}
	if len(fields) < 2 {
		m.addBlock(block{kind: blockError, text: graphTuneUsage})
		return nil
	}

	rank := m.live.Rank()
	switch what {
	case "sense", "вес":
		v, err := strconv.ParseFloat(strings.Replace(fields[1], ",", ".", 1), 64)
		if err != nil || v < 0 {
			m.addBlock(block{kind: blockError, text: "/graph tune sense: нужно число не меньше нуля, " +
				"например 1.5 (0 — не пересортировывать связи вовсе)"})
			return nil
		}
		rank.SenseWeight = v
	case "pool", "пул":
		v, err := strconv.Atoi(fields[1])
		if err != nil || v < 1 {
			m.addBlock(block{kind: blockError, text: "/graph tune pool: нужно целое не меньше единицы, " +
				"например 3 — во столько раз шире показанного берётся пул"})
			return nil
		}
		rank.Pool = v
	default:
		m.addBlock(block{kind: blockError, text: graphTuneUsage})
		return nil
	}

	m.live.SetRank(rank)
	m.addBlock(block{kind: blockNotice, text: m.tuneReport()})
	return nil
}

const graphTuneUsage = "/graph tune — отбор связей на сеанс:\n" +
	"  /graph tune             показать нынешние значения\n" +
	"  /graph tune sense 1.5   вес уместности связи против её подтверждённости (0 — не менять порядок)\n" +
	"  /graph tune pool 8      во сколько раз шире показанного брать пул для пересортировки\n" +
	"  /graph tune reset       вернуть значения из конфига\n" +
	"В конфиг не пишется. Замер на 100 вопросах говорит, что 0 лучше 1.5, — " +
	"меняйте, если проверите на своих книгах."

// kbTuneCmd — /kb tune [что] [значение].
func (m *Model) kbTuneCmd(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockNotice, text: m.tuneReport()})
		return nil
	}

	what := strings.ToLower(fields[0])
	if what == "reset" || what == "сброс" {
		m.live.SetKB(m.cfg.KB.TopK, m.cfg.KB.MaxPerBook, m.cfg.KB.MinCosine, m.cfg.KB.SemanticWeight)
		m.addBlock(block{kind: blockNotice, text: "поиск по книгам: значения из конфига возвращены\n" +
			m.tuneReport()})
		return nil
	}
	if len(fields) < 2 {
		m.addBlock(block{kind: blockError, text: kbTuneUsage})
		return nil
	}

	topK, maxPerDoc, minCos, semWeight := m.live.KB()
	switch what {
	case "top_k", "topk":
		v, err := strconv.Atoi(fields[1])
		if err != nil || v < 1 {
			m.addBlock(block{kind: blockError, text: "/kb tune top_k: нужно целое не меньше единицы"})
			return nil
		}
		topK = v
	case "max_per_book", "перкнигу":
		v, err := strconv.Atoi(fields[1])
		if err != nil || v < 1 {
			m.addBlock(block{kind: blockError, text: "/kb tune max_per_book: нужно целое не меньше единицы"})
			return nil
		}
		maxPerDoc = v
	case "min_cosine", "косинус":
		v, err := strconv.ParseFloat(strings.Replace(fields[1], ",", ".", 1), 64)
		if err != nil || v < 0 || v > 1 {
			m.addBlock(block{kind: blockError, text: "/kb tune min_cosine: нужно число от 0 до 1, " +
				"например 0.35 — ниже порога находки отбрасываются"})
			return nil
		}
		minCos = v
	case "semantic_weight", "смысл":
		v, err := strconv.ParseFloat(strings.Replace(fields[1], ",", ".", 1), 64)
		if err != nil || v < 0 {
			m.addBlock(block{kind: blockError, text: "/kb tune semantic_weight: нужно число не меньше нуля, " +
				"например 1.5 — вес смысла против совпадения слов"})
			return nil
		}
		semWeight = v
	default:
		m.addBlock(block{kind: blockError, text: kbTuneUsage})
		return nil
	}

	m.live.SetKB(topK, maxPerDoc, minCos, semWeight)
	m.addBlock(block{kind: blockNotice, text: m.tuneReport()})
	return nil
}

const kbTuneUsage = "/kb tune — числа поиска по книгам на сеанс:\n" +
	"  /kb tune                       показать нынешние значения\n" +
	"  /kb tune top_k 12              сколько фрагментов возвращать\n" +
	"  /kb tune max_per_book 3        не больше стольких из одной книги\n" +
	"  /kb tune min_cosine 0.35       порог смысловой близости\n" +
	"  /kb tune semantic_weight 1.5   вес смысла против совпадения слов\n" +
	"  /kb tune reset                 вернуть значения из конфига\n" +
	"В конфиг не пишется, только на этот сеанс."

// tuneReport — что стоит сейчас и что записано в конфиге.
//
// Показываются обе величины: иначе, покрутив три раза, уже не помнишь,
// откуда начинал, а вернуться надо к конфигу, а не к прошлому шагу.
func (m *Model) tuneReport() string {
	rank := m.live.Rank()
	topK, maxPerDoc, minCos, semWeight := m.live.KB()

	var b strings.Builder
	b.WriteString("Числа отбора (сеанс → в конфиге)\n")
	b.WriteString("  граф:\n")
	fmt.Fprintf(&b, "    sense (вес уместности связи)   %s → %s\n",
		num(rank.SenseWeight), num(m.cfg.Graph.NeighborSenseWeight))
	fmt.Fprintf(&b, "    pool  (ширина пула)            %s → %s\n",
		poolText(rank.Pool), poolText(m.cfg.Graph.NeighborPool))
	b.WriteString("  книги:\n")
	fmt.Fprintf(&b, "    top_k                          %d → %d\n", topK, m.cfg.KB.TopK)
	fmt.Fprintf(&b, "    max_per_book                   %d → %d\n", maxPerDoc, m.cfg.KB.MaxPerBook)
	fmt.Fprintf(&b, "    min_cosine                     %s → %s\n", num(minCos), num(m.cfg.KB.MinCosine))
	fmt.Fprintf(&b, "    semantic_weight                %s → %s\n", num(semWeight), num(m.cfg.KB.SemanticWeight))
	b.WriteString("Меняются командами /graph tune и /kb tune; в конфиг не пишутся.")
	return b.String()
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// poolText показывает ноль как умолчание: ноль означает не «пул пустой»,
// а «взять значение пакета графа», и печатать голый ноль было бы обманом.
func poolText(v int) string {
	if v <= 0 {
		return fmt.Sprintf("%d (умолч.)", graph.DefaultNeighborRank.Pool)
	}
	return strconv.Itoa(v)
}

// mixShowCmd — /mix show <вопрос>: что уйдёт модели вместе с этим вопросом.
//
// **Зачем отдельная команда.** Крутилки меняют отбор, но увидеть их действие
// по ответу модели нельзя: ответ меняется и сам по себе, от температуры,
// и два запуска с одинаковыми настройками дадут разный текст. Значит,
// смотреть надо на **материал**, а не на прозу: карту понятий и выдержки,
// которые реально ушли бы в контекст. Здесь они печатаются целиком и
// без единого обращения к модели.
func (m *Model) mixShowCmd(question string) tea.Cmd {
	question = strings.TrimSpace(question)
	if question == "" {
		m.addBlock(block{kind: blockError, text: "использование: /mix show <вопрос> — " +
			"показать, что подмешалось бы к этому вопросу, не спрашивая модель"})
		return nil
	}

	// Считается в фоне: внутри открытие графа и запрос к эмбеддеру, а лента
	// должна оставаться живой (этап 91, R6.1). Раньше это шло прямо в Update.
	job, ok := m.mixPlan()
	if !ok {
		m.addBlock(block{kind: blockNotice, text: mixShowText(question, mixer.Result{}, m.tuneReport())})
		return nil
	}
	prog := make(chan graph.OpenProgress, 8)
	m.addBlock(block{kind: blockNotice, text: "считаю подмес…"})
	return tea.Batch(waitGraphProgress(genWarm, prog), runMixShowCmd(question, job, m.tuneReport(), prog))
}

// mixShowText — отчёт /mix show: что ушло бы модели и почему.
func mixShowText(question string, res mixer.Result, report string) string {
	if res.Empty() {
		return "к этому вопросу не подмешалось бы ничего.\n" +
			"Привратник связывает вопрос с понятиями графа; не связался — не подмешивается.\n" +
			"Проверьте /graph auto и /kb auto, выбрана ли коллекция (/kb use) и есть ли граф."
	}
	var b strings.Builder
	b.WriteString("Подмес к вопросу «" + question + "»\n")
	b.WriteString(mixLine(res))
	b.WriteString("\n\n── что уходит модели ──\n")
	b.WriteString(res.Text)
	b.WriteString("\n── конец подмеса ──\n")
	b.WriteString(report)
	return b.String()
}

// mixCmd — /mix: показать, что подмешивается, и как это настроено.
func (m *Model) mixCmd(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockNotice, text: mixUsage + "\n\n" + m.tuneReport()})
		return nil
	}
	switch strings.ToLower(fields[0]) {
	case "show", "показать":
		return m.mixShowCmd(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), fields[0])))
	}
	m.addBlock(block{kind: blockError, text: mixUsage})
	return nil
}

const mixUsage = "/mix — подмешивание знаний к вопросу:\n" +
	"  /mix show <вопрос>   показать, что ушло бы модели с этим вопросом (без обращения к ней)\n" +
	"Включается и выключается: /graph auto on|off — карта понятий, /kb auto on|off — выдержки."
