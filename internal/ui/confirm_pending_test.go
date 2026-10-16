package ui

import (
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
)

// Аудит 16.10.2026, находка 10: с очередью реплик человек жмёт Enter, чтобы
// отправить набранное; окно подтверждения, появившееся за время раздумий,
// Enter при непустом поле не принимает — только букву.
func TestConfirmEnterWithPendingTextDoesNotAnswer(t *testing.T) {
	m, clk := confirmModel(t)
	m.ta.SetValue("и ещё прогони тесты после правки")
	reply := askConfirm(m, "git push origin main")
	clk.advance(700 * time.Millisecond)
	m.Update(confirmQuietMsg{seq: m.confirmSeq})
	m.Update(keyPress("enter"))
	if a, ok := answerOf(reply); ok {
		t.Fatalf("Enter при тексте в поле ответил окну: %v", a)
	}
	if m.confirm == nil {
		t.Fatal("окно закрылось без ответа")
	}
	m.Update(keyPress("y"))
	if a, ok := answerOf(reply); !ok || a != agent.AnswerYes {
		t.Errorf("буква «y» при тексте в поле не ответила «да»: %v, %v", a, ok)
	}
	// Пустое поле — Enter по-прежнему «да».
	m2, clk2 := confirmModel(t)
	reply2 := askConfirm(m2, "git push origin main")
	clk2.advance(700 * time.Millisecond)
	m2.Update(confirmQuietMsg{seq: m2.confirmSeq})
	m2.Update(keyPress("enter"))
	if a, ok := answerOf(reply2); !ok || a != agent.AnswerYes {
		t.Errorf("Enter при пустом поле: %v, %v — ждали «да»", a, ok)
	}
}
