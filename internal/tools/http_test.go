package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fetch(t *testing.T, url string) (string, error) {
	t.Helper()
	opts, _ := hangTestOptions(t)
	return fetchURL(context.Background(), url, 64*1024, opts)
}

// Перенаправление в пределах того же адреса проходится, как и раньше.
func TestFetchFollowsSameOriginRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte("новое место"))
	}))
	defer srv.Close()

	out, err := fetch(t, srv.URL+"/old")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "новое место") || !strings.Contains(out, "HTTP 200") {
		t.Fatalf("перенаправление в пределах адреса не пройдено:\n%s", out)
	}
}

// Перенаправление на другой адрес клиент сам не проходит: правило Fetch
// проверяло только первый адрес, и узкое Fetch(https://доверенный/**)
// обходилось ответом 302. Модель получает адрес и запрашивает его отдельно —
// через проверку разрешений.
func TestFetchStopsAtCrossOriginRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("внутренняя служба"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/secret", http.StatusFound)
	}))
	defer srv.Close()

	out, err := fetch(t, srv.URL+"/go")
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatalf("клиент сам ушёл на другой адрес (%d запросов)", hits.Load())
	}
	want := "HTTP 302 → " + other.URL + "/secret; запросите этот адрес отдельно"
	if !strings.Contains(out, want) {
		t.Fatalf("ответ не называет адрес перенаправления:\n%s\nожидалось: %s", out, want)
	}
	if strings.Contains(out, "внутренняя служба") {
		t.Fatal("в ответе содержимое чужого адреса")
	}
}

// Петля в пределах адреса обрывается, а не крутится до таймаута.
func TestFetchRedirectLoopStops(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetch(t, srv.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "перенаправлений") {
		t.Fatalf("петля не оборвана: %v", err)
	}
	if n := hits.Load(); n != maxRedirects+1 {
		t.Fatalf("запросов %d, ожидалось %d", n, maxRedirects+1)
	}
}

// Адреса метаданных облака закрыты всегда — и записанные цифрами (отказ
// ещё до подтверждения), и полученные из имени (проверка при соединении).
func TestFetchRefusesMetadataAddresses(t *testing.T) {
	r, _ := newTestRegistry(t)
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://169.254.170.2/v2/credentials",
		"http://[fe80::1]/",
		"http://[fe80::1%25eth0]:8080/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/",
	} {
		_, err := r.Plan(NameHTTPFetch, map[string]any{"url": u})
		if err == nil || !strings.Contains(err.Error(), "закрыт") {
			t.Errorf("%s: ожидался отказ, получено %v", u, err)
		}
	}
	// Петля и частные сети не закрыты: о них решает правило Fetch.
	for _, u := range []string{"http://127.0.0.1:11434/api/tags", "http://192.168.1.10/", "http://10.0.0.5:8080/"} {
		if _, err := r.Plan(NameHTTPFetch, map[string]any{"url": u}); err != nil {
			t.Errorf("%s: отказ там, где решает правило: %v", u, err)
		}
	}

	for _, addr := range []string{"169.254.169.254:80", "[fe80::1]:443", "[fd00:ec2::254]:80", "[::ffff:169.254.169.254]:80"} {
		if err := refuseClosed("tcp", addr, nil); err == nil {
			t.Errorf("%s: соединение не отклонено", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.1:443", "93.184.216.34:443"} {
		if err := refuseClosed("tcp", addr, nil); err != nil {
			t.Errorf("%s: отклонено зря: %v", addr, err)
		}
	}
}

// Проверка стоит на самом соединении клиента: имя, разрешившееся в служебный
// адрес (в том числе подменой DNS после проверки), туда не пускает.
// Прокси здесь выключен, чтобы тест не ушёл в сеть ни при каком окружении;
// Control срабатывает до соединения, пакеты не уходят.
func TestFetchClientDialRefusesMetadata(t *testing.T) {
	tr := fetchClient.Transport.(*http.Transport).Clone()
	tr.Proxy = nil
	client := &http.Client{Transport: tr, CheckRedirect: fetchClient.CheckRedirect}
	resp, err := client.Get("http://169.254.169.254:9/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("соединение со служебным адресом не отклонено")
	}
	if !strings.Contains(err.Error(), "закрыт") {
		t.Fatalf("отказ без объяснения: %v", err)
	}
}
