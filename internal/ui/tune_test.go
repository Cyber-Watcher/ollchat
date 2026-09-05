package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Крутилка меняет отбор на сеанс и не трогает конфиг.
func TestGraphTuneChangesSessionValues(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) {
		c.Graph.NeighborSenseWeight = 0
		c.Graph.NeighborPool = 3
	})

	m.runCommand("/graph tune sense 1.5")
	if got := m.live.Rank().SenseWeight; got != 1.5 {
		t.Errorf("вес уместности = %v, ожидалось 1.5", got)
	}
	m.runCommand("/graph tune pool 8")
	if got := m.live.Rank().Pool; got != 8 {
		t.Errorf("пул = %d, ожидалось 8", got)
	}
	// Конфиг остаётся прежним: подобранное значение переносит человек.
	if m.cfg.Graph.NeighborSenseWeight != 0 || m.cfg.Graph.NeighborPool != 3 {
		t.Error("крутилка не должна менять конфиг")
	}

	m.runCommand("/graph tune reset")
	if r := m.live.Rank(); r.SenseWeight != 0 || r.Pool != 3 {
		t.Errorf("после reset ожидались значения конфига, получено %+v", r)
	}
}

// Бессмысленные значения отклоняются с объяснением, а не применяются молча.
func TestGraphTuneRejectsBadValues(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) { c.Graph.NeighborSenseWeight = 0 })

	for _, cmd := range []string{"/graph tune sense -1", "/graph tune pool 0", "/graph tune sense ага"} {
		before := m.live.Rank()
		m.runCommand(cmd)
		if m.live.Rank() != before {
			t.Errorf("%s не должна была примениться", cmd)
		}
		if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
			t.Errorf("%s: ожидалась ошибка, получено %v", cmd, last.kind)
		}
	}
}

// Числа поиска по книгам крутятся так же.
func TestKBTuneChangesSessionValues(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) {
		c.KB.TopK = 8
		c.KB.MaxPerBook = 3
		c.KB.SemanticWeight = 1.0
	})

	m.runCommand("/kb tune top_k 12")
	m.runCommand("/kb tune semantic_weight 1.5")
	topK, _, _, sem := m.live.KB()
	if topK != 12 || sem != 1.5 {
		t.Errorf("top_k = %d, semantic_weight = %v; ожидались 12 и 1.5", topK, sem)
	}
	if m.cfg.KB.TopK != 8 {
		t.Error("крутилка не должна менять конфиг")
	}

	m.runCommand("/kb tune reset")
	if topK, _, _, sem = m.live.KB(); topK != 8 || sem != 1.0 {
		t.Errorf("после reset: top_k = %d, semantic_weight = %v", topK, sem)
	}
}

// Порог косинуса — от нуля до единицы, всё прочее отклоняется.
func TestKBTuneChecksCosineRange(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) { c.KB.MinCosine = 0.2 })

	m.runCommand("/kb tune min_cosine 1.5")
	if _, _, cos, _ := m.live.KB(); cos != 0.2 {
		t.Errorf("косинус вне диапазона не должен применяться, стало %v", cos)
	}
	m.runCommand("/kb tune min_cosine 0,35") // запятая как разделитель — тоже число
	if _, _, cos, _ := m.live.KB(); cos != 0.35 {
		t.Errorf("косинус = %v, ожидалось 0.35 (запятая допустима)", cos)
	}
}

// Отчёт показывает и сеансовое значение, и записанное в конфиге.
func TestTuneReportShowsBothValues(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) { c.Graph.NeighborSenseWeight = 0 })
	m.runCommand("/graph tune sense 2")

	report := m.tuneReport()
	if !strings.Contains(report, "2 → 0") {
		t.Errorf("в отчёте нет пары «сеанс → конфиг»:\n%s", report)
	}
}

// /mix show без вопроса объясняет, чего от него хотят.
func TestMixShowNeedsQuestion(t *testing.T) {
	m := newTestModel(t)
	m.runCommand("/mix show")
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
		t.Errorf("ожидалось объяснение, получено %v: %s", last.kind, last.text)
	}
}

// /mix без доводов показывает, как настроено подмешивание.
func TestMixWithoutArgsExplains(t *testing.T) {
	m := newTestModel(t)
	m.runCommand("/mix")
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.text, "/mix show") || !strings.Contains(last.text, "Числа отбора") {
		t.Errorf("ожидалось объяснение и числа отбора:\n%s", last.text)
	}
}
