package ui

import (
	"errors"
	"strings"
	"testing"
)

// Сообщение о недоступном эмбеддере обязано говорить три вещи: что сломалось,
// чем это обернётся и что делать. Без второй части предупреждение пропускают.
func TestEmbedCheckNoteExplainsConsequence(t *testing.T) {
	note := embedCheckNote("bge-m3", errors.New("connection refused"))

	for _, want := range []string{"bge-m3", "connection refused"} {
		if !strings.Contains(note, want) {
			t.Errorf("в сообщении нет %q:\n%s", want, note)
		}
	}
	if !strings.Contains(note, "только по совпадению слов") {
		t.Errorf("не сказано, чем обернётся:\n%s", note)
	}
	if !strings.Contains(note, "kb.embed_url") {
		t.Errorf("не сказано, что проверить при недоступном сервере:\n%s", note)
	}
}

// Разные причины — разный совет: нет модели и нет сервера лечатся по-разному.
func TestEmbedCheckNoteTellsCauseApart(t *testing.T) {
	missing := embedCheckNote("bge-m3", errors.New(`model "bge-m3" not found`))
	if !strings.Contains(missing, "ollama pull bge-m3") {
		t.Errorf("для отсутствующей модели ожидался совет её скачать:\n%s", missing)
	}

	down := embedCheckNote("bge-m3", errors.New("dial tcp: no such host"))
	if strings.Contains(down, "ollama pull") {
		t.Errorf("недоступный сервер не лечится скачиванием модели:\n%s", down)
	}

	other := embedCheckNote("bge-m3", errors.New("что-то пошло не так"))
	if !strings.Contains(other, "kb.embed_model") {
		t.Errorf("на непонятную ошибку ожидался общий совет:\n%s", other)
	}
}

// Проверка не затевается там, где смысловой поиск и не предполагался:
// нет модели эмбеддингов в настройках — нет и проверки.
func TestEmbedCheckSkippedWithoutEmbedder(t *testing.T) {
	m := newTestModel(t)
	if cmd := m.checkEmbedderCmd(); cmd != nil {
		t.Error("без настроенного эмбеддера проверять нечего")
	}
}

// Индикатор молчит, пока проверка не прошла: пустое место лучше, чем «неизвестно».
func TestEmbedStatusSilentUntilChecked(t *testing.T) {
	m := newTestModel(t)
	if name, _ := m.embedStatus(); name != "" {
		t.Errorf("до проверки индикатора быть не должно, получено %q", name)
	}

	m.embedModel, m.embed = "bge-m3", embedReady
	name, state := m.embedStatus()
	if name != "bge-m3" || state != embedReady {
		t.Errorf("после успешной проверки ожидалось имя модели и состояние «отвечает», получено %q/%v", name, state)
	}
}

// О поломке говорится один раз на переход, а не на каждую проверку: раз в минуту
// повторять одно и то же — значит приучить не читать предупреждения.
func TestEmbedFailureReportedOncePerTransition(t *testing.T) {
	m := newTestModel(t)
	count := func() int {
		n := 0
		for _, b := range m.blocks {
			if b.kind == blockHint && strings.Contains(b.text, "не отвечает") {
				n++
			}
		}
		return n
	}

	fail := embedCheckMsg{model: "bge-m3", err: errors.New("connection refused")}
	m.Update(fail)
	m.Update(fail)
	m.Update(fail)
	if got := count(); got != 1 {
		t.Errorf("о поломке сказано %d раз, ожидался один", got)
	}
	if m.embed != embedDown {
		t.Error("состояние должно быть «не отвечает»")
	}

	// Вернулась — об этом сказать надо, иначе человек не узнает, что можно
	// снова доверять поиску.
	m.Update(embedCheckMsg{model: "bge-m3"})
	if m.embed != embedReady {
		t.Error("после успешной проверки состояние должно стать «отвечает»")
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.text, "снова отвечает") {
		t.Errorf("о возвращении модели не сказано:\n%s", last.text)
	}
}

// Значка второй ступени нет, пока переранжирование не настроено: пустой
// kb.rerank_url означает «не хотим», и красный значок сообщал бы о поломке
// там, где ничего не ломалось.
func TestRerankIndicatorHiddenWhenNotConfigured(t *testing.T) {
	m := newTestModel(t)
	if cmd := m.checkRerankerCmd(); cmd != nil {
		t.Error("без kb.rerank_url проверять нечего")
	}
	if show, _ := m.rerankStatus(); show {
		t.Error("значка не должно быть, пока проверка не прошла")
	}
}

// Значок появляется после проверки и меняет состояние; о поломке говорится
// один раз на переход.
func TestRerankIndicatorTracksState(t *testing.T) {
	m := newTestModel(t)

	fail := rerankCheckMsg{model: "bge-reranker-v2-m3", err: errors.New("connection refused")}
	m.Update(fail)
	m.Update(fail)

	show, state := m.rerankStatus()
	if !show || state != embedDown {
		t.Fatalf("после неудачи ожидался красный значок, получено show=%v state=%v", show, state)
	}
	n := 0
	for _, b := range m.blocks {
		if b.kind == blockHint && strings.Contains(b.text, "переранжирования") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("о поломке сказано %d раз, ожидался один", n)
	}

	m.Update(rerankCheckMsg{model: "bge-reranker-v2-m3"})
	if _, state = m.rerankStatus(); state != embedReady {
		t.Error("после успешной проверки значок должен стать зелёным")
	}
}

// Тон сообщения о реранкере мягче: без него нужное находится, только не первым.
func TestRerankNoteSaysItIsNotFatal(t *testing.T) {
	note := rerankCheckNote("bge-reranker-v2-m3", errors.New("no such host"))
	if !strings.Contains(note, "Поиск работает") {
		t.Errorf("надо сказать, что поиск не сломан:\n%s", note)
	}
	if !strings.Contains(note, "kb.rerank_url") {
		t.Errorf("надо сказать, что проверить:\n%s", note)
	}
}
