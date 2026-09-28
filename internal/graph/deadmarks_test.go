package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// Чистка отметок мёртвых книг: убрать ровно их и ничего больше.
//
// Проверяется то, из-за чего правило 1 («граф — священная корова») и написано:
// действие, меняющее граф, обязано быть узким и обратимым. Узким — трогает
// только отметки книг, которых в коллекции нет; обратимым — прежний журнал
// остаётся копией.

func marksFixture(t *testing.T) (string, *Progress) {
	t.Helper()
	dir := t.TempDir()
	p, err := openProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Две живые книги и две удалённые, у каждой свои отметки разных видов.
	for _, c := range []struct {
		doc, ord, mark uint32
	}{
		{1, 0, MarkDone}, {1, 1, MarkEmpty}, {1, 2, MarkDone},
		{2, 0, MarkDone}, {2, 1, MarkService},
		{7, 0, MarkSkipped}, {7, 1, MarkSkipped}, {7, 2, MarkService},
		{9, 0, MarkSkipped},
	} {
		if err := p.Mark(ChunkKey{Doc: c.doc, Ord: c.ord}, c.mark); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	return dir, p
}

func TestDeadBookMarksCountsOnlyDeadBooks(t *testing.T) {
	dir, _ := marksFixture(t)
	alive := func(doc uint32) bool { return doc == 1 || doc == 2 }
	by, err := DeadBookMarks(dir, alive)
	if err != nil {
		t.Fatal(err)
	}
	if len(by) != 2 || by[7] != 3 || by[9] != 1 {
		t.Fatalf("отметки мёртвых книг: %+v (ждали 7→3, 9→1)", by)
	}
	if _, ok := by[1]; ok {
		t.Error("живая книга попала в список мёртвых")
	}
}

func TestDropDeadBookMarksRemovesOnlyDead(t *testing.T) {
	dir, _ := marksFixture(t)
	alive := func(doc uint32) bool { return doc == 1 || doc == 2 }

	st, err := DropDeadBookMarks(dir, alive, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Marks != 4 || st.Books != 2 {
		t.Fatalf("убрано отметок %d у %d книг (ждали 4 у 2)", st.Marks, st.Books)
	}
	if st.Backup == "" {
		t.Fatal("копия прежнего журнала не сделана — правило 1 нарушено")
	}
	if _, err := os.Stat(st.Backup); err != nil {
		t.Fatalf("копия не на диске: %v", err)
	}
	if len(st.Dead) != 2 || st.Dead[0] != 7 {
		t.Fatalf("список мёртвых по убыванию отметок: %+v", st.Dead)
	}

	// Перечитываем журнал заново: отметки живых книг обязаны остаться все
	// до одной, мёртвых — исчезнуть.
	again, err := openProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		doc, ord, want uint32
	}{
		{1, 0, MarkDone}, {1, 1, MarkEmpty}, {1, 2, MarkDone},
		{2, 0, MarkDone}, {2, 1, MarkService},
	} {
		got, ok := again.MarkOf(ChunkKey{Doc: c.doc, Ord: c.ord})
		if !ok || got != c.want {
			t.Errorf("живая книга %d#%d: отметка %d (есть=%v), ждали %d",
				c.doc, c.ord, got, ok, c.want)
		}
	}
	for _, k := range []ChunkKey{{7, 0}, {7, 1}, {7, 2}, {9, 0}} {
		if _, ok := again.MarkOf(k); ok {
			t.Errorf("отметка мёртвой книги %s осталась", k)
		}
	}
	if n := again.Count(); n != 5 {
		t.Errorf("в журнале %d отметок, ждали 5", n)
	}
}

func TestDropDeadBookMarksDryChangesNothing(t *testing.T) {
	dir, _ := marksFixture(t)
	alive := func(doc uint32) bool { return doc == 1 || doc == 2 }
	before, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	st, err := DropDeadBookMarks(dir, alive, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Marks != 4 {
		t.Errorf("dry насчитал %d отметок, ждали 4", st.Marks)
	}
	after, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("dry-прогон изменил журнал — так нельзя проверять перед правкой графа")
	}
}

func TestDropDeadBookMarksRefusesWithoutAliveList(t *testing.T) {
	dir, _ := marksFixture(t)
	// Без списка живых книг «мёртвыми» окажутся все: такой вызов обязан
	// отказаться, а не вычистить журнал целиком.
	if _, err := DropDeadBookMarks(dir, nil, false); err == nil {
		t.Fatal("чистка без списка живых книг не отказалась")
	}
	p, err := openProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := p.Count(); n != 9 {
		t.Fatalf("журнал тронут при отказе: %d отметок вместо 9", n)
	}
}

func TestDropDeadBookMarksNoDeadIsNoOp(t *testing.T) {
	dir, _ := marksFixture(t)
	st, err := DropDeadBookMarks(dir, func(uint32) bool { return true }, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Marks != 0 || st.Books != 0 || st.Backup != "" {
		t.Fatalf("на графе без мёртвых книг что-то сделано: %+v", st)
	}
}
