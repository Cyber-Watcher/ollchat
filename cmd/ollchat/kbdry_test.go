package main

import (
	"strings"
	"testing"
	"time"

	kmaint "github.com/Cyber-Watcher/ollchat/internal/kb/maint"
)

// Сухой прогон обязан быть сухим при любой команде.
//
// 29.08.2026 --kb-sync --kb-dry-run доиндексировал 49 книг по-настоящему:
// ключ действовал только на --kb-embed, а к остальным молча не применялся.
// Человек просил оценку, а получил работу.
func TestDryRunFlagCoversIndexCommands(t *testing.T) {
	// Проверяется описание ключа: оно и есть обещание пользователю.
	// Если команду добавили, а в описание не внесли — тест напомнит.
	for _, cmd := range []string{"--kb-embed", "--kb-sync", "--kb-index", "--kb-refresh", "--kb-rebase"} {
		if !strings.Contains(dryRunFlagHelp, cmd) {
			t.Errorf("в описании --kb-dry-run не упомянута команда %s", cmd)
		}
	}
}

// Срок оценки короткий: человек спрашивает «сколько займёт» и ждёт ответа
// быстрее, чем за минуту работы.
func TestEstimateTimeoutIsShort(t *testing.T) {
	if kmaint.EstimateTimeout > 2*time.Minute {
		t.Errorf("срок оценки %v слишком велик", kmaint.EstimateTimeout)
	}
	if kmaint.EstimateTimeout < 10*time.Second {
		t.Errorf("срок оценки %v слишком мал: первый запрос грузит модель", kmaint.EstimateTimeout)
	}
}
