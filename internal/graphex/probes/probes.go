// Пакет probes — замеры устойчивости и детерминизма извлечения графа понятий:
// перенесены из `privatescripts/tempprobe`, `textprobe`, `seqcheck`, `detcheck`
// одним ключом внутрь ollchat, режим выбирается `-only`, как у census:
//
//	ollchat --probes books -- -only stability -axis temp -n 200
//	ollchat --probes books -- -only stability -axis textfix -n 200
//	ollchat --probes lab -- -only seq -chunks 13#7,4#109
//	ollchat --probes lab -- -only det -graph lab -check detcheck
//	ollchat --probes books -- -only direction -url <сервер сборки> -prompt-file <кандидат.txt>
//	ollchat --probes books -- -only stability -axis prompt -url <сервер сборки> -prompt-file <кандидат.txt> -n 200
//
// Свои ключи идут ПОСЛЕ «--»: иначе их разбирает сам ollchat и отказывает.
// Список режимов и ключей — `ollchat --probes books -- -h`.
//
// **Зачем один ключ на все замеры.** Слово владельца 30.09.2026: «отдельный
// бинарь не надо», всё ключом внутрь ollchat (этап 114, разделы Г3 и Г7).
//
// **Какому режиму нужна карта.** stability (все оси), seq и direction гоняют модель
// извлечения — карта занята, пока идёт замер. det только читает два уже
// собранных графа — карта не нужна.
package probes

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// stop — выход из замера с ошибкой, не роняя ollchat паникой.
type stop struct{ err error }

func die(err error) {
	if err != nil {
		panic(stop{err})
	}
}

// modes — что умеет пакет. Имя режима идёт в `-only`.
var modes = []struct {
	name, about string
}{
	{"stability", "устойчивость извлечения по оси температуры, починки текста или промпта (-axis temp|textfix|prompt; нужна карта; prompt — старый ×2 и кандидат ×2 на обычной прозе: -prompt-file и -url обязательны)"},
	{"seq", "детерминизм извлечения по одному запросу, дважды подряд, температура 0 (нужна карта)"},
	{"det", "детерминизм сборки построчно: сравнение опорного и проверочного графа (карта не нужна)"},
	{"compare", "два итога stability -axis temp по одним кускам (два сервера, до и после обновления): -a A.tsv -b B.tsv (карта не нужна)"},
	{"direction", "в какую сторону модель ставит «является»/«часть» между понятиями встречных пар рабочего графа: старый промпт ×2 и кандидат ×1 на кусках-доказательствах (-url и -prompt-file обязательны; нужна карта)"},
}

// Run — один замер по выбору `-only`. Коллекция приходит ключом ollchat.
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

	fs := flag.NewFlagSet("probes", flag.ContinueOnError)
	fs.SetOutput(stdout)
	only := fs.String("only", "", "какой замер гнать (обязательно); список режимов — ниже")

	// stability (бывшие tempprobe и textprobe)
	axis := fs.String("axis", "", "с -only stability: ось замера — temp|textfix|prompt (обязательно)")
	n := fs.Int("n", 200, "с -only stability: сколько кусков взять (и для -axis prompt)")
	out := fs.String("out", "", "с -only stability (все оси) и direction: куда писать построчный итог (TSV); пусто — не писать")
	cold := fs.Float64("cold", 0, "с -only stability -axis temp: «холодная» температура")
	warm := fs.Float64("warm", -1, "с -only stability -axis temp: «рабочая» температура; -1 — из настроек")
	wraps := fs.Int("wraps", 3, "с -only stability -axis textfix: не меньше стольких переносов в куске")

	// seq (бывший seqcheck)
	chunks := fs.String("chunks", "", "с -only seq: куски через запятую — 13#7,4#109")
	seqN := fs.Int("seq-n", 20, "с -only seq: сколько первых кусков книги 4 добавить (равномерно), если -chunks пуст")

	// det (бывший detcheck)
	gname := fs.String("graph", "lab", "с -only det: опорный граф")
	cname := fs.String("check", "detcheck", "с -only det: проверочный граф (второй прогон)")
	show := fs.Int("show", 12, "с -only det: сколько расхождений показать")

	// compare
	fileA := fs.String("a", "", "с -only compare: первый итог stability (TSV из -out)")
	fileB := fs.String("b", "", "с -only compare: второй итог stability (TSV из -out)")

	// direction
	pairsN := fs.Int("pairs", dirDefaultPairs, "с -only direction: сколько встречных пар взять (0 — 60, отрицательное — все)")
	per := fs.Int("per", dirDefaultPer, "с -only direction: сколько кусков-доказательств брать с каждой стороны пары (0 — 2, отрицательное — все)")
	seed := fs.Int64("seed", 20261107, "с -only direction: зерно выборки пар")
	rel := fs.String("rel", "", "с -only direction: брать встречные пары только этого типа — является|часть (пусто — оба)")
	baseFile := fs.String("base-file", "", "с -only direction: файл, подменяющий старый промпт в прогонах «старый-1» и «старый-2» (не пустой); пусто — рабочий промпт")
	promptFile := fs.String("prompt-file", "", "с -only direction и -only stability -axis prompt: файл с промптом-кандидатом (обязательно, не пустой)")
	url := fs.String("url", "", "с -only direction и -only stability -axis prompt: адрес сервера сборки (обязательно; запасного адреса из настроек нет)")
	workers := fs.Int("workers", dirDefaultWorkers, "с -only direction и -only stability -axis prompt: параллельных запросов (0 — 6)")
	triples := fs.String("triples", "", "с -only stability -axis prompt: куда писать каждую связь каждого прогона (TSV: кусок, прогон, источник, тип, цель); пусто — не писать")

	timeout := fs.Duration("timeout", 5*time.Minute, "с -only stability, seq и direction: предел одного запроса")

	fs.Usage = func() {
		fmt.Fprintf(stdout, "замер извлечения: ollchat --probes <коллекция> -- -only <режим> [ключи]\n\nрежимы:\n")
		for _, m := range modes {
			fmt.Fprintf(stdout, "  %-10s %s\n", m.name, m.about)
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
	case "stability":
		return runStability(stdout, cfg, collName, *axis, *n, *out, *cold, *warm, *wraps, *timeout,
			promptAxisArgs{URL: *url, PromptFile: *promptFile, Workers: *workers, Triples: *triples})
	case "seq":
		return runSeq(stdout, cfg, collName, *chunks, *seqN, *timeout)
	case "det":
		return runDet(stdout, cfg, collName, *gname, *cname, *show)
	case "compare":
		return runCompare(stdout, *fileA, *fileB)
	case "direction":
		return runDirection(stdout, cfg, collName, directionArgs{
			URL: *url, PromptFile: *promptFile, Out: *out, Rel: *rel, BaseFile: *baseFile,
			Pairs: *pairsN, Per: *per, Workers: *workers, Seed: *seed, Timeout: *timeout,
		})
	}
	return fmt.Errorf("неизвестный режим %q; есть: %s", *only, strings.Join(modeNames(), ", "))
}

// modeNames — имена режимов для сообщений об ошибке.
func modeNames() []string {
	names := make([]string, 0, len(modes))
	for _, m := range modes {
		names = append(names, m.name)
	}
	return names
}

// openColl — коллекция на чтение. Нужна stability и seq (читают куски для
// запроса модели) и det (граф открывается поверх той же коллекции).
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
