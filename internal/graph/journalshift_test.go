package graph

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Записи журналов для проверки сдвига. Величины — как у настоящего графа,
// а не единицы: номера понятий за тысячу (у рабочего графа их сотни тысяч,
// номеров меньше 256 — доли процента), номера книг за сотню (перечитанная
// книга получает новый номер), куски — от нуля. На единицах проверка слепнет
// там, где слепнет и на деле: отметка, сдвинутая на 8 байт, берёт признак
// из номера книги, и книги 1..4 дают допустимый признак.
type shiftRec struct {
	ent, dst, doc, ord uint32
	typ                uint8
}

const shiftRecs = 60

func shiftRecords() []shiftRec {
	out := make([]shiftRec, shiftRecs)
	for i := range out {
		ent := uint32(1000 + 7*i)
		out[i] = shiftRec{ent: ent, dst: ent + 3, doc: uint32(101 + i%3), ord: uint32(i / 3), typ: uint8(1 + i%7)}
	}
	return out
}

func encodeMention(r shiftRec) []byte {
	b := make([]byte, mentionSize)
	binary.LittleEndian.PutUint32(b[0:], r.ent)
	binary.LittleEndian.PutUint32(b[4:], r.doc)
	binary.LittleEndian.PutUint32(b[8:], r.ord)
	return b
}

func encodeEdgeRec(r shiftRec) []byte {
	b := make([]byte, edgeSize)
	encodeEdge(b, Edge{Src: r.ent, Dst: r.dst, Type: r.typ, Weight: 1, Evidence: ChunkKey{Doc: r.doc, Ord: r.ord}})
	return b
}

func encodeMark(r shiftRec) []byte {
	b := make([]byte, progressSize)
	binary.LittleEndian.PutUint32(b[0:], r.doc)
	binary.LittleEndian.PutUint32(b[4:], r.ord)
	binary.LittleEndian.PutUint32(b[8:], MarkDone)
	return b
}

// tornJournal — журнал так, как его оставлял код до 07.10.2026: целые
// записи до обрыва, обрывок первых k байт записи at (k == 0 — обрыва нет),
// а за ним — снова целые записи, начиная с той же at: заход после обрыва
// разбирает кусок заново и пишет его записи целиком.
func tornJournal(recs []shiftRec, enc func(shiftRec) []byte, at, k int) []byte {
	var out []byte
	for i, r := range recs {
		if i == at && k > 0 {
			out = append(out, enc(r)[:k]...)
		}
		out = append(out, enc(r)...)
	}
	return out
}

type tornSpec struct{ mentions, edges, progress int }

