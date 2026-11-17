package oai

import (
	"net/http"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Поле user уходит в каждый запрос чата, когда задано WithUser, — по нему шлюз
// LiteLLM ведёт учёт конечных пользователей; без WithUser поля нет вовсе.
func TestUserFieldSentOnlyWhenSet(t *testing.T) {
	const reply = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	req := ollama.ChatRequest{Model: "m", Messages: []ollama.Message{{Role: ollama.RoleUser, Content: "привет"}}}

	srv, cp := interceptor(t, http.StatusOK, reply)
	chatOnce(New(srv.URL, "", 0, 0, 0, nil).WithUser(" ivanov "), req)
	cp.mu.Lock()
	got := cp.body["user"]
	cp.mu.Unlock()
	if got != "ivanov" {
		t.Errorf("user в теле запроса = %v, ждали ivanov (пробелы срезаны)", got)
	}

	srv2, cp2 := interceptor(t, http.StatusOK, reply)
	chatOnce(New(srv2.URL, "", 0, 0, 0, nil), req)
	cp2.mu.Lock()
	_, has := cp2.body["user"]
	cp2.mu.Unlock()
	if has {
		t.Error("без WithUser поле user уходит в запрос")
	}
}
