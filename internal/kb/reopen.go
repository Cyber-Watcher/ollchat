package kb

import (
	"os"
	"path/filepath"
)

// Слежение за тем, что коллекцию изменили снаружи.
//
// Beда, которую это закрывает (25.08.2026). Долгоживущий читатель — служба
// ollmcp, а равно и сам ollchat с открытым чатом — держит файлы коллекции
// открытыми. Индексация дописывает сегменты и помечает прежние куски
// удалёнными; уплотнение и вовсе подменяет каталог коллекции переименованием.
// Читатель продолжает читать прежние inode и отдаёт состояние на момент своего
// запуска.
//
// Замечено это было не сразу и только сравнением двух процессов: устаревший
// бинарь виден по поведению, а устаревший индекс не виден ничем — выдача
// выглядит нормальной, со ссылками и страницами, просто текст вчерашний.
// У пойманного процесса в /proc были открыты **удалённые** файлы каталога,
// который снесло уплотнение девятью часами раньше.
//
// Признак изменения — отпечаток из нескольких файлов. Одного времени правки
// каталога мало: дозапись в docs.jsonl и deleted.ids его не меняет, а
// уплотнение подменяет каталог целиком, и время у нового может совпасть
// с точностью файловой системы.

// stamp — отпечаток состояния коллекции на диске.
type stamp struct {
	dir  os.FileInfo // сам каталог: подмена при уплотнении видна по identity
	meta os.FileInfo
	docs os.FileInfo
	del  os.FileInfo
}

// stampOf снимает отпечаток. Отсутствующие файлы — не ошибка: коллекция
// может быть свежей и пустой.
func stampOf(dir string) stamp {
	st := func(name string) os.FileInfo {
		fi, err := os.Stat(name)
		if err != nil {
			return nil
		}
		return fi
	}
	return stamp{
		dir:  st(dir),
		meta: st(filepath.Join(dir, "meta.json")),
		docs: st(filepath.Join(dir, "docs.jsonl")),
		del:  st(filepath.Join(dir, "deleted.ids")),
	}
}

// same сообщает, что состояние на диске не менялось.
func (s stamp) same(o stamp) bool {
	return sameFile(s.dir, o.dir) && sameFile(s.meta, o.meta) &&
		sameFile(s.docs, o.docs) && sameFile(s.del, o.del)
}

// sameFile сравнивает два описания одного файла: тот же ли это файл и не
// изменился ли он. Пропажа и появление файла считаются изменением.
func sameFile(a, b os.FileInfo) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}

// Stale сообщает, что коллекцию изменили после того, как её открыли.
func (c *Collection) Stale() bool {
	c.diskMu.Lock()
	prev := c.disk
	c.diskMu.Unlock()
	return !prev.same(stampOf(c.dir))
}

// setStamp записывает отпечаток под своим замком.
func (c *Collection) setStamp(s stamp) {
	c.diskMu.Lock()
	c.disk = s
	c.diskMu.Unlock()
}

// restamp обновляет отпечаток после того, как эта же коллекция сама что-то
// записала.
//
// Без этого писатель считал бы изменившейся коллекцию, которую сам же и правил,
// и при следующем обращении перечитывал бы её целиком с диска. На библиотеке
// в 463 МБ это заметная и совершенно напрасная работа.
func (c *Collection) restamp() {
	c.setStamp(stampOf(c.dir))
}
