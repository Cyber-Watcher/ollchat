package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Журнал причин пропуска проверяется на том, ради чего он заведён: по нему
// через сутки должно быть видно, КАКАЯ книга потеряла куски и ПОЧЕМУ.
// До 27.09.2026 в итоге стояло одно число «пропущено N», и разобрать
// 107 потерянных кусков было нечем.

func TestSkipLogWritesReasonAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	s := openSkipLog(dir)
	if n := s.Count(); n != 0 {
		t.Fatalf("новый журнал не пуст: %d", n)
	}
	// Файла у коллекции без пропусков не должно быть вовсе.
	if _, err := os.Stat(filepath.Join(dir, skipLogFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("журнал создан до первой записи: %v", err)
	}

	recs := []SkipRec{
		{Doc: 7, Ord: 12, Book: "Библия C#", Unit: "стр. 40", Kind: SkipParse, Why: "разбор ответа: ключа entities нет"},
		{Doc: 7, Ord: 13, Book: "Библия C#", Unit: "стр. 41", Kind: SkipEmptyAnswer, Why: "пустой ответ"},
		{Doc: 9, Ord: 1, Book: "Agentic DevOps", Kind: SkipParse, Why: "разбор ответа: обрыв JSON"},
	}
	for _, r := range recs {
		if err := s.Add(r); err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	if n := s.Count(); n != 3 {
		t.Fatalf("после трёх записей в журнале %d", n)
	}

	// Перечитанный с диска журнал обязан давать то же самое: иначе через
	// сутки (другой процесс) причины не увидеть, а именно для этого он есть.
	again := openSkipLog(dir)
	if n := again.Count(); n != 3 {
		t.Fatalf("перечитанный журнал: %d записей вместо 3", n)
	}
	if p := again.Problem(); p != "" {
		t.Fatalf("беда чтения на целом журнале: %s", p)
	}
	kinds := again.Kinds(0)
	if len(kinds) != 2 || kinds[0].Kind != SkipParse || kinds[0].Count != 2 {
		t.Fatalf("виды причин по убыванию: %+v", kinds)
	}
	books := again.Books(0)
	if len(books) != 2 || books[0].Kind != "Библия C#" || books[0].Count != 2 {
		t.Fatalf("книги по убыванию: %+v", books)
	}
}

func TestSkipLogKindsSinceCutsOldRecords(t *testing.T) {
	dir := t.TempDir()
	s := openSkipLog(dir)
	old := time.Now().Add(-48 * time.Hour).Unix()
	if err := s.Add(SkipRec{Doc: 1, Ord: 1, Kind: SkipParse, At: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(SkipRec{Doc: 1, Ord: 2, Kind: SkipEmptyAnswer}); err != nil {
		t.Fatal(err)
	}
	// Сводка после книги обязана показывать пропуски ЭТОЙ книги, а не годовые:
	// иначе числа итога не сойдутся с числом «пропущено».
	since := time.Now().Add(-time.Hour).Unix()
	kinds := s.Kinds(since)
	if len(kinds) != 1 || kinds[0].Kind != SkipEmptyAnswer || kinds[0].Count != 1 {
		t.Fatalf("отбор по времени не сработал: %+v", kinds)
	}
	if all := s.Kinds(0); len(all) != 2 {
		t.Fatalf("без отбора должно быть два вида: %+v", all)
	}
}

func TestSkipLogSurvivesTruncatedLastLine(t *testing.T) {
	dir := t.TempDir()
	s := openSkipLog(dir)
	if err := s.Add(SkipRec{Doc: 1, Ord: 1, Kind: SkipParse, Why: "целая запись"}); err != nil {
		t.Fatal(err)
	}
	// Падение на записи оставляет обрезанную строку. Терять из-за неё весь
	// журнал нельзя: причины прошлых книг — единственный их след.
	f, err := os.OpenFile(filepath.Join(dir, skipLogFile), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"doc":2,"ord":2,"kind":"отв`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	again := openSkipLog(dir)
	if n := again.Count(); n != 1 {
		t.Fatalf("целая запись потеряна из-за обрезанной: осталось %d", n)
	}
	if p := again.Problem(); p == "" || !strings.Contains(p, "не до конца") {
		t.Fatalf("об обрезанной строке не сказано: %q", p)
	}
}

func TestSkipKindOfNamesTheCause(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrEmptyAnswer, SkipEmptyAnswer},
		{fmt.Errorf("обёртка: %w", ErrEmptyAnswer), SkipEmptyAnswer},
		{errors.New("разбор ответа: ключа entities нет"), SkipParse},
		{errors.New("invalid character '}' looking for beginning of object key string (JSON)"), SkipParse},
		{errors.New("что-то третье"), SkipOther},
		{nil, SkipOther},
	}
	for _, c := range cases {
		if got := SkipKindOf(c.err); got != c.want {
			t.Errorf("SkipKindOf(%v) = %q, ждали %q", c.err, got, c.want)
		}
	}
}

func TestSkipLogTrimsLongReasonByRunes(t *testing.T) {
	dir := t.TempDir()
	s := openSkipLog(dir)
	// Модель однажды ответит килобайтом текста. Резать надо по знакам:
	// обрезанный посередине русский знак сломал бы строку JSON.
	long := strings.Repeat("я", skipWhyMax+50)
	if err := s.Add(SkipRec{Doc: 1, Ord: 1, Kind: SkipParse, Why: long}); err != nil {
		t.Fatal(err)
	}
	again := openSkipLog(dir)
	if n := again.Count(); n != 1 {
		t.Fatalf("запись с длинной причиной не перечиталась: %d", n)
	}
	again.mu.Lock()
	why := again.recs[0].Why
	again.mu.Unlock()
	if r := []rune(why); len(r) != skipWhyMax+1 || r[len(r)-1] != '…' {
		t.Fatalf("причина обрезана неверно: %d знаков, последний %q", len(r), string(r[len(r)-1]))
	}
}

func TestSkipLogNilIsSafe(t *testing.T) {
	// У графа формата 1 и в тестах журнала может не быть вовсе: обращение
	// к нему не должно ронять сборку.
	var s *SkipLog
	if err := s.Add(SkipRec{Doc: 1}); err != nil {
		t.Fatalf("запись в nil-журнал вернула ошибку: %v", err)
	}
	if n := s.Count(); n != 0 {
		t.Fatalf("nil-журнал насчитал %d", n)
	}
	if k := s.Kinds(0); k != nil {
		t.Fatalf("nil-журнал вернул виды: %+v", k)
	}
	if b := s.Books(0); b != nil {
		t.Fatalf("nil-журнал вернул книги: %+v", b)
	}
	if p := s.Problem(); p != "" {
		t.Fatalf("nil-журнал вернул беду: %q", p)
	}
}

// Цепочка, а не звено: настоящая сборка на подставной модели обязана положить
// причину каждого пропуска в журнал. Тест того же ключа, что и
// TestUnparsedAnswerSkipsChunk рядом, но смотрит не на число, а на след:
// до 27.09.2026 след не оставался вовсе, и 107 потерянных кусков нельзя было
// разобрать даже на следующий день.
func TestBuildWritesSkipReasons(t *testing.T) {
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
	if res.Skipped != 3 {
		t.Fatalf("пропущено %d, ждали 3", res.Skipped)
	}
	log := g.SkipLog()
	if n := log.Count(); n != res.Skipped {
		t.Fatalf("в журнале %d причин при %d пропусках", n, res.Skipped)
	}
	kinds := log.Kinds(0)
	if len(kinds) != 1 || kinds[0].Count != 3 {
		t.Fatalf("виды причин: %+v", kinds)
	}
	if kinds[0].Kind != SkipParse {
		t.Errorf("неразобранный ответ модели назван %q, а не %q", kinds[0].Kind, SkipParse)
	}
	books := log.Books(0)
	if len(books) != 1 || books[0].Count != 3 || books[0].Kind == "(книга не названа)" {
		t.Fatalf("книга в журнале не названа: %+v", books)
	}
	// Записи обязаны читаться другим процессом: журнал для того и файл.
	again := openSkipLog(g.dir)
	if n := again.Count(); n != 3 {
		t.Fatalf("перечитанный журнал сборки: %d записей", n)
	}
	again.mu.Lock()
	first := again.recs[0]
	again.mu.Unlock()
	if first.Why == "" {
		t.Error("причина пуста — по такой записи ничего не разобрать")
	}
	if first.At == 0 {
		t.Error("время записи не поставлено")
	}
}
