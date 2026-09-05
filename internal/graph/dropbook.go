package graph

// Отброшенные книги: их вклад в граф невидим, но не стёрт.
//
// **Зачем.** Книга разобрана плохо (грязный текст, битые шрифты, скан) — её
// вклад надо убрать, но пересобирать весь граф ради одной книги дорого. Решение
// лежит в отдельном журнале `dropped-books.jsonl` и НАДЕВАЕТСЯ на выдачу при
// чтении: упоминания и связи из отброшенной книги перестают показываться, а
// реестр понятий и сами журналы целы. Откат — одна строка, как у склеек.
//
// Это представление, а не удаление: переизвлечь книгу заново (в опытный граф)
// и снять отметку можно в любой момент, ничего не потеряв.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const droppedBooksFile = "dropped-books.jsonl"

// DropRec — одно решение отбросить книгу.
type DropRec struct {
	Book uint32 `json:"book"`
	Path string `json:"path,omitempty"` // ради человека: по номеру книгу не узнать
	Why  string `json:"why,omitempty"`
	At   int64  `json:"at,omitempty"`
	// Undo == true снимает прежнее отбрасывание этой книги: журнал только на
	// дозапись, поэтому отмена — это тоже запись, а не правка старой строки.
	Undo bool `json:"undo,omitempty"`
}

// DroppedBooks — множество отброшенных книг, собранное по журналу.
type DroppedBooks struct {
	mu   sync.RWMutex
	path string
	set  map[uint32]bool
	recs []DropRec
}

func openDroppedBooks(dir string) (*DroppedBooks, error) {
	d := &DroppedBooks{path: filepath.Join(dir, droppedBooksFile), set: map[uint32]bool{}}
	f, err := os.Open(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r DropRec
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Book == 0 {
			continue // оборванная последняя строка — не беда
		}
		d.recs = append(d.recs, r)
	}
	d.rebuild()
	return d, nil
}

// rebuild собирает множество из журнала: последняя запись по книге побеждает,
// поэтому Undo, идущий после отбрасывания, снимает его.
func (d *DroppedBooks) rebuild() {
	d.set = make(map[uint32]bool, len(d.recs))
	for _, r := range d.recs {
		if r.Undo {
			delete(d.set, r.Book)
		} else {
			d.set[r.Book] = true
		}
	}
}

// Dropped сообщает, отброшена ли книга. Nil-приёмник — ничего не отброшено.
func (d *DroppedBooks) Dropped(book uint32) bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.set[book]
}

// Count — сколько книг отброшено сейчас.
func (d *DroppedBooks) Count() int {
	if d == nil {
		return 0
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.set)
}

// Books — номера отброшенных книг.
func (d *DroppedBooks) Books() []uint32 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]uint32, 0, len(d.set))
	for b := range d.set {
		out = append(out, b)
	}
	return out
}

// add дозаписывает решение и обновляет множество.
func (d *DroppedBooks) add(r DropRec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r.At = time.Now().Unix()
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	d.recs = append(d.recs, r)
	if r.Undo {
		delete(d.set, r.Book)
	} else {
		d.set[r.Book] = true
	}
	return nil
}

// DropBook помечает книгу отброшенной: её вклад перестаёт показываться.
func (g *Graph) DropBook(book uint32, path, why string) error {
	return g.dropped.add(DropRec{Book: book, Path: path, Why: why})
}

// RestoreBook снимает отбрасывание книги.
func (g *Graph) RestoreBook(book uint32, path string) error {
	return g.dropped.add(DropRec{Book: book, Path: path, Undo: true})
}

// Dropped отдаёт множество отброшенных книг.
func (g *Graph) Dropped() *DroppedBooks { return g.dropped }
