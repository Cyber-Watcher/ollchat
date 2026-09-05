package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// ErrEncrypted возвращается для зашифрованных документов: их текст закрыт,
// и притворяться, что файл просто пуст, нельзя.
var ErrEncrypted = errors.New("документ зашифрован, извлечь текст нельзя")

// ErrNotPDF возвращается, если файл не начинается с сигнатуры PDF.
var ErrNotPDF = errors.New("файл не является документом PDF")

// Document — разобранный документ.
//
// Таблица xref намеренно не используется: в живых файлах она бывает битой,
// смещения не совпадают, а инкрементальные обновления запутывают ссылки.
// Вместо неё объекты ищутся прямым сканированием файла по заголовкам «N G obj»,
// причём при повторах побеждает последнее определение — то есть самое свежее.
type Document struct {
	data    []byte
	offsets map[int]int    // номер объекта → смещение заголовка в файле
	inStm   map[int][]byte // номер объекта → тело, извлечённое из объектного потока
	cache   map[int]Object
	loading map[int]bool // защита от циклических ссылок
	trailer Dict

	// crypt — расшифровка содержимого. Пусто у обычных документов;
	// у зашифрованных с пустым паролем пользователя — см. crypt.go.
	crypt *crypt
}

// Open разбирает документ из памяти.
func Open(data []byte) (doc *Document, err error) {
	defer catch("открытие PDF", &err)

	if !IsPDF(data) {
		return nil, ErrNotPDF
	}
	d := &Document{
		data:    data,
		offsets: map[int]int{},
		inStm:   map[int][]byte{},
		cache:   map[int]Object{},
		loading: map[int]bool{},
	}
	d.scanObjects()
	d.readTrailer()
	if d.encrypted() {
		// Пустой пароль пользователя — обычное дело у покупных книг: шифрование
		// там стоит ради ограничений печати, а не от читателя. Такую книгу
		// читаем; закрытую настоящим паролём — по-прежнему нет. См. crypt.go.
		if d.crypt = d.setupCrypt(); d.crypt == nil {
			return nil, ErrEncrypted
		}
	}
	d.loadObjectStreams()
	return d, nil
}

// IsPDF сообщает, похожи ли данные на документ PDF.
func IsPDF(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("%PDF-"))
}

// scanObjects проходит по файлу и запоминает смещение каждого «N G obj».
func (d *Document) scanObjects() {
	b := d.data
	for i := 0; i+3 <= len(b); {
		k := bytes.Index(b[i:], []byte("obj"))
		if k < 0 {
			return
		}
		pos := i + k
		i = pos + 3
		// За «obj» должен идти разделитель, иначе это часть другого слова.
		if pos+3 < len(b) && isRegular(b[pos+3]) {
			continue
		}
		// Назад: пробелы, поколение, пробелы, номер.
		j := pos - 1
		for j >= 0 && isSpace(b[j]) {
			j--
		}
		genEnd := j + 1
		for j >= 0 && b[j] >= '0' && b[j] <= '9' {
			j--
		}
		genStart := j + 1
		if genStart == genEnd {
			continue
		}
		for j >= 0 && isSpace(b[j]) {
			j--
		}
		numEnd := j + 1
		if numEnd == genStart {
			continue
		}
		for j >= 0 && b[j] >= '0' && b[j] <= '9' {
			j--
		}
		numStart := j + 1
		if numStart == numEnd {
			continue
		}
		if j >= 0 && isRegular(b[j]) {
			continue
		}
		num := atoi(b[numStart:numEnd])
		if num < 0 {
			continue
		}
		d.offsets[num] = numStart
	}
}

func atoi(b []byte) int {
	if len(b) == 0 || len(b) > 9 {
		return -1
	}
	v := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return -1
		}
		v = v*10 + int(c-'0')
	}
	return v
}

