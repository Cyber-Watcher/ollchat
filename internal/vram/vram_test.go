package vram

import (
	"math"
	"testing"
)

// Числа взяты с настоящего стенда: A100 80 ГиБ, qwen3.5:122b (Q4_K_M).
// Замеры получены через /api/ps, а разбор кэша подтверждён журналом Ollama
// (llama_kv_cache: size = 3072.00 MiB при 131072 ячейках).
var standSamples = []Sample{
	{Context: 131072, UsageMiB: 78674, PSTotalMiB: 78674, PSVRAMMiB: 78674},
	{Context: 163840, UsageMiB: 80527, PSTotalMiB: 80527, PSVRAMMiB: 78848}, // здесь уже вытеснение
}

func TestFitNeedsTwoCleanSamples(t *testing.T) {
	if _, err := FitSamples(standSamples); err == nil {
		t.Error("замер с вытеснением надо отбрасывать, а без него точек не хватает")
	}
	if _, err := FitSamples(nil); err == nil {
		t.Error("без замеров подгонка невозможна")
	}
	one := []Sample{{Context: 32768, UsageMiB: 76122, PSTotalMiB: 76122, PSVRAMMiB: 76122}}
	if _, err := FitSamples(one); err == nil {
		t.Error("по одной точке вес модели и кэш не разделить")
	}
}

// Ровная линия: восстанавливаем заложенные A и k.
func TestFitRecoversKnownLine(t *testing.T) {
	const (
		baseMiB       = 75602.0
		bytesPerToken = 24576.0 // 24 КиБ — столько даёт кэш qwen3.5:122b
	)
	perTokenMiB := bytesPerToken / (1024 * 1024)

	var samples []Sample
	for _, c := range []int{32768, 65536, 98304, 131072} {
		total := baseMiB + perTokenMiB*float64(c)
		samples = append(samples, Sample{Context: c, UsageMiB: total, PSTotalMiB: total, PSVRAMMiB: total})
	}

	fit, err := FitSamples(samples)
	if err != nil {
		t.Fatalf("подгонка: %v", err)
	}
	if math.Abs(fit.BaseMiB-baseMiB) > 1 {
		t.Errorf("A = %.0f МиБ, ожидалось %.0f", fit.BaseMiB, baseMiB)
	}
	if math.Abs(fit.BytesPerToken-bytesPerToken) > 16 {
		t.Errorf("k = %.0f байт, ожидалось %.0f", fit.BytesPerToken, bytesPerToken)
	}
	if fit.MaxDeviationMiB > 1 {
		t.Errorf("на идеальной прямой отклонение должно быть нулевым, получено %.1f", fit.MaxDeviationMiB)
	}
	if !fit.Linear() {
		t.Error("идеальная прямая должна признаваться линейной")
	}
}

// Настоящая зависимость с ростом окна загибается вверх: вместе с кэшем растут
// буферы вычислений. Считать потолок по среднему наклону опасно — проверяем,
// что берётся верхний.
func TestFitPrefersSteeperTopSlope(t *testing.T) {
	samples := []Sample{
		{Context: 32768, UsageMiB: 76122, PSTotalMiB: 76122, PSVRAMMiB: 76122},
		{Context: 65536, UsageMiB: 76890, PSTotalMiB: 76890, PSVRAMMiB: 76890},
		{Context: 131072, UsageMiB: 78674, PSTotalMiB: 78674, PSVRAMMiB: 78674},
	}
	fit, err := FitSamples(samples)
	if err != nil {
		t.Fatalf("подгонка: %v", err)
	}
	if fit.TopBytesPerToken <= fit.BytesPerToken {
		t.Fatalf("на загибающейся кривой верхний наклон должен быть круче: верхний %.0f, средний %.0f",
			fit.TopBytesPerToken, fit.BytesPerToken)
	}
	if got := fit.Slope(); got != fit.TopBytesPerToken {
		t.Errorf("для потолка должен браться верхний наклон, взят %.0f", got)
	}

	// По осторожному наклону потолок ниже, чем по среднему, — это и требуется.
	const budget = 80000.0
	cautious := fit.ContextMax(budget, 1, 262144)
	optimistic := int((budget - fit.BaseMiB) / (fit.BytesPerToken / (1024 * 1024)))
	if cautious >= optimistic {
		t.Errorf("осторожный потолок %d должен быть ниже оптимистичного %d", cautious, optimistic)
	}
}

