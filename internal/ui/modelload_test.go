package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Пока модель грузится в память карты, в строке состояния — «загружается»,
// а не «думает»: модель ещё ничего не делает, и ждать придётся не секунды.
func TestLoadingStatusShownWhileModelLoads(t *testing.T) {
	now := time.Now()
	m := &Model{streaming: true, startedAt: now.Add(-42 * time.Second)}
	m.residency = ollama.Residency{Known: true, Loaded: false}

	got := m.loadingStatus(now)
	if !strings.Contains(got, "загружается") {
		t.Fatalf("нет слова о загрузке: %q", got)
	}
	// Неподвижная надпись сама читается как зависание — нужен счётчик.
	if !strings.Contains(got, "42 с") {
		t.Errorf("нет времени ожидания: %q", got)
	}
}

// Как только пошёл ответ — надпись обычная, чем бы модель ни была занята до.
func TestLoadingStatusGoesAwayOnFirstToken(t *testing.T) {
	now := time.Now()
	m := &Model{streaming: true, startedAt: now.Add(-time.Minute), gotOutput: true}
	m.residency = ollama.Residency{Known: true, Loaded: false}
	if got := m.loadingStatus(now); got != "" {
		t.Errorf("после первого куска ответа всё ещё «загружается»: %q", got)
	}
}

// Модель на месте — обычная надпись.
func TestLoadingStatusSilentWhenLoaded(t *testing.T) {
	now := time.Now()
	m := &Model{streaming: true, startedAt: now}
	m.residency = ollama.Residency{Known: true, Loaded: true}
	if got := m.loadingStatus(now); got != "" {
		t.Errorf("модель загружена, а надпись про загрузку есть: %q", got)
	}
}

// Сервер не ответил — молчим.
//
// «Не знаю» и «не загружена» — разные вещи: показать «загружается» там,
// где просто оборвалась сеть, значит соврать человеку о причине ожидания.
func TestLoadingStatusSilentWhenUnknown(t *testing.T) {
	now := time.Now()
	m := &Model{streaming: true, startedAt: now.Add(-time.Minute)}
	m.residency = ollama.Residency{} // Known == false
	if got := m.loadingStatus(now); got != "" {
		t.Errorf("сервер не ответил, а надпись выдумана: %q", got)
	}
}

func TestHumanWait(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                 "0 с",
		9 * time.Second:   "9 с",
		59 * time.Second:  "59 с",
		60 * time.Second:  "1 мин 00 с",
		135 * time.Second: "2 мин 15 с",
	} {
		if got := humanWait(d); got != want {
			t.Errorf("humanWait(%v) = %q, ожидалось %q", d, got, want)
		}
	}
}

// Подсказка называет модель и говорит, что программа не зависла.
func TestLoadingHintSaysWhatIsHappening(t *testing.T) {
	h := loadingHint("qwen3.5:122b")
	for _, want := range []string{"qwen3.5:122b", "не зависла", "Esc"} {
		if !strings.Contains(h, want) {
			t.Errorf("в подсказке нет %q: %s", want, h)
		}
	}
}

// Объяснение приходит с задержкой и только если ждать действительно пришлось.
func TestLoadHintOnlyWhenWaitIsReal(t *testing.T) {
	cases := []struct {
		name string
		set  func(m *Model)
		want bool
	}{
		{"ждём того же обмена, ответа нет", func(m *Model) {}, true},
		{"ответ уже пошёл", func(m *Model) { m.gotOutput = true }, false},
		{"обмен уже другой", func(m *Model) { m.gen.run = 7 }, false},
		{"поток кончился", func(m *Model) { m.streaming = false }, false},
		{"уже объясняли", func(m *Model) { m.saidLoad = true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.streaming, m.gen.run, m.modelName = true, 3, "qwen3.5:122b"
			tc.set(m)
			before := len(m.blocks)
			m.Update(hintTimerMsg{gen: 3})
			added := len(m.blocks) > before
			if added != tc.want {
				t.Errorf("подсказка добавлена=%v, ожидалось %v", added, tc.want)
			}
			if added && m.blocks[len(m.blocks)-1].kind != blockHint {
				t.Error("подсказка добавлена не как подсказка")
			}
		})
	}
}

