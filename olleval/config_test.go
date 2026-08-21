package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigПоверхУмолчаний(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "olleval.toml")
	body := `
url = "http://10.2.7.51:11434"

[schedule]
gap = "30m"

[schedule.days]
monday = ["23:00-06:00"]
saturday = ["09:00-13:00", "20:00-23:59"]

[guard]
free_checks = 5
poll = "30s"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.URL != "http://10.2.7.51:11434" {
		t.Errorf("адрес из файла не подхвачен: %q", cfg.URL)
	}
	if got := cfg.Schedule.Days.Monday; len(got) != 1 || got[0] != "23:00-06:00" {
		t.Errorf("расписание понедельника не подхвачено: %v", got)
	}
	if got := cfg.Schedule.Days.Saturday; len(got) != 2 {
		t.Errorf("два субботних промежутка не подхвачены: %v", got)
	}
	if cfg.Guard.FreeChecks != 5 || cfg.Guard.Poll.Get(0) != 30*time.Second {
		t.Errorf("настройки проверки не подхвачены: %+v", cfg.Guard)
	}
	// Незаданное берётся из умолчаний, а не обнуляется.
	if cfg.Schedule.Timezone != "Europe/Moscow" {
		t.Errorf("умолчание пояса затёрто: %q", cfg.Schedule.Timezone)
	}
	// Дни, которых нет в файле, остаются от умолчаний: правка одного дня
	// не должна молча отменять прогоны в остальные.
	if got := cfg.Schedule.Days.Sunday; len(got) != 1 || got[0] != "00:00-23:59" {
		t.Errorf("воскресенье затёрто правкой других дней: %v", got)
	}
	if cfg.Run.Repeats != 3 || cfg.Run.NumCtx != 32768 {
		t.Errorf("умолчания прогона затёрты: %+v", cfg.Run)
	}
}

// Отсутствие файла — обычный способ запуска, а не ошибка.
func TestLoadConfigБезФайла(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "нет.toml"))
	if err != nil {
		t.Fatalf("LoadConfig без файла: %v", err)
	}
	if got := cfg.Schedule.Days.Monday; len(got) != 1 || got[0] != "00:00-07:00" {
		t.Errorf("умолчание расписания = %v", got)
	}
}

func TestConfigValidateЛовитОшибки(t *testing.T) {
	cases := map[string]func(*Config){
		"неизвестный пояс":   func(c *Config) { c.Schedule.Timezone = "Марс/Олимп" },
		"время не того вида": func(c *Config) { c.Schedule.Until = "утром" },
		"ноль проверок":      func(c *Config) { c.Guard.FreeChecks = 0 },
		"ноль повторов":      func(c *Config) { c.Run.Repeats = 0 },
		"крошечное окно":     func(c *Config) { c.Run.NumCtx = 16 },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			break_(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("принято: %s", name)
			}
		})
	}
}

// Скрипт ночи берёт расписание отсюда, а не держит те же числа у себя.
func TestConfigGet(t *testing.T) {
	cfg := DefaultConfig()
	for key, want := range map[string]string{
		"schedule.days.monday": "00:00-07:00", "schedule.days.saturday": "00:00-23:59",
		"schedule.timezone": "Europe/Moscow", "schedule.gap": "15m0s", "guard.free_checks": "3",
	} {
		got, err := cfg.Get(key)
		if err != nil || got != want {
			t.Errorf("Get(%q) = %q, %v; ожидалось %q", key, got, err, want)
		}
	}
	if _, err := cfg.Get("несуществующий.ключ"); err == nil {
		t.Error("неизвестный ключ принят")
	}
}

func TestWriteConfigTemplateНеЗатираетГотовый(t *testing.T) {
	path := filepath.Join(t.TempDir(), "olleval.toml")
	if err := WriteConfigTemplate(path, "/home/ai/ollevals"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("образец не читается: %v", err)
	}
	if got := cfg.Schedule.Days.Monday; len(got) != 1 || got[0] != "00:00-07:00" || cfg.Guard.FreeChecks != 3 {
		t.Errorf("образец разошёлся с умолчаниями: %+v %+v", cfg.Schedule, cfg.Guard)
	}
	if err := WriteConfigTemplate(path, "/home/ai/ollevals"); err == nil {
		t.Error("готовый файл затёрт")
	}
}

// Правку расписания надо замечать на ходу: её делают обычно затем, чтобы
// забрать стенд себе прямо сейчас, а не после конца суточного окна.
func TestWatchScheduleВидитПравкуНаХоду(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "olleval.toml")
	день := strings.ToLower(time.Now().Weekday().String())

	// Каждый день перечисляется ровно один раз: повтор ключа TOML не примет,
	// и вместо правки расписания получилась бы ошибка разбора.
	write := func(spans string) {
		var b strings.Builder
		b.WriteString("[schedule]\ntimezone = \"Europe/Moscow\"\ngap = \"0s\"\n\n[schedule.days]\n")
		for wd := time.Sunday; wd <= time.Saturday; wd++ {
			name := strings.ToLower(wd.String())
			value := "[]"
			if name == день {
				value = spans
			}
			fmt.Fprintf(&b, "%s = %s\n", name, value)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	watch := WatchSchedule(path)

	write(`["00:00-23:59"]`)
	deadline, ok := watch()
	if !ok {
		t.Fatal("окно на целые сутки не признано действующим")
	}

	// Стенд понадобился человеку: день выключают прямо во время прогона.
	write(`[]`)
	if _, ok := watch(); ok {
		t.Error("прогон не заметил, что окно убрали из настроек")
	}

	// Вернули обратно — прогон снова наш, предел тот же.
	write(`["00:00-23:59"]`)
	again, ok := watch()
	if !ok || !again.Equal(deadline) {
		t.Errorf("после возврата окна предел = %v (%v), ожидался %v", again, ok, deadline)
	}
}

// Сломанный файл настроек не должен ронять идущую ночь: чинить его будут
// утром, а прогон пусть доработает по тому расписанию, с которым начинался.
func TestWatchScheduleНеРонитПрогонНаБитомФайле(t *testing.T) {
	path := filepath.Join(t.TempDir(), "olleval.toml")
	if err := os.WriteFile(path, []byte("это не toml ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := WatchSchedule(path)(); !ok {
		t.Error("битый файл настроек остановил прогон")
	}
}