// shiftGraph — граф с тремя понятиями в реестре (граница номеров 3+65536)
// и журналами из shiftRecords, оборванными на записи shiftRecs/2.
func shiftGraph(t *testing.T, torn tornSpec) (*Graph, string) {
	t.Helper()
	coll := collection(t)
	g, err := Create(coll, "books", 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Альфа", "Бета", "Гамма"} {
		if _, _, err := g.Entities().Add(name, TypeConcept); err != nil {
			t.Fatal(err)
		}
	}
	dir := g.Dir()
	must(t, g.Close())
	recs := shiftRecords()
	at := shiftRecs / 2
	for _, j := range []struct {
		name string
		enc  func(shiftRec) []byte
		k    int
	}{{mentionsFile, encodeMention, torn.mentions}, {edgesFile, encodeEdgeRec, torn.edges}, {progressFile, encodeMark, torn.progress}} {
		if err := os.WriteFile(filepath.Join(dir, j.name), tornJournal(recs, j.enc, at, j.k), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, err = Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, dir
}

func knownShiftBooks(doc uint32) bool { return doc >= 101 && doc <= 103 }

// shiftFrom — смещение первой записи, на которой виден сдвиг (недопустимые
// поля или кусок без отметки); -1 — нигде.
func shiftFrom(c JournalCheck) int64 {
	switch {
	case c.BadFirst < 0:
		return c.UnmarkedFirst
	case c.UnmarkedFirst < 0:
		return c.BadFirst
	default:
		return min(c.BadFirst, c.UnmarkedFirst)
	}
}

// Целые журналы сдвига не показывают: ни недопустимых полей, ни кусков
// без отметки, ни чужих книг, ни хвоста.
func TestJournalShiftCleanJournals(t *testing.T) {
	g, _ := shiftGraph(t, tornSpec{})
	rep, err := g.JournalShift(knownShiftBooks)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Limit != 3+maxIDSlack {
		t.Errorf("граница номеров %d, ожидалась %d — как при открытии графа", rep.Limit, 3+maxIDSlack)
	}
	for _, c := range rep.All() {
		if c.Missing || c.Records != shiftRecs || c.Tail != 0 || c.Bad != 0 || c.Unmarked != 0 || c.Unknown != 0 {
			t.Errorf("%s: %+v — у целого журнала ничего не должно найтись", c.File, c)
		}
	}
	if rep.Bad() != 0 || rep.Unmarked() != 0 || rep.Unknown() != 0 {
		t.Errorf("итоги: недопустимых %d, без отметки %d, чужих книг %d", rep.Bad(), rep.Unmarked(), rep.Unknown())
	}
}

// Журнал, дописанный после обрывка (так писал код до 07.10.2026), — сдвиг
// виден с самого места обрыва, и ни одной записи до него не помечено.
//
// Длины обрывков: у упоминаний все 1..11, у связей 4, 8 и 16, у отметок 4 и 8.
// Настоящие — кратные четырём: буфер 64 КиБ даёт 65536 % 12 = 4 и
// 65536 % 24 = 16, у отметок буфер 32 КиБ — 32768 % 12 = 8. Именно при
// сдвиге упоминаний на 4 и 8 байт поля записи остаются допустимыми (на месте
// понятия встаёт номер куска или книги) — их выдаёт только кусок без отметки.
func TestJournalShiftFindsTornAppend(t *testing.T) {
	at := int64(shiftRecs / 2)
	type tc struct {
		file string
		k    int
		pick func(JournalShiftReport) JournalCheck
		size int
	}
	var cases []tc
	for k := 1; k < mentionSize; k++ {
		cases = append(cases, tc{mentionsFile, k, func(r JournalShiftReport) JournalCheck { return r.Mentions }, mentionSize})
	}
	for _, k := range []int{4, 8, 16} {
		cases = append(cases, tc{edgesFile, k, func(r JournalShiftReport) JournalCheck { return r.Edges }, edgeSize})
	}
	for _, k := range []int{4, 8} {
		cases = append(cases, tc{progressFile, k, func(r JournalShiftReport) JournalCheck { return r.Progress }, progressSize})
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%d", c.file, c.k), func(t *testing.T) {
			var spec tornSpec
			switch c.file {
			case mentionsFile:
				spec.mentions = c.k
			case edgesFile:
				spec.edges = c.k
			default:
				spec.progress = c.k
			}
			g, _ := shiftGraph(t, spec)
			rep, err := g.JournalShift(knownShiftBooks)
			if err != nil {
				t.Fatal(err)
			}
			j := c.pick(rep)
			tear := at * int64(c.size)
			t.Logf("обрыв на %d: недопустимых %d (первая на %d), без отметки %d (первая на %d)",
				tear, j.Bad, j.BadFirst, j.Unmarked, j.UnmarkedFirst)
			// Байт стало на k больше: записей столько же, хвост — k.
			if j.Records != shiftRecs || j.Tail != int64(c.k) {
				t.Errorf("записей %d, хвост %d — ожидалось %d и %d", j.Records, j.Tail, shiftRecs, c.k)
			}
			if got := shiftFrom(j); got != tear {
				t.Errorf("сдвиг виден с %d, обрыв на %d: %+v", got, tear, j)
			}
			if j.UnknownFirst >= 0 && j.UnknownFirst < tear {
				t.Errorf("чужая книга до обрыва, на %d", j.UnknownFirst)
			}
			if c.file == progressFile {
				// Сверять отметки не с чем, кроме их признака.
				if j.Bad == 0 || j.BadFirst != tear {
					t.Errorf("отметки: недопустимых %d, первая на %d, обрыв на %d", j.Bad, j.BadFirst, tear)
				}
			} else if got := j.Bad + j.Unmarked; got != shiftRecs-int(at) {
				// Каждая запись после обрывка читается неправильной:
				// у неё либо поля недопустимы, либо кусок — мусор.
				t.Errorf("помечено %d записей после обрыва из %d: %+v", got, shiftRecs-int(at), j)
			}
			if j.Bad > 0 && j.BadAfter != shiftRecs-int(j.BadFirst/int64(c.size)) {
				t.Errorf("записей после первой недопустимой %d", j.BadAfter)
			}
			// Остальные журналы целы: недопустимых полей и хвоста в них нет.
			// Куски без отметки в них бывают только при сдвинутых отметках:
			// отметки после обрыва прочитаны мусором, и упоминания и связи
			// тех кусков честно остаются неразмеченными — это следствие
			// сдвига отметок, а не второй сдвиг.
			for _, o := range rep.All() {
				if o.File == c.file {
					continue
				}
				if o.Bad != 0 || o.Tail != 0 || (c.file != progressFile && o.Unmarked != 0) {
					t.Errorf("%s цел, а найдено: %+v", o.File, o)
				}
				if c.file == progressFile && o.Unmarked != shiftRecs-int(at) {
					t.Errorf("%s: без отметки %d, ожидалось %d — кусков, чьи отметки легли после обрыва",
						o.File, o.Unmarked, shiftRecs-int(at))
				}
			}
		})
	}
}