// Дважды за обмен не объясняем.
func TestLoadHintOnlyOncePerTurn(t *testing.T) {
	m := newTestModel(t)
	m.streaming, m.gen.run, m.modelName = true, 1, "qwen3.5:122b"
	m.Update(hintTimerMsg{gen: 1})
	n := len(m.blocks)
	m.Update(hintTimerMsg{gen: 1})
	if len(m.blocks) != n {
		t.Errorf("объяснение повторилось: было %d блоков, стало %d", n, len(m.blocks))
	}
}

// Проверка со старта принимается всегда: она делалась вне обмена.
//
// Иначе весь смысл ранней проверки пропадает — её ответ отбрасывался бы
// сравнением поколений, и программа снова узнавала бы о загрузке только
// посреди ожидания.
func TestStartupResidencyAccepted(t *testing.T) {
	m := newTestModel(t)
	m.gen.run = 5
	m.modelName = "qwen3.5:122b"
	m.Update(residencyMsg{gen: genStartup, res: ollama.Residency{Known: true}})

	if !m.residency.Known {
		t.Fatal("ответ со старта отброшен сравнением поколений")
	}
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockHint {
		t.Fatal("при незагруженной модели нет предупреждения на старте")
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "не зависание") {
		t.Errorf("предупреждение не снимает главного недоумения: %s",
			m.blocks[len(m.blocks)-1].text)
	}
}

// Модель загружена — на старте молчим.
func TestStartupSilentWhenLoaded(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "qwen3.5:122b"
	before := len(m.blocks)
	m.Update(residencyMsg{gen: genStartup, res: ollama.Residency{Known: true, Loaded: true}})
	if len(m.blocks) != before {
		t.Errorf("модель в памяти, а предупреждение выведено: %+v", m.blocks[len(m.blocks)-1])
	}
}

// Одно и то же не говорится дважды: предупредили на старте — посреди
// ожидания молчим.
func TestNoDoubleExplanation(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "qwen3.5:122b"
	m.Update(residencyMsg{gen: genStartup, res: ollama.Residency{Known: true}})
	after := len(m.blocks)

	m.streaming, m.gen.run = true, 1
	m.Update(hintTimerMsg{gen: 1})
	if len(m.blocks) != after {
		t.Errorf("объяснение повторилось: было %d блоков, стало %d", after, len(m.blocks))
	}
}

// А после того как модель увидена загруженной, о следующей выгрузке
// предупреждаем снова: это уже другая загрузка.
func TestWarnsAgainAfterReload(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "qwen3.5:122b"
	m.Update(residencyMsg{gen: genStartup, res: ollama.Residency{Known: true}})
	m.Update(residencyMsg{gen: genStartup, res: ollama.Residency{Known: true, Loaded: true}})
	if m.saidLoad {
		t.Fatal("после загрузки признак «уже объясняли» не снят")
	}

	m.streaming, m.gen.run = true, 2
	before := len(m.blocks)
	m.Update(hintTimerMsg{gen: 2})
	if len(m.blocks) == before {
		t.Error("о новой загрузке не предупредили")
	}
}

// Предупреждение при запуске гасится general.startup_hints = off,
// а объяснение посреди настоящего ожидания — нет.
//
// Разница существенная: первое — строка на каждый пуск программы, ровно та,
// ради которой настройку и заводили. Второе человек видит, только уже сидя
// в тишине, и это не совет, а ответ на вопрос «что происходит».
func TestStartupHintObeysSetting(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) { c.General.StartupHints = "off" })
	m.modelName = "qwen3.5:122b"

	before := len(m.blocks)
	m.Update(residencyMsg{gen: genStartup, res: ollama.Residency{Known: true}})
	if len(m.blocks) != before {
		t.Errorf("startup_hints = off, а предупреждение при запуске выведено: %+v",
			m.blocks[len(m.blocks)-1])
	}

	// Но ждать всё равно придётся, и об этом сказать надо.
	m.streaming, m.gen.run = true, 1
	m.Update(hintTimerMsg{gen: 1})
	if len(m.blocks) == before+1 && m.blocks[before].kind == blockHint {
		return
	}
	t.Error("объяснение посреди ожидания погашено настройкой — оно не совет")
}
