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
	if err := placeMarker(path); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("архив коллекции уже идёт: %s", path)
		}
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
	if held, desc := markerState(path); held {
		return desc
	}
	os.Remove(path)
	return ""
}

// markerFresh — сколько признак без номера процесса считается только что
// поставленным, а не брошенным.
//
// Признак создаётся пустым (O_EXCL) и лишь следом получает номер процесса.
// Читатель, попавший между этими шагами, видит пустой файл; сочти он его
// брошенным — снял бы чужой замок, который вот-вот допишут.
const markerFresh = 10 * time.Second

// markerState читает признак, ничего не трогая: держит ли его живой процесс
// и кто это.
//
// Пустой или испорченный признак не роняет программу: до 07.10.2026 замок
// коллекции разбирался как strings.Fields(...)[0], и пустой LOCK — обрыв
// питания между созданием файла и записью в него — давал панику при каждой
// индексации. Теперь такой признак свежее markerFresh считается занятым,
// давний — брошенным.
func markerState(path string) (held bool, desc string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	fields := strings.Fields(string(data))
	pid := 0
	if len(fields) > 0 {
		pid, _ = strconv.Atoi(fields[0])
	}
	if pid <= 0 {
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < markerFresh {
			return true, "признак только что поставлен"
		}
		return false, ""
	}
	if !processAlive(pid) {
		return false, ""
	}
	if len(fields) > 1 {
		return true, fmt.Sprintf("процесс %d, с %s", pid, fields[1])
	}
	return true, fmt.Sprintf("процесс %d", pid)
}

// markerPID — номер процесса из признака; 0 — признака нет или он испорчен.
func markerPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	pid, _ := strconv.Atoi(fields[0])
	return pid
}

// placeMarker ставит признак «pid время» созданием с O_EXCL: из двух процессов,
// пришедших разом, его получает ровно один. Занято — ошибка, на которой
// os.IsExist отвечает «да».
//
// До 07.10.2026 замок ставился «прочитать, нет ли, — записать», и два процесса,
// прочитавшие «нет» одновременно, оба считали замок своим.
func placeMarker(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return werr
	}
	return nil
}