func TestContextMaxScalesInverselyWithUsers(t *testing.T) {
	fit := Fit{BaseMiB: 70000, BytesPerToken: 24576, TopBytesPerToken: 24576}
	const budget = 79000.0

	one := fit.ContextMax(budget, 1, 0)
	two := fit.ContextMax(budget, 2, 0)
	four := fit.ContextMax(budget, 4, 0)

	if one <= 0 {
		t.Fatal("для одного пользователя окно должно быть положительным")
	}
	// Обратная пропорция: вдвое больше пользователей — вдвое меньше окно.
	if d := math.Abs(float64(two) - float64(one)/2); d > 2048 {
		t.Errorf("для двоих ожидалась половина от %d, получено %d", one, two)
	}
	if d := math.Abs(float64(four) - float64(one)/4); d > 2048 {
		t.Errorf("для четверых ожидалась четверть от %d, получено %d", one, four)
	}
}

// Потолок не может превышать окно, на которое модель обучена.
func TestContextMaxRespectsModelMaximum(t *testing.T) {
	fit := Fit{BaseMiB: 4000, BytesPerToken: 1024, TopBytesPerToken: 1024}
	got := fit.ContextMax(80000, 1, 8192)
	if got != 8192 {
		t.Errorf("окно = %d, а модель больше 8192 не умеет", got)
	}
}

// Модель, которая не влезает даже пустой, честно даёт ноль, а не отрицательное.
func TestContextMaxZeroWhenModelDoesNotFit(t *testing.T) {
	fit := Fit{BaseMiB: 90000, BytesPerToken: 24576, TopBytesPerToken: 24576}
	if got := fit.ContextMax(80000, 1, 262144); got != 0 {
		t.Errorf("окно = %d, ожидался 0: модель не помещается и без контекста", got)
	}
}

func TestUsersMax(t *testing.T) {
	fit := Fit{BaseMiB: 70000, BytesPerToken: 24576, TopBytesPerToken: 24576}
	// Свободно 9000 МиБ, окно 32768 стоит 0.75 ГиБ = 768 МиБ на слот.
	if got := fit.UsersMax(79000, 32768); got != 11 {
		t.Errorf("пользователей = %d, ожидалось 11", got)
	}
	if got := fit.UsersMax(79000, 0); got != 0 {
		t.Errorf("при нулевом окне ответ должен быть 0, получено %d", got)
	}
}

func TestSpilledDetectsPartialOffload(t *testing.T) {
	if (Sample{UsageMiB: 100, PSTotalMiB: 100, PSVRAMMiB: 100}).Spilled() {
		t.Error("целиком размещённая модель не вытеснена")
	}
	if !(Sample{UsageMiB: 100, PSTotalMiB: 100, PSVRAMMiB: 98}).Spilled() {
		t.Error("расхождение размеров означает вытеснение")
	}
}

// Случай, на котором ранняя версия ошибалась вчетверо.
//
// gemma4:12b занимает около 8 ГиБ на карте в 80, а её максимум окна — 256k.
// Потолок для одного слота упирается в максимум модели, а не в память,
// поэтому делить его между пользователями нельзя: восьмерым спокойно хватит
// по 256k каждому. Ранняя версия делила всегда и выдавала им по 32k.
func TestContextForUsersDoesNotDivideWhenModelIsTheLimit(t *testing.T) {
	const (
		modelMax = 262144
		budget   = 78580.0 // МиБ, замер стенда
	)
	fit := Fit{
		BaseMiB:          7905,
		BytesPerToken:    9893,
		TopBytesPerToken: 4096,
		Samples: []Sample{
			{Context: 32768, UsageMiB: 8033, PSTotalMiB: 8033, PSVRAMMiB: 8033},
			{Context: 131072, UsageMiB: 9051, PSTotalMiB: 9051, PSVRAMMiB: 9051},
			{Context: 262144, UsageMiB: 9563, PSTotalMiB: 9563, PSVRAMMiB: 9563},
		},
	}

	for _, users := range []int{1, 2, 4, 8} {
		got := fit.ContextForUsers(budget, users, modelMax, modelMax)
		if got != modelMax {
			t.Errorf("для %d пользователей окно = %d, а памяти хватает на полные %d",
				users, got, modelMax)
		}
	}

	// И проверим по-честному: восемь слотов по 256k действительно влезают.
	if used := fit.Predict(modelMax, 8); used > budget {
		t.Errorf("прогноз %0.f МиБ превышает бюджет %.0f — тогда и окно должно быть меньше",
			used, budget)
	}
	if !LimitedByModel(modelMax, modelMax) {
		t.Error("предел должен признаваться максимумом модели, а не памятью")
	}
}

