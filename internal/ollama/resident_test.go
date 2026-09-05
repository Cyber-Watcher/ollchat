package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func psServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 5*time.Second, 5*time.Second, nil)
}

// Имя в конфиге пишут и с тегом, и без него, а сервер отвечает всегда с тегом.
// Без этого проверка «загружена ли» отвечала бы «нет» половине конфигов.
func TestResidentMatchesNameWithAndWithoutTag(t *testing.T) {
	c := psServer(t, `{"models":[{"name":"qwen3.5:122b","model":"qwen3.5:122b",
		"size":100,"size_vram":100}]}`)
	for _, want := range []string{"qwen3.5", "qwen3.5:122b"} {
		got := c.Resident(context.Background(), want)
		if !got.Known || !got.Loaded {
			t.Errorf("модель %q не признана загруженной: %+v", want, got)
		}
		if got.VRAMPct != 100 {
			t.Errorf("модель %q: в видеопамяти %d%%, ожидалось 100", want, got.VRAMPct)
		}
	}
	// Чужая модель на сервере не должна сходить за нашу.
	if got := c.Resident(context.Background(), "qwen3"); got.Loaded {
		t.Errorf("«qwen3» принята за «qwen3.5:122b»: %+v", got)
	}
}

// Пустой список — модель не загружена, и это точно известно.
func TestResidentEmptyMeansNotLoaded(t *testing.T) {
	c := psServer(t, `{"models":[]}`)
	got := c.Resident(context.Background(), "qwen3.5")
	if !got.Known {
		t.Error("сервер ответил, а Known ложно")
	}
	if got.Loaded {
		t.Error("при пустом списке модель считается загруженной")
	}
}

// Сервер не ответил — молчим, а не выдумываем.
//
// Это важнее, чем кажется: «не знаю» и «не загружена» ведут к разным надписям,
// и показать «загружается» там, где просто оборвалась сеть, значит соврать.
func TestResidentUnknownOnError(t *testing.T) {
	c := New("http://127.0.0.1:1", time.Second, time.Second, nil)
	got := c.Resident(context.Background(), "qwen3.5")
	if got.Known {
		t.Errorf("недоступный сервер дал уверенный ответ: %+v", got)
	}
}

// Часть весов в оперативной памяти — видно по size_vram, а не по size.
func TestResidentPartialVRAM(t *testing.T) {
	c := psServer(t, `{"models":[{"name":"glm:q8_0","model":"glm:q8_0",
		"size":1000,"size_vram":400}]}`)
	got := c.Resident(context.Background(), "glm:q8_0")
	if !got.Loaded || got.VRAMPct != 40 {
		t.Errorf("ожидалось «загружена, 40%% в видеопамяти», получено %+v", got)
	}
}
