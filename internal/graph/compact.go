package graph

// Уплотнение реестра понятий.
//
// **Зачем.** Реестр — файл только на дозапись: обновление счётчиков понятия
// кладёт в конец новую копию записи, а побеждает последняя. За недели сборки
// это даёт двадцатикратный перевес. Замер 02.09.2026 на библиотеке 465 тыс.
// кусков: 3 251 704 записи на 161 239 понятий, 523 МБ файла. При открытии
// разбираются и раскладываются по словарям ВСЕ записи, поэтому открытие
// занимало 41 с, из которых 39 приходились на реестр. С уплотнённым реестром
// то же открытие — 2.4 с и 26 МБ файла.
//
// **Почему это не делается само.** Реестр стоит недель работы видеокарты,
// а замена файла необратима. Поэтому: команду отдаёт человек, старый файл
// остаётся рядом с отметкой времени, а перед подменой словари обоих реестров
// сличаются — и при расхождении подмены не происходит.
//
// **Почему сличать обязательно.** Синоним занимает ключ, только если тот
// свободен, а основа слова — только первая по счёту. В исходном потоке записи
// одного понятия идут вперемешку с чужими, и кто занял спорный ключ, решает
// порядок. В уплотнённом файле порядок другой. Итоговый набор понятий тот же,
// а вот владелец спорного ключа мог бы оказаться другим — это и проверяется.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// CompactStats — что дало уплотнение реестра.
type CompactStats struct {
	RecordsBefore int   // записей в файле до уплотнения
	RecordsAfter  int   // столько же понятий, по одной записи на каждое
	BytesBefore   int64 // размер файла до
	BytesAfter    int64 // и после
	Diffs         []string
	Backup        string // куда убран прежний файл
	Applied       bool   // подменён ли реестр на самом деле

	// Размер словарей поиска и число разошедшихся в них ключей. Доля важнее
	// самого числа: 700 спорных синонимов на четверть миллиона ключей и
	// 700 на тысячу — это разные решения.
	Keys, KeysDiff   int
	Stems, StemsDiff int

	// Dropped — сколько мёртвых понятий выброшено из реестра (см. CompactDrop).
	Dropped int
}

// Compact уплотняет реестр понятий коллекции.
//
// check — только сличить и рассказать, ничего не подменяя.
// force — подменить даже при расхождении словарей.
func Compact(collDir, name string, check, force bool) (CompactStats, error) {
	return CompactDrop(collDir, name, check, force, nil)
}

