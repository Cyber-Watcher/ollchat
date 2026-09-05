package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// source — выдуманная коллекция: столько кусков, сколько нужно тесту.
type source struct {
	chunks []kb.ChunkInfo
}

func (s *source) ChunkCount() int { return len(s.chunks) }

func (s *source) EachChunkRef(f kb.ChunkFilter, fn func(kb.ChunkRef) error) error {
	var n int
	for _, c := range s.chunks {
		if f.PathContains != "" && !strings.Contains(c.Book.Path, f.PathContains) {
			continue
		}
		ref := kb.ChunkRef{Index: c.Index, Doc: c.Doc, Ord: c.Ord,
			UnitFrom: c.UnitFrom, UnitTo: c.UnitTo, Unit: c.Unit, Book: c.Book}
		if err := fn(ref); err != nil {
			return err
		}
		n++
		if f.Limit > 0 && n >= f.Limit {
			return nil
		}
	}
	return nil
}

func (s *source) ChunkTexts(indexes []int) (map[int]string, error) {
	out := make(map[int]string, len(indexes))
	for _, i := range indexes {
		for _, c := range s.chunks {
			if c.Index == i {
				out[i] = c.Text
				break
			}
		}
	}
	return out, nil
}

func chunksFor(n int, path string) *source {
	s := &source{}
	for i := 0; i < n; i++ {
		s.chunks = append(s.chunks, kb.ChunkInfo{
			Index: i, Doc: 1, Ord: uint32(i + 1), UnitFrom: i + 1, Unit: "стр.",
			Book: kb.BookRec{ID: 1, Title: "Проба", Path: path},
			Text: fmt.Sprintf("фрагмент номер %d про KV-кэш (KV cache) и контекстное окно (context window)", i),
		})
	}
	return s
}

// model — подменная модель: отвечает заранее заданным, считает вызовы.
type model struct {
	mu     sync.Mutex
	calls  int
	answer func(n int) (string, error)
	spy    func(user string) // необязательный: подсмотреть, что ушло модели
}

func (m *model) Model() string { return "проба" }

func (m *model) Extract(ctx context.Context, system, user string) (string, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	spy := m.spy
	m.mu.Unlock()
	if spy != nil {
		spy(user)
	}
	return m.answer(n)
}

const goodAnswer = `{"entities":[{"name":"KV-кэш","type":"понятие","aliases":["KV cache"]},
 {"name":"контекстное окно","type":"понятие"}],
 "relations":[{"src":"контекстное окно","dst":"KV-кэш","type":"влияет"}]}`

