package maint

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/nodeprobe"
)

// Состояние серверов сборки по данным наблюдателей ollnode (этап 96).
//
// Отвечает на вопрос, который иначе решается заходом по ssh на каждый сервер:
// свободна ли карта, не вытеснена ли модель в оперативную память, сколько
// слотов у службы на самом деле и что она пишет в журнал.

// Nodes печатает состояние всех узлов сборки.
func Nodes(stdout io.Writer, cfg *config.Config) error {
	nodes := cfg.Graph.ExtractNodes()
	if len(nodes) == 0 {
		// Узлов нет — значит сборка идёт одним сервером. Показать всё равно
		// есть что, если у него задан наблюдатель в общем разделе.
		fmt.Fprintln(stdout, "узлы сборки не заданы: раздел [[graph.nodes]] пуст,")
		fmt.Fprintln(stdout, "граф собирается одним сервером из graph.url")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	withProbe := 0
	for _, n := range nodes {
		fmt.Fprintf(stdout, "\n%s — слотов %d\n", n.Name, n.Workers)
		if n.Probe == "" {
			fmt.Fprintln(stdout, "  наблюдателя нет (задайте probe у узла — см. ollnode/README.md)")
			continue
		}
		withProbe++
		client := nodeprobe.NewClient(n.Probe, n.ProbeToken, 10*time.Second)
		rep, err := client.Node(ctx, false)
		if err != nil {
			fmt.Fprintf(stdout, "  %v\n", err)
			continue
		}
		printNode(stdout, rep)
	}
	if withProbe == 0 {
		fmt.Fprintln(stdout, "\nНи у одного узла нет наблюдателя. Поставьте ollnode рядом с Ollama")
		fmt.Fprintln(stdout, "и укажите его адрес в probe — тогда будет видно чужую работу на карте,")
		fmt.Fprintln(stdout, "вытеснение модели в ОЗУ и настоящее число слотов службы.")
	}
	return nil
}

// printNode печатает один снимок.
func printNode(stdout io.Writer, rep *nodeprobe.Report) {
	for _, g := range rep.GPUs {
		line := fmt.Sprintf("  карта %d: %s — %d/%d МиБ, загрузка %d%%",
			g.Index, g.Name, g.MemUsed, g.MemTotal, g.Util)
		if g.TempC > 0 {
			line += fmt.Sprintf(", %d °C", g.TempC)
		}
		fmt.Fprintln(stdout, line)
		if g.Throttle != "" {
			fmt.Fprintf(stdout, "    снижение частоты: %s\n", g.Throttle)
		}
	}
	for _, p := range rep.GPUProcs {
		whose := "чужой"
		if p.Ours {
			whose = "наш"
		}
		fmt.Fprintf(stdout, "    на карте %s процесс %d %s — %d МиБ\n", whose, p.PID, p.Name, p.UsedMiB)
	}
	if s := rep.Service; s.State != "" {
		line := fmt.Sprintf("  служба %s: %s", s.Name, s.State)
		if n := s.Slots(); n > 0 {
			line += fmt.Sprintf(", слотов %d", n)
		}
		fmt.Fprintln(stdout, line)
	}
	for _, m := range rep.Models {
		line := fmt.Sprintf("  модель %s: %.1f ГиБ", m.Name, float64(m.Size)/(1<<30))
		if m.Evicted() {
			line += fmt.Sprintf(" — ВЫТЕСНЕНА: на карте %d%%, в ОЗУ %.1f ГиБ",
				m.VRAMPct, float64(m.SizeRAM)/(1<<30))
		}
		if m.ContextLength > 0 {
			line += fmt.Sprintf(", окно %d", m.ContextLength)
		}
		fmt.Fprintln(stdout, line)
	}
	if h := rep.Host; h != nil {
		fmt.Fprintf(stdout, "  ОЗУ: свободно %d из %d МиБ, средняя загрузка %.2f на %d ядер\n",
			h.RAMFreeMiB, h.RAMTotalMiB, h.LoadAvg[0], h.Cores)
	}
	if pr := rep.Ollama; pr != nil {
		fmt.Fprintf(stdout, "  процесс Ollama %d: %d МиБ ОЗУ, потоков %d\n", pr.PID, pr.RSSMiB, pr.Threads)
	}
	if d := rep.Disk; d != nil {
		fmt.Fprintf(stdout, "  диск %s: свободно %d из %d МиБ\n", d.Path, d.FreeMiB, d.TotalMiB)
	}
	for _, j := range rep.Journal {
		fmt.Fprintf(stdout, "  журнал (%s): %s\n", j.Kind, j.Text)
	}
	if reason := rep.Busy(0); reason != "" {
		fmt.Fprintf(stdout, "  ЗАНЯТО: %s\n", reason)
	}
	// Несобранное показывается всегда: пустой раздел без причины читается
	// как «там ничего нет», а это разные вещи.
	for _, m := range rep.Missing {
		fmt.Fprintf(stdout, "  не видно (%s): %s\n", m.Section, m.Reason)
	}
}
