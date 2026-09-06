package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/nodeprobe"
)

// testServer — служба с подменённым сбором: карта и systemd в тестах не нужны.
func testServer(t *testing.T, ttl time.Duration, calls *int32) *server {
	t.Helper()
	run := func(ctx context.Context, name string, args ...string) (string, int, error) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		switch {
		case name == "nvidia-smi" && strings.Contains(args[0], "memory.total"):
			return "0, NVIDIA A100, 81920, 20480, 61440, 3, 41\n", 0, nil
		case name == "nvidia-smi" && strings.Contains(args[0], "compute-apps"):
			return "12345, /usr/local/bin/ollama, 20480\n", 0, nil
		case name == "systemctl":
			return "ActiveState=active\nMainPID=12345\nEnvironment=OLLAMA_NUM_PARALLEL=4\n", 0, nil
		case name == "journalctl":
			return "сен 06 ollama[1]: WARN does not currently support parallel requests\n", 0, nil
		}
		return "", 0, nil
	}
	return &server{
		opts:  nodeprobe.Opts{Run: run, Service: "ollama"},
		ttl:   ttl,
		token: "секрет",
	}
}

func request(t *testing.T, s *server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.node(w, r)
	return w
}

// Без токена и с чужим токеном служба не отдаёт ничего: она рассказывает
// об устройстве машины.
func TestNodeRequiresToken(t *testing.T) {
	s := testServer(t, time.Second, nil)
	if w := request(t, s, "/api/v1/node", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("без токена код %d", w.Code)
	}
	if w := request(t, s, "/api/v1/node", "не-тот"); w.Code != http.StatusUnauthorized {
		t.Errorf("с чужим токеном код %d", w.Code)
	}
	if w := request(t, s, "/api/v1/node", "секрет"); w.Code != http.StatusOK {
		t.Errorf("со своим токеном код %d", w.Code)
	}
}

// Снимок отдаётся разобранным, а не как попало.
func TestNodeReturnsSnapshot(t *testing.T) {
	s := testServer(t, time.Second, nil)
	w := request(t, s, "/api/v1/node", "секрет")

	var rep nodeprobe.Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("ответ не разбирается: %v", err)
	}
	if len(rep.GPUs) != 1 || rep.GPUs[0].MemUsed != 20480 {
		t.Errorf("карта в ответе: %+v", rep.GPUs)
	}
	if rep.Service.Slots() != 4 {
		t.Errorf("слоты службы не доехали: %+v", rep.Service)
	}
	if len(rep.Journal) != 1 || rep.Journal[0].Kind != "parallel" {
		t.Errorf("журнал: %+v", rep.Journal)
	}
}

// Кэш: два запроса подряд — один сбор. Снимок стоит секунд, а пул карт
// спрашивает часто.
func TestNodeCachesSnapshot(t *testing.T) {
	var calls int32
	s := testServer(t, time.Minute, &calls)

	request(t, s, "/api/v1/node", "секрет")
	first := atomic.LoadInt32(&calls)
	if first == 0 {
		t.Fatal("сбор не запускался вовсе")
	}
	request(t, s, "/api/v1/node", "секрет")
	if got := atomic.LoadInt32(&calls); got != first {
		t.Errorf("второй запрос собрал заново: было %d вызовов, стало %d", first, got)
	}
}

// Дешёвый снимок не читает журнал и диск, но полный запрос после дешёвого
// собирается заново: иначе за журналом пришёл бы ответ без журнала.
func TestNodeLightSnapshot(t *testing.T) {
	s := testServer(t, time.Minute, nil)
	w := request(t, s, "/api/v1/node?light=1", "секрет")

	var light nodeprobe.Report
	if err := json.Unmarshal(w.Body.Bytes(), &light); err != nil {
		t.Fatal(err)
	}
	if len(light.Journal) != 0 {
		t.Errorf("дешёвый снимок прочитал журнал: %+v", light.Journal)
	}

	w = request(t, s, "/api/v1/node", "секрет")
	var full nodeprobe.Report
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Journal) == 0 {
		t.Error("полный запрос получил дешёвый снимок из кэша")
	}
}

// Кэш стареет.
func TestNodeCacheExpires(t *testing.T) {
	var calls int32
	s := testServer(t, 10*time.Millisecond, &calls)
	request(t, s, "/api/v1/node", "секрет")
	first := atomic.LoadInt32(&calls)
	time.Sleep(20 * time.Millisecond)
	request(t, s, "/api/v1/node", "секрет")
	if got := atomic.LoadInt32(&calls); got == first {
		t.Error("устаревший снимок отдан из кэша")
	}
}
