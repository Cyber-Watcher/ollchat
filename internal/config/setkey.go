package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Запись одного значения в файл конфига из самой программы (этап 123: выбор
// сервера графа и эмбеддера из TUI пишется в конфиг, его перечитывают сборка
// и ночной cron).
//
// Решение владельца 04.11.2026 — точечная правка строки, а не перекодирование
// файла: toml.NewEncoder выбросил бы все комментарии, а в конфиге владельца
// они и есть документация. Готовое не годится: `olldeploy.sh --set` — shell
// для выкладки на чужую машину, он ищет первую строку `ключ =` во всём файле,
// а не в разделе (`url` попал бы в [confluence]), и стирает хвостовой
// комментарий; `ollpatchcfg.py` только дописывает. Правило взято у первого:
// заменить значение существующей строки или дописать строку в раздел, файл
// целиком не переписывать.

// KeyEdit — одна правка: какой ключ какого раздела и чем заполнить.
type KeyEdit struct {
	Section string // раздел через точку: "graph", "kb", "graph.lab"
	Key     string
	Value   string // строка; пишется в кавычках TOML
	// After — если ключа нет, дописать его после первого из этих ключей
	// раздела (вместе с их продолжением комментария); не нашлось ни одного —
	// после последнего ключа раздела.
	After []string
	// Comment — хвостовой комментарий только для НОВОЙ строки: у
	// существующей строки остаётся её собственный.
	Comment string
}

// SetString записывает строку в ключ файла конфига path. Меняется только
// значение: хвостовой комментарий строки, соседние ключи, порядок и пустые
// строки остаются байт в байт. Ключа нет — строка дописывается в раздел;
// нет раздела — он дописывается в конец файла.
//
// До замены файла три проверки, любая — отказ без записи:
//   - разобранное содержимое до и после отличается ровно этим ключом;
//   - новый файл проходит Load — тот же разбор и те же проверки, что у запуска;
//   - файл не изменился с момента чтения (его могли править в редакторе).
//
// Два писателя из ollchat (окно и ключ обвязки) идут по очереди: вся цепочка
// чтение → правка → замена держит замок на файле рядом с конфигом
// (.<имя>.lock). Без него вторая правка читала файл до замены первой и
// затирала её молча (аудит этапа 123: потеря в 23 случаях из 300).
// Замена — fsx.WriteFileAtomicCheck: данные и каталог сброшены на диск,
// права прежние; запущенный от root, писатель возвращает файлу владельца.
//
// Файл без права записи владельцу и файл с жёсткой ссылкой не заменяются:
// первый закрыт нарочно, у второго замена разорвала бы связь, и по второму
// имени остался бы прежний конфиг.
//
// Ссылка не заменяется файлом: пишется её цель, с теми же правами.
func SetString(path string, e KeyEdit) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("конфиг %s: %w", path, err)
	}
	unlock, err := lockConfig(real)
	if err != nil {
		return fmt.Errorf("конфиг %s: %w", path, err)
	}
	defer unlock()

	st, err := os.Stat(real)
	if err != nil {
		return fmt.Errorf("конфиг %s: %w", path, err)
	}
	if err := replaceable(st); err != nil {
		return fmt.Errorf("конфиг %s: %w — выбор не записан", path, err)
	}
	old, err := os.ReadFile(real)
	if err != nil {
		return fmt.Errorf("чтение конфига %s: %w", path, err)
	}
	out, err := setStringIn(old, e)
	if err != nil {
		return fmt.Errorf("конфиг %s: %w", path, err)
	}
	if bytes.Equal(out, old) {
		return nil
	}

	var checkErr error // отказ проверки — уже с путём конфига, без обёртки
	err = fsx.WriteFileAtomicCheck(real, out, st.Mode().Perm(), func(tmpName string) error {
		if err := keepOwner(tmpName, st); err != nil {
			checkErr = fmt.Errorf("конфиг %s: владелец копии: %w", path, err)
		} else if _, _, err := Load(tmpName); err != nil {
			msg := strings.ReplaceAll(err.Error(), tmpName, path)
			checkErr = fmt.Errorf("после правки %s.%s конфиг не проходит проверку, файл не тронут: %s",
				e.Section, e.Key, msg)
		} else if now, err := os.ReadFile(real); err != nil || !bytes.Equal(now, old) {
			checkErr = fmt.Errorf("конфиг %s изменился, пока шла правка, — файл не тронут, повторите", path)
		}
		return checkErr
	})
	switch {
	case checkErr != nil:
		return checkErr
	case err != nil:
		return fmt.Errorf("конфиг %s: %w", path, err)
	}
	return nil
}