// Обратный случай: потолок задан памятью — вот тут делить обязательно.
func TestContextForUsersDividesWhenMemoryIsTheLimit(t *testing.T) {
	const (
		modelMax = 262144
		verified = 136192 // проверено загрузкой на стенде
		budget   = 79855.0
	)
	fit := Fit{
		BaseMiB:          75472,
		BytesPerToken:    25600,
		TopBytesPerToken: 25600,
		Samples: []Sample{
			{Context: 32768, UsageMiB: 76272, PSTotalMiB: 76272, PSVRAMMiB: 76272},
			{Context: 131072, UsageMiB: 78672, PSTotalMiB: 78672, PSVRAMMiB: 78672},
			{Context: verified, UsageMiB: 78797, PSTotalMiB: 78797, PSVRAMMiB: 78797},
		},
	}

	if got := fit.ContextForUsers(budget, 1, modelMax, verified); got != verified {
		t.Errorf("для одного окно = %d, ожидался проверенный потолок %d", got, verified)
	}
	if got := fit.ContextForUsers(budget, 2, modelMax, verified); got != verified/2/1024*1024 {
		t.Errorf("для двоих окно = %d, ожидалась половина потолка", got)
	}
	if got := fit.ContextForUsers(budget, 8, modelMax, verified); got != verified/8/1024*1024 {
		t.Errorf("для восьмерых окно = %d, ожидалась восьмая часть потолка", got)
	}
	if LimitedByModel(verified, modelMax) {
		t.Error("предел задан памятью, а не максимумом модели")
	}
}

// Промежуточный случай: маленькой модели памяти хватает не на всех.
func TestContextForUsersFallsBackToBudgetWhenCrowded(t *testing.T) {
	const modelMax = 262144
	fit := Fit{
		BaseMiB:          7905,
		BytesPerToken:    9893,
		TopBytesPerToken: 9893,
		Samples:          []Sample{{Context: 262144, UsageMiB: 9563, PSTotalMiB: 9563, PSVRAMMiB: 9563}},
	}
	// Бюджет урезан так, что полное окно всем уже не раздать.
	const budget = 20000.0
	got := fit.ContextForUsers(budget, 8, modelMax, modelMax)
	if got <= 0 || got >= modelMax {
		t.Fatalf("окно = %d, ожидалось значение меньше максимума модели", got)
	}
	if used := fit.Predict(got, 8); used > budget {
		t.Errorf("прогноз %.0f МиБ не помещается в бюджет %.0f", used, budget)
	}
}

// Сколько человек потянут полное окно модели — самый частый практический
// вопрос: «мы хотим максимум контекста, сколько нас поместится».

func TestUsersAtFullContextForSmallModel(t *testing.T) {
	// gemma4:31b со стенда: помещается легко, вопрос лишь сколько раз.
	const (
		modelMax = 262144
		budget   = 69520.0
	)
	fit := Fit{
		BaseMiB:          19829,
		BytesPerToken:    15342,
		TopBytesPerToken: 4096,
		Samples: []Sample{
			{Context: 32768, UsageMiB: 19957, PSTotalMiB: 19957, PSVRAMMiB: 19957},
			{Context: 131072, UsageMiB: 21571, PSTotalMiB: 21571, PSVRAMMiB: 21571},
			{Context: 262144, UsageMiB: 20405, PSTotalMiB: 20405, PSVRAMMiB: 20405},
		},
	}
	got := fit.UsersAtFullContext(budget, modelMax, modelMax)
	if got < 1 {
		t.Fatalf("модель на 19 ГиБ при бюджете 68 ГиБ должна тянуть хотя бы одного, получено %d", got)
	}
	// Проверяем ответ на прочность: столько сессий обязаны помещаться,
	// а на одну больше — уже нет.
	if used := fit.Predict(modelMax, got); used > budget {
		t.Errorf("%d сессий занимают %.0f МиБ при бюджете %.0f", got, used, budget)
	}
	if used := fit.Predict(modelMax, got+1); used <= budget {
		t.Errorf("ответ занижен: %d сессий тоже помещаются (%.0f МиБ)", got+1, used)
	}
}

