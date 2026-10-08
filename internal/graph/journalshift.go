package graph

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Проверка двоичных журналов графа на сдвиг записей.
//
// **Беда.** До 07.10.2026 после жёсткого обрыва (kill -9, OOM, питание)
// в хвосте журнала оставался обрывок записи, а новые записи дописывались
// ПОСЛЕ него (journal.go). Всё дописанное с того места читается со сдвигом:
// мусорные номера понятий, книг, кусков, типов связей. Новый код срезает
// обрывок перед дозаписью, но уже сдвинутые журналы так и лежат, и чтение
// (store.go, progress.go) принимает в них любые значения.
//
// **Чем не годится готовое.** Не TornTails: он заполняется только при
// дозаписи, когда срезается СВЕЖИЙ обрывок, и о записях, уже легших со
// сдвигом, не знает. Не DoctorTo: доктор считает только принятые записи,
// а принято всё. Не rewriteBinaryWith: он переписывает журнал, а здесь нужно
// только чтение. Не DeadBookMarks: он читает журнал отметок через
// Progress.load, где смещение записи потеряно, а нужно именно смещение —
// с какого места журнал испорчен.
//
// **Чем узнаётся сдвиг.** Три признака, и они не равноценны.
//
//  1. Недопустимые поля (Bad) — то, чего у правильной записи не бывает:
//     понятие 0 или дальше границы номеров; у связи тип вне 1..7, ненулевые
//     байты дополнения 9–11, концы 0 или равные друг другу (Edges.Add таких
//     не пишет); у отметки признак вне 1..4. Ложных срабатываний не даёт.
//  2. Кусок без отметки разбора (Unmarked) — упоминание или связь, чей кусок
//     не отмечен в progress.log. Дописывает эти журналы только сборка, и она
//     пишет упоминания и связи куска раньше его отметки (build.go); чистка
//     (forget.go) и перенос номеров книг (bookmap.go) записей не добавляют,
//     а номер книги меняют во всех трёх журналах разом.
//     Признак нужен потому, что первый у упоминаний СЛЕП как раз к настоящим
//     обрывкам: буфер записи 64 КиБ, 65536 % 12 = 4, и обрыв оставляет 4 или
//     8 байт. При сдвиге на 4 байта на место понятия встаёт номер куска, на 8 —
//     номер книги, а это малые числа в пределах границы; зато пара «книга,
//     кусок» становится мусором, которого нет среди отметок. Законно он
//     срабатывает на хвосте идущей сборки (отметки ещё в её буфере), на кусках
//     последнего жёсткого обрыва, которые с тех пор не разбирались заново,
//     и на книгах, чьи отметки сняты чисткой мёртвых отметок.
//  3. Книга не из реестра (Unknown) — не признак сдвига вовсе: так выглядит
//     и законный след перечитанной книги (запись реестра о её пути заменена
//     новым номером) или уплотнённой коллекции. Считается отдельно, чтобы
//     человек видел, сколько таких записей и с какого места.
//
// Запись, у которой недопустимы поля, во второй и третий счёт не идёт: её
// книга и кусок — тот же мусор, и двойной счёт только путал бы.
//
// **Только чтение.** Журналы читаются с диска, а не из памяти: в индексах
// открытого графа смещение записи потеряно, а оно и есть ответ — с какого
// места журнал испорчен. Файлы не открываются на запись и не создаются.

// JournalCheck — что нашлось в одном двоичном журнале.
type JournalCheck struct {
	File    string // имя журнала: mentions.log, edges.log, progress.log
	Missing bool   // журнала нет — проверять нечего
	Size    int64  // байт прочитано
	Records int    // целых записей
	Tail    int64  // нецелый хвост, байт: Size % длина записи

	// Bad — записи с недопустимыми полями: признак сдвига. BadFirst
	// и BadLast — смещения первой и последней в байтах (-1 — таких нет).
	// BadAfter — записей от первой такой до конца журнала вместе с ней:
	// когда всё после обрывка читается со сдвигом, Bad/BadAfter близко к 1.
	Bad               int
	BadFirst, BadLast int64
	BadAfter          int

	// Unmarked — упоминания и связи, чей кусок не отмечен разобранным;
	// у журнала отметок всегда 0. Смысл полей — как у Bad.
	Unmarked      int
	UnmarkedFirst int64
	UnmarkedAfter int

	// Unknown — записи с книгой не из реестра коллекции. Связь без
	// подтверждения (книга 0) сюда не идёт: кусок у неё не назван вовсе.
	Unknown      int
	UnknownFirst int64

	size int // длина записи
}

// JournalShiftReport — проверка всех трёх двоичных журналов графа.
type JournalShiftReport struct {
	// Limit — граница номера понятия: номер дальше — мусор сдвига.
	// Та же, что при открытии графа (graph.go, перед reserve).
	Limit uint32

	Mentions, Edges, Progress JournalCheck
}

// All — журналы в порядке печати.
func (r JournalShiftReport) All() []JournalCheck {
	return []JournalCheck{r.Mentions, r.Edges, r.Progress}
}

// Bad — записей с недопустимыми полями во всех журналах.
func (r JournalShiftReport) Bad() int { return r.Mentions.Bad + r.Edges.Bad + r.Progress.Bad }

// Unmarked — упоминаний и связей из кусков без отметки разбора.
func (r JournalShiftReport) Unmarked() int { return r.Mentions.Unmarked + r.Edges.Unmarked }

