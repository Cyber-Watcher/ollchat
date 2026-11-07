// askpool.go — общий пул запросов к модели извлечения для замеров, которым
// нужно несколько прогонов на один кусок: `-only direction` и
// `-only stability -axis prompt`. Вынесен из direction.go, когда второй замер
// потребовал того же: кусок, системный промпт на каждый прогон, параллельные
// запросы, разбор ParseFacts и время каждого ответа. Ось temp того же режима
// stability гоняет запросы по одному и этого пула не использует.
//
// Запросы одного куска идут подряд по номерам прогонов, куски — по порядку, а
// свободный из workers горутин берёт следующий запрос: прогоны разных
// промптов вперемешку нагружают сервер одинаково, и среднее время ответа
// сравнимо между прогонами.
package probes

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// poolChunk — то, что нужно от куска для вопроса модели.
type poolChunk struct {
	Key         graph.ChunkKey
	Title, Unit string
	From, To    int
	Text        string
}

// poolChunkOf — кусок коллекции в виде для вопроса модели. Заголовок книги,
// если его нет, — имя файла: без него «он» и «эта библиотека» не к чему привязать
// (так же, как в stability и seq).
func poolChunkOf(ci kb.ChunkInfo) poolChunk {
	title := ci.Book.Title
	if title == "" {
		title = filepath.Base(ci.Book.Path)
	}
	return poolChunk{Key: graph.ChunkKey{Doc: ci.Doc, Ord: ci.Ord}, Title: title,
		Unit: ci.Unit, From: ci.UnitFrom, To: ci.UnitTo, Text: ci.Text}
}

// poolAnswer — разобранный ответ модели на один запрос.
type poolAnswer struct {
	Facts graph.Facts
	OK    bool
	Err   error
	// Dur — сколько шёл запрос до ответа; считается и у ответа, который не
	// разобрался, но в среднее времени входят только OK.
	Dur time.Duration
}

// askOne — один запрос и разбор ответа тем же ParseFacts, что у stability.
func askOne(ctx context.Context, ex graph.Extractor, system, user, text string) poolAnswer {
	started := time.Now()
	answer, err := ex.Extract(ctx, system, user)
	dur := time.Since(started)
	if err != nil {
		return poolAnswer{Err: err, Dur: dur}
	}
	facts, err := graph.ParseFacts(answer, text)
	if err != nil {
		return poolAnswer{Err: err, Dur: dur}
	}
	return poolAnswer{Facts: facts, OK: true, Dur: dur}
}

// askRuns гонит len(systems) запросов на каждый кусок, workers горутин: прогон r
// идёт с системным промптом systems[r]. Каждый запрос пишет в свою ячейку
// результата, поэтому замок нужен только печати хода. Ход печатается по готовым
// запросам.
func askRuns(ctx context.Context, ex graph.Extractor, systems []string, chunks []poolChunk, workers int, progress io.Writer) [][]poolAnswer {
	runs := len(systems)
	answers := make([][]poolAnswer, len(chunks))
	for i := range answers {
		answers[i] = make([]poolAnswer, runs)
	}
	type job struct{ chunk, run int }
	jobs := make(chan job)
	total := len(chunks) * runs
	var done atomic.Int64
	var mu sync.Mutex
	started := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				ch := chunks[j.chunk]
				user := graph.UserPrompt(ch.Title, ch.Unit, ch.From, ch.To, ch.Text)
				answers[j.chunk][j.run] = askOne(ctx, ex, systems[j.run], user, ch.Text)
				n := int(done.Add(1))
				if n%5 == 0 || n == total {
					mu.Lock()
					fmt.Fprintf(progress, "%s запросов %d/%d, прошло %s\n", time.Now().Format("15:04:05"), n, total,
						time.Since(started).Round(time.Second))
					mu.Unlock()
				}
			}
		}()
	}
	for i := range chunks {
		for r := 0; r < runs; r++ {
			jobs <- job{chunk: i, run: r}
		}
	}
	close(jobs)
	wg.Wait()
	return answers
}

// meanSeconds — среднее время одного запроса прогона run в секундах по запросам
// с ответом; второй результат — сколько их. Без ответов — ноль и ноль.
func meanSeconds(answers [][]poolAnswer, run int) (float64, int) {
	sum, n := 0.0, 0
	for _, ans := range answers {
		if run < len(ans) && ans[run].OK {
			sum += ans[run].Dur.Seconds()
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// printRunSeconds печатает среднее время запроса прогона одной строкой.
func printRunSeconds(w io.Writer, sec float64, n int) {
	if n == 0 {
		fmt.Fprintln(w, "  среднее время запроса: нет ответов, считать нечего")
		return
	}
	fmt.Fprintf(w, "  среднее время запроса: %.2f с (по %d запросам с ответом)\n", sec, n)
}

// firstAnswerErr — первая ошибка запроса или разбора по порядку кусков, либо nil.
// Без неё итог «сбоев: 360» не говорит, чей это сбой (адрес, модель, разбор).
func firstAnswerErr(answers [][]poolAnswer) error {
	for _, ans := range answers {
		for _, a := range ans {
			if a.Err != nil {
				return a.Err
			}
		}
	}
	return nil
}

// allAnswered — у всех ли прогонов куска есть ответ.
func allAnswered(ans []poolAnswer) bool {
	for _, a := range ans {
		if !a.OK {
			return false
		}
	}
	return true
}
