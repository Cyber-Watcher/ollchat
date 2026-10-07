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
//
// **Сам каталог — только своей identity, без времени правки** (07.10.2026).
// Время каталога меняет любое создание и удаление файла в нём: замок LOCK,
// временный файл атомарной записи, каждая волна --kb-embed, переписывающая
// vectors.meta. Отпечаток с ним объявлял коллекцию изменённой при всяком чихе,
// и служба во время счёта смыслов перечитывала её целиком на каждый запрос —
// вместе с сотнями мегабайт векторов. Теперь в отпечатке названные файлы:
// паспорт, реестр, пометки удалённых, указатели кусков (доливка, проход
// --kb-flag-toc) и паспорт векторов.

// stamp — отпечаток состояния коллекции на диске.
type stamp struct {
	dir  os.FileInfo // сам каталог: подмена при уплотнении видна по identity
	meta os.FileInfo
	docs os.FileInfo
	del  os.FileInfo
	idx  os.FileInfo // chunks.idx
	vecs os.FileInfo // vectors.meta
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
	_, vecMeta := vecPaths(dir)
	return stamp{
		dir:  st(dir),
		meta: st(filepath.Join(dir, "meta.json")),
		docs: st(filepath.Join(dir, "docs.jsonl")),
		del:  st(filepath.Join(dir, "deleted.ids")),
		idx:  st(filepath.Join(dir, "chunks.idx")),
		vecs: st(vecMeta),
	}
}

// same сообщает, что состояние на диске не менялось.
func (s stamp) same(o stamp) bool {
	return s.sameIndex(o) && sameFile(s.vecs, o.vecs)
}

// sameIndex — то же без векторов: каталог, реестр и индекс по словам.
func (s stamp) sameIndex(o stamp) bool {
	return sameIdentity(s.dir, o.dir) && sameFile(s.meta, o.meta) &&
		sameFile(s.docs, o.docs) && sameFile(s.del, o.del) && sameFile(s.idx, o.idx)
}

// sameIdentity — тот же ли это файл, без оглядки на время и размер.
func sameIdentity(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b)
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
//
// Новые векторы подхватываются не волной, а работой: пока счёт смыслов идёт
// (замок коллекции держит живой процесс), паспорт векторов переписывается
// после каждой пачки, и перечитывать ради него сотни мегабайт векторов
// на каждый запрос незачем — прежние векторы верны, просто их меньше.
// Когда счёт закончится и замок снимут, коллекция перечитается один раз.
func (c *Collection) Stale() bool {
	c.diskMu.Lock()
	prev := c.disk
	c.diskMu.Unlock()
	now := stampOf(c.dir)
	if !prev.sameIndex(now) {
		return true
	}
	if sameFile(prev.vecs, now.vecs) {
		return false
	}
	held, _ := markerState(filepath.Join(c.dir, lockMark))
	return !held
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
