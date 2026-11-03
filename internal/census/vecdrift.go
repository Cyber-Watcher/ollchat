// Перепись сдвига эмбеддера: дают ли сервер из настроек (и, с -vs, второй
// сервер) те же векторы тех же текстов, что уже лежат в индексе коллекции.
//
// **Зачем.** Векторы кусков посчитаны одной Ollama, а вопрос векторизует та,
// на которую смотрит `kb.embed_url` сейчас. Обновление Ollama или переезд
// эмбеддера на другой сервер меняет ядра расчёта при тех же весах `bge-m3`,
// и если векторы разъедутся, поиск начнёт сравнивать вопрос «из одного
// пространства» с кусками «из другого» — молча, отвечать он не перестанет.
// Digest весов это не ловит: он одинаковый (паспорт векторов, kb/vectors.go).
//
// **Как.** n кусков равномерно по векторизованной части хранилища, текст —
// ровно тот, что векторизовал --kb-embed (kb.Collection.EmbedProbe). Каждый
// векторизуется заново и квантуется как при записи (kb.Quantize); считается
// полный косинус с сохранённым (vecstand.CosineRaw — без поправки на норму,
// см. vec-norms). С -vs те же тексты идут и на второй сервер, и печатается
// ещё косинус «сервер против сервера» по векторам до квантования.
//
// Запросы идут только пачками от двух текстов и без кеша на диске: кеш
// эмбеддера держит одиночные тексты по паре «модель, текст» без адреса
// сервера, и второй сервер получил бы вектор первого.
package census

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph/vecstand"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// vecDriftBatch — сколько текстов в одном запросе к эмбеддеру.
const vecDriftBatch = 16

// vecDrift — режим `-only vec-drift`.
func vecDrift(stdout io.Writer, cfg *config.Config, collName string, n int, vs string) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	texts, stored, err := c.EmbedProbe(n)
	if err != nil {
		return err
	}
	if len(texts) < 2 {
		return fmt.Errorf("кусков с векторами меньше двух — сверять нечего")
	}
	meta := c.VecMeta()
	fmt.Fprintf(stdout, "коллекция %s: векторы %s, размерность %d, шапка %v; к сверке %d кусков\n",
		collName, meta.Model, meta.Dim, meta.Header, len(texts))

	mk := func(url string) *kbembed.Embedder {
		o := cfg.KB.EmbedOptions()
		o.CacheDir = ""
		if url != "" {
			o.URL = url
		}
		return kbembed.New(o, cfg.EmbedFallback(), 5*time.Minute, cfg.EmbedFallbackHeaders())
	}
	main := mk("")
	if main == nil {
		return fmt.Errorf("эмбеддер не настроен: kb.embed_model")
	}
	ctx := context.Background()
	fmt.Fprintf(stdout, "эмбеддер: %s на %s\n", main.Model(), main.URL())
	got, err := embedBatches(ctx, main, texts)
	if err != nil {
		return err
	}
	printDrift(stdout, "сервер из настроек против индекса", vsStored(got, stored))

	if vs != "" {
		second := mk(vs)
		fmt.Fprintf(stdout, "\nвторой сервер: %s на %s\n", second.Model(), second.URL())
		got2, err := embedBatches(ctx, second, texts)
		if err != nil {
			return err
		}
		printDrift(stdout, "второй сервер против индекса", vsStored(got2, stored))
		cross := make([]float64, len(got))
		for i := range got {
			cross[i] = cosineFloat(got[i], got2[i])
		}
		printDrift(stdout, "сервер против сервера (до квантования)", cross)
	}
	return nil
}

// embedBatches векторизует тексты пачками не меньше двух: одиночный текст
// эмбеддер отдал бы из кеша (см. шапку файла).
func embedBatches(ctx context.Context, e *kbembed.Embedder, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for from := 0; from < len(texts); {
		to := from + vecDriftBatch
		if to > len(texts) || len(texts)-to < 2 {
			to = len(texts)
		}
		v, err := e.Embed(ctx, texts[from:to])
		if err != nil {
			return nil, fmt.Errorf("эмбеддер %s: %w", e.URL(), err)
		}
		if len(v) != to-from {
			return nil, fmt.Errorf("эмбеддер %s вернул %d векторов на %d текстов", e.URL(), len(v), to-from)
		}
		out = append(out, v...)
		from = to
	}
	return out, nil
}

// vsStored — полный косинус нового (квантованного, как при записи) вектора
// с сохранённым.
func vsStored(got [][]float32, stored [][]int8) []float64 {
	out := make([]float64, len(got))
	for i := range got {
		out[i] = vecstand.CosineRaw(kb.Quantize(got[i]), stored[i])
	}
	return out
}

func cosineFloat(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// printDrift — распределение косинусов: худший, 1-й процентиль, медиана и
// сколько пар ниже порогов. Сколько отнимает само квантование до int8,
// показывает этот же прибор на том сервере и той версии Ollama, которыми
// считался индекс: это шум, с ним и сравнивается всякий другой сервер.
func printDrift(stdout io.Writer, label string, cos []float64) {
	s := append([]float64(nil), cos...)
	sort.Float64s(s)
	at := func(q float64) float64 { return s[int(q*float64(len(s)-1))] }
	below := func(t float64) int {
		n := 0
		for _, x := range s {
			if x < t {
				n++
			}
		}
		return n
	}
	fmt.Fprintf(stdout, "%s: косинус мин %.6f / 1-й проц. %.6f / медиана %.6f / макс %.6f; ниже 0,9999 — %d, ниже 0,999 — %d, ниже 0,99 — %d из %d\n",
		label, s[0], at(0.01), at(0.5), s[len(s)-1], below(0.9999), below(0.999), below(0.99), len(s))
}
