package vram

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// Математика расчёта видеопамяти.
//
// Занятая видеопамять складывается так:
//
//	VRAM = W + P × C × k + O
//
//	W — вес модели в видеопамяти,
//	C — окно контекста на один слот,
//	P — число параллельных слотов (OLLAMA_NUM_PARALLEL),
//	k — кэш ключей и значений на один токен,
//	O — накладные расходы: буферы вычислений, контекст CUDA, резерв драйвера.
//
// W и O по отдельности из API не видны, но для расчёта это и не нужно: при
// фиксированном P зависимость от C линейна, поэтому по нескольким замерам
// находятся наклон k и свободный член A = W + O. Дальше из бюджета карты
// выводится предельное окно на любое число пользователей.
//
// Почему замер, а не формула из метаданных: k теоретически равен
// 2 × слои × KV-головы × размер головы × байт на элемент, но `/api/show`
// не отдаёт ни числа KV-голов, ни того, что кэш несут не все слои. У
// qwen3.5:122b, например, кэш есть лишь у 12 слоёв из 48 — по метаданным
// это не восстановить, а ошибка вышла бы вчетверо.

// Sample — один замер: модель загружена с окном Context.
//
// Считать надо по показаниям карты, а не по /api/ps. Проверено на стенде:
// gemma4:31b с окном 256k занимает по nvidia-smi 42533 МиБ, а /api/ps в тот же
// момент сообщает 20404 — и это устойчиво, один процесс, сорок секунд подряд.
// Расхождение растёт с окном: Ollama не учитывает буферы вычислений, которые
// на больших окнах и есть основной расход. Расчёт по /api/ps завышал бы число
// пользователей вдвое и больше.
type Sample struct {
	Context int `json:"context"`
	// UsageMiB — сколько реально занято на карте этим экземпляром модели:
	// показание nvidia-smi за вычетом фона (чужих процессов). Именно эта
	// величина идёт в подгонку.
	UsageMiB float64 `json:"usage_mib"`
	// GPUUsedMiB — сырое показание карты, как есть.
	GPUUsedMiB float64 `json:"gpu_used_mib"`
	// PSTotalMiB и PSVRAMMiB — что сообщает /api/ps. Для расхода они непригодны,
	// но по расхождению между ними видно вытеснение в оперативную память.
	PSTotalMiB float64 `json:"ps_total_mib"`
	PSVRAMMiB  float64 `json:"ps_vram_mib"`
}

// Spilled сообщает, что часть модели не поместилась и уехала в оперативную
// память. Такой замер испорчен: он показывает не аппетит модели, а потолок
// карты, и в подгонку его брать нельзя.
//
// Вытеснение видно только по /api/ps: nvidia-smi показывает лишь то, что
// осталось на карте, и по нему недостачу не отличить от экономии.
func (s Sample) Spilled() bool { return s.PSTotalMiB > s.PSVRAMMiB+0.5 }

// Fit — результат подгонки по замерам.
type Fit struct {
	// BaseMiB — свободный член A = W + O: всё, что не зависит от окна.
	BaseMiB float64 `json:"base_mib"`
	// BytesPerToken — наклон k по всем замерам, байт видеопамяти на токен.
	BytesPerToken float64 `json:"bytes_per_token"`
	// TopBytesPerToken — наклон между двумя самыми большими замерами.
	//
	// Он важнее среднего: рост памяти от окна на практике не строго линеен —
	// вместе с кэшем растут буферы вычислений. На qwen3.5:122b чистый кэш даёт
	// 24 КиБ на токен, а полный расход в области 131k–164k — около 58 КиБ.
	// Считать потолок по среднему наклону значит промахнуться в опасную
	// сторону, поэтому предельное окно берётся по наибольшему из двух.
	TopBytesPerToken float64 `json:"top_bytes_per_token"`
	// MaxDeviationMiB — наибольшее расхождение замера с прямой. Показывает,
	// насколько модель линейна на самом деле: большое значение означает, что
	// доверять расчёту нельзя.
	MaxDeviationMiB float64  `json:"max_deviation_mib"`
	Samples         []Sample `json:"samples"`
}

