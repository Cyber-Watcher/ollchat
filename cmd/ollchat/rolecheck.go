package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/roleserver"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
)

// roleCheckTimeout — предел клиента на один узел; Stamp сам режет свои
// запросы десятью секундами.
const roleCheckTimeout = 30 * time.Second

// checkRoleModel — ключ --role-check <коллекция>: та ли модель извлечения
// стоит на узлах сборки, которой собран граф коллекции. Ничего не пишет:
// паспорт графа читается файлом (graph.QuickHealth — ни реестра, ни замков),
// узлы опрашиваются только /api/version, /api/tags и /api/show — модель на
// карту не грузится.
//
// Зачем (my-mistakes #140 п. 2, аудит 05.11.2026, п. 11.3). Смена сервера
// (--use-graph-server) сверяет веса с прежним сервером, а когда сервер не
// меняется, сверки до постановки не было: на сервере 2×3090 тег назывался
// иначе, чем в паспорте, и заход падал «нет модели» уже после архива и
// постановки очереди. Обвязка (graph put, books, folder) зовёт ключ до архива
// и очереди; код 1 — расходится, отказ; код 3 — узел не ответил, модель не
// сверена: обвязка предупреждает и ставит, очередь подождёт сервер, а заход
// сверит модель сам (Pool.Check). Имя ключа — не `--graph-*`: незнакомый ключ
// `--graph-*` сторож закона 1 считает пишущим (так же назван --role-servers).
//
// Что сверяется:
//   - имя: модель в паспорте графа против graph.model (сборка без
//     --graph-allow-model-change откажет и сама, но уже после архива);
//   - на каждом узле — тег с этим именем есть (Stamp: digest манифеста,
//     файлы весов, квантование);
//   - веса каждого узла против отпечатка в паспорте графа (Meta.Weights,
//     решение владельца 05.11.2026) — правилом graph.MatchWeights, тем же,
//     каким сборка сверяет их до первого запроса к модели: так виден и
//     перетег под тем же именем на единственном узле. Паспорт без отпечатка
//     (до 05.11.2026) — не расхождение: так и печатается, запишется первым
//     заходом. Имя в паспорте уже расходится — веса не сверяются: другая
//     модель заведомо даёт другие файлы, и строка повторяла бы ту же беду;
//   - веса и рецепт узлов между собой — правилами пула перед заходом
//     (graphex.CompareStamps).
func checkRoleModel(w, errw io.Writer, cfg *config.Config, name string) error {
	model := cfg.Graph.Model
	if model == "" {
		return errors.New("graph.model пуст — сборка графа выключена, сверять нечего")
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return fmt.Errorf("база знаний: %w", err)
	}
	names, err := base.Names()
	dir := base.CollectionDir(name)
	base.Close()
	if err != nil {
		return fmt.Errorf("коллекции не перечислены: %w", err)
	}
	if !slices.Contains(names, name) {
		return fmt.Errorf("коллекции %q нет (есть: %s)", name, strings.Join(names, ", "))
	}

	bad := 0
	say := func(format string, a ...any) { fmt.Fprintln(w, textx.TermSafe(fmt.Sprintf(format, a...))) }
	h, ok := graph.QuickHealth(dir, cfg.Graph.Name, 0)
	switch {
	case !ok || h.Model == "":
		say("граф %s: паспорта графа нет или модель в нём не записана — имя сверять не с чем; в настройках %s", name, model)
	case h.Model != model:
		bad++
		say("граф %s: в паспорте модель %s, в настройках %s — расходится (сборка откажет без --graph-allow-model-change)",
			name, h.Model, model)
	default:
		say("граф %s: в паспорте модель %s, в настройках %s — совпадает", name, h.Model, model)
	}
	// Веса узлов сверяются с паспортом, только когда в нём то же имя и есть
	// отпечаток (см. комментарий к функции).
	passport := h.Weights
	vsPassport := false
	switch {
	case !ok:
	case h.Model != "" && h.Model != model:
		say("  отпечаток весов в паспорте (%s) не сверяется: имя модели уже расходится", passport)
	case passport.IsZero():
		say("  в паспорте отпечатка весов нет, запишется первым заходом")
	default:
		vsPassport = true
		say("  в паспорте отпечаток весов: %s", passport)
	}

	nodes := roleserver.CurrentGraphNodes(cfg)
	if len(nodes) == 0 {
		return errors.New("узлов сборки нет: ни [[graph.nodes]], ни [graph].url, ни запасного адреса")
	}
	if len(cfg.Graph.ExtractNodes()) == 0 {
		nodes[0].Name = "основной" // так его зовёт сборка (graphrun.go)
	}
	fmt.Fprintf(errw, "сверяю модель %s на узлах сборки: %d (не дольше 10 с на узел)…\n", model, len(nodes))

	opts := cfg.Graph.ExtractOptions()
	headers := cfg.GraphHeaders()
	ctx := context.Background()
	var ref graphex.ModelStamp
	refName := ""
	silent := 0
	for _, n := range nodes {
		o := opts
		o.URL = n.URL
		ex := graphex.New(o, n.URL, roleCheckTimeout, headers)
		if ex == nil {
			bad++
			say("узел %s: модель %s — расходится: извлекатель не собран", n.Name, model)
			continue
		}
		st, err := ex.Stamp(ctx)
		var ue *url.Error
		switch {
		case err != nil && errors.As(err, &ue):
			// Узел не ответил вовсе (связь, туннель): это не расхождение, а
			// «не сверено» — очередь подождёт сервер, а заход сверит модель
			// сам (Pool.Check). Отказывать здесь значило бы не дать поставить
			// книги, пока лежит канал.
			silent++
			say("узел %s: модель %s — не сверена: узел не ответил (%v)", n.Name, model, err)
			continue
		case err != nil:
			bad++
			say("узел %s: модель %s — расходится: %v", n.Name, model, err)
			continue
		}
		desc := fmt.Sprintf("модель %s digest %s файлы %s квантование %s",
			st.Model, short12(st.Digest), shortBlobs(st.Files), dashIfEmpty(st.Quant))
		var differs []string
		if refName == "" {
			ref, refName = st, n.Name
		} else {
			notes, err := graphex.CompareStamps(refName, n.Name, ref, st)
			if err != nil {
				differs = append(differs, strings.SplitN(err.Error(), "\n", 2)[0])
			}
			for _, note := range notes {
				say("  замечание: %s", note)
			}
		}
		if vsPassport {
			switch graph.MatchWeights(passport, st.Weights()) {
			case graph.WeightsOtherFiles, graph.WeightsOtherDigest:
				differs = append(differs, fmt.Sprintf(
					"веса не те, что в паспорте графа: в паспорте %s, на узле %s "+
						"(сборка откажет без --graph-allow-model-change)", passport, st.Weights()))
			case graph.WeightsUnchecked:
				say("  замечание: узел %s: веса с паспортом графа не сверены — в паспорте %s, на узле %s",
					n.Name, passport, st.Weights())
			}
		}
		verdict := "совпадает"
		if len(differs) > 0 {
			bad++
			verdict = "расходится: " + strings.Join(differs, "; ")
		}
		say("узел %s: %s — %s", n.Name, desc, verdict)
	}
	if bad > 0 {
		return &exitCode{code: 1, msg: fmt.Sprintf(
			"модель на узлах сборки не сходится с графом %s (расхождений: %d) — сборку не начинать", name, bad)}
	}
	if silent > 0 {
		return &exitCode{code: 3, msg: fmt.Sprintf(
			"узлов не ответило: %d из %d — модель на них не сверена; заход сверит её сам при старте (Pool.Check)",
			silent, len(nodes))}
	}
	fmt.Fprintln(w, "итог: совпадает")
	return nil
}

// short12 — начало отпечатка для строки сверки; пусто — «нет».
func short12(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	switch {
	case d == "":
		return "нет"
	case len(d) > 12:
		return d[:12]
	}
	return d
}

// shortBlobs — имена блобов весов, укороченные до «sha256-» и 12 знаков.
func shortBlobs(files []string) string {
	if len(files) == 0 {
		return "не названы"
	}
	out := make([]string, len(files))
	for i, f := range files {
		if strings.HasPrefix(f, "sha256-") && len(f) > 19 {
			f = f[:19]
		}
		out[i] = f
	}
	return strings.Join(out, " ")
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