// lockTimeout — сколько ждать чужой правки конфига. Правка — доли секунды,
// так что дольше ждать значит, что замок держит зависший процесс.
const lockTimeout = 10 * time.Second

// lockConfig берёт замок на правку конфига real: flock на файле
// .<имя>.lock в том же каталоге. Замок на самом конфиге не годится — его
// файл заменяется переименованием, и второй писатель запер бы уже другой
// файл. Снимая замок, писатель удаляет его файл, чтобы рядом с конфигом
// ничего не оставалось; поэтому взявший замок сверяет, что по имени лежит
// тот самый файл, который он запер: ждавший на удалённом файле иначе
// прошёл бы вместе с тем, кто создал новый.
func lockConfig(real string) (func(), error) {
	name := filepath.Join(filepath.Dir(real), "."+filepath.Base(real)+".lock")
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("замок правки %s: %w", name, err)
		}
		for {
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil || !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
				break
			}
			if time.Now().After(deadline) {
				f.Close()
				return nil, fmt.Errorf("конфиг правит другой процесс дольше %v (замок %s) — повторите", lockTimeout, name)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("замок правки %s: %w", name, err)
		}
		held, herr := f.Stat()
		now, nerr := os.Stat(name)
		if herr == nil && nerr == nil && os.SameFile(held, now) {
			return func() {
				_ = os.Remove(name)
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		f.Close() // прежний писатель удалил этот файл — запирать заново
	}
}

// replaceable — можно ли заменить файл конфига новым: у владельца есть право
// записи, и других имён у файла нет.
func replaceable(st os.FileInfo) error {
	if st.Mode().Perm()&0o200 == 0 {
		return fmt.Errorf("файл только для чтения (права %04o)", st.Mode().Perm())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Nlink > 1 {
		return fmt.Errorf("у файла есть жёсткие ссылки (имён: %d) — замена разорвала бы их, правьте файл руками", sys.Nlink)
	}
	return nil
}

// keepOwner отдаёт копии владельца прежнего файла, если пишет root (sudo):
// иначе конфиг человека стал бы файлом root, и его правка закрылась бы.
// Не root сменить владельца не может — и копия остаётся его.
func keepOwner(tmpName string, st os.FileInfo) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 || int(sys.Uid) == os.Geteuid() && int(sys.Gid) == os.Getegid() {
		return nil
	}
	return os.Chown(tmpName, int(sys.Uid), int(sys.Gid))
}