// Slope — наклон, по которому считается предельное окно: осторожный,
// то есть наибольший из среднего и «верхнего».
func (f Fit) Slope() float64 {
	if f.TopBytesPerToken > f.BytesPerToken {
		return f.TopBytesPerToken
	}
	return f.BytesPerToken
}

// Linear сообщает, можно ли доверять линейной модели: наклоны по всем точкам
// и по верхним двум не должны расходиться больше чем в полтора раза.
func (f Fit) Linear() bool {
	if f.BytesPerToken <= 0 || f.TopBytesPerToken <= 0 {
		return false
	}
	ratio := f.TopBytesPerToken / f.BytesPerToken
	return ratio > 1/1.5 && ratio < 1.5
}

// FitSamples подбирает k и A методом наименьших квадратов.
//
// Нужно минимум два замера с разными окнами: одно уравнение не разделяет вес
// модели и кэш контекста. Замеры с вытеснением отбрасываются — на них
// зависимость уже не линейна.
func FitSamples(samples []Sample) (Fit, error) {
	clean := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.Context > 0 && !s.Spilled() {
			clean = append(clean, s)
		}
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].Context < clean[j].Context })

	if len(clean) < 2 {
		return Fit{}, errors.New("нужно минимум два замера без вытеснения и с разными окнами")
	}
	if clean[0].Context == clean[len(clean)-1].Context {
		return Fit{}, errors.New("все замеры сделаны с одним и тем же окном — наклон не найти")
	}

	var sumX, sumY, sumXY, sumXX float64
	n := float64(len(clean))
	for _, s := range clean {
		x := float64(s.Context)
		y := s.UsageMiB
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return Fit{}, errors.New("замеры вырождены: окна совпадают")
	}
	slopeMiB := (n*sumXY - sumX*sumY) / denom // МиБ на токен
	base := (sumY - slopeMiB*sumX) / n

	if slopeMiB <= 0 {
		return Fit{}, fmt.Errorf("наклон вышел неположительным (%.6f МиБ/токен): "+
			"замеры противоречивы, память не растёт с окном", slopeMiB)
	}

	fit := Fit{
		BaseMiB:       base,
		BytesPerToken: slopeMiB * 1024 * 1024,
		Samples:       clean,
	}
	// Наклон на верхнем участке — там, где предстоит работать.
	last, prev := clean[len(clean)-1], clean[len(clean)-2]
	if dx := float64(last.Context - prev.Context); dx > 0 {
		fit.TopBytesPerToken = (last.UsageMiB - prev.UsageMiB) / dx * 1024 * 1024
	}
	for _, s := range clean {
		predicted := base + slopeMiB*float64(s.Context)
		if d := abs(s.UsageMiB - predicted); d > fit.MaxDeviationMiB {
			fit.MaxDeviationMiB = d
		}
	}
	return fit, nil
}

// Predict возвращает ожидаемый объём видеопамяти для окна ctx и users слотов.
// Считает по осторожному наклону — тому же, по которому берётся потолок.
func (f Fit) Predict(ctx, users int) float64 {
	if users < 1 {
		users = 1
	}
	return f.BaseMiB + float64(users)*float64(ctx)*f.Slope()/(1024*1024)
}

// ContextMax — наибольшее окно на один слот, при котором users слотов ещё
// помещаются в бюджет. Результат округляется вниз до кратного 1024 (Ollama
// всё равно выравнивает окно) и ограничивается максимумом модели.
func (f Fit) ContextMax(budgetMiB float64, users, modelMax int) int {
	if users < 1 {
		users = 1
	}
	perToken := float64(users) * f.Slope() / (1024 * 1024) // МиБ на токен окна
	if perToken <= 0 {
		return 0
	}
	free := budgetMiB - f.BaseMiB
	if free <= 0 {
		return 0
	}
	ctx := int(free / perToken)
	ctx = ctx / 1024 * 1024
	if modelMax > 0 && ctx > modelMax {
		ctx = modelMax
	}
	if ctx < 0 {
		ctx = 0
	}
	return ctx
}

