package kb

import (
	"context"
	"testing"
)

// Локальная коллекция обязана удовлетворять интерфейсу источника без обёрток.
//
// Проверка не формальная. Смысл шва в том, что он **ничего не стоит** локальному
// пути: подписи интерфейса сняты с `*Collection`, и стоит кому-нибудь изменить
// их «для удобства удалённого драйвера», как локальному пути потребуется
// переходник — а это лишний слой на самом горячем месте программы.
func TestCollectionIsSource(t *testing.T) {
	var _ Source = (*Collection)(nil)
	var _ Library = (*Base)(nil)
}

// Base.Source отдаёт ту же коллекцию, что и Open.
//
// Два способа открыть одно и то же — повод разойтись. Здесь закреплено,
// что расхождения нет: Source это Open, приведённый к интерфейсу.
func TestBaseSourceReturnsSameCollection(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if _, err := base.Create("proba", ""); err != nil {
		t.Fatal(err)
	}

	direct, err := base.Open("proba")
	if err != nil {
		t.Fatal(err)
	}
	viaIface, err := base.Source("proba")
	if err != nil {
		t.Fatal(err)
	}
	if viaIface != Source(direct) {
		t.Error("Source и Open отдали разные коллекции — открытие раздвоилось")
	}
	if viaIface.Name() != "proba" {
		t.Errorf("имя коллекции через интерфейс = %q", viaIface.Name())
	}
}

// Несуществующая коллекция — ошибка, а не пустой источник.
//
// Пустой источник молча отвечал бы «ничего не нашлось» на каждый вопрос,
// и человек искал бы беду в книгах, а не в имени коллекции.
func TestBaseSourceRefusesUnknownCollection(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	src, err := base.Source("no-such")
	if err == nil {
		t.Error("ожидалась ошибка на неизвестной коллекции")
	}
	if src != nil {
		t.Error("при ошибке источник должен быть пустым")
	}
}

// Пустая коллекция через интерфейс отвечает пустой выдачей, а не падением.
//
// Так ведёт себя свежесозданная база до первой индексации, и удалённый
// драйвер обязан вести себя так же.
func TestSourceSearchOnEmptyCollection(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if _, err := base.Create("empty", ""); err != nil {
		t.Fatal(err)
	}
	src, err := base.Source("empty")
	if err != nil {
		t.Fatal(err)
	}

	hits, err := src.SearchWith(context.Background(), "что угодно", DefaultSearchOpts(), nil)
	if err != nil {
		t.Fatalf("поиск по пустой коллекции не должен быть ошибкой: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("в пустой коллекции нашлось %d фрагментов", len(hits))
	}
	if st := src.Stats(); st.Indexed != 0 {
		t.Errorf("в пустой коллекции книг %d", st.Indexed)
	}
	if len(src.Books()) != 0 {
		t.Errorf("в пустой коллекции реестр не пуст: %v", src.Books())
	}
}
