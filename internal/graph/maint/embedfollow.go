package maint

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// Догонщик векторов понятий: считает векторы по мере появления понятий.
//
// **Зачем отдельной командой, а не внутри сборки.** Очередь понятий без
// векторов не хранится нигде — она выводится из двух чисел, лежащих на диске:
// номера от `Count` паспорта векторов до числа понятий в реестре. Значит
// догонщику не нужно ничего знать о сборке и не нужно ничего ей сообщать.
// Отдельный процесс, который раз в N минут смотрит, не появилось ли новых
// понятий, решает задачу целиком и **не трогает код сборки ни строкой**.
//
// **Почему граф открывается заново каждый круг.** Сборка дописывает реестр
// в другом процессе, и однажды открытый граф новых понятий не увидит.
// Открытие живого графа стоит десятки секунд и пару гигабайт памяти, поэтому
// срок между кругами по умолчанию длинный: считать надо не «как можно чаще»,
// а «достаточно часто, чтобы отставание не накапливалось».
//
// **Почему предел на заход.** Заход обязан кончаться за обозримое время:
// тогда отпечаток весов сверяется каждую пачку, остановка теряет не всю
// работу, а последнюю пачку, и человек видит движение числами.

// EmbedFollowOpts — как гнаться за графом.
type EmbedFollowOpts struct {
	// Node — адрес сервера с эмбеддером; пусто — тот же, что в настройках kb.
	//
	// Ради этого ключа всё и затевалось: извлечение занимает карту неделями,
	// и досчёт векторов встаёт в ту же очередь, хотя нужен ему эмбеддер
	// на 567 млн параметров. Отдельный узел вынимает эту работу из очереди
	// целиком, а не делит её.
	Node string

	// Every — сколько ждать между кругами; 0 — десять минут.
	Every time.Duration

	// Limit — сколько понятий считать за один заход; 0 — тысяча.
	Limit int

	// Once — один круг и выход. Для проверки и для разового досчёта.
	Once bool
}

func (o EmbedFollowOpts) norm() EmbedFollowOpts {
	if o.Every <= 0 {
		o.Every = 10 * time.Minute
	}
	if o.Limit <= 0 {
		o.Limit = 1000
	}
	return o
}

// EmbedFollow досчитывает векторы новых понятий, пока его не остановят.
func EmbedFollow(stdout io.Writer, cfg *config.Config, name string, o EmbedFollowOpts) error {
	o = o.norm()

	eo := cfg.KB.EmbedOptions()
	if o.Node != "" {
		eo.URL = o.Node
	}
	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	emb := kbembed.New(eo, fallback, 5*time.Minute, nil)
	if emb == nil {
		return fmt.Errorf("смысловой поиск не настроен: задайте kb.embed_model в %s", cfg.Path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "догонщик векторов: коллекция %s, модель %s на %s\n",
		name, emb.Model(), emb.URL())
	fmt.Fprintf(stdout, "круг раз в %s, до %d понятий за заход; остановка — Ctrl+C\n",
		o.Every, o.Limit)

	for {
		res, err := embedFollowRound(ctx, stdout, cfg, name, emb, o)
		switch {
		case err != nil && ctx.Err() != nil:
			// Остановлено человеком посреди круга: досчитанное уже на диске.
			fmt.Fprintln(stdout, "остановлено")
			return nil
		case err != nil:
			// Круг не удался — это не повод бросать: сборка могла держать замок,
			// сервер мог перезагружаться. Скажем и подождём следующего круга.
			fmt.Fprintf(stdout, "%s: круг не удался: %v\n", time.Now().Format("15:04:05"), err)
		default:
			fmt.Fprintf(stdout, "%s: понятий %d, вектор был у %d, досчитано %d, ждут %d\n",
				time.Now().Format("15:04:05"), res.Total, res.Before, res.Added, res.Pending())
		}
		if o.Once {
			return nil
		}
		// Ждать полный срок незачем, когда очередь ещё не разобрана: предел
		// на заход поставлен ради коротких кругов, а не ради пауз между ними.
		wait := o.Every
		if err == nil && res.Pending() > 0 {
			wait = time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			fmt.Fprintln(stdout, "остановлено")
			return nil
		}
	}
}

// embedFollowRound — один круг: открыть граф, досчитать хвост, закрыть.
//
// Граф открывается и закрывается внутри круга намеренно: держать его открытым
// значит не видеть понятий, которые допишет сборка, а держать открытым живой
// граф — это ещё и пара гигабайт памяти на всё время работы догонщика.
func embedFollowRound(ctx context.Context, stdout io.Writer, cfg *config.Config, name string,
	emb kb.Embedder, o EmbedFollowOpts) (graph.EmbedNewResult, error) {

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return graph.EmbedNewResult{}, err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return graph.EmbedNewResult{}, err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return graph.EmbedNewResult{}, err
	}
	defer g.Close()

	unmark, err := markWork(g, "векторы понятий")
	if err != nil {
		return graph.EmbedNewResult{}, err
	}
	defer unmark()

	last := time.Now()
	return g.EmbedNewEntities(ctx, emb, graph.EmbedOpts{
		Batch:   cfg.KB.EmbedBatch,
		Workers: cfg.KB.EmbedWorkers,
	}, o.Limit, func(p graph.EmbedProgress) {
		if time.Since(last) < time.Second && p.Done < p.Total {
			return
		}
		last = time.Now()
		fmt.Fprintf(os.Stderr, "\r  %d/%d понятий   ", p.Done, p.Total)
	})
}
