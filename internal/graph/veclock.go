package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Замок на счёт векторов понятий.
//
// **Зачем.** Файл `entities.vec` пишут двое: полный счёт (`--graph-embed`,
// embed.go) переписывает его целиком атомарно, а догонщик (vecfollow.go)
// дописывает хвост на месте. Ночная цепочка гонит `--graph-embed` после
// сборки, догонщик тем временем может идти на второй машине — и без замка
// они однажды встретятся: догонщик допишет хвост в файл, который полный
// счёт уже подменил, паспорт разойдётся с данными, и векторы отклонятся
// при следующем открытии. Это не порча графа, но потеря часов счёта карты.
//
// **Почему не WORK-признак.** Признак WORK-<pid> (work.go) ставится ради
// архива и заведомо не исключает соседей: ночная цепочка гонит векторы рядом
// с идущей сборкой намеренно. Здесь нужно ровно исключение — по одному
// писателю на файл, — и это тот же приём, что у замка сборки LOCK: файл
// с O_EXCL и номером процесса, брошенный замок снимается по мёртвому pid.
//
// Замок берётся внутри пакета graph, а не в командах: писателей двое сегодня
// и может стать больше, и каждый обязан пройти через одну дверь.

const vecLockFile = "VEC-LOCK"

// VectorsLockedError — векторы этого графа уже считает другой процесс.
type VectorsLockedError struct {
	Path  string
	PID   int
	Since string
}

func (e *VectorsLockedError) Error() string {
	s := "векторы понятий уже считает другой процесс"
	if e.PID > 0 {
		s += fmt.Sprintf(" (%d", e.PID)
		if e.Since != "" {
			s += ", с " + e.Since
		}
		s += ")"
	}
	return s + ": два счёта в один файл не идут — дождитесь его конца.\n" +
		"Если процесса нет, а замок остался: " + e.Path
}

// lockVectors берёт замок на счёт векторов в каталоге графа и возвращает
// снятие. Занято живым процессом — VectorsLockedError; брошенный замок
// мёртвого процесса снимается и берётся себе.
func lockVectors(dir string) (release func(), err error) {
	path := filepath.Join(dir, vecLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if os.IsExist(err) {
		owner := readLock(path)
		if owner.alive() {
			return nil, &VectorsLockedError{Path: path, PID: owner.PID, Since: owner.Since}
		}
		if rmErr := os.Remove(path); rmErr != nil {
			return nil, fmt.Errorf("остался замок векторов от неживого процесса, "+
				"и его не удалось убрать: %w", rmErr)
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		if os.IsExist(err) {
			return nil, &VectorsLockedError{Path: path}
		}
		return nil, err
	}
	// Тот же вид записи, что у LOCK: readLock разбирает оба.
	fmt.Fprintf(f, "pid %d, начато %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	f.Close()
	return func() { os.Remove(path) }, nil
}

// vectorsBusy — идёт ли счёт векторов в каталоге графа; пусто — нет.
// Брошенный замок мёртвого процесса занятостью не считается: его снимет
// следующий счёт.
func vectorsBusy(dir string) string {
	path := filepath.Join(dir, vecLockFile)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	owner := readLock(path)
	if !owner.alive() {
		return ""
	}
	s := "счёт векторов понятий"
	if owner.PID > 0 {
		s += fmt.Sprintf(" (процесс %d, с %s)", owner.PID, owner.Since)
	}
	return s
}