// Большая модель: полное окно не тянет даже один пользователь. Ноль — законный
// ответ, и именно его надо вернуть, а не единицу «на всякий случай».
func TestUsersAtFullContextZeroForLargeModel(t *testing.T) {
	const (
		modelMax = 262144
		verified = 136192
		budget   = 79855.0
	)
	fit := Fit{
		BaseMiB:          75472,
		BytesPerToken:    25600,
		TopBytesPerToken: 25600,
		Samples: []Sample{
			{Context: 131072, UsageMiB: 78672, PSTotalMiB: 78672, PSVRAMMiB: 78672},
			{Context: verified, UsageMiB: 78797, PSTotalMiB: 78797, PSVRAMMiB: 78797},
		},
	}
	if got := fit.UsersAtFullContext(budget, modelMax, verified); got != 0 {
		t.Errorf("полное окно qwen3.5:122b не тянет никто, получено %d", got)
	}
	// При этом одному по-прежнему есть что дать — об этом и сообщаем.
	if single := fit.ContextForUsers(budget, 1, modelMax, verified); single != verified {
		t.Errorf("максимум для одного = %d, ожидался проверенный потолок %d", single, verified)
	}
}

func TestUsersAtFullContextHandlesUnknownMaximum(t *testing.T) {
	fit := Fit{BaseMiB: 1000, BytesPerToken: 1024, TopBytesPerToken: 1024}
	if got := fit.UsersAtFullContext(80000, 0, 0); got != 0 {
		t.Errorf("без максимума модели считать нечего, ожидался 0, получено %d", got)
	}
}

// Расход считается по карте, а не по /api/ps.
//
// Проверено на стенде: gemma4:31b с окном 256k занимает по nvidia-smi
// 42533 МиБ, а /api/ps в тот же момент сообщает 20404 — устойчиво, один
// процесс, сорок секунд подряд. Ollama не учитывает буферы вычислений,
// и расхождение растёт с окном. Подгонка по /api/ps завысила бы число
// пользователей вдвое.
func TestFitUsesGPUReadingNotOllamaReport(t *testing.T) {
	samples := []Sample{
		// Числа стенда: слева карта, справа то, что сообщает Ollama.
		{Context: 32768, UsageMiB: 24165, PSTotalMiB: 19957, PSVRAMMiB: 19957},
		{Context: 131072, UsageMiB: 33459, PSTotalMiB: 21571, PSVRAMMiB: 21571},
		{Context: 262144, UsageMiB: 42533, PSTotalMiB: 20405, PSVRAMMiB: 20405},
	}
	fit, err := FitSamples(samples)
	if err != nil {
		t.Fatalf("подгонка: %v", err)
	}
	// Наклон должен получиться из показаний карты: примерно
	// (42533-24165)/(262144-32768) = 0.080 МиБ на токен.
	if got := fit.Slope() / (1024 * 1024); got < 0.07 || got > 0.09 {
		t.Errorf("расход на токен = %.4f МиБ, ожидалось около 0.080 — подгонка идёт не по карте", got)
	}
	// А по данным Ollama наклон был бы почти нулевым — вот цена ошибки.
	psSlope := (samples[2].PSTotalMiB - samples[0].PSTotalMiB) / float64(samples[2].Context-samples[0].Context)
	if psSlope > 0.01 {
		t.Fatalf("подготовка: по /api/ps наклон должен быть почти нулевым, вышло %.4f", psSlope)
	}
}

// Вытеснение видно только по /api/ps: карта показывает лишь то, что на ней
// осталось, и по ней недостачу не отличить от экономии.
func TestSpillDetectedByOllamaReport(t *testing.T) {
	fits := Sample{UsageMiB: 78000, PSTotalMiB: 78672, PSVRAMMiB: 78672}
	spills := Sample{UsageMiB: 78848, PSTotalMiB: 80527, PSVRAMMiB: 78848}
	if fits.Spilled() {
		t.Error("совпадающие размеры означают, что модель целиком на карте")
	}
	if !spills.Spilled() {
		t.Error("расхождение размеров в /api/ps означает вытеснение")
	}
}