// readTrailer собирает словарь трейлера: и классический «trailer <<…>>»,
// и словарь потока xref из файлов версии 1.5 и новее.
func (d *Document) readTrailer() {
	d.trailer = Dict{}
	merge := func(src Dict) {
		for k, v := range src {
			if _, ok := d.trailer[k]; !ok {
				d.trailer[k] = v
			}
		}
	}
	// Классические трейлеры: последний в файле — самый свежий.
	for pos := len(d.data); pos > 0; {
		k := bytes.LastIndex(d.data[:pos], []byte("trailer"))
		if k < 0 {
			break
		}
		pos = k
		p := newParser(d.data, d)
		p.pos = k + len("trailer")
		if obj, err := p.object(); err == nil {
			if dict, ok := obj.(Dict); ok {
				merge(dict)
			}
		}
	}
	if _, ok := d.trailer["Root"]; ok {
		return
	}
	// Файлы с потоком xref держат /Root в словаре самого потока.
	nums := d.objectNumbers()
	for i := len(nums) - 1; i >= 0; i-- {
		obj := d.object(nums[i])
		s, ok := obj.(*Stream)
		if !ok {
			continue
		}
		if t, _ := s.Dict["Type"].(Name); t == "XRef" {
			merge(s.Dict)
			if _, ok := d.trailer["Root"]; ok {
				return
			}
		}
	}
}

func (d *Document) encrypted() bool {
	if _, ok := d.trailer["Encrypt"]; ok {
		return true
	}
	return false
}

// loadObjectStreams распаковывает объектные потоки (/Type /ObjStm), внутри
// которых в файлах 1.5+ лежит большинство словарей, включая каталог и страницы.
func (d *Document) loadObjectStreams() {
	for _, num := range d.objectNumbers() {
		obj := d.object(num)
		s, ok := obj.(*Stream)
		if !ok {
			continue
		}
		if t, _ := s.Dict["Type"].(Name); t != "ObjStm" {
			continue
		}
		d.expandObjStm(s)
	}
}

func (d *Document) expandObjStm(s *Stream) {
	data, err := d.Decode(s)
	if err != nil && len(data) == 0 {
		return
	}
	n, _ := toInt(d.Resolve(s.Dict["N"]))
	first, _ := toInt(d.Resolve(s.Dict["First"]))
	if n <= 0 || first <= 0 || first > len(data) {
		return
	}
	head := newParser(data[:first], d)
	type entry struct{ num, off int }
	entries := make([]entry, 0, n)
	for i := 0; i < n; i++ {
		numObj, err := head.object()
		if err != nil {
			break
		}
		offObj, err := head.object()
		if err != nil {
			break
		}
		num, ok1 := toInt(numObj)
		off, ok2 := toInt(offObj)
		if !ok1 || !ok2 {
			break
		}
		entries = append(entries, entry{num, off})
	}
	for i, e := range entries {
		start := first + e.off
		end := len(data)
		if i+1 < len(entries) {
			if next := first + entries[i+1].off; next <= len(data) && next > start {
				end = next
			}
		}
		if start < 0 || start >= len(data) || end < start {
			continue
		}
		// Прямое определение объекта в файле новее сжатого, оно и побеждает.
		if _, ok := d.offsets[e.num]; ok {
			continue
		}
		if _, ok := d.inStm[e.num]; ok {
			continue
		}
		d.inStm[e.num] = data[start:end]
	}
}