// Граница номера понятия — та же, что при открытии: номер на границе
// допустим, за ней — уже нет. Связь без подтверждения (книга 0) ни куском
// без отметки, ни чужой книгой не считается.
func TestJournalShiftLimitAndNoEvidence(t *testing.T) {
	g, dir := shiftGraph(t, tornSpec{})
	limit := uint32(3 + maxIDSlack)
	recs := shiftRecords()
	edge := []shiftRec{recs[0], {ent: limit, dst: 1, typ: RelUses}, {ent: limit + 1, dst: 1, typ: RelUses, doc: 101}}
	var raw []byte
	for _, r := range edge {
		raw = append(raw, encodeEdgeRec(r)...)
	}
	if err := os.WriteFile(filepath.Join(dir, edgesFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := g.JournalShift(knownShiftBooks)
	if err != nil {
		t.Fatal(err)
	}
	e := rep.Edges
	if e.Records != 3 || e.Bad != 1 || e.BadFirst != 2*edgeSize || e.Unmarked != 0 || e.Unknown != 0 {
		t.Errorf("связи: %+v — ожидалась одна недопустимая (номер за границей) на %d", e, 2*edgeSize)
	}
}

// Книга не из реестра — отдельный счёт, а не сдвиг; без реестра (nil)
// книги не сверяются.
func TestJournalShiftUnknownBooks(t *testing.T) {
	g, _ := shiftGraph(t, tornSpec{})
	rep, err := g.JournalShift(func(doc uint32) bool { return doc == 101 })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.All() {
		// Книги 102 и 103 — у двух записей из трёх, первая из них — запись №1.
		if c.Unknown != shiftRecs*2/3 || c.UnknownFirst != int64(c.size) || c.Bad != 0 {
			t.Errorf("%s: чужих книг %d с %d, недопустимых %d", c.File, c.Unknown, c.UnknownFirst, c.Bad)
		}
	}
	rep, err = g.JournalShift(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unknown() != 0 {
		t.Errorf("без реестра чужих книг %d", rep.Unknown())
	}
}

// Проверка — только чтение: журнала нет — он и не появляется.
func TestJournalShiftDoesNotCreateJournals(t *testing.T) {
	g, dir := shiftGraph(t, tornSpec{})
	for _, name := range []string{mentionsFile, edgesFile, progressFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := g.JournalShift(knownShiftBooks)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.All() {
		if !c.Missing || c.Records != 0 {
			t.Errorf("%s: %+v — журнала нет", c.File, c)
		}
		if _, err := os.Stat(filepath.Join(dir, c.File)); !os.IsNotExist(err) {
			t.Errorf("%s появился после проверки: %v", c.File, err)
		}
	}
}
