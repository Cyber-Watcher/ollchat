package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Служба не отвечает — свои файлы (этап 121, А4): kb.Fallback.

// stubSource — коллекция, отвечающая заранее заданным.
type stubSource struct {
	name string
	err  error
	hits []Result

	searches, arounds int
	ctxErr            error // состояние контекста, с которым пришёл поиск
}

func (s *stubSource) Name() string { return s.name }
func (s *stubSource) SearchWith(ctx context.Context, _ string, _ SearchOpts, _ Embedder) ([]Result, error) {
	s.searches++
	s.ctxErr = ctx.Err()
	if s.err != nil {
		return nil, s.err
	}
	return s.hits, nil
}
func (s *stubSource) Around(id string, _ int) ([]Result, error) {
	s.arounds++
	if s.err != nil {
		return nil, s.err
	}
	return []Result{{ID: id, Text: s.name}}, nil
}
func (s *stubSource) Books() []BookRec   { return nil }
func (s *stubSource) Stats() Stats       { return Stats{} }
func (s *stubSource) SearchNote() string { return "" }

// stubLibrary — набор коллекций с одной коллекцией или с отказом.
type stubLibrary struct {
	src   Source
	err   error
	names []string
	asked []string
}

func (l *stubLibrary) Names() ([]string, error) { return l.names, l.err }
func (l *stubLibrary) Source(name string) (Source, error) {
	l.asked = append(l.asked, name)
	if l.err != nil {
		return nil, l.err
	}
	return l.src, nil
}

// errDown — отказ в соединении так, как его отдаёт kbremote.
var errDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", ErrUnreachable)

// Служба не ответила на открытие — открывается своя коллекция, и источник
// об этом помнит.
func TestFallbackOpensOwnWhenServiceDown(t *testing.T) {
	local := &stubLibrary{src: &stubSource{name: "books"}}
	src, err := Fallback{Remote: &stubLibrary{err: errDown}, Local: local, Default: "books"}.Source("")
	if err != nil {
		t.Fatalf("при недоступной службе и своих файлах открытие не должно падать: %v", err)
	}
	if !Fell(src) {
		t.Error("источник не помнит, что искал у себя")
	}
	if len(local.asked) != 1 || local.asked[0] != "books" {
		t.Errorf("своя коллекция открыта как %v, ждали books (kb.default)", local.asked)
	}
}

// refSource — своя коллекция, умеющая отдать кусок по ссылке.
type refSource struct{ stubSource }

func (refSource) ChunkByRef(doc, ord uint32) (ChunkInfo, bool) {
	return ChunkInfo{Doc: doc, Ord: ord, Book: BookRec{Title: "Книга", Year: 2021}}, true
}

// На запасном пути кусок по ссылке — от своей коллекции: по нему карта
// печатает год связи (mix.relation_years). Пока отвечает служба, кусков по
// ссылке нет, как у неё самой.
func TestFallbackKeepsChunkByRef(t *testing.T) {
	type refs interface {
		ChunkByRef(doc, ord uint32) (ChunkInfo, bool)
	}
	local := &stubLibrary{src: &refSource{stubSource{name: "books"}}}
	src, err := Fallback{Remote: &stubLibrary{err: errDown}, Local: local, Default: "books"}.Source("")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := src.(refs)
	if !ok {
		t.Fatal("источник запасного пути прячет ChunkByRef своей коллекции")
	}
	if c, ok := r.ChunkByRef(3, 7); !ok || c.Book.Year != 2021 || c.Doc != 3 || c.Ord != 7 {
		t.Errorf("кусок по ссылке: %+v, %v", c, ok)
	}

	served, err := Fallback{Remote: &stubLibrary{src: &stubSource{name: "books"}}, Local: local}.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := served.(refs).ChunkByRef(3, 7); ok {
		t.Error("служба кусков по ссылке не отдаёт, а обёртка их выдумала")
	}
}

// Отказ по смыслу — ошибка, а не переход: свои файлы спрятали бы беду службы.
func TestFallbackKeepsRefusal(t *testing.T) {
	refusal := errors.New("на сервере http://kb.example:8377 нет коллекции \"bookz\"")
	local := &stubLibrary{src: &stubSource{name: "books"}}
	_, err := Fallback{Remote: &stubLibrary{err: refusal}, Local: local}.Source("bookz")
	if !errors.Is(err, refusal) {
		t.Errorf("отказ службы подменён: %v", err)
	}
	if len(local.asked) != 0 {
		t.Error("при отказе по смыслу открыта своя коллекция")
	}
}

// Своих файлов нет — остаётся ошибка службы: адрес и метка «не отвечает».
func TestFallbackWithoutOwnFiles(t *testing.T) {
	_, err := Fallback{Remote: &stubLibrary{err: errDown}}.Source("books")
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "kb.example") {
		t.Errorf("ошибка без своих файлов: %v", err)
	}
}

// Служба отвечала на открытие, но замолчала на поиске — тот же поиск идёт
// к своей коллекции, и с живым контекстом, хотя срок вызывающего истёк:
// его и съела служба.
func TestFallbackMidSearch(t *testing.T) {
	remote := &stubSource{name: "books", err: errDown}
	own := &stubSource{name: "books", hits: []Result{{ID: "books/1#1"}}}
	src, err := Fallback{Remote: &stubLibrary{src: remote}, Local: &stubLibrary{src: own}}.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	if Fell(src) {
		t.Fatal("служба ответила на открытие, а источник считает, что искал у себя")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	hits, err := src.SearchWith(ctx, "мьютекс", SearchOpts{}, nil)
	if err != nil || len(hits) != 1 {
		t.Fatalf("поиск у себя: %v, %v", hits, err)
	}
	if own.ctxErr != nil {
		t.Errorf("поиск у себя получил истёкший контекст: %v", own.ctxErr)
	}
	if !Fell(src) {
		t.Error("после перехода источник не помнит, что искал у себя")
	}
	// Дальше — сразу у себя: молчащую службу второй раз не ждут.
	if _, err := src.Around("books/1#1", 1); err != nil {
		t.Fatal(err)
	}
	if remote.searches != 1 || remote.arounds != 0 || own.arounds != 1 {
		t.Errorf("служба: поисков %d, чтений %d; свои: чтений %d", remote.searches, remote.arounds, own.arounds)
	}
}

// Имя не названо и kb.default пуст — своя коллекция единственная или никакая.
func TestFallbackPicksSingleOwnCollection(t *testing.T) {
	one := &stubLibrary{src: &stubSource{name: "only"}, names: []string{"only"}}
	if _, err := (Fallback{Remote: &stubLibrary{err: errDown}, Local: one}).Source(""); err != nil {
		t.Fatal(err)
	}
	if len(one.asked) != 1 || one.asked[0] != "only" {
		t.Errorf("открыта %v, ждали единственную", one.asked)
	}

	two := &stubLibrary{src: &stubSource{name: "a"}, names: []string{"a", "b"}}
	_, err := Fallback{Remote: &stubLibrary{err: errDown}, Local: two}.Source("")
	if err == nil || !strings.Contains(err.Error(), "kb.default") {
		t.Errorf("при двух своих коллекциях без kb.default ждали объяснение: %v", err)
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("причина «служба не отвечает» потерялась: %v", err)
	}
}