// Сборка наполняет граф.
func TestBuildFillsGraph(t *testing.T) {
	g, _ := graph(t)
	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}

	res, err := Build(context.Background(), chunksFor(10, "/AI/книга.pdf"), g, m,
		BuildOpts{Workers: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 10 || res.Skipped != 0 {
		t.Errorf("разобрано %d, пропущено %d", res.Done, res.Skipped)
	}
	// Одни и те же понятия во всех кусках — это две сущности, а не двадцать.
	if res.Entities != 2 {
		t.Errorf("сущностей = %d, ожидалось 2", res.Entities)
	}
	if res.Edges != 10 {
		t.Errorf("связей = %d, ожидалось 10 подтверждений одной связи", res.Edges)
	}
	kv, ok := g.Entities().Lookup("KV cache")
	if !ok {
		t.Fatal("сущность не найдена по синониму")
	}
	if got := g.Mentions().Of(kv.ID); len(got) != 10 {
		t.Errorf("упоминаний = %d, ожидалось 10", len(got))
	}
	window, ok := g.Entities().Lookup("контекстное окно")
	if !ok {
		t.Fatal("сущность «контекстное окно» не заведена")
	}
	neighbors := g.Edges().Neighbors(window.ID)
	if len(neighbors) != 1 || neighbors[0].Count != 10 {
		t.Errorf("соседи = %+v", neighbors)
	}
}

// Повторный запуск не должен переделывать сделанное: на библиотеке это дни.
func TestRestartResumesFromMark(t *testing.T) {
	g, dir := graph(t)
	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}
	src := chunksFor(10, "/AI/книга.pdf")

	if _, err := Build(context.Background(), src, g, m, BuildOpts{Workers: 1, Limit: 4}, nil); err != nil {
		t.Fatal(err)
	}
	if m.calls != 4 {
		t.Fatalf("вызовов модели = %d, ожидалось 4", m.calls)
	}
	g.Close()

	g2, err := Open(dir, 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	res, err := Build(context.Background(), src, g2, m, BuildOpts{Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 6 {
		t.Errorf("к разбору взято %d кусков, ожидалось 6 оставшихся", res.Total)
	}
	if m.calls != 10 {
		t.Errorf("всего вызовов = %d, ожидалось 10 — сделанное переразобрано", m.calls)
	}
}

// Модель, которая не смогла выдать JSON, не должна останавливать работу:
// кусок помечается пропущенным, и прогон идёт дальше.
func TestUnparsedAnswerSkipsChunk(t *testing.T) {
	g, _ := graph(t)
	m := &model{answer: func(n int) (string, error) {
		if n%2 == 0 {
			return "не могу помочь с этим", nil
		}
		return goodAnswer, nil
	}}
	res, err := Build(context.Background(), chunksFor(6, "/AI/к.pdf"), g, m,
		BuildOpts{Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 3 || res.Done != 6 {
		t.Errorf("пропущено %d из %d разобранных", res.Skipped, res.Done)
	}
	done, empty, skipped := g.Progress().Counts()
	if done != 3 || empty != 0 || skipped != 3 {
		t.Errorf("отметки = %d/%d/%d", done, empty, skipped)
	}
}

// Пустой кусок (оглавление, список литературы) — это результат, а не сбой.
func TestEmptyChunkMarkedEmpty(t *testing.T) {
	g, _ := graph(t)
	m := &model{answer: func(int) (string, error) {
		return `{"entities":[],"relations":[]}`, nil
	}}
	res, err := Build(context.Background(), chunksFor(3, "/AI/к.pdf"), g, m, BuildOpts{Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Empty != 3 || res.Entities != 0 {
		t.Errorf("пустых = %d, сущностей = %d", res.Empty, res.Entities)
	}
}

// Отвалившийся сервер — не повод пометить двадцать тысяч кусков пропущенными.
// Различается это по источнику ошибки, а не по её тексту: здесь ошибка нарочно
// написана словами, каких ни в одном списке признаков нет.
func TestDeadServerStopsBuild(t *testing.T) {
	g, _ := graph(t)
	m := &model{answer: func(n int) (string, error) {
		if n >= 3 {
			return "", errors.New("что-то пошло не так на той стороне")
		}
		return goodAnswer, nil
	}}
	res, err := Build(context.Background(), chunksFor(20, "/AI/к.pdf"), g, m, BuildOpts{Workers: 1}, nil)
	if err == nil {
		t.Fatal("работа продолжилась при мёртвом сервере")
	}
	if res.Done >= 20 {
		t.Errorf("разобрано %d кусков при мёртвом сервере", res.Done)
	}
	if _, _, skipped := g.Progress().Counts(); skipped > 0 {
		t.Errorf("куски помечены пропущенными из-за беды сервера: %d", skipped)
	}
}

// Отбор по каталогу.
func TestFolderFilter(t *testing.T) {
	g, _ := graph(t)
	src := &source{}
	src.chunks = append(chunksFor(3, "/AI/книга.pdf").chunks, chunksFor(4, "/Coding/другая.pdf").chunks...)
	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}

	res, err := Build(context.Background(), src, g, m, BuildOpts{Folder: "/AI/", Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 {
		t.Errorf("взято кусков = %d, ожидалось 3 из каталога AI", res.Total)
	}
}

// Сборка записывает модель в паспорт.
func TestBuildRecordsModelInMeta(t *testing.T) {
	g, _ := graph(t)
	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}
	if _, err := Build(context.Background(), chunksFor(1, "/AI/к.pdf"), g, m, BuildOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if g.Meta().Model != "проба" {
		t.Errorf("модель в паспорте = %q", g.Meta().Model)
	}
}

// modelWith — подменная модель с заданным именем: нужна проверке смены модели.
type modelWith struct {
	model
	name string
}

func (m *modelWith) Model() string { return m.name }

// Досборка чужой моделью отклоняется: смешанный граф выглядит исправным
// и не чинится ничем, кроме полной пересборки.
func TestModelChangeRefused(t *testing.T) {
	g, _ := graph(t)
	good := func(int) (string, error) { return goodAnswer, nil }

	firstModel := &modelWith{model: model{answer: good}, name: "перваяМодель"}
	if _, err := Build(context.Background(), chunksFor(4, "/AI/книга.pdf"), g, firstModel,
		BuildOpts{Workers: 2}, nil); err != nil {
		t.Fatalf("первая сборка: %v", err)
	}
	if got := g.Meta().Model; got != "перваяМодель" {
		t.Fatalf("в паспорте графа модель %q", got)
	}

	other := &modelWith{model: model{answer: good}, name: "другаяМодель"}
	_, err := Build(context.Background(), chunksFor(8, "/AI/книга.pdf"), g, other,
		BuildOpts{Workers: 2}, nil)
	if err == nil {
		t.Fatal("сборка чужой моделью должна была отказать")
	}
	for _, want := range []string{"перваяМодель", "другаяМодель", "заново"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в отказе нет %q: %v", want, err)
		}
	}

	// Осознанное решение возможно, но только явным разрешением.
	if _, err := Build(context.Background(), chunksFor(8, "/AI/книга.pdf"), g, other,
		BuildOpts{Workers: 2, AllowModelChange: true}, nil); err != nil {
		t.Fatalf("с явным разрешением сборка должна идти: %v", err)
	}
}

// Подстрока имени файла отбирает одну книгу: так книга переизвлекается
// в опытный граф без отдельного ключа (этап 90, пункт 3).
func TestFolderFilterByFileName(t *testing.T) {
	g, _ := graph(t)
	src := &source{}
	src.chunks = append(chunksFor(3, "/DevOps/NGINX HTTP Server 2024.pdf").chunks,
		chunksFor(4, "/DevOps/Kubernetes 2025.pdf").chunks...)
	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}

	res, err := Build(context.Background(), src, g, m, BuildOpts{Folder: "NGINX HTTP Server", Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 {
		t.Errorf("взято кусков = %d, ожидалось 3 одной книги", res.Total)
	}
}