// objectNumbers возвращает номера объектов, найденных прямым сканированием.
func (d *Document) objectNumbers() []int {
	nums := make([]int, 0, len(d.offsets))
	for n := range d.offsets {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}

// allObjectNumbers возвращает номера всех объектов, включая сжатые.
func (d *Document) allObjectNumbers() []int {
	seen := map[int]bool{}
	nums := make([]int, 0, len(d.offsets)+len(d.inStm))
	for n := range d.offsets {
		if !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	for n := range d.inStm {
		if !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums
}

// object читает объект по номеру, разбирая его при первом обращении.
func (d *Document) object(num int) Object {
	if obj, ok := d.cache[num]; ok {
		return obj
	}
	if d.loading[num] {
		return nil // ссылка на самого себя
	}
	d.loading[num] = true
	defer delete(d.loading, num)

	var result Object
	if off, ok := d.offsets[num]; ok {
		result = d.parseAt(off)
	} else if body, ok := d.inStm[num]; ok {
		p := newParser(body, d)
		if obj, err := p.object(); err == nil {
			result = obj
		}
	}
	d.cache[num] = result
	return result
}

// parseAt разбирает объект, заголовок которого начинается со смещения off.
func (d *Document) parseAt(off int) Object {
	p := newParser(d.data, d)
	p.pos = off
	// Пропускаем «N G obj».
	p.skipSpace()
	for p.pos < len(d.data) && d.data[p.pos] >= '0' && d.data[p.pos] <= '9' {
		p.pos++
	}
	p.skipSpace()
	for p.pos < len(d.data) && d.data[p.pos] >= '0' && d.data[p.pos] <= '9' {
		p.pos++
	}
	p.skipSpace()
	if p.pos+3 <= len(d.data) && string(d.data[p.pos:p.pos+3]) == "obj" {
		p.pos += 3
	}
	obj, err := p.object()
	if err != nil {
		return nil
	}
	return obj
}

// Resolve разрешает ссылку, возвращая сам объект. Цепочки ссылок разрешаются
// до конца, циклы обрываются.
func (d *Document) Resolve(o Object) Object {
	for i := 0; i < 32; i++ {
		ref, ok := o.(Ref)
		if !ok {
			return o
		}
		o = d.object(ref.Num)
	}
	return nil
}

// dictOf возвращает словарь объекта: у потока это его словарь.
func (d *Document) dictOf(o Object) Dict {
	switch v := d.Resolve(o).(type) {
	case Dict:
		return v
	case *Stream:
		return v.Dict
	}
	return nil
}

// Pages возвращает страницы в порядке документа.
func (d *Document) Pages() []Dict {
	if pages := d.pagesFromTree(); len(pages) > 0 {
		return pages
	}
	return d.pagesByScan()
}

// pagesFromTree обходит дерево страниц от каталога — это даёт верный порядок.
func (d *Document) pagesFromTree() []Dict {
	root := d.dictOf(d.trailer["Root"])
	if root == nil {
		return nil
	}
	node := d.dictOf(root["Pages"])
	if node == nil {
		return nil
	}
	var pages []Dict
	seen := map[string]bool{}
	var walk func(node Dict, inherited Dict, depth int)
	walk = func(node Dict, inherited Dict, depth int) {
		if node == nil || depth > 64 || len(pages) > 20000 {
			return
		}
		// Наследуемые атрибуты: ресурсы, размер листа, поворот.
		merged := Dict{}
		for k, v := range inherited {
			merged[k] = v
		}
		for _, k := range []Name{"Resources", "MediaBox", "CropBox", "Rotate"} {
			if v, ok := node[k]; ok {
				merged[k] = v
			}
		}
		kids := asArray(d.Resolve(node["Kids"]))
		if len(kids) == 0 {
			page := Dict{}
			for k, v := range node {
				page[k] = v
			}
			for k, v := range merged {
				if _, ok := page[k]; !ok {
					page[k] = v
				}
			}
			pages = append(pages, page)
			return
		}
		for _, kid := range kids {
			if ref, ok := kid.(Ref); ok {
				key := fmt.Sprintf("%d_%d", ref.Num, ref.Gen)
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			walk(d.dictOf(kid), merged, depth+1)
		}
	}
	walk(node, Dict{}, 0)
	return pages
}

// pagesByScan — запасной путь: каталога нет или дерево битое, тогда берём все
// объекты /Type /Page по возрастанию номера.
func (d *Document) pagesByScan() []Dict {
	var pages []Dict
	for _, num := range d.allObjectNumbers() {
		dict, ok := d.Resolve(Ref{Num: num}).(Dict)
		if !ok {
			continue
		}
		if t, _ := dict["Type"].(Name); t == "Page" {
			pages = append(pages, dict)
		}
	}
	return pages
}

// contentOf собирает содержимое страницы из /Contents.
func (d *Document) contentOf(page Dict) []byte {
	var out []byte
	for _, item := range asArray(d.Resolve(page["Contents"])) {
		s, ok := d.Resolve(item).(*Stream)
		if !ok {
			continue
		}
		data, err := d.Decode(s)
		if err != nil && len(data) == 0 {
			continue
		}
		out = append(out, data...)
		out = append(out, '\n')
	}
	return out
}

// Info возвращает словарь /Info с метаданными документа.
func (d *Document) Info() Dict { return d.dictOf(d.trailer["Info"]) }
