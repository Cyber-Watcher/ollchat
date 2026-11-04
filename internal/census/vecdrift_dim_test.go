package census

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dimEmbedServer — эмбеддер, отдающий векторы размерности dim.
func dimEmbedServer(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			v := make([]float32, dim)
			for j := range v {
				v[j] = float32(j%7 + 1)
			}
			vecs[i] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Сервер, отдающий векторы другой размерности, чем лежат в индексе, — это
// другая модель под тем же именем: тот самый случай, ради которого прибор
// заведён. Прежде длинный вектор ронял прибор паникой, а короткий давал
// правдоподобный косинус по префиксу. Теперь — отказ до счёта, с размерностями.
func TestVecDriftRefusesOtherDim(t *testing.T) {
	cfg, name := newVectorFixture(t, "bge-m3", "bge-m3", "")
	const dim = 1024 // размерность фикстуры
	same := dimEmbedServer(t, dim)
	short := dimEmbedServer(t, dim-1)
	long := dimEmbedServer(t, dim+1)
	run := func(main, vs string) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("паника: %v", r)
			}
		}()
		cfg.KB.EmbedURL, cfg.KB.EmbedModel = main, "bge-m3"
		return vecDrift(io.Discard, cfg, name, 4, vs)
	}

	if err := run(same.URL, same.URL); err != nil {
		t.Fatalf("та же размерность — отказ: %v", err)
	}
	for _, c := range []struct{ name, main, vs string }{
		{"сервер из настроек короче индекса", short.URL, ""},
		{"сервер из настроек длиннее индекса", long.URL, ""},
		{"второй сервер короче", same.URL, short.URL},
		{"второй сервер длиннее", same.URL, long.URL},
	} {
		err := run(c.main, c.vs)
		if err == nil || strings.Contains(err.Error(), "паника") || !strings.Contains(err.Error(), "размерност") {
			t.Errorf("%s: ожидался отказ с размерностями, получено %v", c.name, err)
		}
	}

	// Сами косинусы разную длину тоже не считают — ни в одну сторону.
	for _, pair := range [][2][]float32{{{1, 2, 3, 4}, {1, 2, 3}}, {{1, 2, 3}, {1, 2, 3, 4}}} {
		if _, err := cosineFloat(pair[0], pair[1]); err == nil {
			t.Errorf("cosineFloat по векторам длины %d и %d — без ошибки", len(pair[0]), len(pair[1]))
		}
		if _, err := vsStored([][]float32{pair[0]}, [][]int8{make([]int8, len(pair[1]))}); err == nil {
			t.Errorf("vsStored по векторам длины %d и %d — без ошибки", len(pair[0]), len(pair[1]))
		}
	}
}