// CompactDrop — то же уплотнение, ещё и без понятий из drop.
//
// **Зачем.** Уплотнение оставляет «столько же понятий, по одной записи»:
// понятие, у которого не осталось ни одного упоминания — книга отброшена,
// куски забыты чисткой, — лежит в реестре вечно, со своими ключами и
// вектором, и оплачивается при каждом открытии графа (этап 90, п. 3).
// Какие понятия мёртвые, решает вызывающий (Graph.DeadEntities: ни упоминаний,
// ни связей, ни склеек); здесь они лишь не переписываются в новый файл.
// Номера не перенумеровываются — вектор понятия лежит по номеру, и место
// остаётся пустым. Прежний файл сохраняется, так что шаг обратим.
//
// Словари сличаются на реестре БЕЗ выбрасывания: уплотнение дубликатов должно
// быть тождественным, а пропажа ключей мёртвых понятий — ожидаемое действие,
// а не расхождение.
func CompactDrop(collDir, name string, check, force bool, drop map[uint32]bool) (CompactStats, error) {
	dir := filepath.Join(collDir, DirFor(name))
	path := filepath.Join(dir, entitiesFile)

	var st CompactStats
	fi, err := os.Stat(path)
	if err != nil {
		return st, err
	}
	st.BytesBefore = fi.Size()

	// Уплотнение с подменой идёт под тем же признаком, что и сборка, и берёт
	// его ДО чтения реестра. Сборка держит реестр открытым на дозапись весь
	// заход: подмени файл под ней — и все её новые понятия уйдут в копию
	// «.bak-…», а связи будут ссылаться на понятия, которых в реестре нет;
	// записи, дописанные после чтения, пропали бы точно так же. До 17.09.2026
	// уплотнение ставило лишь WORK-<pid>, которого сборка не спрашивает
	// (аудит обвязки, находка S1). Проверка без подмены замка не берёт:
	// она ничего не пишет.
	if !check {
		release, err := holdBuildLock(dir)
		if err != nil {
			return st, err
		}
		defer release()
	}

	last, order, count, err := lastPerID(path)
	if err != nil {
		return st, err
	}
	st.RecordsBefore = count
	st.RecordsAfter = len(order)

	tmp := path + ".compact"
	if err := writeRecords(tmp, last, order); err != nil {
		return st, err
	}
	defer os.Remove(tmp) // при удачной подмене файла по этому имени уже нет

	if fi, err := os.Stat(tmp); err == nil {
		st.BytesAfter = fi.Size()
	}

	// Сличение: два реестра целиком в памяти. Дорого по памяти (около 300 МБ
	// на каждый), но это разовая работа обслуживания, а цена ошибки здесь —
	// недели видеокарты.
	before, err := readEntitiesFile(path)
	if err != nil {
		return st, err
	}
	after, err := readEntitiesFile(tmp)
	if err != nil {
		return st, err
	}
	st.Diffs = diffEntities(before, after, 20)
	st.Keys, st.Stems = len(before.byKey), len(before.byStem)
	st.KeysDiff = countIndexDiff(before.byKey, after.byKey)
	st.StemsDiff = countIndexDiff(before.byStem, after.byStem)

	if check {
		st.Dropped = countDrop(order, drop)
		return st, nil
	}
	if len(st.Diffs) > 0 && !force {
		return st, nil
	}
	if len(drop) > 0 {
		// Итоговый файл — без мёртвых понятий; сличение выше их не касалось.
		kept := order[:0:0]
		for _, id := range order {
			if drop[id] {
				st.Dropped++
				continue
			}
			kept = append(kept, id)
		}
		if err := writeRecords(tmp, last, kept); err != nil {
			return st, err
		}
		st.RecordsAfter = len(kept)
		if fi, err := os.Stat(tmp); err == nil {
			st.BytesAfter = fi.Size()
		}
	}

	st.Backup = path + ".bak-" + time.Now().Format("20060102-150405")
	if err := os.Rename(path, st.Backup); err != nil {
		return st, err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Возврат прежнего файла: остаться без реестра нельзя ни при каких
		// обстоятельствах — это и есть сам граф.
		_ = os.Rename(st.Backup, path)
		return st, err
	}
	syncDir(dir)
	st.Applied = true
	return st, nil
}

func countDrop(order []uint32, drop map[uint32]bool) int {
	n := 0
	for _, id := range order {
		if drop[id] {
			n++
		}
	}
	return n
}

// DeadEntities — понятия, которых в графе больше ничто не держит: ни одного
// упоминания (упоминания из отброшенных книг не в счёт), ни одной связи в обе
// стороны, и они не участвуют в склейках ни поглощённым, ни выжившим.
// Их выбрасывает уплотнение с CompactDrop; прочее, что может быть пустым
// «по ошибке модели» (есть упоминания, но нет связей), здесь не трогается.
func (g *Graph) DeadEntities() []uint32 {
	var dead []uint32
	for _, e := range g.ents.Live() {
		if g.merges.Gone(e.ID) || len(g.merges.Absorbed(e.ID)) > 0 {
			continue
		}
		alive := false
		for _, k := range g.Mentions().Of(e.ID) {
			if !g.dropped.Dropped(k.Doc) {
				alive = true
				break
			}
		}
		if alive || len(g.edge.Of(e.ID)) > 0 || len(g.edge.incoming(e.ID)) > 0 {
			continue
		}
		dead = append(dead, e.ID)
	}
	return dead
}

// holdBuildLock занимает признак сборки графа на время работы, которая
// подменяет файлы реестра. Формат и правила — те же, что у Graph.Lock:
// живой хозяин — отказ, признак от мёртвого процесса снимается.
func holdBuildLock(dir string) (release func(), err error) {
	path := filepath.Join(dir, lockFile)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid %d, начато %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		owner := readLock(path)
		if owner.alive() {
			return nil, &LockedError{Path: path, PID: owner.PID, Since: owner.Since}
		}
		if rmErr := os.Remove(path); rmErr != nil {
			return nil, fmt.Errorf("остался признак сборки от неживого процесса, "+
				"и его не удалось убрать: %w", rmErr)
		}
	}
	return nil, &LockedError{Path: path}
}

