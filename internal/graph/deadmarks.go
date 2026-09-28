package graph

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"time"
)

// Чистка отметок разбора у книг, которых в коллекции больше нет.
//
// ЗАЧЕМ. Книгу удаляют из коллекции надгробием в `deleted.ids`: запись реестра
// остаётся, куски перестают находиться. В графе при этом остаются отметки
// разбора её кусков в `progress.log` — навсегда. Перепись 27.09.2026 (этап 110,
// Б1) показала, что кроме отметок от удалённых книг не остаётся НИЧЕГО:
// упоминаний 0 из 2 825 358, подтверждений связей 0, понятий, держащихся только
// на них, 0. То есть чистить нужно ровно одно — отметки, и делать это безопасно.
//
// К 28.09.2026 таких отметок 11 861 у 12 записей книг: ни одна не влияет
// на работу (книг нет, куски не берутся), но они попадают в отчёт доктора
// отдельной строкой и путают глаз. Слово владельца 28.09.2026: «убери
// этот мусор».
//
// КАК. Тем же переписывателем, которым работает забывание кусков
// (`rewriteBinary`): журнал читается, записи мёртвых книг не переписываются,
// прежний файл остаётся рядом копией с отметкой времени. Ничего, кроме
// `progress.log`, не трогается — упоминаний и связей у этих книг нет.
//
// ЧЕГО НЕ ДЕЛАЕТ. Не решает, какая книга мертва: это дело вызывающего
// (у коллекции есть `LiveBooks`). Не трогает реестр понятий — там своё
// уплотнение (`--graph-compact`). Не запускается при идущей сборке: журнал
// пишется заходом, и переписывать его под ним нельзя (правило владельца
// «уплотнение только когда граф не собирается»), поэтому команда берёт
// замок сборки.

// DeadMarksStats — что нашлось и что убрано.
type DeadMarksStats struct {
	Marks  int      // отметок убрано
	Books  int      // записей книг, которым они принадлежали
	Backup string   // копия прежнего журнала
	Dead   []uint32 // номера этих записей, по убыванию числа отметок
}

// DeadBookMarks — сколько отметок у каждой книги, которую alive не признаёт.
// Читает журнал, ничего не меняя: нужен и отчёту, и dry-прогону команды.
func DeadBookMarks(dir string, alive func(doc uint32) bool) (map[uint32]int, error) {
	p, err := openProgress(dir)
	if err != nil {
		return nil, err
	}
	out := map[uint32]int{}
	// Docs уже считает отметки по книгам — своего обхода не пишем.
	for doc, n := range p.Docs() {
		if alive != nil && alive(doc) {
			continue
		}
		out[doc] = n
	}
	return out, nil
}

// DropDeadBookMarks убирает из `progress.log` отметки книг, которых alive
// не признаёт живыми. dry — только посчитать.
//
// Вызывающий обязан держать замок сборки: журнал дозаписывается заходом,
// и перезапись под ним потеряла бы отметки разобранных кусков — то есть часы
// работы видеокарты.
func DropDeadBookMarks(dir string, alive func(doc uint32) bool, dry bool) (DeadMarksStats, error) {
	var st DeadMarksStats
	if alive == nil {
		return st, fmt.Errorf("чистка отметок без списка живых книг: так убралось бы всё")
	}
	byDoc, err := DeadBookMarks(dir, alive)
	if err != nil {
		return st, err
	}
	if len(byDoc) == 0 {
		return st, nil
	}
	stamp := time.Now().Format("20060102-150405")
	res, err := rewriteBinaryWith(filepath.Join(dir, progressFile), progressSize, stamp, dry,
		func(b []byte) bool {
			doc := binary.LittleEndian.Uint32(b[0:])
			return !alive(doc)
		}, true)
	if err != nil {
		return st, err
	}
	st.Marks = res.dropped
	st.Books = len(byDoc)
	st.Backup = res.backup
	for doc := range byDoc {
		st.Dead = append(st.Dead, doc)
	}
	sortDeadByCount(st.Dead, byDoc)
	return st, nil
}

// sortDeadByCount — по убыванию числа отметок: человек читает список сверху
// и хочет видеть сперва крупные следы.
func sortDeadByCount(docs []uint32, by map[uint32]int) {
	for i := 1; i < len(docs); i++ {
		for j := i; j > 0; j-- {
			a, b := docs[j-1], docs[j]
			if by[a] > by[b] || (by[a] == by[b] && a <= b) {
				break
			}
			docs[j-1], docs[j] = docs[j], docs[j-1]
		}
	}
}
