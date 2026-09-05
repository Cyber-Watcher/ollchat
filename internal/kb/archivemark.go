package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Признак идущего архива коллекции и разбор занятости.
//
// Архив читает коллекцию целиком — куски, векторы, граф — и занимает на это
// до минуты (замер 04.09.2026: books, 1.15 ГБ → 53 с). Всё, что в это время
// пишет в коллекцию, испортило бы снимок: дозапись журнала связей попала бы
// в архив наполовину, и такой архив открылся бы, но врал. Поэтому архив
// ставит признак ARCHIVE в каталоге коллекции, а пишущие работы перед
// началом ждут его снятия.
//
// **Ждут, а не отказывают.** Ночная сборка графа стартует по cron в назначенную
// минуту; наткнись она на секунды архива — сорвалась бы целая ночь карты.
// Две минуты ожидания покрывают замеренный архив вдвое.
//
// Брошенный признак (процесс убит) снимается сам по мёртвому номеру
// процесса — так же, как замок индексации.

const archiveMark = "ARCHIVE"

// ArchiveWait — сколько пишущая работа ждёт снятия признака архива.
var ArchiveWait = 2 * time.Minute

// MarkArchive ставит признак идущего архива и возвращает его снятие.
//
// Отказ — если признак уже стоит и его хозяин жив: два архива одной
// коллекции разом не нужны никому.
func MarkArchive(collDir string) (release func(), err error) {
	path := filepath.Join(collDir, archiveMark)
	if desc := markerOwner(path); desc != "" {
		return nil, fmt.Errorf("архив коллекции уже идёт: %s", desc)
	}
	body := fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

// ArchiveInProgress описывает идущий архив коллекции; пусто — не идёт.
func ArchiveInProgress(collDir string) string {
	return markerOwner(filepath.Join(collDir, archiveMark))
}

// WaitArchive ждёт снятия признака архива, но не дольше max.
//
// Возвращает ошибку с описанием хозяина, если признак так и не снят: дальше
// человек решает сам — подождать ещё или снять файл руками.
func WaitArchive(collDir string, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		desc := ArchiveInProgress(collDir)
		if desc == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("идёт архив коллекции (%s), ждал %s — дальше не жду;\n"+
				"если архив не идёт, снимите признак: rm %s",
				desc, max, filepath.Join(collDir, archiveMark))
		}
		time.Sleep(archivePoll)
	}
}

// archivePoll — как часто перепроверять признак при ожидании.
var archivePoll = time.Second

// CollectionBusy описывает идущую индексацию, счёт векторов или уплотнение
// коллекции (замок LOCK с живым процессом); пусто — коллекция свободна.
func CollectionBusy(collDir string) string {
	if desc := markerOwner(filepath.Join(collDir, lockMark)); desc != "" {
		return "индексация коллекции (" + desc + ")"
	}
	return ""
}

// lockMark — замок индексации; см. Collection.lock.
const lockMark = "LOCK"

// markerOwner читает признак вида «pid время» и описывает живого хозяина.
// Пусто — признака нет либо его хозяин мёртв; мёртвый снимается.
func markerOwner(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	pid := 0
	if len(fields) > 0 {
		pid, _ = strconv.Atoi(fields[0])
	}
	if pid > 0 && processAlive(pid) {
		if len(fields) > 1 {
			return fmt.Sprintf("процесс %d, с %s", pid, fields[1])
		}
		return fmt.Sprintf("процесс %d", pid)
	}
	os.Remove(path)
	return ""
}