// setStringIn — сама правка над содержимым файла, без диска.
func setStringIn(data []byte, e KeyEdit) ([]byte, error) {
	if e.Section == "" || e.Key == "" {
		return nil, errors.New("правка без раздела или ключа")
	}
	want := strings.Split(e.Section, ".")
	entries, err := scanTOML(data)
	if err != nil {
		return nil, err
	}

	header := -1        // заголовок нужного раздела
	var inSection []int // пары ключ = значение нужного раздела
	var cur []string
	curArray := false
	for i, en := range entries {
		if en.header {
			cur, curArray = en.path, en.array
			if !en.array && equalPath(en.path, want) {
				header = i
			}
			continue
		}
		if !curArray && equalPath(cur, want) {
			inSection = append(inSection, i)
		}
	}

	lit := quoteBasic(e.Value)
	var out []byte
	if i := findKey(entries, inSection, e.Key); i >= 0 {
		en := entries[i]
		if bytes.IndexByte(data[en.valStart:en.valEnd], '\n') >= 0 {
			return nil, fmt.Errorf("%s.%s: значение занимает несколько строк — такую правку делать руками",
				e.Section, e.Key)
		}
		out = splice(data, en.valStart, en.valEnd, lit)
	} else {
		eol := "\n"
		if bytes.Contains(data, []byte("\r\n")) {
			eol = "\r\n"
		}
		line := e.Key + " = " + lit
		if e.Comment != "" {
			line += "   # " + e.Comment
		}
		if header < 0 {
			var b bytes.Buffer
			b.Write(data)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				b.WriteString(eol)
			}
			if len(data) > 0 {
				b.WriteString(eol)
			}
			b.WriteString("[" + renderPath(want) + "]" + eol + line + eol)
			out = b.Bytes()
		} else {
			anchor := -1
			for _, k := range e.After {
				if anchor = findKey(entries, inSection, k); anchor >= 0 {
					break
				}
			}
			if anchor < 0 && len(inSection) > 0 {
				anchor = inSection[len(inSection)-1]
			}
			at, indent := entries[header].lineEnd, ""
			if anchor >= 0 {
				at = skipContinuation(data, entries[anchor].lineEnd)
				indent = leadingBlank(data, entries[anchor].lineStart)
			}
			ins := indent + line + eol
			if at > 0 && data[at-1] != '\n' {
				ins = eol + ins
			}
			out = splice(data, at, at, ins)
		}
	}
	if err := sameExceptKey(data, out, want, e.Key, e.Value); err != nil {
		return nil, err
	}
	return out, nil
}

// sameExceptKey сверяет разобранное содержимое до и после правки: всё,
// кроме одного ключа, обязано совпасть. Это страховка разбора строк выше —
// ошибись он в границах раздела или значения, правка ушла бы в чужой ключ
// молча, а так она не случится вовсе.
func sameExceptKey(old, out []byte, section []string, key, value string) error {
	var a, b map[string]any
	if _, err := toml.Decode(string(old), &a); err != nil {
		return fmt.Errorf("конфиг сейчас не разбирается, правка не делается: %w", err)
	}
	if _, err := toml.Decode(string(out), &b); err != nil {
		return fmt.Errorf("после правки конфиг не разбирается — файл не тронут: %w", err)
	}
	if a == nil {
		a = map[string]any{}
	}
	m := a
	for _, s := range section {
		next, ok := m[s]
		if !ok {
			nm := map[string]any{}
			m[s] = nm
			m = nm
			continue
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: это не таблица, правка не делается", strings.Join(section, "."))
		}
		m = nm
	}
	m[key] = value
	if !sameTOML(a, b) {
		return fmt.Errorf("правка задела не только %s.%s — файл не тронут", strings.Join(section, "."), key)
	}
	return nil
}

