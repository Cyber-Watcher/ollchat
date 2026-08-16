package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/vram"
)

// Числа профиля взяты с настоящего стенда: A100 80 ГиБ, qwen3.5:122b.
func standProfile() *vram.Profile {
	return &vram.Profile{
		Format:      vram.ProfileFormat,
		Host:        "ai-server",
		GeneratedAt: "2026-08-14T08:00:00Z",
		Parallel:    1,
		Models: []vram.ModelResult{{
			Name:               "test-model",
			MaxContext:         262144,
			VerifiedMaxContext: 131072,
			Fit: vram.Fit{
				BaseMiB:          75472,
				BytesPerToken:    25600,
				TopBytesPerToken: 25600,
				Samples: []vram.Sample{
					{Context: 32768, UsageMiB: 76272, PSTotalMiB: 76272, PSVRAMMiB: 76272},
					{Context: 131072, UsageMiB: 78672, PSTotalMiB: 78672, PSVRAMMiB: 78672},
				},
			},
			Budget: vram.Budget{UsableMiB: 79855, TotalMiB: 81920},
		}},
	}
}

func calcWithProfile(t *testing.T, users int) string {
	t.Helper()
	m := newTestModel(t)
	m.vramProfile = standProfile()
	m.modelMaxCtx = 262144
	return m.calcReport(calcMsg{users: users, model: m.modelName})
}

// Без аргумента считаем на одного — это и есть поведение по умолчанию.
func TestCalcWithoutArgumentIsOneUser(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = standProfile()

	if cmd := m.calcArgCmd(""); cmd == nil {
		t.Fatal("/calc без аргумента должен запускать расчёт")
	}
	report := m.calcReport(calcMsg{users: 1, model: m.modelName})
	if !strings.Contains(report, "на 1 пользователя") {
		t.Errorf("в отчёте нет числа пользователей:\n%s", report)
	}
}

// Окно делится между пользователями обратно пропорционально: вдвое больше
// людей — вдвое меньше окно каждому.
func TestCalcSplitsWindowBetweenUsers(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = standProfile()
	res, _ := m.vramProfile.Model("test-model")

	one := m.contextForUsers(res, 1)
	if one != 131072 {
		t.Fatalf("для одного окно = %d, ожидался проверенный потолок 131072", one)
	}
	if got := m.contextForUsers(res, 2); got != 65536 {
		t.Errorf("для двоих окно = %d, ожидалось 65536", got)
	}
	if got := m.contextForUsers(res, 4); got != 32768 {
		t.Errorf("для четверых окно = %d, ожидалось 32768", got)
	}
	if got := m.contextForUsers(res, 8); got != 16384 {
		t.Errorf("для восьмерых окно = %d, ожидалось 16384", got)
	}
}

// Проверенный загрузкой потолок важнее расчётного: расчёт по замерам до 128k
// давал 175k, а на деле уже 139k вытесняется.
func TestCalcPrefersVerifiedCeiling(t *testing.T) {
	report := calcWithProfile(t, 1)
	if !strings.Contains(report, "проверенный загрузкой") {
		t.Errorf("в отчёте не сказано, что потолок проверен:\n%s", report)
	}
	if !strings.Contains(report, "128k") {
		t.Errorf("в отчёте нет проверенного потолка 128k:\n%s", report)
	}
	if strings.Contains(report, "175k") {
		t.Errorf("расчётное значение не должно подменять проверенное:\n%s", report)
	}
}

// В отчёте видно, откуда взяты числа, — иначе им нельзя доверять.
func TestCalcShowsMeasurementSource(t *testing.T) {
	report := calcWithProfile(t, 2)
	for _, want := range []string{"olldiagtools", "ai-server", "вес модели", "расход на токен"} {
		if !strings.Contains(report, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, report)
		}
	}
}

// Без профиля расчёт грубый, и это должно быть сказано прямо.
func TestCalcWithoutProfileWarnsAboutRoughness(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.Servers[0].VRAMGiB = 80
	})
	m.vramProfile = nil
	m.modelMaxCtx = 262144

	report := m.calcReport(calcMsg{
		users:       4,
		model:       m.modelName,
		fileSizeMiB: 77600,
		running: &ollama.RunningModel{
			Name: m.modelName, ContextLength: 131072,
			Size: 78672 << 20, SizeVRAM: 78672 << 20,
		},
	})

	if !strings.Contains(report, "грубо") {
		t.Errorf("грубую оценку надо называть грубой:\n%s", report)
	}
	if !strings.Contains(report, "olldiagtools") {
		t.Errorf("надо подсказать, чем считать точно:\n%s", report)
	}
	if !strings.Contains(report, "≈") {
		t.Errorf("оценочные величины должны быть помечены знаком ≈:\n%s", report)
	}
}

// Без размера карты и без профиля считать нечего — говорим, чего не хватает.
func TestCalcAsksForVRAMSizeWhenUnknown(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = nil

	report := m.calcReport(calcMsg{
		users:       1,
		model:       m.modelName,
		fileSizeMiB: 3000,
		running: &ollama.RunningModel{
			Name: m.modelName, ContextLength: 32768,
			Size: 4000 << 20, SizeVRAM: 4000 << 20,
		},
	})
	if !strings.Contains(report, "vram_gib") {
		t.Errorf("надо сказать, какую настройку задать:\n%s", report)
	}
}

// Незагруженная модель — не повод врать: мерить нечего.
func TestCalcSaysWhenModelNotLoaded(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = nil

	report := m.calcReport(calcMsg{users: 1, model: m.modelName})
	if !strings.Contains(report, "не загружена") {
		t.Errorf("надо сказать, что модель не загружена:\n%s", report)
	}
}

