package graph

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Обслуживание графа: проверка целостности, перенос на другую машину,
// удаление.
//
// Всё здесь работает с файлами графа напрямую, без модели и без сети.

// CheckResult — что показала проверка.
type CheckResult struct {
	Entities  int
	Edges     int
	Mentions  int
	Parsed    int // сколько кусков разобрано
	Chunks    int // сколько кусков в коллекции сейчас
	Model     string
	Version   int
	Community int // сообществ уровня 0; -1 — файла нет
	Described int // из них с резюме

	// Problems — найденные беды, человеческими словами. Пусто — всё хорошо.
	Problems []string
	// Notes — не беды, но стоит знать.
	Notes []string
}

// OK сообщает, что бед не найдено.
func (r CheckResult) OK() bool { return len(r.Problems) == 0 }

// Check проверяет граф: не разъехался ли он с коллекцией и нет ли внутри
// ссылок в пустоту.
//
// Проверка нужна потому, что граф и коллекция живут порознь: книги можно
// перечитать, удалить, уплотнить — граф об этом не узнает. Ошибки такого рода
// не проявляются сразу: поиск продолжает работать и молча отдаёт ссылки
// на чужие страницы.
func (g *Graph) Check(chunks int) CheckResult {
	m := g.Meta()
	r := CheckResult{
		Entities:  g.Entities().Count(),
		Edges:     g.Edges().Count(),
		Mentions:  g.Mentions().Count(),
		Parsed:    g.Progress().Count(),
		Chunks:    chunks,
		Model:     m.Model,
		Version:   m.Version,
		Community: -1,
	}

	// Незнакомая версия — беда; знакомая, но не та, которой пишутся новые
	// графы, — норма: рабочий и опытный графы живут рядом и умышленно разные.
	if !KnownVersion(m.Version) {
		r.Problems = append(r.Problems,
			fmt.Sprintf("граф собран форматом версии %d, а читатель знает %s",
				m.Version, versionsHuman()))
	}
	if m.Chunks > chunks {
		r.Problems = append(r.Problems,
			fmt.Sprintf("в коллекции кусков %d, а при сборке было %d — коллекцию уплотнили, "+
				"ссылки графа указывают не туда", chunks, m.Chunks))
	}
	if r.Entities > 0 && r.Edges == 0 {
		r.Problems = append(r.Problems, "понятия есть, а связей нет — сборка оборвалась в начале")
	}

	// Связи в пустоту: ссылка на понятие, которого нет в реестре.
	dangling, checked := 0, 0
	for _, e := range g.Entities().All() {
		for _, ed := range g.Edges().Of(e.ID) {
			checked++
			if _, ok := g.Entities().Get(ed.Dst); !ok {
				dangling++
			}
		}
	}
	if dangling > 0 {
		r.Problems = append(r.Problems,
			fmt.Sprintf("связей в пустоту: %d из %d — они ссылаются на понятия, которых нет",
				dangling, checked))
	}

	if r.Parsed < chunks {
		r.Notes = append(r.Notes,
			fmt.Sprintf("разобрано %d кусков из %d (%d%%) — остальные книги граф не видит",
				r.Parsed, chunks, 100*r.Parsed/max(chunks, 1)))
	}

	if c, err := g.LoadCommunities(); err == nil && c != nil {
		lvl0 := c.Level(0)
		r.Community = len(lvl0)
		for _, com := range lvl0 {
			if com.Title != "" {
				r.Described++
			}
		}
		if c.Entities != r.Entities {
			r.Notes = append(r.Notes,
				fmt.Sprintf("сообщества размечены на %d понятиях, а сейчас их %d — "+
					"граф дособирали, разбиение стоит обновить", c.Entities, r.Entities))
		}
	} else if err != nil {
		r.Problems = append(r.Problems, "разбиение на сообщества не читается: "+err.Error())
	}
	return r
}