// sameTOML — reflect.DeepEqual для разобранного TOML, только NaN здесь равен
// NaN: значение nan в любом разделе иначе делало любую правку «задевшей
// чужое», и выбор сервера не писался с вводящим в заблуждение отказом.
func sameTOML(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || math.IsNaN(x) && math.IsNaN(y))
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameTOML(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameTOML(x[i], y[i]) {
				return false
			}
		}
		return true
	case []map[string]any:
		y, ok := b.([]map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameTOML(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// tomlEntry — заголовок раздела или пара ключ = значение и её место в файле.
type tomlEntry struct {
	header    bool
	array     bool     // заголовок массива таблиц [[…]]
	path      []string // путь раздела у заголовка, ключ (возможно с точками) у пары
	lineStart int      // начало физической строки
	valStart  int      // пара: начало значения
	valEnd    int      // пара: конец значения, без хвостового комментария
	lineEnd   int      // после перевода строки, на которой кончилось значение или заголовок
}

// scanTOML находит в файле заголовки разделов и пары ключ = значение.
// Это не разборщик TOML, а разметка границ: значения он пропускает,
// понимая строки (в том числе многострочные), массивы и встроенные таблицы
// на нескольких строках — иначе `[` в начале строки внутри массива или
// многострочной строки читался бы заголовком раздела.
//
// BOM в начале файла пропускается: разборщик TOML его принимает, и файл,
// который программа читает, должен и правиться.
func scanTOML(data []byte) ([]tomlEntry, error) {
	var out []tomlEntry
	i := 0
	if bytes.HasPrefix(data, []byte("\xEF\xBB\xBF")) {
		i = 3
	}
	for i < len(data) {
		ls := i
		i = skipBlank(data, i)
		if i >= len(data) {
			break
		}
		switch c := data[i]; {
		case c == '\n' || c == '\r' || c == '#':
			i = afterEOL(data, i)
		case c == '[':
			e := tomlEntry{header: true, lineStart: ls}
			j := i + 1
			if j < len(data) && data[j] == '[' {
				e.array = true
				j++
			}
			path, k, err := scanKey(data, j)
			if err != nil {
				return nil, lineErr(data, ls, err)
			}
			k = skipBlank(data, k)
			closing := "]"
			if e.array {
				closing = "]]"
			}
			if !bytes.HasPrefix(data[k:], []byte(closing)) {
				return nil, lineErr(data, ls, errors.New("заголовок раздела не закрыт"))
			}
			e.path = path
			e.lineEnd = afterEOL(data, k+len(closing))
			out = append(out, e)
			i = e.lineEnd
		default:
			path, k, err := scanKey(data, i)
			if err != nil {
				return nil, lineErr(data, ls, err)
			}
			k = skipBlank(data, k)
			if k >= len(data) || data[k] != '=' {
				return nil, lineErr(data, ls, errors.New("ожидался знак ="))
			}
			k = skipBlank(data, k+1)
			end, err := skipValue(data, k)
			if err != nil {
				return nil, lineErr(data, ls, err)
			}
			e := tomlEntry{path: path, lineStart: ls, valStart: k, valEnd: end, lineEnd: afterEOL(data, end)}
			out = append(out, e)
			i = e.lineEnd
		}
	}
	return out, nil
}

// scanKey читает ключ или путь раздела: голые и взятые в кавычки части через
// точку. Возвращает части без кавычек и место сразу после ключа.
func scanKey(data []byte, i int) ([]string, int, error) {
	var parts []string
	for {
		i = skipBlank(data, i)
		if i >= len(data) {
			return nil, i, errors.New("ключ оборван")
		}
		switch data[i] {
		case '"', '\'':
			end, err := skipValue(data, i)
			if err != nil {
				return nil, i, err
			}
			s := string(data[i:end])
			if data[i] == '"' {
				if u, err := strconv.Unquote(s); err == nil {
					s = u
				} else {
					s = s[1 : len(s)-1]
				}
			} else {
				s = s[1 : len(s)-1]
			}
			parts = append(parts, s)
			i = end
		default:
			j := i
			for j < len(data) && isBareKeyByte(data[j]) {
				j++
			}
			if j == i {
				return nil, i, fmt.Errorf("недопустимый знак %q в ключе", data[i])
			}
			parts = append(parts, string(data[i:j]))
			i = j
		}
		k := skipBlank(data, i)
		if k < len(data) && data[k] == '.' {
			i = k + 1
			continue
		}
		return parts, i, nil
	}
}

// skipValue возвращает конец значения, начавшегося в i.
func skipValue(data []byte, i int) (int, error) {
	if i >= len(data) {
		return i, errors.New("нет значения")
	}
	switch {
	case bytes.HasPrefix(data[i:], []byte(`"""`)):
		return skipMultiline(data, i, `"""`, true)
	case bytes.HasPrefix(data[i:], []byte(`'''`)):
		return skipMultiline(data, i, `'''`, false)
	case data[i] == '"':
		for j := i + 1; j < len(data); j++ {
			switch data[j] {
			case '\\':
				j++
			case '"':
				return j + 1, nil
			case '\n':
				return 0, errors.New("строка не закрыта")
			}
		}
		return 0, errors.New("строка не закрыта")
	case data[i] == '\'':
		for j := i + 1; j < len(data); j++ {
			switch data[j] {
			case '\'':
				return j + 1, nil
			case '\n':
				return 0, errors.New("строка не закрыта")
			}
		}
		return 0, errors.New("строка не закрыта")
	case data[i] == '[' || data[i] == '{':
		return skipBracketed(data, i)
	}
	j := i
	for j < len(data) && data[j] != '#' && data[j] != '\n' && data[j] != '\r' {
		j++
	}
	// Хвостовые пробелы перед комментарием — не часть значения.
	for j > i && (data[j-1] == ' ' || data[j-1] == '\t') {
		j--
	}
	if j == i {
		return 0, errors.New("нет значения")
	}
	return j, nil
}

// skipMultiline пропускает многострочную строку с разделителем delim.
func skipMultiline(data []byte, i int, delim string, escapes bool) (int, error) {
	j := i + len(delim)
	for j < len(data) {
		if escapes && data[j] == '\\' {
			j += 2
			continue
		}
		if bytes.HasPrefix(data[j:], []byte(delim)) {
			j += len(delim)
			// До двух кавычек сразу перед закрывающими принадлежат содержимому.
			for n := 0; n < 2 && j < len(data) && data[j] == delim[0]; n++ {
				j++
			}
			return j, nil
		}
		j++
	}
	return 0, errors.New("многострочная строка не закрыта")
}

// skipBracketed пропускает массив или встроенную таблицу, в том числе на
// нескольких строках и с комментариями между элементами.
func skipBracketed(data []byte, i int) (int, error) {
	var stack []byte
	for j := i; j < len(data); {
		switch c := data[j]; {
		case c == '[' || c == '{':
			stack = append(stack, c)
			j++
		case c == ']' || c == '}':
			if len(stack) == 0 || (c == ']') != (stack[len(stack)-1] == '[') {
				return 0, errors.New("скобки не сходятся")
			}
			stack = stack[:len(stack)-1]
			j++
			if len(stack) == 0 {
				return j, nil
			}
		case c == '"' || c == '\'':
			end, err := skipValue(data, j)
			if err != nil {
				return 0, err
			}
			j = end
		case c == '#':
			j = afterEOL(data, j)
		default:
			j++
		}
	}
	return 0, errors.New("скобка не закрыта")
}

// findKey — индекс пары с ключом key среди пар раздела; -1 — нет такой.
func findKey(entries []tomlEntry, inSection []int, key string) int {
	for _, i := range inSection {
		if p := entries[i].path; len(p) == 1 && p[0] == key {
			return i
		}
	}
	return -1
}

// skipContinuation пропускает строки-продолжения комментария после пары:
// с отступом и сразу с «#». Новая строка не должна разрезать пояснение
// соседнего ключа надвое — в конфиге владельца оно часто на трёх строках.
func skipContinuation(data []byte, at int) int {
	for at < len(data) {
		j := skipBlank(data, at)
		if j == at || j >= len(data) || data[j] != '#' {
			return at
		}
		at = afterEOL(data, j)
	}
	return at
}

func skipBlank(data []byte, i int) int {
	for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
		i++
	}
	return i
}

func afterEOL(data []byte, i int) int {
	if k := bytes.IndexByte(data[i:], '\n'); k >= 0 {
		return i + k + 1
	}
	return len(data)
}

func leadingBlank(data []byte, ls int) string {
	return string(data[ls:skipBlank(data, ls)])
}

func isBareKeyByte(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func splice(data []byte, from, to int, ins string) []byte {
	out := make([]byte, 0, len(data)-(to-from)+len(ins))
	out = append(out, data[:from]...)
	out = append(out, ins...)
	return append(out, data[to:]...)
}

// quoteBasic — строка в кавычках TOML.
func quoteBasic(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// renderPath — путь раздела для заголовка: голые части как есть, прочие в кавычках.
func renderPath(parts []string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		bare := p != ""
		for j := 0; j < len(p); j++ {
			if !isBareKeyByte(p[j]) {
				bare = false
				break
			}
		}
		if bare {
			out[i] = p
		} else {
			out[i] = quoteBasic(p)
		}
	}
	return strings.Join(out, ".")
}

// lineErr называет номер строки файла в ошибке разметки.
func lineErr(data []byte, at int, err error) error {
	return fmt.Errorf("строка %d: %w", bytes.Count(data[:at], []byte("\n"))+1, err)
}
