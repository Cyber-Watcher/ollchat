package vram

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Профиль пишет olldiagtools, а читает ollchat. Разъехаться формату негде —
// обе программы используют этот пакет, — но проверить круг «записали → прочитали
// → посчитали» стоит: именно на нём вылезет забытое поле.
func TestProfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")

	want := Profile{
		Format:      ProfileFormat,
		GeneratedAt: "2026-08-14T12:00:00Z",
		Host:        "ai-server",
		OllamaURL:   "http://127.0.0.1:11434",
		Parallel:    1,
		GPU:         GPU{Name: "A100", TotalMiB: 81920, UsedMiB: 0, Available: true},
		Models: []ModelResult{{
			Name:               "gemma4:31b",
			FileSizeMiB:        18944,
			MaxContext:         262144,
			Vision:             false,
			VerifiedMaxContext: 262144,
			FullContextUsers:   2,
			Fit: Fit{
				BaseMiB:          21477,
				BytesPerToken:    97262,
				TopBytesPerToken: 97262,
				Samples: []Sample{
					{Context: 32768, UsageMiB: 24165, PSTotalMiB: 19957, PSVRAMMiB: 19957},
					{Context: 262144, UsageMiB: 42533, PSTotalMiB: 20405, PSVRAMMiB: 20405},
				},
			},
			Budget: Budget{TotalMiB: 81920, UsableMiB: 81408, Source: "nvidia-smi"},
			Users:  []UserLimit{{Users: 1, ContextMax: 262144}, {Users: 4, ContextMax: 157696}},
		}},
	}

	if err := WriteProfile(path, want); err != nil {
		t.Fatalf("запись: %v", err)
	}
	got, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}

	if got.Outdated() {
		t.Error("свежезаписанный профиль не может считаться устаревшим")
	}
	m, ok := got.Model("gemma4:31b")
	if !ok {
		t.Fatal("модель не нашлась в прочитанном профиле")
	}

	// Всё, чем пользуется ollchat, обязано пережить круг.
	if m.Fit.BaseMiB != want.Models[0].Fit.BaseMiB {
		t.Errorf("вес модели = %.0f, ожидалось %.0f", m.Fit.BaseMiB, want.Models[0].Fit.BaseMiB)
	}
	if m.Fit.Slope() != want.Models[0].Fit.Slope() {
		t.Errorf("расход на токен = %.0f, ожидалось %.0f", m.Fit.Slope(), want.Models[0].Fit.Slope())
	}
	if m.VerifiedMaxContext != 262144 || m.MaxContext != 262144 || m.FullContextUsers != 2 {
		t.Errorf("потерялись поля модели: %+v", m)
	}
	if m.Budget.UsableMiB != 81408 {
		t.Errorf("бюджет = %.0f, ожидалось 81408", m.Budget.UsableMiB)
	}
	if len(m.Fit.Samples) != 2 || m.Fit.Samples[0].UsageMiB != 24165 {
		t.Errorf("замеры не пережили круг: %+v", m.Fit.Samples)
	}

	// И расчёт по прочитанному профилю должен дать то же, что по исходному.
	for _, users := range []int{1, 2, 4} {
		a := want.Models[0].Fit.ContextForUsers(81408, users, 262144, 262144)
		b := m.Fit.ContextForUsers(81408, users, 262144, 262144)
		if a != b {
			t.Errorf("для %d пользователей до записи %d, после чтения %d", users, a, b)
		}
	}
}

// Профиль без номера формата — это версия 1, считавшая расход по /api/ps.
func TestProfileWithoutFormatIsOutdated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(path, []byte(`{"host":"x","models":[]}`), 0o600); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !p.Outdated() {
		t.Error("профиль без номера формата обязан считаться устаревшим")
	}
}

func TestLoadProfileReportsMissingFile(t *testing.T) {
	if _, err := LoadProfile(filepath.Join(t.TempDir(), "нет.json")); err == nil {
		t.Error("отсутствие файла должно быть ошибкой — вызывающий сам решит, страшно ли это")
	}
}

// Профиль подменяется целиком, а не переписывается на месте: тот, кто открыл
// прежний файл, дочитывает прежний. Запись на месте сначала обрезала файл,
// и оборванная запись оставляла вместо замеров пустоту.
func TestWriteProfileReplacesWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "olldiag-profile.json")
	if err := WriteProfile(path, Profile{Format: ProfileFormat, Host: "old"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := WriteProfile(path, Profile{Format: ProfileFormat, Host: "new"}); err != nil {
		t.Fatal(err)
	}
	var old Profile
	if err := json.NewDecoder(f).Decode(&old); err != nil || old.Host != "old" {
		t.Errorf("открытый прежде файл переписан на месте: host=%q, ошибка %v", old.Host, err)
	}
	cur, err := LoadProfile(path)
	if err != nil || cur.Host != "new" {
		t.Errorf("новый профиль не читается: %v", err)
	}
}
