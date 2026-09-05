package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Ключи перекрывают конфиг, а незаданные его не трогают.
//
// Проверка нужна ради замеров: смысл режима в том, чтобы гонять один вопрос
// при разных числах, и «ключ не подействовал» испортил бы весь прогон молча.
func TestAskSettingsOverrideConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Graph.NeighborSenseWeight = 0
	cfg.Graph.NeighborPool = 3
	cfg.KB.TopK = 8
	cfg.KB.SemanticWeight = 1.0
	cfg.Mix.Entities = 6

	s := askSettings(cfg, askOpts{})
	if s.Rank.SenseWeight != 0 || s.TopK != 8 || s.Entities != 6 {
		t.Errorf("без ключей должны действовать значения конфига: %+v", s)
	}

	s = askSettings(cfg, askOpts{
		SenseWeight: 1.5, HasSense: true, Pool: 8,
		TopK: 12, Entities: 3,
		SemanticWeight: 2, HasSemWeight: true,
	})
	if s.Rank.SenseWeight != 1.5 || s.Rank.Pool != 8 || s.TopK != 12 ||
		s.Entities != 3 || s.SemanticWeight != 2 {
		t.Errorf("ключи не перекрыли конфиг: %+v", s)
	}
	// Незаданные ключи не сбрасывают настройку в ноль.
	if s.MaxPerBook != cfg.KB.MaxPerBook {
		t.Errorf("max_per_book затёрт: %d вместо %d", s.MaxPerBook, cfg.KB.MaxPerBook)
	}
}

// Температура ноль по умолчанию: сравнивать ответы при чужой температуре
// значит мерить сэмплирование, а не настройку.
func TestAskOptionsDefaultTemperatureZero(t *testing.T) {
	srv := &config.Server{Options: map[string]any{"num_ctx": 4096}}

	opts := askOptions(srv, askOpts{})
	if opts["temperature"] != 0.0 {
		t.Errorf("температура по умолчанию = %v, ожидался 0", opts["temperature"])
	}
	if opts["num_ctx"] != 4096 {
		t.Error("настройки сервера должны сохраняться")
	}
	if _, ok := opts["seed"]; ok {
		t.Error("зерно без ключа задавать нельзя — сервер выберет своё")
	}

	opts = askOptions(srv, askOpts{Temperature: 0.7, HasTemp: true, Seed: 42, HasSeed: true, NumCtx: 32768})
	if opts["temperature"] != 0.7 || opts["seed"] != 42 || opts["num_ctx"] != 32768 {
		t.Errorf("ключи не применились: %v", opts)
	}
}

// Что подмешивать: ключ главнее конфига, «off» отключает всё.
func TestAskMixName(t *testing.T) {
	cfg := config.Default()
	cfg.Mix.Graph, cfg.Mix.Books = true, false

	if got := askMixName(cfg, askOpts{}); got != "graph" {
		t.Errorf("без ключа = %q, ожидалось graph", got)
	}
	if got := askMixName(cfg, askOpts{Mix: "off"}); got != "off" || askMixWanted(cfg, askOpts{Mix: "off"}) {
		t.Errorf("--mix off должен выключать подмес, получено %q", got)
	}
	if !askMixBooks(cfg, askOpts{Mix: "all"}) || !askMixGraph(cfg, askOpts{Mix: "all"}) {
		t.Error("--mix all включает и книги, и граф")
	}
	if askMixGraph(cfg, askOpts{Mix: "books"}) {
		t.Error("--mix books не должен включать граф")
	}
}

// Вопросы читаются из ключа и из файла; пустые строки пропускаются.
func TestAskQuestionsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.txt")
	if err := os.WriteFile(path, []byte("первый\n\n  второй  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := askQuestions(askOpts{Questions: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "первый" || got[1] != "второй" {
		t.Errorf("вопросы разобраны неверно: %q", got)
	}

	if got, _ = askQuestions(askOpts{Question: "один"}); len(got) != 1 || got[0] != "один" {
		t.Errorf("вопрос из ключа: %q", got)
	}
	if got, _ = askQuestions(askOpts{}); len(got) != 0 {
		t.Errorf("без вопросов ожидался пустой список, получено %q", got)
	}
}

// В строке json записано, чем считали: через неделю иначе не разобрать,
// какими ключами получена строка.
func TestAskSettingsMapRecordsNumbers(t *testing.T) {
	cfg := config.Default()
	o := askOpts{SenseWeight: 1.5, HasSense: true, Pool: 8, Mix: "graph", Seed: 3, HasSeed: true}
	m := askSettingsMap(o, askSettings(cfg, o), cfg)

	for _, k := range []string{"mix", "graph_sense", "graph_pool", "kb_topk", "temperature", "seed"} {
		if _, ok := m[k]; !ok {
			t.Errorf("в настройках нет %q: %v", k, m)
		}
	}
	if m["graph_sense"] != 1.5 || m["graph_pool"] != 8 || m["mix"] != "graph" {
		t.Errorf("настройки записаны неверно: %v", m)
	}
}