// lastPerID читает реестр и оставляет последнюю запись на каждый номер.
//
// Строки хранятся как есть, а не пересобираются из разобранной структуры:
// в записи могут быть поля, которых эта версия программы не знает, и
// пересборка молча их выбросила бы.
func lastPerID(path string) (map[uint32][]byte, []uint32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	defer f.Close()

	last := map[uint32][]byte{}
	var order []uint32 // порядок первого появления номера
	count := 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		count++
		var head struct {
			ID uint32 `json:"id"`
		}
		if err := json.Unmarshal(line, &head); err != nil || head.ID == 0 {
			continue // битую строку чтение реестра тоже пропускает
		}
		if _, seen := last[head.ID]; !seen {
			order = append(order, head.ID)
		}
		last[head.ID] = append([]byte(nil), line...)
	}
	return last, order, count, sc.Err()
}

// writeRecords пишет уплотнённый реестр.
//
// Порядок — по первому появлению понятия в исходном файле, а не по номеру:
// спорный ключ-синоним достаётся тому, кто пришёл раньше, и сохранённый
// порядок первого появления ближе всего повторяет исходный спор.
func writeRecords(path string, last map[uint32][]byte, order []uint32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	for _, id := range order {
		if _, err := w.Write(last[id]); err != nil {
			f.Close()
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readEntitiesFile читает реестр только для чтения, без файла на дозапись.
func readEntitiesFile(path string) (*Entities, error) {
	return readEntitiesFileWith(path, DefaultStemMinLen)
}

// readEntitiesFileWith читает реестр с заданной длиной основы для указателя byStem.
func readEntitiesFileWith(path string, stemMinLen int) (*Entities, error) {
	e := &Entities{stemMinLen: stemMinLen, path: path, byKey: map[string]uint32{}, byStem: map[string]uint32{}}
	if err := e.load(nil); err != nil {
		return nil, err
	}
	return e, nil
}

// diffEntities сличает два реестра: набор понятий и оба словаря поиска.
// Возвращает не больше limit расхождений — их список нужен человеку для
// решения, а не для полного перечисления.
func diffEntities(a, b *Entities, limit int) []string {
	var out []string
	add := func(format string, args ...any) {
		if len(out) < limit {
			out = append(out, fmt.Sprintf(format, args...))
		}
	}

	if len(a.list) != len(b.list) {
		add("понятий было %d, стало %d", len(a.list), len(b.list))
	}
	n := len(a.list)
	if len(b.list) < n {
		n = len(b.list)
	}
	for i := 0; i < n; i++ {
		x, y := a.list[i], b.list[i]
		if x.ID != y.ID || x.Norm != y.Norm || x.Name != y.Name ||
			x.Type != y.Type || x.Docs != y.Docs || x.Count != y.Count {
			add("понятие %d: было %q (%s, книг %d), стало %q (%s, книг %d)",
				i+1, x.Name, x.Type, x.Docs, y.Name, y.Type, y.Docs)
		}
	}

	out = append(out, diffIndex("ключ", a.byKey, b.byKey, limit-len(out))...)
	out = append(out, diffIndex("основа", a.byStem, b.byStem, limit-len(out))...)
	return out
}

// diffIndex сличает словарь поиска: и потерянные ключи, и сменившие владельца.
func diffIndex(what string, a, b map[string]uint32, limit int) []string {
	if limit <= 0 {
		return nil
	}
	var keys []string
	for k, av := range a {
		if bv, ok := b[k]; !ok || bv != av {
			keys = append(keys, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	// Порядок обхода карты в Go случаен, а список расхождений человек будет
	// сравнивать между запусками — поэтому сортируем.
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if len(out) >= limit {
			out = append(out, fmt.Sprintf("…и ещё расхождений по %s: %d", what, len(keys)-len(out)))
			break
		}
		av, aok := a[k]
		bv, bok := b[k]
		switch {
		case aok && !bok:
			out = append(out, fmt.Sprintf("%s %q был у понятия %d, стал ничей", what, k, av))
		case !aok && bok:
			out = append(out, fmt.Sprintf("%s %q не был занят, стал у понятия %d", what, k, bv))
		default:
			out = append(out, fmt.Sprintf("%s %q был у понятия %d, стал у %d", what, k, av, bv))
		}
	}
	return out
}

// countIndexDiff считает ключи, которые пропали или сменили владельца.
func countIndexDiff(a, b map[string]uint32) int {
	n := 0
	for k, av := range a {
		if bv, ok := b[k]; !ok || bv != av {
			n++
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			n++
		}
	}
	return n
}

// syncDir сбрасывает на диск сам каталог: без этого переименование может
// не пережить внезапное выключение, и реестра не окажется вовсе.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