// UsersMax — сколько слотов с окном ctx поместится в бюджет.
func (f Fit) UsersMax(budgetMiB float64, ctx int) int {
	if ctx <= 0 {
		return 0
	}
	perUser := float64(ctx) * f.Slope() / (1024 * 1024)
	if perUser <= 0 {
		return 0
	}
	free := budgetMiB - f.BaseMiB
	if free <= 0 {
		return 0
	}
	return int(free / perUser)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// Profile — то, что инструмент оставляет после себя. Этот же файл читает
// команда /calc в ollchat, поэтому формат менять надо в обоих местах разом.
// ProfileFormat — версия формата профиля.
//
// Версия 1 считала расход по /api/ps и занижала его в разы; числа из таких
// профилей нельзя показывать как замеры. Версия 2 считает по nvidia-smi.
const ProfileFormat = 2

type Profile struct {
	Format      int           `json:"format"`
	GeneratedAt string        `json:"generated_at"`
	Host        string        `json:"host"`
	OllamaURL   string        `json:"ollama_url"`
	Parallel    int           `json:"parallel"`
	GPU         GPU           `json:"gpu"`
	Models      []ModelResult `json:"models"`
}

type ModelResult struct {
	// FullContextUsers — сколько человек одновременно потянут полное окно
	// модели. Ноль означает, что не потянет ни один.
	FullContextUsers int `json:"full_context_users"`
	// VerifiedMaxContext — наибольшее окно, при котором вытеснения не было,
	// подтверждённое загрузкой. Это надёжнее расчёта: рост памяти от окна
	// линеен только внутри измеренного диапазона, а выше идёт круче.
	VerifiedMaxContext int         `json:"verified_max_context,omitempty"`
	Name               string      `json:"name"`
	FileSizeMiB        float64     `json:"file_size_mib"`
	MaxContext         int         `json:"max_context"`
	Vision             bool        `json:"vision"`
	Samples            []Sample    `json:"-"` // лежат внутри Fit
	Fit                Fit         `json:"fit"`
	Budget             Budget      `json:"budget"`
	Users              []UserLimit `json:"users"`
}

type Budget struct {
	TotalMiB    float64 `json:"total_mib"`
	OverheadMiB float64 `json:"overhead_mib"`
	ReserveMiB  float64 `json:"reserve_mib"`
	UsableMiB   float64 `json:"usable_mib"`
	Source      string  `json:"source"`
}

type UserLimit struct {
	Users      int `json:"users"`
	ContextMax int `json:"context_max"`
}

func WriteProfile(path string, p Profile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// GPU — состояние видеокарты на момент замера.
type GPU struct {
	Name      string  `json:"name"`
	TotalMiB  float64 `json:"total_mib"`
	UsedMiB   float64 `json:"used_mib"`
	Available bool    `json:"available"`
}

// LoadProfile читает профиль, оставленный olldiagtools.
//
// Отсутствие файла — не ошибка: ollchat умеет считать и без замеров, просто
// грубо. Поэтому вызывающий отличает «нет файла» от «файл испорчен».
func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("разбор профиля %s: %w", path, err)
	}
	return &p, nil
}

// Outdated сообщает, что профиль снят старой версией инструмента и его числа
// доверия не заслуживают.
func (p *Profile) Outdated() bool {
	return p != nil && p.Format < ProfileFormat
}

// Model находит в профиле замеры нужной модели.
func (p *Profile) Model(name string) (ModelResult, bool) {
	if p == nil {
		return ModelResult{}, false
	}
	for _, m := range p.Models {
		if m.Name == name {
			return m, true
		}
	}
	return ModelResult{}, false
}

