package graph

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Архив коллекции с графом и восстановление из него.
//
// **Зачем.** Рабочий граф — самое дорогое, что есть в проекте: недели работы
// видеокарты, которые нельзя ни купить, ни ускорить. Он портится молча:
// неудачное уплотнение, склейка не тех двойников, сбой диска. Лучше старый
// рабочий граф, чем никакой, — поэтому снимок делается по расписанию, сам,
// а восстановление стоит одной команды.
//
// **Почему коллекция целиком, а не только граф.** Граф ссылается на куски
// парой «номер книги, номер куска», и номер книги выдаёт коллекция по
// счётчику при индексации. Пересобранная из тех же книг коллекция раздала бы
// номера иначе, и восстановленный поверх неё граф молча указывал бы не на те
// книги. Архив коллекции целиком самодостаточен: восстанавливается всё разом,
// в любую ситуацию, включая пустой диск. Цена — размер: замер 04.09.2026 на
// books: 1.15 ГБ на диске → около 600 МБ в tar.gz за 53 с; только граф был бы
// 155 МБ за 17 с. Решение владельца 04.09.2026: целиком.
//
// **Что не идёт в архив.** Замки и признаки работ (на другой машине они
// значили бы «сборка идёт»), резервные копии реестра `*.bak-*` (полгигабайта
// прежнего реестра, который человек оставил себе для проверки) и недописанные
// части других архивов.
//
// **Согласованность снимка.** Архив ставит признак kb.MarkArchive и не
// начинается, пока кто-то пишет в коллекцию или граф (Busy). Пишущие работы
// ждут снятия признака — см. work.go.

// ErrBusy — с коллекцией или её графом идёт работа; архив отложен.
var ErrBusy = errors.New("с коллекцией идёт работа")

// ArchiveOpts — куда и как архивировать.
type ArchiveOpts struct {
	// Dir — каталог архивов. Создаётся, если нет.
	Dir string
	// Keep — сколько последних архивов коллекции хранить; 0 — все.
	// Считаются только плановые (без пометки): снимок «перед восстановлением»
	// хранится, пока человек не уберёт его сам.
	Keep int
	// Tag — пометка в имени файла: books-20260904-111305-<tag>.tar.gz.
	Tag string
}

// ArchiveResult — что вышло.
type ArchiveResult struct {
	Path    string
	Files   int
	Bytes   int64 // прочитано с диска
	Wrote   int64 // размер архива
	Elapsed time.Duration
	Removed []string // убранные по ротации
}

// ArchiveInfo — один архив в каталоге.
type ArchiveInfo struct {
	Path       string
	Collection string
	Time       time.Time
	Tag        string
	Size       int64
}

// archiveNow — часы; подменяются в тестах, где нужны разные имена подряд.
var archiveNow = time.Now

const archiveExt = ".tar.gz"

var archiveNameRe = regexp.MustCompile(`^([A-Za-z0-9_-]+?)-(\d{8}-\d{6})(?:-([a-z][a-z0-9-]*))?\.tar\.gz$`)

// ArchiveName — имя файла архива: books-20260904-111305[-tag].tar.gz.
func ArchiveName(coll string, t time.Time, tag string) string {
	name := coll + "-" + t.Format("20060102-150405")
	if tag != "" {
		name += "-" + tag
	}
	return name + archiveExt
}

// parseArchiveName разбирает имя файла; ok=false — это не наш архив.
func parseArchiveName(base string) (coll string, t time.Time, tag string, ok bool) {
	m := archiveNameRe.FindStringSubmatch(base)
	if m == nil {
		return "", time.Time{}, "", false
	}
	t, err := time.ParseInLocation("20060102-150405", m[2], time.Local)
	if err != nil {
		return "", time.Time{}, "", false
	}
	return m[1], t, m[3], true
}

