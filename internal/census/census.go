// Пакет census — переписи состояния библиотеки и графа: что и сколько плохо,
// какие книги задеты, что подать на правку. Все счёты только читают, идут
// на процессоре, карта не нужна. Вызываются из ollchat одним ключом, режим
// выбирается `-only`, как у `--graph-stats`:
//
//	ollchat --census books -- -only toc                 сколько кусков похожи на оглавление
//	ollchat --census books -- -only toc-calibrate -chunks 104#21,104#682
//	ollchat --census books -- -only toc-epub -show 6    что в EPUB похоже на оглавление
//
// Свои ключи идут ПОСЛЕ «--»: иначе их разбирает сам ollchat и отказывает.
// Список режимов — `ollchat --census books -- -h`.
//
// **Зачем один ключ на все переписи.** Поодиночке их не находят: за два месяца
// в `privatescripts/` набралось больше десятка программ-переписей, и вместо
// готовой писали новую. Слово владельца 30.09.2026 — «отдельный бинарь
// не надо», всё ключом внутрь ollchat (этап 114, раздел В).
package census

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// stop — выход из счёта с ошибкой, не роняя ollchat паникой.
type stop struct{ err error }

func die(err error) {
	if err != nil {
		panic(stop{err})
	}
}

// modes — что умеет перепись. Имя режима идёт в `-only`.
var modes = []struct {
	name, about string
}{
	{"toc", "сколько в коллекции кусков похожи на оглавление и какая доля упоминаний и связей графа извлечена из них"},
	{"toc-calibrate", "калибровка kb.LooksLikeTOC на названных кусках: доля строк, кончающихся числом (нужен -chunks)"},
	{"toc-epub", "что в EPUB библиотеки похоже на оглавление и предметный указатель: признак по строению главы, а не по имени файла"},
	{"misattrib-acronym", "ложные раскрытия аббревиатур: «RE» из кусков про регулярные выражения в понятии Relation extraction"},
	{"misattrib-alias", "ложные синонимы, работающие ключами реестра: «указатель» в синонимах credentials"},
	{"misattrib-mention", "упоминания, приписанные не тем понятиям: понятия в тексте куска не видно, и кусок лежит дальше по смыслу"},
	{"text", "порча текста кусков: все 14 родов разом по всем кускам — переносы, лигатуры, невидимые знаки, слипшиеся слова"},
	{"text-dots", "точки вместо пробелов ВСЮДУ в куске (общая плотность, не цепочка) — отдельный признак, не покрытый режимом text"},
	{"text-longwords", "слипшиеся слова длиннее -minlen: список с книгами, без идентификаторов и листингов"},
	{"text-dotprefix", "куски, где четверть и больше слов начинается с точки: «.выполнить .действие»"},
	{"text-dotprefix-fresh", "тот же признак на файле книги с диска, разобранном заново мимо индекса — отвечает «починит ли книгу перечитывание»; файлы доводами после ключей. Восстановлен 30.09.2026 взамен утраченного privatescripts/dotprefixcensus/fresh"},
	{"vec-norms", "длина квантованных векторов кусков и понятий графа, и насколько на настоящих парах расходятся kb.Cosine и vecstand.CosineRaw — прибор долга Д15"},
}