// MaxMeasuredContext — наибольшее окно среди удачных замеров.
// За его пределами расчёт превращается в экстраполяцию, а она уже подводила:
// на qwen3.5:122b зависимость линейна до 128k и заметно круче выше.
func (f Fit) MaxMeasuredContext() int {
	max := 0
	for _, s := range f.Samples {
		if s.Context > max {
			max = s.Context
		}
	}
	return max
}

// ContextForUsers — окно контекста на каждого из users. Это главный ответ,
// который даёт весь инструмент.
//
// Ключевая тонкость, на которой легко ошибиться: **делить потолок на число
// пользователей можно только тогда, когда потолок задан памятью.** Потолок
// для одного слота может упереться и в максимум самой модели — тогда память
// вообще не при чём, и делить нечего. У gemma4:12b именно так: модель занимает
// около 8 ГиБ на карте в 80, её максимум 256k, и восьмерым спокойно хватит
// по 256k каждому (это меньше 28 ГиБ на всех). Ранняя версия делила потолок
// всегда и выдавала им по 32k — вчетверо меньше, чем есть на самом деле.
//
// Поэтому:
//   - потолок уперся в память → она и есть предел, считаем от него;
//   - потолок уперся в максимум модели → считаем от бюджета карты.
//
// verified — потолок для одного слота, подтверждённый загрузкой (0 — нет).
func (f Fit) ContextForUsers(budgetMiB float64, users, modelMax, verified int) int {
	if users < 1 {
		users = 1
	}

	// Потолок, заданный памятью, надёжнее любой прямой: выше него расчёт
	// уже подводил, там растут ещё и буферы вычислений.
	usableMiB, perTokenMiB := f.basis(budgetMiB, modelMax, verified)

	if perTokenMiB <= 0 {
		return 0
	}
	free := usableMiB - f.BaseMiB
	if free <= 0 {
		return 0
	}
	ctx := int(free/(float64(users)*perTokenMiB)) / 1024 * 1024
	if modelMax > 0 && ctx > modelMax {
		ctx = modelMax
	}
	if ctx < 0 {
		ctx = 0
	}
	return ctx
}

// totalAt возвращает занятую память в точке замера с указанным окном.
func (f Fit) totalAt(ctx int) (float64, bool) {
	for _, s := range f.Samples {
		if s.Context == ctx && !s.Spilled() {
			return s.UsageMiB, true
		}
	}
	return 0, false
}

// UsersAtFullContext — сколько человек могут одновременно работать с полным
// окном модели.
//
// Это самый частый практический вопрос: «мы хотим максимум контекста, сколько
// нас поместится». Ноль — законный ответ: у больших моделей полное окно не
// вытягивает даже один пользователь, и тогда важно сказать об этом прямо,
// а не показывать пустую таблицу.
func (f Fit) UsersAtFullContext(budgetMiB float64, modelMax, verified int) int {
	if modelMax <= 0 {
		return 0
	}
	usableMiB, perTokenMiB := f.basis(budgetMiB, modelMax, verified)
	if perTokenMiB <= 0 {
		return 0
	}
	free := usableMiB - f.BaseMiB
	if free <= 0 {
		return 0
	}
	perUser := float64(modelMax) * perTokenMiB
	if perUser <= 0 {
		return 0
	}
	return int(free / perUser)
}

// basis выбирает, от чего считать: от бюджета карты или от проверенного
// потолка. Разница принципиальна — см. ContextForUsers.
func (f Fit) basis(budgetMiB float64, modelMax, verified int) (usableMiB, perTokenMiB float64) {
	perTokenMiB = f.Slope() / (1024 * 1024)
	usableMiB = budgetMiB

	if verified > 0 && (modelMax == 0 || verified < modelMax) {
		if total, ok := f.totalAt(verified); ok {
			usableMiB = total
			perTokenMiB = (total - f.BaseMiB) / float64(verified)
		}
	}
	return usableMiB, perTokenMiB
}

// LimitedByModel сообщает, что предел ставит сама модель, а не видеопамять.
func LimitedByModel(verified, modelMax int) bool {
	return verified > 0 && modelMax > 0 && verified >= modelMax
}
