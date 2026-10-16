package ui

import "testing"

// У9 (16.10.2026): при kb.followup_concat подмешивание ищет по «предыдущий
// вопрос + реплика», если реплика — продолжение; выключено — по самой реплике.
// Предыдущий вопрос — прошлый блок человека: текущий уже в ленте.
func TestFollowupQueryForMix(t *testing.T) {
	m := newTestModel(t)
	m.addBlock(block{kind: blockUser, text: "Хочу мониторить сервер Ubuntu через Grafana."})
	m.addBlock(block{kind: blockAssistant, text: "Поставьте Prometheus и node_exporter."})
	reply := "а как настроить сбор логов с сервера?"
	m.addBlock(block{kind: blockUser, text: reply})

	if q := m.followupQuery(reply); q != "" {
		t.Fatalf("ключ выключен, а запрос склеен: %q", q)
	}
	m.cfg.KB.FollowupConcat = true
	if q := m.followupQuery(reply); q != "Хочу мониторить сервер Ubuntu через Grafana. "+reply {
		t.Errorf("продолжение не склеено с прошлым вопросом: %q", q)
	}

	long := "Какие функции OpenCV используются для аффинных преобразований изображения?"
	m.addBlock(block{kind: blockUser, text: long})
	if q := m.followupQuery(long); q != "" {
		t.Errorf("смена темы склеена: %q", q)
	}

	first := newTestModel(t)
	first.cfg.KB.FollowupConcat = true
	first.addBlock(block{kind: blockUser, text: "а дальше?"})
	if q := first.followupQuery("а дальше?"); q != "" {
		t.Errorf("первый вопрос сеанса склеен ни с чем: %q", q)
	}
}