// Remove удаляет граф, не трогая коллекцию.
//
// Возвращает освобождённое место: человеку важно знать цену решения,
// а пересборка стоит часов работы видеокарты.
func Remove(collDir, name string) (int64, error) {
	dir := filepath.Join(collDir, DirFor(name))
	if _, err := os.Stat(filepath.Join(dir, metaFile)); err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNoGraph
		}
		return 0, err
	}
	if _, err := os.Stat(filepath.Join(dir, lockFile)); err == nil {
		return 0, fmt.Errorf("идёт сборка графа — сперва остановите её")
	}
	size := dirBytes(dir)
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	return size, nil
}

// PackResult — что вышло у переноса.
type PackResult struct {
	Files int
	Bytes int64 // сколько прочитано с диска
	Wrote int64 // сколько получилось в архиве
}

// Pack складывает коллекцию вместе с графом в переносимый архив.
//
// Переносится **вся коллекция**, а не только граф: граф ссылается на куски
// по номеру книги и номеру внутри неё, и без самой коллекции он бесполезен.
// Абсолютных путей внутри нет ни одного — это свойство формата, ради него
// граф и живёт внутри каталога коллекции.
//
// Имя, оканчивающееся на .gz или .tgz, включает сжатие.
func Pack(collDir, archive string) (PackResult, error) {
	return packDir(collDir, archive, skipInArchive)
}

// packDir складывает каталог в tar, пропуская то, что велит skip.
// Общее ядро переноса (Pack) и плановых архивов (Archive).
func packDir(collDir, archive string, skip func(rel string, fi os.FileInfo) bool) (PackResult, error) {
	var res PackResult
	info, err := os.Stat(collDir)
	if err != nil {
		return res, err
	}
	if !info.IsDir() {
		return res, fmt.Errorf("%s не каталог коллекции", collDir)
	}

	f, err := os.Create(archive)
	if err != nil {
		return res, err
	}
	defer f.Close()

	var w io.Writer = f
	var gz *gzip.Writer
	if lower := strings.ToLower(archive); strings.HasSuffix(lower, ".gz") ||
		strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".gz.part") {
		gz = gzip.NewWriter(f)
		w = gz
	}
	tw := tar.NewWriter(w)
	// Порядок закрытия важен и потому явный: сперва tar (концевые блоки),
	// затем gzip (хвост потока), и только потом fsync — иначе на диск
	// уходит поток без хвоста, а «готовый» архив не распаковывается.
	closeAll := func() error {
		if err := tw.Close(); err != nil {
			return err
		}
		if gz != nil {
			return gz.Close()
		}
		return nil
	}

	base := filepath.Base(collDir)
	var files []string
	err = filepath.Walk(collDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(collDir, p)
		if err != nil {
			return err
		}
		// Признак идущей сборки в архив не идёт: на другой машине он означал бы
		// «граф собирается» и запрещал бы работу с ним. Остальные исключения —
		// в skipInArchive.
		if skip != nil && skip(rel, fi) {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return res, err
	}
	// Устойчивый порядок: два архива одной коллекции должны совпадать.
	sort.Strings(files)

	for _, p := range files {
		fi, err := os.Stat(p)
		if err != nil {
			return res, err
		}
		rel, err := filepath.Rel(collDir, p)
		if err != nil {
			return res, err
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return res, err
		}
		hdr.Name = filepath.ToSlash(filepath.Join(base, rel))
		if err := tw.WriteHeader(hdr); err != nil {
			return res, err
		}
		src, err := os.Open(p)
		if err != nil {
			return res, err
		}
		n, err := io.Copy(tw, src)
		src.Close()
		if err != nil {
			return res, err
		}
		res.Files++
		res.Bytes += n
	}
	if err := closeAll(); err != nil {
		return res, err
	}
	// Данные обязаны дойти до диска до переименования в готовый архив:
	// иначе после отключения питания «готовый» файл окажется пустым.
	if err := f.Sync(); err != nil {
		return res, err
	}
	if fi, err := os.Stat(archive); err == nil {
		res.Wrote = fi.Size()
	}
	return res, nil
}

// dirBytes считает размер каталога.
func dirBytes(dir string) int64 {
	var sum int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			sum += fi.Size()
		}
		return nil
	})
	return sum
}