// Run — одна перепись по выбору `-only`. Коллекция приходит ключом ollchat.
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

	fs := flag.NewFlagSet("census", flag.ContinueOnError)
	fs.SetOutput(stdout)
	only := fs.String("only", "", "какую перепись считать (обязательно); список — ниже")
	chunks := fs.String("chunks", "", "с -only toc-calibrate: куски через запятую — 104#21,104#682")
	root := fs.String("root", "", "с -only toc-epub: каталог с книгами; пусто — первый из kb.roots конфига")
	show := fs.Int("show", 6, "с -only toc-epub: сколько строк показывать из подозрительной главы")
	all := fs.Bool("all", false, "с -only toc-epub: печатать все главы, не только подозрительные")
	out := fs.String("out", "", "с режимами misattrib-*: каталог для списка кусков в формате --graph-forget-chunks")
	showEnt := fs.Uint("show-entity", 0, "с -only misattrib-acronym: разобрать одно понятие по номеру")
	capPer := fs.Int("cap", 600, "с -only misattrib-acronym: не больше стольких упоминаний на понятие (равномерная выборка); 0 — умолчание 600, отрицательное — без предела")
	// У alias и mention свои умолчания порогов (8 и 20, 0,10 и 0,08), а ключ
	// один. Ноль здесь читается как «умолчание режима» — правило проекта
	// «ноль значит умолчание, выключает отрицательное» (internal/CLAUDE.md).
	minN := fs.Int("min", 0, "с misattrib-alias и misattrib-mention: порог упоминаний; 0 — умолчание режима (8 и 20)")
	gap := fs.Float64("gap", 0, "с misattrib-alias и misattrib-mention: разрыв по смыслу; 0 — умолчание режима (0,10 и 0,08)")
	top := fs.Int("top", 12, "с -only text и text-longwords: сколько книг показывать")
	examples := fs.Int("examples", 6, "с -only text: сколько примеров на род порчи")
	minLen := fs.Int("minlen", 25, "с -only text-longwords: с какой длины слово считается слипшимся")
	fs.Usage = func() {
		fmt.Fprintf(stdout, "перепись состояния: ollchat --census <коллекция> -- -only <режим> [ключи]\n\nрежимы:\n")
		for _, m := range modes {
			fmt.Fprintf(stdout, "  %-*s %s\n", nameWidth(), m.name, m.about)
		}
		fmt.Fprintf(stdout, "\nключи:\n")
		fs.PrintDefaults()
	}
	die(fs.Parse(args))

	if *only == "" {
		fs.Usage()
		return fmt.Errorf("укажите режим: -only <%s>", strings.Join(modeNames(), "|"))
	}

	switch *only {
	case "toc":
		return tocCensus(stdout, cfg, collName)
	case "toc-calibrate":
		if *chunks == "" {
			return fmt.Errorf("режиму toc-calibrate нужны куски: -chunks 104#21,104#682")
		}
		return tocCalibrate(stdout, cfg, collName, strings.Split(*chunks, ","))
	case "toc-epub":
		return tocEPUB(stdout, cfg, *root, *show, *all)
	case "misattrib-acronym":
		return misattribAcronym(stdout, cfg, collName, *out, *showEnt, orInt(*capPer, 600))
	case "misattrib-alias":
		return misattribAlias(stdout, cfg, collName, *out, orInt(*minN, 8), orFloat(*gap, 0.10))
	case "misattrib-mention":
		return misattribMention(stdout, cfg, collName, *out, orInt(*minN, 20), orFloat(*gap, 0.08))
	case "text":
		return textCorruption(stdout, cfg, collName, *top, *examples)
	case "text-dots":
		return textDots(stdout, cfg, collName)
	case "text-longwords":
		return textLongWords(stdout, cfg, collName, *minLen, *top)
	case "text-dotprefix":
		return textDotPrefix(stdout, cfg, collName)
	case "text-dotprefix-fresh":
		if len(fs.Args()) == 0 {
			return fmt.Errorf("режиму text-dotprefix-fresh нужны файлы книг доводами: ollchat --census %s -- -only text-dotprefix-fresh /путь/к/книге.pdf [ещё…]", collName)
		}
		return textDotPrefixFresh(stdout, cfg, fs.Args())
	case "vec-norms":
		return vecNorms(stdout, cfg, collName)
	}
	return fmt.Errorf("неизвестный режим %q; есть: %s", *only, strings.Join(modeNames(), ", "))
}

// orInt и orFloat — «ноль значит умолчание режима»: у alias и mention пороги
// разные, а ключ общий, поэтому ноль подставляет умолчание того режима,
// который выбран. Отрицательное значение остаётся как есть — им признак
// выключают, и это тоже правило проекта.
func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func orFloat(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

// nameWidth — ширина столбца имён в помощи: по самому длинному имени режима,
// иначе длинные («misattrib-acronym») разъезжают таблицу.
func nameWidth() int {
	w := 0
	for _, m := range modes {
		if n := len(m.name); n > w {
			w = n
		}
	}
	return w
}

// modeNames — имена режимов для сообщений об ошибке.
func modeNames() []string {
	names := make([]string, 0, len(modes))
	for _, m := range modes {
		names = append(names, m.name)
	}
	return names
}

// openColl — коллекция на чтение. Отдельно, потому что нужна каждому режиму,
// кроме разведки EPUB: та читает книги с диска, минуя индекс.
func openColl(cfg *config.Config, name string) (*kb.Base, *kb.Collection, error) {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return nil, nil, err
	}
	c, err := base.Open(name)
	if err != nil {
		base.Close()
		return nil, nil, err
	}
	return base, c, nil
}
