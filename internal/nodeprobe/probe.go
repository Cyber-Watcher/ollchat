// Package nodeprobe снимает состояние машины, на которой работает Ollama:
// видеокарта, служба, загруженные модели, память и процессор хоста, диск,
// журнал.
//
// **Зачем отдельный пакет.** Этими же данными живут трое: ночной прогон
// `olleval` (начинать ли работу), служба `ollnode` на сервере (отдать по сети)
// и сам `ollchat` (идти ли сборке графа и на какие карты её раздавать). Раньше
// сбор был заперт внутри `olleval/guard.go` вместе с решениями, которые
// по нему принимаются, — а решения у всех троих разные, факты же одни.
//
// **Здесь только факты.** Пакет не отвечает на вопрос «свободна ли карта»:
// у ночного прогона порог занятости один, у сборки графа другой, и заводить
// общий значило бы решать за обоих. Пороги остаются у вызывающего.
//
// **Честное «не знаю» вместо нулей.** Нет `nvidia-smi`, нет прав на журнал,
// карта не NVIDIA — раздел приходит пустым, а причина попадает в Missing.
// Ноль в поле «занято памяти» неотличим от «карта свободна», и на таком нуле
// строятся неверные решения.
package nodeprobe

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner запускает внешнюю команду и возвращает её вывод, код возврата
// и ошибку. Подменяется в тестах: снимок должен проверяться без видеокарты.
type Runner func(ctx context.Context, name string, args ...string) (string, int, error)

// Opts — что и как собирать.
type Opts struct {
	// Run — чем запускать внешние команды. nil — обычный exec.
	Run Runner

	// PS и Version — как спросить сам Ollama. nil — не спрашивать.
	PS      func(ctx context.Context) ([]RunningModel, error)
	Version func(ctx context.Context) (string, error)

	// Service — имя юнита systemd. Пусто — «ollama».
	Service string

	// Want — какие разделы собирать. Нулевое значение не собирает ничего:
	// вызывающий обязан сказать, что ему нужно, — снимок стоит секунд.
	Want Sections

	// UtilSamples и UtilSampleGap — сколько выборок загрузки карты брать
	// и с каким шагом. Одна выборка nvidia-smi — мгновенный снимок: между
	// двумя токенами чужого ответа загрузка падает в ноль, и единственный
	// замер сказал бы «свободно» посреди чужой работы.
	UtilSamples   int
	UtilSampleGap time.Duration

	// JournalWindow — за какое время смотреть журнал службы.
	JournalWindow time.Duration
	// JournalCmd — чем читать журнал. Пусто — {"journalctl"}. Ночному прогону
	// на стенде нужен sudo, службе на сервере — членство в systemd-journal.
	JournalCmd []string

	// SessionsCmd — чем смотреть чужие сеансы. Пусто — {"w", "-h"}.
	SessionsCmd []string

	// ModelsDir — каталог моделей для проверки места. Пусто — взять
	// из переменных окружения службы, а если и там нет — не проверять.
	ModelsDir string

	// OwnProcs — пути своих процессов на карте, которые не опознаются
	// родством со службой Ollama: наш реранкер поднят контейнером и службе
	// не родня (см. own.go). Подстроки пути; пусто — только родство.
	OwnProcs []string
}

// Sections — какие разделы снимка нужны.
type Sections struct {
	GPU      bool // карта и процессы на ней
	Service  bool // состояние службы и её переменные окружения
	Models   bool // что загружено в память сервера (/api/ps)
	Host     bool // оперативная память, процессор, процесс Ollama
	Disk     bool // место под моделями
	Journal  bool // журнал службы
	Sessions bool // чужие сеансы в системе
}

// All — собрать всё.
func All() Sections {
	return Sections{GPU: true, Service: true, Models: true, Host: true,
		Disk: true, Journal: true, Sessions: true}
}

// Light — дешёвая часть: без журнала и диска. Ею опрашивают часто.
func Light() Sections {
	return Sections{GPU: true, Service: true, Models: true, Host: true}
}

// RunningModel — модель, загруженная в память сервера. Повторяет нужные поля
// ollama.RunningModel, чтобы пакет не зависел от клиента: снимок снимается
// и там, где клиента нет.
type RunningModel struct {
	Name          string
	Size          int64
	SizeVRAM      int64
	ContextLength int
	ExpiresAt     string
}