// Unknown — записей с книгой не из реестра во всех журналах.
func (r JournalShiftReport) Unknown() int {
	return r.Mentions.Unknown + r.Edges.Unknown + r.Progress.Unknown
}

// JournalShift проверяет сырые журналы mentions.log, edges.log и progress.log
// на сдвиг записей. known отвечает, есть ли книга в реестре коллекции (все
// записи реестра, вместе с помеченными удалёнными: отметка удалённой книги —
// законный след, а не сдвиг); nil — книги не сверять.
func (g *Graph) JournalShift(known func(doc uint32) bool) (JournalShiftReport, error) {
	rep := JournalShiftReport{Limit: g.shiftLimit()}
	limit := rep.Limit

	// Отметки читаются первыми: по ним сверяются куски упоминаний и связей.
	// Берутся только записи с допустимым признаком: мусор сдвига в журнале
	// отметок не должен «отмечать» мусор в соседних журналах.
	marked := map[uint64]struct{}{}
	var err error
	rep.Progress, err = scanJournal(g.dir, progressFile, progressSize, func(c *JournalCheck, off int64, b []byte) {
		doc := binary.LittleEndian.Uint32(b[0:])
		ord := binary.LittleEndian.Uint32(b[4:])
		if m := binary.LittleEndian.Uint32(b[8:]); m < MarkDone || m > MarkService {
			c.bad(off)
			return
		}
		marked[ChunkKey{Doc: doc, Ord: ord}.Pack()] = struct{}{}
		if known != nil && !known(doc) {
			c.unknown(off)
		}
	})
	if err != nil {
		return rep, err
	}
	chunk := func(c *JournalCheck, off int64, doc, ord uint32) {
		if _, ok := marked[ChunkKey{Doc: doc, Ord: ord}.Pack()]; !ok {
			c.unmarked(off)
		}
		if known != nil && !known(doc) {
			c.unknown(off)
		}
	}
	rep.Mentions, err = scanJournal(g.dir, mentionsFile, mentionSize, func(c *JournalCheck, off int64, b []byte) {
		if ent := binary.LittleEndian.Uint32(b[0:]); ent == 0 || ent > limit {
			c.bad(off)
			return
		}
		chunk(c, off, binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:]))
	})
	if err != nil {
		return rep, err
	}
	rep.Edges, err = scanJournal(g.dir, edgesFile, edgeSize, func(c *JournalCheck, off int64, b []byte) {
		ed := decodeEdge(b)
		if ed.Src == 0 || ed.Src > limit || ed.Dst == 0 || ed.Dst > limit || ed.Src == ed.Dst ||
			ed.Type < RelIs || ed.Type > RelRelated || b[9]|b[10]|b[11] != 0 {
			c.bad(off)
			return
		}
		if ed.Evidence.Doc == 0 {
			return
		}
		chunk(c, off, ed.Evidence.Doc, ed.Evidence.Ord)
	})
	return rep, err
}

// shiftLimit — граница номера понятия, как её считает открытие графа
// (graph.go): наибольшее из числа записей реестра, отметки уплотнения
// и числа векторов, плюс maxIDSlack. Пересчитывается, а не берётся
// idSpace(): после открытия в нём сидит и reserve, поднятый ссылками
// журналов до самой границы, и граница от этого отъехала бы на второй
// maxIDSlack.
func (g *Graph) shiftLimit() uint32 {
	g.ents.mu.RLock()
	n := uint32(len(g.ents.list))
	g.ents.mu.RUnlock()
	return max(n, loadMaxID(g.dir), uint32(g.vecs.Count())) + maxIDSlack
}

// scanJournal читает журнал записями по size байт и зовёт check на каждую
// целую запись с её смещением. Нецелый хвост учитывается, но не проверяется:
// это обрывок, который срежется перед дозаписью, а во время идущей сборки —
// её же недописанная запись.
func scanJournal(dir, name string, size int, check func(c *JournalCheck, off int64, b []byte)) (JournalCheck, error) {
	c := JournalCheck{File: name, size: size, BadFirst: -1, BadLast: -1, UnmarkedFirst: -1, UnknownFirst: -1}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.Missing = true
			return c, nil
		}
		return c, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256*1024)
	buf := make([]byte, size)
	var off int64
	for {
		n, err := io.ReadFull(r, buf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			c.Tail = int64(n)
			break
		}
		if err != nil {
			return c, err
		}
		check(&c, off, buf)
		c.Records++
		off += int64(size)
	}
	c.Size = off + c.Tail
	c.BadAfter = c.after(c.Bad, c.BadFirst)
	c.UnmarkedAfter = c.after(c.Unmarked, c.UnmarkedFirst)
	return c, nil
}

// after — записей от смещения first до конца журнала вместе с ней.
func (c *JournalCheck) after(n int, first int64) int {
	if n == 0 {
		return 0
	}
	return c.Records - int(first/int64(c.size))
}

func (c *JournalCheck) bad(off int64) {
	if c.Bad == 0 {
		c.BadFirst = off
	}
	c.Bad++
	c.BadLast = off
}

func (c *JournalCheck) unmarked(off int64) {
	if c.Unmarked == 0 {
		c.UnmarkedFirst = off
	}
	c.Unmarked++
}

func (c *JournalCheck) unknown(off int64) {
	if c.Unknown == 0 {
		c.UnknownFirst = off
	}
	c.Unknown++
}
