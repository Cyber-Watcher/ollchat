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
		return kbembed.New(o, cfg.EmbedFallback(), 5*time.Minute, cfg.EmbedHeaders())
	}
	main := mk("")
	if main == nil {
		return fmt.Errorf("эмбеддер не настроен: kb.embed_model")
	}
	// Размерность индекса — из паспорта векторов; у паспорта без неё —
	// по сохранённому вектору.
	dim := meta.Dim
	if dim <= 0 && len(stored) > 0 {
		dim = len(stored[0])
	}
	ctx := context.Background()
	fmt.Fprintf(stdout, "эмбеддер: %s на %s\n", main.Model(), main.URL())
	got, err := embedBatches(ctx, main, texts)
	if err != nil {
		return err
	}
	if err := checkDim(main.URL(), got, dim); err != nil {
		return err
	}
	cos, err := vsStored(got, stored)
	if err != nil {
		return err
	}
	printDrift(stdout, "сервер из настроек против индекса", cos)

	if vs != "" {
		second := mk(vs)
		fmt.Fprintf(stdout, "\nвторой сервер: %s на %s\n", second.Model(), second.URL())
		got2, err := embedBatches(ctx, second, texts)
		if err != nil {
			return err
		}
		if err := checkDim(second.URL(), got2, dim); err != nil {
			return err
		}
		cos, err := vsStored(got2, stored)
		if err != nil {
			return err
		}
		printDrift(stdout, "второй сервер против индекса", cos)
		cross := make([]float64, len(got))
		for i := range got {
			if cross[i], err = cosineFloat(got[i], got2[i]); err != nil {
				return fmt.Errorf("кусок %d, сервер против сервера: %w", i+1, err)
			}
		}
		printDrift(stdout, "сервер против сервера (до квантования)", cross)
	}
	return nil
}

// checkDim сверяет размерность векторов эмбеддера с размерностью индекса —
// до счёта косинусов. Другая размерность — это другая модель под тем же
// именем, ровно тот случай, ради которого прибор заведён; счёт по векторам
// разной длины прежде либо падал паникой (вектор сервера длиннее), либо
// давал правдоподобное число по префиксу (короче).
func checkDim(url string, vecs [][]float32, dim int) error {
	for i, v := range vecs {
		if len(v) != dim {
			return fmt.Errorf("эмбеддер %s отдал вектор размерности %d, а в индексе размерность %d "+
				"(кусок %d из %d): это другая модель под тем же именем, сверять косинусы нечего",
				url, len(v), dim, i+1, len(vecs))
		}
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
// с сохранённым. Разная длина пары или разное число векторов — ошибка, а не
// косинус по префиксу: vecstand.CosineRaw длину не сверяет.
func vsStored(got [][]float32, stored [][]int8) ([]float64, error) {
	if len(got) != len(stored) {
		return nil, fmt.Errorf("векторов сервера %d, сохранённых %d", len(got), len(stored))
	}
	out := make([]float64, len(got))
	for i := range got {
		if len(got[i]) != len(stored[i]) {
			return nil, fmt.Errorf("кусок %d: размерность вектора сервера %d, сохранённого %d",
				i+1, len(got[i]), len(stored[i]))
		}
		out[i] = vecstand.CosineRaw(kb.Quantize(got[i]), stored[i])
	}
	return out, nil
}

// cosineFloat — косинус двух векторов до квантования. Разная размерность —
// ошибка: косинус по префиксу выглядел бы правдоподобным числом.
func cosineFloat(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("размерность векторов %d и %d", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return dot / math.Sqrt(na*nb), nil
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
