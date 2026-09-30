// Пакет probes — замеры устойчивости и детерминизма извлечения графа понятий:
// перенесены из `privatescripts/tempprobe`, `textprobe`, `seqcheck`, `detcheck`
// одним ключом внутрь ollchat, режим выбирается `-only`, как у census:
//
//	ollchat --probes books -- -only stability -axis temp -n 200
//	ollchat --probes books -- -only stability -axis textfix -n 200
//	ollchat --probes lab -- -only seq -chunks 13#7,4#109
//	ollchat --probes lab -- -only det -graph lab -check detcheck
//
// Свои ключи идут ПОСЛЕ «--»: иначе их разбирает сам ollchat и отказывает.
// Список режимов и ключей — `ollchat --probes books -- -h`.
//
// **Зачем один ключ на все замеры.** Слово владельца 30.09.2026: «отдельный
// бинарь не надо», всё ключом внутрь ollchat (этап 114, разделы Г3 и Г7).
//
// **Какому режиму нужна карта.** stability и seq гоняют модель извлечения —
// карта занята, пока идёт замер. det только читает два уже собранных графа —
// карта не нужна.
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
	{"stability", "устойчивость извлечения по оси температуры или починки текста (-axis temp|textfix; нужна карта)"},
	{"seq", "детерминизм извлечения по одному запросу, дважды подряд, температура 0 (нужна карта)"},
	{"det", "детерминизм сборки построчно: сравнение опорного и проверочного графа (карта не нужна)"},
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
	axis := fs.String("axis", "", "с -only stability: ось замера — temp|textfix (обязательно)")
	n := fs.Int("n", 200, "с -only stability: сколько кусков взять")
	out := fs.String("out", "", "с -only stability: куда писать построчный итог (TSV); пусто — не писать")
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

	timeout := fs.Duration("timeout", 5*time.Minute, "с -only stability и seq: предел одного запроса")

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
		return runStability(stdout, cfg, collName, *axis, *n, *out, *cold, *warm, *wraps, *timeout)
	case "seq":
		return runSeq(stdout, cfg, collName, *chunks, *seqN, *timeout)
	case "det":
		return runDet(stdout, cfg, collName, *gname, *cname, *show)
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
