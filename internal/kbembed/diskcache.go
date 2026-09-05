package kbembed

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Кэш векторов запросов на диске (этап 91, R2.12).
//
// Кэш в памяти не переживает перезапуск и не общий между ollchat и ollmcp,
// а книги говорят, что это самая дешёвая из оптимизаций («Agentic RAG
// Systems», 2026, стр. 219). Здесь дело не в деньгах: повторный вопрос при
// занятой карте отвечает из файла, не дожидаясь сервера эмбеддингов.
//
// Файл — строки JSON: модель, запрос, вектор как base64 от float32 LE.
// Только дозапись; при старте читаются последние queryCacheMax строк; когда
// строк вдвое больше предела, файл переписывается атомарно. Два процесса,
// пишущие одновременно, могут продублировать строку — это не потеря.

type diskEntry struct {
	Model string `json:"m"`
	Query string `json:"q"`
	Vec   string `json:"v"`
}

var disk struct {
	mu    sync.Mutex
	path  string
	lines int
}

// useDiskCache включает файл кэша и подгружает его в память.
func useDiskCache(path string) {
	disk.mu.Lock()
	defer disk.mu.Unlock()
	if disk.path == path {
		return
	}
	disk.path, disk.lines = path, 0
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var entries []diskEntry
	for sc.Scan() {
		var e diskEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Query != "" {
			entries = append(entries, e)
		}
	}
	disk.lines = len(entries)
	if len(entries) > queryCacheMax {
		entries = entries[len(entries)-queryCacheMax:]
	}
	for _, e := range entries {
		if vec := decodeVec(e.Vec); len(vec) > 0 {
			cachePut(e.Model, e.Query, vec)
		}
	}
}

// diskPut дописывает вектор в файл; переполненный файл переписывается.
func diskPut(model, text string, vec []float32) {
	disk.mu.Lock()
	defer disk.mu.Unlock()
	if disk.path == "" {
		return
	}
	line, err := json.Marshal(diskEntry{Model: model, Query: text, Vec: encodeVec(vec)})
	if err != nil {
		return
	}
	if disk.lines >= 2*queryCacheMax {
		disk.lines = 0
		_ = fsx.WriteFileAtomic(disk.path, dumpLocked(), 0o600)
		return
	}
	_ = os.MkdirAll(filepath.Dir(disk.path), 0o755)
	f, err := os.OpenFile(disk.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err == nil {
		disk.lines++
	}
}

// dumpLocked — содержимое кэша в памяти строками файла.
func dumpLocked() []byte {
	queryCache.mu.Lock()
	defer queryCache.mu.Unlock()
	var b strings.Builder
	for _, key := range queryCache.order {
		model, query, _ := strings.Cut(key, "\x00")
		line, err := json.Marshal(diskEntry{Model: model, Query: query, Vec: encodeVec(queryCache.byKey[key])})
		if err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	disk.lines = len(queryCache.order)
	return []byte(b.String())
}

func encodeVec(v []float32) string {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func decodeVec(s string) []float32 {
	buf, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(buf)%4 != 0 {
		return nil
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
	}
	return out
}
