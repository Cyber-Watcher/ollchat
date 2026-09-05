package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Признак работы с графом и разбор занятости коллекции.
//
// Замок LOCK ставит только сборка: две сборки разом перемешали бы журналы.
// Остальные работы — векторы понятий, темы, описания, разборы, склейки,
// уплотнение, скрытие книги, группы — замка не ставят и ставить не должны:
// ночная цепочка гонит векторы рядом с идущей сборкой намеренно. Но архиву
// коллекции нужно знать о каждой из них: снимок, снятый посреди записи
// файла тем, откроется и будет врать. Поэтому пишущая работа ставит
// в каталоге графа признак WORK-<pid> с названием работы, а архив
// перед началом смотрит на все признаки разом.
//
// Признак живёт столько, сколько процесс: брошенный (kill -9, отключение
// питания) снимается сам по мёртвому номеру процесса — тем же разбором,
// что и замок сборки.

const workPrefix = "WORK-"

// MarkWork ставит признак работы с графом в его каталоге и возвращает
// снятие. Перед этим ждёт окончания идущего архива коллекции — до
// kb.ArchiveWait: архив занимает до минуты, а работа шла бы часами.
func MarkWork(graphDir, what string) (release func(), err error) {
	if err := kb.WaitArchive(filepath.Dir(graphDir), kb.ArchiveWait); err != nil {
		return nil, err
	}
	path := filepath.Join(graphDir, workPrefix+fmt.Sprint(os.Getpid()))
	body := fmt.Sprintf("pid %d, начато %s, работа: %s\n",
		os.Getpid(), time.Now().Format(time.RFC3339), what)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

// Busy описывает, кто сейчас пишет в коллекцию или в любой её граф:
// индексация коллекции, сборка графа, помеченная работа. Пусто — никто.
//
// Смотрятся все графы коллекции — рабочий и опытные рядом: архив снимает
// каталог коллекции целиком, и опытный граф в нём тоже.
func Busy(collDir string) string {
	var parts []string
	if s := kb.CollectionBusy(collDir); s != "" {
		parts = append(parts, s)
	}
	for _, dir := range GraphDirs(collDir) {
		if s := graphBusy(dir); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// graphBusy — что идёт в одном каталоге графа.
func graphBusy(dir string) string {
	var parts []string
	lock := filepath.Join(dir, lockFile)
	if _, err := os.Stat(lock); err == nil {
		if owner := readLock(lock); owner.alive() {
			s := "сборка графа"
			if owner.PID > 0 {
				s += fmt.Sprintf(" (процесс %d, с %s)", owner.PID, owner.Since)
			}
			parts = append(parts, s)
		}
		// Мёртвый хозяин замка — дело сборки: она снимет его сама при
		// следующем заходе и скажет об этом. Здесь он не считается занятостью.
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), workPrefix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		owner := readLock(path)
		if !owner.alive() {
			os.Remove(path)
			continue
		}
		what := "работа с графом"
		if i := strings.Index(owner.Raw, "работа: "); i >= 0 {
			what = strings.TrimSpace(owner.Raw[i+len("работа: "):])
		}
		parts = append(parts, fmt.Sprintf("%s (процесс %d)", what, owner.PID))
	}
	return strings.Join(parts, "; ")
}

// GraphDirs перечисляет каталоги графов коллекции: рабочий `graph`
// и именованные `graph-<имя>`. Только те, где есть паспорт.
func GraphDirs(collDir string) []string {
	entries, err := os.ReadDir(collDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() != DirName && !strings.HasPrefix(e.Name(), DirName+"-") {
			continue
		}
		dir := filepath.Join(collDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, metaFile)); err == nil {
			out = append(out, dir)
		}
	}
	return out
}

// HasAnyGraph — собран ли по коллекции хоть один граф.
func HasAnyGraph(collDir string) bool { return len(GraphDirs(collDir)) > 0 }
