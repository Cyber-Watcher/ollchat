package graph

import (
	"context"
	"errors"
	"testing"
)

// stubExtractor отдаёт заранее заданные ответы по очереди.
type stubExtractor struct {
	answers []string
	errs    []error
	calls   int
}

func (s *stubExtractor) Model() string { return "заглушка" }

func (s *stubExtractor) Extract(context.Context, string, string) (string, error) {
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return "", s.errs[i]
	}
	if i < len(s.answers) {
		return s.answers[i], nil
	}
	return "", ErrEmptyAnswer
}

// Пустой ответ — отказ на этом куске, а не беда дороги: заход обязан
// продолжиться, пропустив кусок. Раньше он валил весь заход.
func TestEmptyAnswerSkipsChunkNotRun(t *testing.T) {
	ex := &stubExtractor{errs: []error{ErrEmptyAnswer, ErrEmptyAnswer}}
	_, err, bad := askModel(context.Background(), ex, SystemPrompt, "книга", "стр.", 1, 2, "текст", true)
	if err == nil {
		t.Fatal("ошибка должна остаться: кусок не разобран")
	}
	if !bad {
		t.Fatal("пустой ответ обязан считаться дурным ответом, иначе он валит весь заход")
	}
}

// Пустота на первой попытке, годный ответ на второй — кусок разбирается.
func TestEmptyAnswerRetriedOnce(t *testing.T) {
	ex := &stubExtractor{
		errs:    []error{ErrEmptyAnswer, nil},
		answers: []string{"", `{"entities":[{"name":"горутина","type":"понятие"}],"relations":[]}`},
	}
	facts, err, bad := askModel(context.Background(), ex, SystemPrompt, "книга", "стр.", 1, 2, "текст", true)
	if err != nil || bad {
		t.Fatalf("повтор должен был удаться: err=%v bad=%v", err, bad)
	}
	if len(facts.Entities) != 1 {
		t.Errorf("разобрано %d понятий, ожидалось 1", len(facts.Entities))
	}
}

// Беда дороги остаётся бедой дороги: она обязана останавливать заход,
// иначе выключенный сервер пометит полбиблиотеки пропущенной.
func TestTransportErrorStillStopsRun(t *testing.T) {
	ex := &stubExtractor{errs: []error{errors.New("connection refused")}}
	_, err, bad := askModel(context.Background(), ex, SystemPrompt, "книга", "стр.", 1, 2, "текст", true)
	if err == nil {
		t.Fatal("ошибка дороги должна вернуться")
	}
	if bad {
		t.Error("ошибка дороги не должна выглядеть дурным ответом модели")
	}
}
