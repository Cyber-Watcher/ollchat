// Команда ollnode — наблюдатель на сервере, где работает Ollama.
//
// Отдаёт по сети то, чего в Ollama API нет и быть не может: чужие процессы
// на видеокарте (обучение чужим скриптом идёт мимо Ollama), температуру карты,
// переменные окружения службы (`OLLAMA_NUM_PARALLEL` определяет число слотов,
// а по сети не виден), журнал службы, место на диске, оперативную память
// и процессор хоста.
//
// **Только чтение.** Список внешних команд зашит в коде, ни одна не принимает
// ничего из запроса. Служба не умеет запускать модели, перезапускать Ollama
// или удалять файлы — не по настройке, а по построению. Это осознанное
// ограничение: наблюдатель, доступный по сети, не должен уметь ничего менять.
//
// Запуск:
//
//	OLLNODE_TOKEN=длинная-случайная-строка ollnode
//	OLLNODE_TOKEN=... ollnode --listen 0.0.0.0:11435 --ollama http://127.0.0.1:11434
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/nodeprobe"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func main() {
	var (
		listen    = flag.String("listen", "127.0.0.1:11435", "адрес и порт службы")
		ollamaURL = flag.String("ollama", "http://127.0.0.1:11434", "адрес Ollama на этой машине")
		service   = flag.String("service", "ollama", "имя юнита systemd")
		modelsDir = flag.String("models-dir", "", "каталог моделей; пусто — из переменных службы")
		cache     = flag.Duration("cache", 5*time.Second, "сколько держать снимок в кэше")
		window    = flag.Duration("journal", 15*time.Minute, "за какое время смотреть журнал")
		samples   = flag.Int("util-samples", 3, "сколько выборок загрузки карты брать")
		gap       = flag.Duration("util-gap", time.Second, "шаг между выборками загрузки")
		useSudo   = flag.Bool("journal-sudo", false, "читать журнал через sudo")
		ours      = flag.String("ours", "", "пути своих процессов на карте через запятую: "+
			"то, что служба Ollama не запускала, но своё (например, контейнер реранкера)")
	)
	flag.Parse()

	// Токен обязателен, и это строже, чем у ollmcp. Наблюдатель рассказывает
	// об устройстве машины, а сервер может стоять за интернетом: служба
	// без проверки подлинности там — приглашение осмотреться.
	token := strings.TrimSpace(os.Getenv("OLLNODE_TOKEN"))
	if token == "" {
		fmt.Fprintln(os.Stderr,
			"ollnode: не задан OLLNODE_TOKEN — служба без проверки подлинности не запускается.\n"+
				"Задайте длинную случайную строку и укажите её же клиенту:\n"+
				"  OLLNODE_TOKEN=$(openssl rand -hex 32) ollnode")
		os.Exit(2)
	}

	client := ollama.New(*ollamaURL, 10*time.Second, 30*time.Second, nil)
	journalCmd := []string{"journalctl"}
	if *useSudo {
		journalCmd = []string{"sudo", "journalctl"}
	}
	var ownProcs []string
	for _, p := range strings.Split(*ours, ",") {
		if p = strings.TrimSpace(p); p != "" {
			ownProcs = append(ownProcs, p)
		}
	}
	base := nodeprobe.Opts{
		Service:       *service,
		OwnProcs:      ownProcs,
		ModelsDir:     *modelsDir,
		UtilSamples:   *samples,
		UtilSampleGap: *gap,
		JournalWindow: *window,
		JournalCmd:    journalCmd,
		PS: func(ctx context.Context) ([]nodeprobe.RunningModel, error) {
			running, err := client.PS(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]nodeprobe.RunningModel, 0, len(running))
			for _, m := range running {
				out = append(out, nodeprobe.RunningModel{Name: m.Name, Size: m.Size,
					SizeVRAM: m.SizeVRAM, ContextLength: m.ContextLength, ExpiresAt: m.ExpiresAt})
			}
			return out, nil
		},
		Version: func(ctx context.Context) (string, error) { return client.Version(ctx) },
	}

	srv := &server{opts: base, ttl: *cache, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/node", srv.node)
	// Проверка живости — без токена и без сбора: ею пользуется systemd
	// и любой, кто хочет знать, отвечает ли служба вообще.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": *service})
	})

	httpSrv := &http.Server{Addr: *listen, Handler: mux,
		ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	fmt.Printf("ollnode слушает %s, Ollama на %s, служба %s\n", *listen, *ollamaURL, *service)
	if !strings.HasPrefix(*listen, "127.") && !strings.HasPrefix(*listen, "localhost") {
		fmt.Println("порт открыт наружу — проверьте, что до него не дотянуться из чужой сети")
	}
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "ollnode:", err)
		os.Exit(1)
	}
}

// server отдаёт снимок, придерживая его в кэше.
//
// Кэш нужен потому, что снимок стоит секунд: серия выборок nvidia-smi идёт
// с шагом в секунду. Пул карт в ollchat спрашивает часто, и без кэша сто
// опросов означали бы сто запусков nvidia-smi на сервере, который и так занят.
type server struct {
	opts  nodeprobe.Opts
	ttl   time.Duration
	token string

	mu    sync.Mutex
	at    time.Time
	last  *nodeprobe.Report
	light bool // каким был последний снимок: дешёвым или полным
}

func (s *server) node(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "нужен заголовок Authorization: Bearer <токен>", http.StatusUnauthorized)
		return
	}
	light := r.URL.Query().Get("light") == "1"
	rep := s.snapshot(r.Context(), light)
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}

// authorized — сравнение токена. Постоянного времени сравнение здесь не нужно:
// токен длинный и случайный, а служба стоит во внутренней сети за туннелем.
func (s *server) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	return h == "Bearer "+s.token
}

func (s *server) snapshot(ctx context.Context, light bool) *nodeprobe.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Дешёвый снимок отдаётся из полного кэша, а полный из дешёвого — нет:
	// иначе запрос за журналом получал бы ответ без журнала.
	if s.last != nil && time.Since(s.at) < s.ttl && (!s.light || light) {
		return s.last
	}
	o := s.opts
	if light {
		o.Want = nodeprobe.Light()
	} else {
		o.Want = nodeprobe.All()
	}
	rep := nodeprobe.Snapshot(ctx, o)
	s.at, s.last, s.light = time.Now(), &rep, light
	return &rep
}