// Archive снимает коллекцию с графом в каталог архивов.
//
// Пишет во временный файл `.part` и переименовывает по окончании: оборванный
// архив не должен выглядеть готовым — по нему решают, пора ли делать
// следующий, и из него восстанавливают.
func Archive(collDir string, o ArchiveOpts) (ArchiveResult, error) {
	var res ArchiveResult
	start := time.Now()
	coll := filepath.Base(collDir)
	if err := kb.ValidName(coll); err != nil {
		return res, fmt.Errorf("каталог %s: %w", collDir, err)
	}
	if _, err := os.Stat(filepath.Join(collDir, "meta.json")); err != nil {
		return res, fmt.Errorf("%s не похож на каталог коллекции: нет meta.json", collDir)
	}
	if o.Dir == "" {
		return res, fmt.Errorf("не задан каталог архивов")
	}
	if b := Busy(collDir); b != "" {
		return res, fmt.Errorf("%w: %s", ErrBusy, b)
	}
	release, err := kb.MarkArchive(collDir)
	if err != nil {
		return res, err
	}
	defer release()
	// Между проверкой и признаком кто-то мог начать писать: смотрим ещё раз
	// уже под признаком — теперь новых работ не появится, они ждут нас.
	if b := Busy(collDir); b != "" {
		return res, fmt.Errorf("%w: %s", ErrBusy, b)
	}

	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return res, err
	}
	removeStaleParts(o.Dir, coll)

	path := filepath.Join(o.Dir, ArchiveName(coll, archiveNow(), o.Tag))
	part := path + ".part"
	pr, err := packDir(collDir, part, skipInArchive)
	if err != nil {
		os.Remove(part)
		return res, err
	}
	if err := os.Rename(part, path); err != nil {
		os.Remove(part)
		return res, err
	}
	res.Path, res.Files, res.Bytes, res.Wrote = path, pr.Files, pr.Bytes, pr.Wrote
	if o.Tag == "" && o.Keep > 0 {
		res.Removed = rotate(o.Dir, coll, o.Keep)
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// skipInArchive — что не попадает в архив и в перенос.
func skipInArchive(rel string, fi os.FileInfo) bool {
	base := filepath.Base(rel)
	switch {
	case base == lockFile, base == "ARCHIVE":
		return true
	case strings.HasPrefix(base, workPrefix):
		return true
	case strings.Contains(base, ".bak-"):
		return true
	case strings.HasSuffix(base, ".part"):
		return true
	}
	return !fi.Mode().IsRegular()
}

// removeStaleParts убирает недописанные части архивов этой коллекции: они
// остаются после обрыва, а второго архива той же коллекции разом не бывает —
// признак kb.MarkArchive один.
func removeStaleParts(dir, coll string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, coll+"-") && strings.HasSuffix(n, archiveExt+".part") {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

// rotate оставляет keep новейших плановых архивов коллекции.
func rotate(dir, coll string, keep int) []string {
	list, err := Archives(dir, coll)
	if err != nil {
		return nil
	}
	var planned []ArchiveInfo
	for _, a := range list {
		if a.Tag == "" {
			planned = append(planned, a)
		}
	}
	var removed []string
	for i := keep; i < len(planned); i++ {
		if err := os.Remove(planned[i].Path); err == nil {
			removed = append(removed, planned[i].Path)
		}
	}
	return removed
}

// Archives перечисляет архивы в каталоге, новые первыми. coll пусто — все.
func Archives(dir, coll string) ([]ArchiveInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ArchiveInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		c, t, tag, ok := parseArchiveName(e.Name())
		if !ok || (coll != "" && c != coll) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ArchiveInfo{Path: filepath.Join(dir, e.Name()),
			Collection: c, Time: t, Tag: tag, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// LastArchive — новейший плановый архив коллекции. По нему решается,
// пора ли делать следующий; снимки перед восстановлением не считаются:
// они делаются не по расписанию.
func LastArchive(dir, coll string) (ArchiveInfo, bool) {
	list, err := Archives(dir, coll)
	if err != nil {
		return ArchiveInfo{}, false
	}
	for _, a := range list {
		if a.Tag == "" {
			return a, true
		}
	}
	return ArchiveInfo{}, false
}

// ArchivePeek — что лежит в архиве, без распаковки.
type ArchivePeek struct {
	Collection string
	Files      int
	// Graphs — паспорта графов внутри: имя каталога → паспорт.
	Graphs map[string]Meta
}

// PeekArchive читает оглавление архива: имя коллекции и паспорта графов.
// Читается весь файл (паспорт лежит после кусков), но не распаковывается
// на диск: секунды, не минута.
func PeekArchive(path string) (ArchivePeek, error) {
	var p ArchivePeek
	p.Graphs = map[string]Meta{}
	err := walkArchive(path, func(hdr *tar.Header, r io.Reader) error {
		top, rest, _ := strings.Cut(strings.TrimPrefix(hdr.Name, "./"), "/")
		if p.Collection == "" {
			p.Collection = top
		} else if top != p.Collection {
			return fmt.Errorf("в архиве больше одной коллекции: %s и %s", p.Collection, top)
		}
		if hdr.Typeflag == tar.TypeReg {
			p.Files++
		}
		dir, file := filepath.Split(rest)
		dir = strings.TrimSuffix(dir, "/")
		if file == metaFile && !strings.Contains(dir, "/") &&
			(dir == DirName || strings.HasPrefix(dir, DirName+"-")) {
			var m Meta
			if err := decodeMeta(r, &m); err != nil {
				return fmt.Errorf("паспорт %s в архиве не читается: %w", hdr.Name, err)
			}
			p.Graphs[dir] = m
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	if p.Collection == "" {
		return p, fmt.Errorf("архив пуст")
	}
	return p, nil
}

// RestoreResult — что сделано при восстановлении.
type RestoreResult struct {
	Collection string
	Dir        string
	Files      int
	Bytes      int64
	// Backup — куда сложена прежняя коллекция; пусто — её не было.
	Backup string
	Graphs map[string]Meta
}

// Restore распаковывает архив в каталог коллекций и подменяет им коллекцию.
//
// Порядок: сперва прежняя коллекция уходит в архив с пометкой before-restore
// (ничего не теряется), затем архив распаковывается в каталог рядом, и только
// потом каталоги меняются местами переименованием — целиком и разом. Обрыв
// на распаковке оставляет прежнюю коллекцию нетронутой; обрыв между двумя
// переименованиями оставляет прежнюю под именем `.<имя>.replaced`.
//
// Отказ, если с коллекцией идёт работа: восстанавливать из-под сборки
// нельзя, а ждать её часами — не дело этой команды.
func Restore(archive, collectionsDir string, o ArchiveOpts) (RestoreResult, error) {
	var res RestoreResult
	peek, err := PeekArchive(archive)
	if err != nil {
		return res, err
	}
	name := peek.Collection
	if err := kb.ValidName(name); err != nil {
		return res, fmt.Errorf("имя коллекции в архиве: %w", err)
	}
	res.Collection, res.Graphs = name, peek.Graphs
	target := filepath.Join(collectionsDir, name)
	res.Dir = target

	exists := false
	if _, err := os.Stat(target); err == nil {
		exists = true
		if b := Busy(target); b != "" {
			return res, fmt.Errorf("%w: %s", ErrBusy, b)
		}
	}

	if exists {
		bo := o
		bo.Tag, bo.Keep = "before-restore", 0
		back, err := Archive(target, bo)
		if err != nil {
			return res, fmt.Errorf("снимок прежней коллекции перед подменой: %w", err)
		}
		res.Backup = back.Path
	}

	tmp := filepath.Join(collectionsDir, "."+name+".restore")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return res, err
	}
	files, bytes, err := unpackInto(archive, name, tmp)
	if err != nil {
		os.RemoveAll(tmp)
		return res, err
	}
	res.Files, res.Bytes = files, bytes
	if _, err := os.Stat(filepath.Join(tmp, "meta.json")); err != nil {
		os.RemoveAll(tmp)
		return res, fmt.Errorf("в архиве нет meta.json коллекции — это не архив ollchat")
	}

	// Подмена. Прежняя коллекция уже в архиве, но и на диске её не трём,
	// пока новая не встала на место.
	old := filepath.Join(collectionsDir, "."+name+".replaced")
	if exists {
		os.RemoveAll(old)
		// Работа могла начаться, пока шла распаковка.
		if b := Busy(target); b != "" {
			os.RemoveAll(tmp)
			return res, fmt.Errorf("%w: %s", ErrBusy, b)
		}
		if err := os.Rename(target, old); err != nil {
			os.RemoveAll(tmp)
			return res, err
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		if exists {
			_ = os.Rename(old, target)
		}
		os.RemoveAll(tmp)
		return res, err
	}
	if exists {
		if err := os.RemoveAll(old); err != nil {
			return res, fmt.Errorf("коллекция восстановлена, но прежнюю не удалось убрать: %s: %w", old, err)
		}
	}
	return res, nil
}

// walkArchive проходит по архиву, отдавая каждую запись.
func walkArchive(path string, fn func(hdr *tar.Header, r io.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: не gzip: %w", path, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := checkEntryName(hdr.Name); err != nil {
			return err
		}
		if err := fn(hdr, tr); err != nil {
			return err
		}
	}
}

// checkEntryName отклоняет пути, ведущие из каталога распаковки наружу.
func checkEntryName(name string) error {
	clean := strings.TrimPrefix(name, "./")
	if clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("недопустимый путь в архиве: %q", name)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return fmt.Errorf("недопустимый путь в архиве: %q", name)
		}
	}
	return nil
}

// unpackInto распаковывает записи коллекции name в каталог dst.
func unpackInto(archive, name, dst string) (files int, bytes int64, err error) {
	err = walkArchive(archive, func(hdr *tar.Header, r io.Reader) error {
		clean := strings.TrimPrefix(hdr.Name, "./")
		top, rest, _ := strings.Cut(clean, "/")
		if top != name {
			return fmt.Errorf("в архиве чужая запись: %s", hdr.Name)
		}
		if rest == "" {
			return nil
		}
		p := filepath.Join(dst, filepath.FromSlash(rest))
		switch hdr.Typeflag {
		case tar.TypeDir:
			return os.MkdirAll(p, 0o755)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm()|0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(w, r)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return fmt.Errorf("%s: %w", hdr.Name, err)
			}
			files++
			bytes += n
			return nil
		default:
			return fmt.Errorf("в архиве запись неподдерживаемого вида: %s", hdr.Name)
		}
	})
	return files, bytes, err
}
