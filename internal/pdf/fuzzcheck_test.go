package pdf

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealBooksCorrupted портит настоящие книги с диска и скармливает разбору.
// Запускается только с OLLCHAT_FUZZ_DIR — в обычном прогоне пропускается.
func TestRealBooksCorrupted(t *testing.T) {
	dir := os.Getenv("OLLCHAT_FUZZ_DIR")
	if dir == "" {
		t.Skip("не задан OLLCHAT_FUZZ_DIR")
	}
	var files []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		t.Skip("книг не нашлось")
	}
	rnd := rand.New(rand.NewSource(1))
	rnd.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
	if len(files) > 25 {
		files = files[:25]
	}

	runs := 0
	for _, f := range files {
		orig, err := os.ReadFile(f)
		if err != nil || len(orig) < 1000 {
			continue
		}
		if len(orig) > 12<<20 {
			orig = orig[:12<<20] // обрезка — сама по себе неплохая порча
		}
		for i := 0; i < 40; i++ {
			data := append([]byte(nil), orig...)
			switch i % 4 {
			case 0: // случайные байты
				for n := rnd.Intn(64) + 1; n > 0; n-- {
					data[rnd.Intn(len(data))] = byte(rnd.Intn(256))
				}
			case 1: // обрезка в случайном месте
				data = data[:rnd.Intn(len(data))]
			case 2: // вырезать кусок из середины
				a := rnd.Intn(len(data))
				b := a + rnd.Intn(len(data)-a)
				data = append(data[:a:a], data[b:]...)
			case 3: // испортить числа в словарях
				data = []byte(strings.ReplaceAll(string(data), "/Length", "/Length 4294967295 %"))
			}
			runs++
			Extract(data, Options{MaxPages: 5})
			ExtractImages(data, ImageOptions{MaxCount: 3})
		}
	}
	t.Logf("прогонов: %d по %d книгам", runs, len(files))
}
