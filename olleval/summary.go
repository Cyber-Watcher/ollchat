package main

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
)

// Summary — сводка по одной паре «модель × набор».
type Summary struct {
	Model           string
	Suite           string
	Attempts        int
	MeanScore       float64
	MedianSeconds   float64
	MedianTokPerSec float64
	Errors          int
	// TimedOut — сколько попыток упёрлось в предел времени. Считается
	// отдельно от сбоев: это результат про модель (не уложилась, ушла
	// в бесконечные рассуждения), а не беда стенда.
	TimedOut int
	Review   int
}

// ReadIndex читает index.jsonl ночи. Битая строка не роняет разбор: пропускаем
// её и читаем дальше — сводка нужнее, чем безупречность файла.
func ReadIndex(path string) ([]Metrics, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Metrics
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m Metrics
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// Summarize складывает попытки в сводку по паре «модель × набор».
//
// Балл — средний по попыткам, а время и скорость — медианные: одна задача,
// упёршаяся в таймаут, сдвинула бы среднее так, что цифра перестала бы
// описывать обычный ответ.
func Summarize(recs []Metrics) map[string]*Summary {
	type bucket struct {
		s     *Summary
		score []float64
		secs  []float64
		tps   []float64
	}
	buckets := make(map[string]*bucket)
	for _, r := range recs {
		key := r.Model + "|" + r.Suite
		b, ok := buckets[key]
		if !ok {
			b = &bucket{s: &Summary{Model: r.Model, Suite: r.Suite}}
			buckets[key] = b
		}
		b.s.Attempts++
		switch {
		case r.TimedOut:
			b.s.TimedOut++
		case r.Error != "":
			b.s.Errors++
		}
		if r.NeedsReview {
			b.s.Review++
		}
		b.score = append(b.score, r.Score)
		b.secs = append(b.secs, r.WallSeconds)
		if r.TokensPerSecond > 0 {
			b.tps = append(b.tps, r.TokensPerSecond)
		}
	}

	out := make(map[string]*Summary, len(buckets))
	for k, b := range buckets {
		b.s.MeanScore = mean(b.score)
		b.s.MedianSeconds = median(b.secs)
		b.s.MedianTokPerSec = median(b.tps)
		out[k] = b.s
	}
	return out
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}
