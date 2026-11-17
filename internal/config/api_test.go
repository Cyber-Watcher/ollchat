package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Сервер с OpenAI-совместимым API (этап 127): поля читаются, ~ в api_key_file
// раскрывается, нынешние конфиги без поля api остаются серверами Ollama.
func TestServerAPIOpenAIFields(t *testing.T) {
	cfg, err := writeConfig(t, `
[[servers]]
name = "vllm"
url  = "http://vllm.example:8000/"
model = "qwen38"
api = "openai"
api_key_file = "~/.config/ollchat/vllm.key"
context_length = 32768
`)
	if err != nil {
		t.Fatalf("конфиг с api = openai не прочитался: %v", err)
	}
	s, ok := cfg.ServerByName("vllm")
	if !ok {
		t.Fatal("сервер vllm не найден")
	}
	if !s.IsOpenAI() || s.ContextLength != 32768 {
		t.Errorf("api/context_length: IsOpenAI=%v context_length=%d", s.IsOpenAI(), s.ContextLength)
	}
	if strings.HasPrefix(s.APIKeyFile, "~") || !strings.HasSuffix(s.APIKeyFile, "/.config/ollchat/vllm.key") {
		t.Errorf("api_key_file не раскрыт: %q", s.APIKeyFile)
	}
	if s.URL != "http://vllm.example:8000" {
		t.Errorf("url не приведён к виду без косой черты: %q", s.URL)
	}
	local, _ := cfg.ServerByName("local")
	if local.IsOpenAI() {
		t.Error("сервер без поля api обязан остаться сервером Ollama")
	}
}

// Порог приоритета без значения — умолчание 10 (больше — позже у vLLM).
func TestServerPriorityDefault(t *testing.T) {
	cfg, err := writeConfig(t, "\n[[servers]]\nname = \"gw\"\nurl = \"http://gw.example:4000\"\napi = \"openai\"\n"+
		"priority_long_tokens = 16000\n")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := cfg.ServerByName("gw")
	if s.PriorityLongTokens != 16000 || s.PriorityLong != 10 {
		t.Errorf("priority_long_tokens %d, priority_long %d — ждали 16000 и 10", s.PriorityLongTokens, s.PriorityLong)
	}
}

// Ошибки настройки — отказ загрузки с понятной строкой, а не молчаливый Ollama.
func TestServerAPIRefusals(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"неизвестный api", `api = "anthropic"`, `api = "anthropic"`},
		{"ключ у сервера Ollama", `api_key_env = "K"`, "только для api"},
		{"оба источника ключа", "api = \"openai\"\napi_key_env = \"K\"\napi_key_file = \"/k\"", "оставьте одно"},
		{"отрицательное окно", "api = \"openai\"\ncontext_length = -1", "context_length"},
		{"приоритет у сервера Ollama", "priority_long_tokens = 16000", "у API Ollama поля priority нет"},
		{"отрицательный порог приоритета", "api = \"openai\"\npriority_long_tokens = -1", "priority_long_tokens"},	}
	for _, c := range cases {
		_, err := writeConfig(t, "\n[[servers]]\nname = \"x\"\nurl = \"http://x.example:8000\"\n"+c.body+"\n")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ждали отказ со словами %q, получили %v", c.name, c.want, err)
		}
	}
}

// Сервер "openai" не может собирать граф и считать векторы: паспорт весов
// сверяется по digest из /api/tags и /api/show, а у /v1/models его нет
// (решение владельца 17.11.2026 — граница этапа 127 «только чат»).
func TestServerAPIOpenAINotForGraphOrEmbed(t *testing.T) {
	srv := "\n[[servers]]\nname = \"vllm\"\nurl = \"http://vllm.example:8000\"\napi = \"openai\"\n"
	for _, c := range []struct{ name, body, want string }{
		{"граф", "[graph]\nserver = \"vllm\"\n" + srv, "граф собирает только Ollama"},
		{"векторы", "[kb]\nembed_server = \"vllm\"\n" + srv, "векторы считает только Ollama"},
	} {
		_, err := writeConfig(t, c.body)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s на сервере openai: ждали отказ «%s», получили %v", c.name, c.want, err)
		}
	}
	// Тот же сервер для чата — законно.
	cfg, err := writeConfig(t, "[general]\ndefault_server = \"vllm\"\n"+srv)
	if err != nil {
		t.Fatalf("сервер openai для чата отклонён: %v", err)
	}
	if cfg.General.DefaultServer != "vllm" {
		t.Errorf("сервер чата %q, ждали vllm", cfg.General.DefaultServer)
	}
}

// Загрузка конфига ключ не читает: файл ключа с правами 644 не ломает запуск
// тем, кто этот сервер не выбрал. Права и пустоту проверяет клиент чата при
// чтении ключа (mcpclient.TokenAs) — там и отказ.
func TestServerAPIKeyNotReadAtLoad(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("sk-secret-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := writeConfig(t, "\n[[servers]]\nname = \"gw\"\nurl = \"http://gw.example:4000\"\napi = \"openai\"\n"+
		"api_key_file = \""+key+"\"\n")
	if err != nil {
		// Права файла ключа конфиг не проверяет — их проверяет чтение ключа
		// клиентом чата; загрузка с любым api_key_file проходит.
		t.Fatalf("загрузка конфига не должна читать ключ: %v", err)
	}
}