// Вытеснение в текущем состоянии сервера — самое важное, что можно сообщить.
func TestCalcReportsCurrentSpill(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = standProfile()

	report := m.calcReport(calcMsg{
		users: 1, model: m.modelName,
		running: &ollama.RunningModel{
			Name: m.modelName, ContextLength: 163840,
			Size: 80527 << 20, SizeVRAM: 78848 << 20,
		},
	})
	if !strings.Contains(report, "вытеснена в ОЗУ") {
		t.Errorf("вытеснение надо называть прямо:\n%s", report)
	}
}

func TestCalcRejectsGarbageArgument(t *testing.T) {
	for _, arg := range []string{"много", "0", "-2"} {
		m := newTestModel(t)
		if cmd := m.calcArgCmd(arg); cmd != nil {
			t.Errorf("аргумент %q не должен запускать расчёт", arg)
		}
		if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
			t.Errorf("аргумент %q должен давать ошибку, получено: %+v", arg, last)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{131072: "128k", 32768: "32k", 1000: "1000", 0: "—"}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %q, ожидалось %q", in, got, want)
		}
	}
}

// В /calc тоже должно быть видно, сколько человек потянут полное окно модели.
func TestCalcReportsFullContextUsers(t *testing.T) {
	// Профиль стенда: qwen3.5:122b, полное окно не тянет никто.
	report := calcWithProfile(t, 1)
	if !strings.Contains(report, "не потянет ни один пользователь") {
		t.Errorf("для большой модели надо прямо сказать, что полное окно недоступно:\n%s", report)
	}
	// Имя модели должно стоять прямо в этой строке: вывод длинный, и искать
	// глазами выше, о какой модели речь, не должно приходиться.
	if !strings.Contains(report, "Полное окно модели test-model (256k)") {
		t.Errorf("в строке вердикта нет имени модели и размера окна:\n%s", report)
	}
	if !strings.Contains(report, "Максимум для одного") {
		t.Errorf("и сразу назвать, что доступно одному:\n%s", report)
	}
}

func TestCalcReportsFullContextUsersForSmallModel(t *testing.T) {
	m := newTestModel(t)
	m.modelMaxCtx = 262144
	m.vramProfile = &vram.Profile{
		Format: vram.ProfileFormat,
		Host:   "ai-server",
		Models: []vram.ModelResult{{
			Name:               m.modelName,
			MaxContext:         262144,
			VerifiedMaxContext: 262144, // потолок — максимум модели, память не предел
			Fit: vram.Fit{
				BaseMiB:          19829,
				BytesPerToken:    15342,
				TopBytesPerToken: 15342,
				Samples:          []vram.Sample{{Context: 262144, UsageMiB: 20405, PSTotalMiB: 20405, PSVRAMMiB: 20405}},
			},
			Budget: vram.Budget{UsableMiB: 69520},
		}},
	}

	report := m.calcReport(calcMsg{users: 1, model: m.modelName})
	if !strings.Contains(report, "одновременно потянут") {
		t.Errorf("для небольшой модели надо назвать число пользователей:\n%s", report)
	}
	if !strings.Contains(report, "модели "+m.modelName+" (256k)") {
		t.Errorf("в строке вердикта нет имени модели и размера окна:\n%s", report)
	}
	if strings.Contains(report, "не потянет ни один") {
		t.Errorf("модель на 19 ГиБ при бюджете 68 ГиБ тянут многие:\n%s", report)
	}
}

// Профиль, снятый старой версией инструмента, считал расход по /api/ps
// и занижал его в разы. Показывать такие числа как замеры нельзя.
func TestCalcRejectsOutdatedProfile(t *testing.T) {
	m := newTestModel(t)
	old := standProfile()
	old.Format = 1
	m.vramProfile = old

	report := m.calcReport(calcMsg{users: 1, model: m.modelName})
	if !strings.Contains(report, "устарел") {
		t.Errorf("устаревший профиль надо назвать устаревшим:\n%s", report)
	}
	if !strings.Contains(report, "olldiag") {
		t.Errorf("надо подсказать, чем снять заново:\n%s", report)
	}
	if strings.Contains(report, "проверенный загрузкой") {
		t.Errorf("числа из устаревшего профиля не должны выдаваться за замеры:\n%s", report)
	}
}

// Недоступный сервер не должен обнулять ответ: расчёт живёт в профиле на диске.
func TestCalcWorksWhenServerIsUnreachable(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = standProfile()
	m.modelMaxCtx = 262144

	report := m.calcReport(calcMsg{
		users: 2, model: m.modelName,
		err: errors.New("connection refused"),
	})

	if !strings.Contains(report, "сервер недоступен") {
		t.Errorf("о недоступности сервера надо сказать:\n%s", report)
	}
	if !strings.Contains(report, "окно на каждого") {
		t.Errorf("расчёт по профилю должен пройти и без сервера:\n%s", report)
	}
	// В фикстуре потолок 128k, значит на двоих — по 64k.
	if !strings.Contains(report, "64k") {
		t.Errorf("для двоих ожидалась половина потолка:\n%s", report)
	}
}

// А вот если и профиля нет, считать действительно нечем.
func TestCalcGivesUpWithoutServerAndProfile(t *testing.T) {
	m := newTestModel(t)
	m.vramProfile = nil

	report := m.calcReport(calcMsg{
		users: 1, model: m.modelName,
		err: errors.New("connection refused"),
	})
	if strings.Contains(report, "окно на каждого") {
		t.Errorf("без сервера и без замеров считать нечего:\n%s", report)
	}
}
