package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/itpro/ollchat/internal/config"
)

// Запрос к серверу живёт до 30 секунд, а переключить сервер можно в любой
// момент. Ответ брошенного сервера не должен ни подписываться именем нового,
// ни затирать его состояние.
//
// Случай из жизни: при старте выбран недоступный сервер (VPN лежит),
// пользователь переключается на рабочий через ssh-туннель, и через полминуты
// прилетает «сервер sshlab: ... 192.168.77.77 ... context deadline exceeded» —
// адрес чужой, имя текущее, туннель ни при чём.

// twoServers добавляет в конфиг второй сервер, как в жизни у пользователя:
// адреса разные, а модель называется одинаково.
const sharedModel = "qwen3.5:122b"

func twoServers(cfg *config.Config) {
	cfg.Servers[0].Model = sharedModel
	cfg.Servers = append(cfg.Servers, config.Server{
		Name:  "sshlab",
		URL:   "http://localhost:11111",
		Model: sharedModel,
	})
}

func TestStaleServerErrorIsNotShownUnderNewName(t *testing.T) {
	m := newTestModelWith(t, twoServers)
	stale := m.srvGen // поколение первого сервера

	m.switchServer("sshlab")

	m.Update(serverInfoMsg{gen: stale,
		err: errors.New(`GET /api/version: Get "http://192.168.77.77:11434/api/version": context deadline exceeded`)})

	for _, b := range m.blocks {
		if b.kind == blockError && strings.Contains(b.text, "192.168.77.77") {
			t.Fatalf("ошибка брошенного сервера попала в ленту: %q", b.text)
		}
	}
}

// Удавшийся, но запоздавший ответ опаснее ошибки: он молча подменяет данные
// уже выбранного сервера.
func TestStaleServerInfoDoesNotOverwriteState(t *testing.T) {
	m := newTestModelWith(t, twoServers)
	stale := m.srvGen

	m.switchServer("sshlab")
	m.Update(serverInfoMsg{gen: m.srvGen, version: "0.32.7",
		models: modelList("живая:latest")})

	// Запоздавший ответ прежнего сервера с другой версией и другим списком.
	m.Update(serverInfoMsg{gen: stale, version: "0.1.0",
		models: modelList("чужая:latest")})

	if m.srvVersion != "0.32.7" {
		t.Errorf("версия сервера подменена ответом брошенного: %q", m.srvVersion)
	}
	if got := len(m.models); got != 1 || m.models[0].Name != "живая:latest" {
		t.Errorf("список моделей подменён ответом брошенного сервера: %v", pickerNames(m))
	}
}

// Имени модели для отсева мало: на разных серверах она зовётся одинаково —
// ровно так и настроено у пользователя.
func TestStaleModelInfoRejectedEvenWithSameModelName(t *testing.T) {
	m := newTestModelWith(t, twoServers)
	stale := m.srvGen

	m.switchServer("sshlab")
	sameName := m.modelName
	if sameName != sharedModel {
		t.Fatalf("подготовка: на обоих серверах модель %q, стало %q", sharedModel, sameName)
	}
	m.Update(modelInfoMsg{gen: m.srvGen, model: sameName,
		caps: []string{"completion", "tools", "vision"}, capacity: 262144})

	// Ответ прежнего сервера про модель с тем же именем, но другой ёмкостью.
	m.Update(modelInfoMsg{gen: stale, model: sameName,
		caps: []string{"completion"}, capacity: 4096})

	if !hasCap(m.modelCaps, "vision") {
		t.Errorf("возможности модели подменены ответом брошенного сервера: %v", m.modelCaps)
	}
	if m.meter.Capacity != 262144 {
		t.Errorf("ёмкость окна подменена ответом брошенного сервера: %d", m.meter.Capacity)
	}
}

func TestStaleModelsListIgnored(t *testing.T) {
	m := newTestModelWith(t, twoServers)
	stale := m.srvGen

	m.switchServer("sshlab")
	m.Update(modelsMsg{gen: stale, models: modelList("чужая:latest"), action: modelsOpenPicker})

	if m.picker != nil {
		t.Error("список выбора не должен открываться по ответу брошенного сервера")
	}
}

// Свои ответы после переключения проходят как обычно.
func TestFreshServerInfoAccepted(t *testing.T) {
	m := newTestModelWith(t, twoServers)
	m.switchServer("sshlab")

	m.Update(serverInfoMsg{gen: m.srvGen, version: "0.32.7", models: modelList("своя:latest")})

	if m.srvVersion != "0.32.7" {
		t.Errorf("ответ текущего сервера отброшен, версия %q", m.srvVersion)
	}
}

func pickerNames(m *Model) []string {
	out := make([]string, 0, len(m.models))
	for _, mi := range m.models {
		out = append(out, mi.Name)
	}
	return out
}
