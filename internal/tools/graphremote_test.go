package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// fakeLibrary — общая библиотека, до которой дело не доходит: проверяется
// только сам факт, что она настроена.
type fakeLibrary struct{}

func (fakeLibrary) Names() ([]string, error)         { return []string{"books"}, nil }
func (fakeLibrary) Source(string) (kb.Source, error) { return nil, errors.New("не нужно") }

// При настроенной общей библиотеке отказ графа объясняет причину.
//
// Без этого человек получал бы «коллекции books нет» и шёл искать беду
// в книгах, хотя книги находятся, а нет именно графа. Ложный след дороже
// самого отказа.
func TestGraphRefusalExplainsSharedLibrary(t *testing.T) {
	err := graphOverNetwork(Options{Library: fakeLibrary{}}, "books",
		errors.New(`коллекции "books" нет`))
	if err == nil {
		t.Fatal("ожидалось объяснение")
	}
	msg := err.Error()
	for _, want := range []string{"граф", "общую библиотеку", "kb_search"} {
		if !strings.Contains(msg, want) {
			t.Errorf("в объяснении нет %q:\n%s", want, msg)
		}
	}
}

// Без общей библиотеки отказ остаётся прежним: лишних слов там не нужно.
func TestGraphRefusalUnchangedWithoutLibrary(t *testing.T) {
	orig := errors.New(`коллекции "books" нет`)
	if got := graphOverNetwork(Options{}, "books", orig); got != orig {
		t.Errorf("отказ подменён без нужды: %v", got)
	}
}

// fakeCaller — общая библиотека, умеющая выполнять инструменты графа.
type fakeCaller struct {
	fakeLibrary
	gotName string
	gotArgs map[string]any
	gotColl string
}

func (f *fakeCaller) GraphTool(_ context.Context, coll, name string, args map[string]any) (string, error) {
	f.gotColl, f.gotName, f.gotArgs = coll, name, args
	return "ответ службы", nil
}

// При общей библиотеке инструменты графа уезжают на службу, а описания
// для модели и разбор доводов остаются местными.
//
// Проверка нужна именно на реестре: подмена делается там, и молчаливо
// не сработавшая подмена означала бы, что клиент ищет по несуществующим
// локальным файлам и получает отказ вместо ответа.
func TestGraphToolsGoToSharedLibrary(t *testing.T) {
	caller := &fakeCaller{}
	r, err := NewRegistry([]string{NameGraphSearch, NameKBSearch}, Options{
		Library: caller, KBDefault: "books",
	})
	if err != nil {
		t.Fatal(err)
	}

	local, err := NewRegistry([]string{NameGraphSearch}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Описание для модели обязано совпасть с местным слово в слово: иначе
	// модель на клиенте станет звать инструмент не так, как на сервере.
	if r.spec(NameGraphSearch) != local.spec(NameGraphSearch) {
		t.Error("описание инструмента разошлось с местным")
	}

	plan, err := r.Plan(NameGraphSearch, map[string]any{"query": "горутины"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out != "ответ службы" {
		t.Errorf("работа не уехала на службу: %q", out)
	}
	if caller.gotName != NameGraphSearch || caller.gotColl != "books" {
		t.Errorf("на службу ушло не то: инструмент %q, коллекция %q", caller.gotName, caller.gotColl)
	}
	if caller.gotArgs["query"] != "горутины" {
		t.Errorf("доводы потерялись: %v", caller.gotArgs)
	}
}

// Без общей библиотеки инструменты графа остаются местными.
func TestGraphToolsStayLocalWithoutLibrary(t *testing.T) {
	r, err := NewRegistry([]string{NameGraphSearch}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.tools[NameGraphSearch].(*remoteGraphTool); ok {
		t.Error("без общей библиотеки инструмент не должен быть удалённым")
	}
}
