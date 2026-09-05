//go:build live

package live

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Замер параметров загрузки: num_batch и num_gpu.
//
// Они отличаются от параметров генерации тем, что действуют в момент, когда
// модель поднимается в память, а не на каждый запрос. Поэтому перед каждой
// точкой модель обязательно выгружается: иначе сервер просто ответит уже
// загруженной моделью с прежними значениями, и замер покажет одно и то же.

// longPrompt собирает длинный текст, чтобы чтение промпта заняло заметное
// время. Скорость чтения промпта и есть то, на что влияет num_batch.
func longPrompt(paragraphs int) string {
	var sb strings.Builder
	sb.WriteString("Ниже приведён технический текст. Прочитай его и ответь одним словом: сколько в нём разделов.\n\n")
	for i := 1; i <= paragraphs; i++ {
		sb.WriteString("Раздел ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(". Каналы в языке Go служат для передачи значений между горутинами. " +
			"Небуферизованный канал синхронизирует отправителя и получателя: отправка блокируется, " +
			"пока другая горутина не заберёт значение. Буферизованный канал позволяет отправить " +
			"столько значений, сколько вмещает буфер, и только после этого блокирует отправителя. " +
			"Закрытие канала сообщает получателям, что значений больше не будет.\n\n")
	}
	return sb.String()
}

// gpuUsedMiB читает занятую видеопамять с сервера, если задан OLLCHAT_GPU_SSH.
//
// По сети этого не видно: /api/ps не учитывает буферы вычислений и занижает
// расход — это замерено раньше и записано в HowToCalcModelPrice.md.
func gpuUsedMiB(t *testing.T) int {
	t.Helper()
	host := os.Getenv("OLLCHAT_GPU_SSH")
	if host == "" {
		return 0
	}
	out, err := exec.Command("ssh", "-o", "ConnectTimeout=10", host,
		"nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits").Output()
	if err != nil {
		t.Logf("nvidia-smi: %v", err)
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.Split(string(out), "\n")[0]))
	if err != nil {
		return 0
	}
	return n
}

// TestNumBatch меряет, как размер пачки влияет на скорость чтения промпта.
func TestNumBatch(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false
	prompt := longPrompt(120) // около 8-9 тысяч токенов

	for _, model := range models(t) {
		for _, batch := range []int{128, 256, 512, 1024, 2048} {
			unload(t, c, model)
			opts := map[string]any{"num_ctx": 16384, "num_batch": batch, "num_predict": 16}

			// Первый запрос грузит модель — его в замер не берём: в нём сидит
			// время загрузки, а меряем мы чтение промпта.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			_, _, _, err := ask(ctx, c, model, "Ответь одним словом: готов", opts, &no)
			cancel()
			if err != nil {
				t.Logf("%s / num_batch=%d: загрузка не удалась: %v", model, batch, err)
				continue
			}

			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
			started := time.Now()
			ans, _, st, err := ask(ctx, c, model, prompt, opts, &no)
			wall := time.Since(started)
			cancel()
			if err != nil {
				t.Logf("%s / num_batch=%d: %v", model, batch, err)
				continue
			}
			used := gpuUsedMiB(t)
			res.add(run{Model: model, Param: "num_batch", Value: batch, Options: opts,
				Probe: "длинный промпт", Attempt: 1, Answer: shorten(ans, 80),
				PromptTokens: st.PromptEvalCount, EvalTokens: st.EvalCount,
				TokPerSec: st.TokensPerSecond(), PromptTokSec: st.PromptTokensPerSecond(),
				DoneReason: st.DoneReason, WallMs: wall.Milliseconds()})
			t.Logf("%s · num_batch=%-5d · промпт %5d токенов · чтение %7.0f ток/с · генерация %5.1f ток/с · карта %d МиБ",
				model, batch, st.PromptEvalCount, st.PromptTokensPerSecond(), st.TokensPerSecond(), used)
		}
		unload(t, c, model)
	}
	res.save(t, "num_batch.json")
}

// TestNumGPU меряет, что происходит, когда часть слоёв уходит на процессор.
func TestNumGPU(t *testing.T) {
	c := client(t)
	res := newResults(c)
	no := false

	for _, model := range models(t) {
		// Сколько всего слоёв у модели — из метаданных: без этого непонятно,
		// что означает конкретное число num_gpu.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		show, err := c.Show(ctx, model)
		cancel()
		blocks := 0
		if err == nil {
			for k, v := range show.ModelInfo {
				if strings.HasSuffix(k, ".block_count") {
					if f, ok := v.(float64); ok {
						blocks = int(f)
					}
				}
			}
		}
		t.Logf("%s: слоёв в модели %d", model, blocks)

		values := []int{-1}
		if blocks > 0 {
			values = append(values, blocks/2, 0)
		}

		for _, ngpu := range values {
			unload(t, c, model)
			opts := map[string]any{"num_ctx": 8192, "num_gpu": ngpu, "num_predict": 64}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			ans, _, st, err := ask(ctx, c, model, "Объясни в трёх предложениях, что такое горутина.", opts, &no)
			cancel()
			if err != nil {
				t.Logf("%s / num_gpu=%d: %v", model, ngpu, err)
				continue
			}

			// Насколько модель уехала в оперативную память, видно по /api/ps:
			// size — сколько занимает всего, size_vram — сколько на карте.
			ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
			running, _ := c.PS(ctx)
			cancel()
			var size, vram int64
			for _, r := range running {
				if r.Name == model || r.Model == model {
					size, vram = r.Size, r.SizeVRAM
				}
			}
			res.add(run{Model: model, Param: "num_gpu", Value: ngpu, Options: opts,
				Probe: "короткий вопрос", Attempt: 1, Answer: shorten(ans, 80),
				PromptTokens: st.PromptEvalCount, EvalTokens: st.EvalCount,
				TokPerSec: st.TokensPerSecond(), PromptTokSec: st.PromptTokensPerSecond(),
				DoneReason: st.DoneReason})
			t.Logf("%s · num_gpu=%-4d · генерация %6.1f ток/с · всего %.1f ГиБ · на карте %.1f ГиБ (%.0f%%)",
				model, ngpu, st.TokensPerSecond(),
				float64(size)/(1<<30), float64(vram)/(1<<30), pct(vram, size))
		}
		unload(t, c, model)
	}
	res.save(t, "num_gpu.json")
}

func pct(part, whole int64) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