// GPU — состояние одной видеокарты.
type GPU struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	MemTotal int    `json:"mem_total_mib"`
	MemUsed  int    `json:"mem_used_mib"`
	MemFree  int    `json:"mem_free_mib"`

	// Util — наибольшая выборка загрузки, Samples — все выборки серии.
	// Наибольшая, а не средняя: нам важно «работала ли карта вообще»,
	// а не сколько она работала в среднем.
	Util    int   `json:"util_pct"`
	Samples []int `json:"util_samples,omitempty"`

	TempC    int     `json:"temp_c,omitempty"`
	PowerW   float64 `json:"power_w,omitempty"`
	Throttle string  `json:"throttle,omitempty"`
}

// GPUProc — процесс, держащий память на карте. Главная причина, по которой
// наблюдатель ставится на сервер: обучение чужим скриптом идёт мимо Ollama,
// и по сети его не видно никак.
type GPUProc struct {
	PID     int    `json:"pid"`
	Name    string `json:"name,omitempty"`
	User    string `json:"user,omitempty"`
	UsedMiB int    `json:"used_mib"`
	// Ours — процесс принадлежит службе Ollama, а не постороннему.
	Ours bool `json:"ours"`
}

// Service — состояние службы Ollama.
type Service struct {
	Name        string            `json:"name"`
	State       string            `json:"state,omitempty"` // active, inactive, failed
	MainPID     int               `json:"main_pid,omitempty"`
	ActiveSince string            `json:"active_since,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// Slots — сколько запросов служба обрабатывает разом по своим настройкам.
// Ноль — переменная не задана, то есть умолчание самой Ollama.
//
// По сети это не видно вовсе, а от этого числа зависит, сколько слотов имеет
// смысл давать узлу при сборке графа.
func (s Service) Slots() int {
	n, _ := strconv.Atoi(s.Env["OLLAMA_NUM_PARALLEL"])
	return n
}

// Model — загруженная модель с разложением памяти.
type Model struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	SizeRAM       int64  `json:"size_ram"` // сколько ушло в оперативную память
	VRAMPct       int    `json:"vram_pct"`
	ContextLength int    `json:"context_length,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// Evicted — часть слоёв считается на процессоре.
//
// Ollama, которой не хватило видеопамяти, не отказывает и не ругается: она
// молча кладёт часть слоёв в оперативную память. Снаружи это выглядит как
// «работает, только медленно» — запрос идёт в десять-двадцать раз дольше,
// а карта показывает почти нулевую загрузку и кажется свободной.
// Порог 90%, как в ollama.Residency.
func (m Model) Evicted() bool { return m.Size > 0 && m.VRAMPct < 90 }

// Host — память и процессор машины.
type Host struct {
	RAMTotalMiB int        `json:"ram_total_mib"`
	RAMFreeMiB  int        `json:"ram_free_mib"` // MemAvailable, а не MemFree
	SwapUsedMiB int        `json:"swap_used_mib"`
	LoadAvg     [3]float64 `json:"load_avg"`
	Cores       int        `json:"cores"`
	CPUPct      int        `json:"cpu_pct,omitempty"` // за время серии выборок
}

// Proc — процесс службы Ollama: сколько он держит оперативной памяти.
type Proc struct {
	PID     int `json:"pid"`
	RSSMiB  int `json:"rss_mib"`
	Threads int `json:"threads,omitempty"`
}

// Disk — место под каталогом моделей.
type Disk struct {
	Path     string `json:"path"`
	TotalMiB int64  `json:"total_mib"`
	FreeMiB  int64  `json:"free_mib"`
}

// JournalLine — строка журнала, попавшая под известную беду.
type JournalLine struct {
	Kind string `json:"kind"` // parallel, cuda, oom, unload
	Text string `json:"text"`
}

// Missing — раздел, который собрать не удалось, и почему.
type Missing struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

// Report — снимок состояния машины.
type Report struct {
	Time time.Time `json:"time"`
	// UptimeSec — в секундах, а не time.Duration: наносекунды в JSON читает
	// только машина, а этот снимок смотрят и глазами.
	UptimeSec     int64  `json:"uptime_sec,omitempty"`
	OllamaVersion string `json:"ollama_version,omitempty"`

	GPUs     []GPU         `json:"gpus,omitempty"`
	GPUProcs []GPUProc     `json:"gpu_procs,omitempty"`
	Service  Service       `json:"service"`
	Models   []Model       `json:"models,omitempty"`
	Host     *Host         `json:"host,omitempty"`
	Ollama   *Proc         `json:"ollama_proc,omitempty"`
	Disk     *Disk         `json:"disk,omitempty"`
	Journal  []JournalLine `json:"journal,omitempty"`
	// Requests — сколько запросов `POST /api/` пришло к службе за окно
	// журнала. Сами строки о запросах в Journal не идут: их тысячи, а нужно
	// от них одно число — была ли у службы чужая работа только что.
	Requests int      `json:"requests,omitempty"`
	Sessions []string `json:"sessions,omitempty"`

	// Missing — что не удалось собрать. Пустой раздел без причины —
	// это утверждение «там ничего нет», и оно должно быть правдой.
	Missing []Missing `json:"missing,omitempty"`
}

// Has — собран ли раздел (не попал ли он в Missing).
func (r *Report) Has(section string) bool {
	for _, m := range r.Missing {
		if m.Section == section {
			return false
		}
	}
	return true
}

// Evicted — есть ли модель, часть которой считается на процессоре.
func (r *Report) Evicted() []Model {
	var out []Model
	for _, m := range r.Models {
		if m.Evicted() {
			out = append(out, m)
		}
	}
	return out
}

// Foreign — процессы на карте, не принадлежащие службе Ollama.
func (r *Report) Foreign() []GPUProc {
	var out []GPUProc
	for _, p := range r.GPUProcs {
		if !p.Ours {
			out = append(out, p)
		}
	}
	return out
}

// GPUUsedMiB и GPUUtil — наибольшие по всем картам. Нужны тем, кто спрашивает
// «занята ли машина», не разбираясь, какой именно картой.
func (r *Report) GPUUsedMiB() int {
	n := 0
	for _, g := range r.GPUs {
		if g.MemUsed > n {
			n = g.MemUsed
		}
	}
	return n
}

func (r *Report) GPUUtil() int {
	n := 0
	for _, g := range r.GPUs {
		if g.Util > n {
			n = g.Util
		}
	}
	return n
}

// apiRequest — строка журнала службы о запросе к модели.
//
// Настоящая строка GIN выглядит так:
//
//	[GIN] 2026/08/22 - 17:29:47 | 200 | 2m12s | 127.0.0.1 | POST     "/api/chat"
//
// — несколько пробелов и кавычка. До 17.09.2026 искалась подстрока
// «POST /api/», которой в такой строке нет: сторож всегда видел ноль запросов,
// а тест проверял выдуманный формат.
var apiRequest = regexp.MustCompile(`POST\s+"?/api/`)

// oomWord — «oom» целым словом: подстрока совпадала с bloom и room (аудит, Б17).
var oomWord = regexp.MustCompile(`(^|[^a-z])oom([^a-z]|$)`)

// Snapshot снимает состояние машины по указанным разделам.
//
// Ошибки не возвращаются: снимок — это то, что удалось увидеть, а неудача
// раздела описывается в Missing. Вызывающий получает ответ всегда, и решает
// сам, достаточно ли ему увиденного.
func Snapshot(ctx context.Context, o Opts) Report {
	if o.Run == nil {
		o.Run = execRun
	}
	if o.Service == "" {
		o.Service = "ollama"
	}
	rep := Report{Time: time.Now(), Service: Service{Name: o.Service}}

	if o.Want.GPU {
		rep.collectGPU(ctx, o)
	}
	if o.Want.Service {
		rep.collectService(ctx, o)
		// Свои процессы на карте опознаются по родству с этой службой: имя
		// программы для этого негодно — счётчики моделей Ollama называются
		// llama-server и выглядели чужими (own.go, 12.09.2026).
		rep.markOwnGPUProcs(o.OwnProcs)
	}
	if o.Want.Host {
		rep.collectHost(ctx, o)
	}
	if o.Want.Models {
		rep.collectModels(ctx, o)
	}
	if o.Want.Disk {
		rep.collectDisk(ctx, o)
	}
	if o.Want.Journal {
		rep.collectJournal(ctx, o)
	}
	if o.Want.Sessions {
		rep.collectSessions(ctx, o)
	}
	if o.Version != nil {
		if v, err := o.Version(ctx); err == nil {
			rep.OllamaVersion = v
		}
	}
	if up, err := uptime(); err == nil {
		rep.UptimeSec = int64(up.Seconds())
	}
	return rep
}

func (r *Report) miss(section, format string, args ...any) {
	r.Missing = append(r.Missing, Missing{Section: section, Reason: fmt.Sprintf(format, args...)})
}

// collectModels раскладывает /api/ps на видеопамять и оперативную.
func (r *Report) collectModels(ctx context.Context, o Opts) {
	if o.PS == nil {
		r.miss("models", "спросить /api/ps нечем")
		return
	}
	running, err := o.PS(ctx)
	if err != nil {
		r.miss("models", "/api/ps не ответил: %v", err)
		return
	}
	for _, m := range running {
		mm := Model{Name: m.Name, Size: m.Size, SizeVRAM: m.SizeVRAM,
			ContextLength: m.ContextLength, ExpiresAt: m.ExpiresAt}
		if m.Size > 0 {
			mm.SizeRAM = m.Size - m.SizeVRAM
			if mm.SizeRAM < 0 {
				mm.SizeRAM = 0
			}
			mm.VRAMPct = int(m.SizeVRAM * 100 / m.Size)
		} else {
			mm.VRAMPct = 100
		}
		r.Models = append(r.Models, mm)
	}
}

// collectJournal читает журнал службы и отбирает известные беды.
//
// Отбор, а не весь журнал: за пятнадцать минут работы Ollama пишет тысячи
// строк о запросах, и отдавать их по сети незачем. Ищется то, что уже стоило
// нам времени: отказ архитектуре в параллельности, ошибки CUDA, нехватка
// памяти, выгрузка модели.
func (r *Report) collectJournal(ctx context.Context, o Opts) {
	window := o.JournalWindow
	if window <= 0 {
		window = 15 * time.Minute
	}
	cmd := o.JournalCmd
	if len(cmd) == 0 {
		cmd = []string{"journalctl"}
	}
	args := append([]string{}, cmd[1:]...)
	args = append(args, "-u", o.Service, "--since",
		"-"+strconv.Itoa(int(window.Minutes()))+" min", "--no-pager", "-q")
	out, _, err := o.Run(ctx, cmd[0], args...)
	if err != nil {
		r.miss("journal", "журнал недоступен: %v", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if apiRequest.MatchString(line) {
			r.Requests++
		}
		if kind := journalKind(line); kind != "" {
			r.Journal = append(r.Journal, JournalLine{Kind: kind, Text: line})
		}
	}
}

// journalKind распознаёт известную беду в строке журнала. Пусто — строка
// обычная и хранить её незачем.
func journalKind(line string) string {
	low := strings.ToLower(line)
	switch {
	case strings.Contains(low, "does not currently support parallel requests"),
		strings.Contains(low, "does not support parallel"):
		return "parallel"
	case strings.Contains(low, "cuda error"), strings.Contains(low, "cudamalloc"):
		return "cuda"
	case strings.Contains(low, "out of memory"), oomWord.MatchString(low):
		return "oom"
	case strings.Contains(low, "unloading model"), strings.Contains(low, "evicting"):
		return "unload"
	}
	return ""
}

// collectSessions смотрит, кто ещё работает в системе. Не запрет, а сведение:
// человек за стендом — причина не начинать долгую работу молча.
func (r *Report) collectSessions(ctx context.Context, o Opts) {
	cmd := o.SessionsCmd
	if len(cmd) == 0 {
		cmd = []string{"w", "-h"}
	}
	out, _, err := o.Run(ctx, cmd[0], cmd[1:]...)
	if err != nil {
		r.miss("sessions", "не удалось спросить о сеансах: %v", err)
		return
	}
	me := currentUser()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == me {
			continue
		}
		r.Sessions = append(r.Sessions, line)
	}
}

// currentUser — под кем идёт процесс. Сначала окружение, затем учётная
// запись по uid: под systemd-таймером и cron переменные USER и LOGNAME
// бывают не заданы, и без запасного пути свои сеансы считались бы чужими.
func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
