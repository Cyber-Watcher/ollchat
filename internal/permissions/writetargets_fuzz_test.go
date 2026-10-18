package permissions

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Фаззинг writeTargets — какие файлы команда перепишет, создаст или тронет.
// От этой функции зависит класс «необратимое» (irreversible.go): промах в
// сторону «не пишет» пропускает перезапись без вопроса.
//
// Настоящий прогон — с пределом времени и памяти; после него смотреть execs/sec
// в строке прогресса (разбор — чистая функция над строкой, ждать десятки тысяч
// в секунду на ядро; ниже тысячи — цель тормозит):
//
//	GOMEMLIMIT=512MiB go test ./internal/permissions/ -run '^$' -fuzz '^FuzzWriteTargets$' -fuzztime 60s
//
// Без -fuzz прогоняются только затравки — обычный быстрый тест. Найденный вход
// go сам кладёт в internal/permissions/testdata/fuzz/FuzzWriteTargets/.
// Функция файлов не пишет и не читает, поэтому каталог для записи не нужен.
//
// Два вида проверок на одном входе (text, sel, name):
//
// Общие, на произвольной строке text: детерминизм; не разбирается (незакрытая
// кавычка, пусто) — всё пусто; целей не больше, чем слов; opaque непуст только
// когда среди доводов есть ключ (начинается с «-»); программа из списка
// «только читает» не даёт ни целей, ни opaque.
//
// По смыслу, на команде из шаблона sel с безопасным именем файла name: tee, cp,
// mv, sort -o, dd of=, sed -i, truncate, touch, rm называют цель в over или
// touch ТОЧНО так, как она написана; путь к программе (/usr/bin/tee) ничего не
// меняет; sed без -i, ls, cat, grep целей не дают; python3 -c — opaque;
// python3 script.py -c cfg — не opaque (ключ скрипта, а не интерпретатора).
//
// Память: вход длиннее 1<<16 байт отбрасывается; функция выделяет память только
// по числу слов строки.

// fuzzWTTemplates — шаблоны команд; NAME заменяется именем файла.
var fuzzWTTemplates = []struct {
	cmd   string
	where string // over, touch, opaque, none, notopaque
}{
	{"tee NAME", "over"},
	{"tee -a NAME", "touch"},
	{"cp in.txt NAME", "over"},
	{"mv in.txt NAME", "over"},
	{"install -m 644 in.txt NAME", "over"},
	{"sort -o NAME in.txt", "over"},
	{"dd if=in.txt of=NAME", "over"},
	{"sed -i s/a/b/ NAME", "over"},
	{"truncate -s 0 NAME", "over"},
	{"touch NAME", "touch"},
	{"rm NAME", "touch"},
	{"/usr/bin/tee NAME", "over"},
	{"./cp in.txt NAME", "over"},
	{"sed s/a/b/ NAME", "none"},
	{"sed -n p NAME", "none"},
	{"ls NAME", "none"},
	{"cat NAME", "none"},
	{"grep x NAME", "none"},
	{"python3 -c NAME", "opaque"},
	{"python3 NAME -c cfg", "notopaque"},
}

// fuzzWTReadOnly — программы, которые по смыслу ничего не пишут.
var fuzzWTReadOnly = []string{"ls", "cat", "echo", "grep", "head", "tail", "wc", "pwd", "diff", "stat"}

func FuzzWriteTargets(f *testing.F) {
	// Затравки — из irreversible_class_test.go и writingflags_test.go.
	for i, s := range []string{
		"cp /dev/null есть.txt", "cp -f новый.txt есть.txt", "cp есть.txt sub", "cp -t sub есть.txt",
		"tee есть.txt", "tee -a есть.txt", "install -m 644 новый.txt есть.txt", "ln -sf новый.txt есть.txt",
		"ln -f новый.txt есть.txt", "ln новый.txt есть.txt", "rsync -a новый.txt есть.txt",
		"tar -xf a.tar", "tar xf a.tar", "tar -C sub -xf a.tar",
		"python3 -c \"open('есть.txt','w')\"", "python3 foo.py -c config.yaml",
		"perl -e 'unlink \"есть.txt\"'", "perl -pi -e s/a/b/ есть.txt",
		"ruby -e 'File.delete(1)'", "node -e 'x'", "php -r 'x;'",
		"awk -i inplace '{print}' есть.txt", "gawk --in-place '{print}' есть.txt",
		"sort -o есть.txt есть.txt", "sort --output=есть.txt есть.txt",
		"sed -i s/a/b/ есть.txt", "sed --in-place=.bak s/a/b/ есть.txt", "dd if=a of=есть.txt",
		"truncate -s 0 есть.txt", "ed -s есть.txt", "mkdir -p sub/x", "chmod 600 есть.txt",
		"echo 'не закрыта", "", "   ", "ls -la", "cat -- -f",
	} {
		f.Add(s, uint8(i), "есть.txt")
	}
	f.Add("cp a b", uint8(2), "out dir")
	f.Add("tee", uint8(0), "..")

	f.Fuzz(func(t *testing.T, text string, sel uint8, name string) {
		if len(text) > 1<<16 || len(name) > 1<<12 {
			t.Skip()
		}
		fuzzWTGeneral(t, text)

		if !fuzzWTSafeName(name) {
			return
		}
		tpl := fuzzWTTemplates[int(sel)%len(fuzzWTTemplates)]
		cmd := strings.ReplaceAll(tpl.cmd, "NAME", name)
		fuzzWTGeneral(t, cmd)
		over, touch, opaque := writeTargets(cmd)
		switch tpl.where {
		case "over":
			if !fuzzWTHas(over, name) {
				t.Fatalf("writeTargets(%q): цель %q не названа в over: over=%q touch=%q", cmd, name, over, touch)
			}
		case "touch":
			if !fuzzWTHas(touch, name) {
				t.Fatalf("writeTargets(%q): цель %q не названа в touch: over=%q touch=%q", cmd, name, over, touch)
			}
		case "opaque":
			if opaque == "" {
				t.Fatalf("writeTargets(%q): код в строке интерпретатора не замечен", cmd)
			}
		case "notopaque":
			if opaque != "" {
				t.Fatalf("writeTargets(%q): ключ скрипта принят за ключ интерпретатора: %q", cmd, opaque)
			}
		case "none":
			if len(over) != 0 || len(touch) != 0 || opaque != "" {
				t.Fatalf("writeTargets(%q): читающая команда названа пишущей: over=%q touch=%q opaque=%q", cmd, over, touch, opaque)
			}
		}
	})
}

// fuzzWTGeneral — проверки, которым не нужен смысл команды.
func fuzzWTGeneral(t *testing.T, text string) {
	t.Helper()
	over, touch, opaque := writeTargets(text)
	over2, touch2, opaque2 := writeTargets(text)
	if strings.Join(over, "\x00") != strings.Join(over2, "\x00") ||
		strings.Join(touch, "\x00") != strings.Join(touch2, "\x00") || opaque != opaque2 {
		t.Fatalf("writeTargets(%q) недетерминирован", text)
	}
	words, err := SplitWords(text)
	if err != nil || len(words) == 0 {
		if len(over) != 0 || len(touch) != 0 || opaque != "" {
			t.Fatalf("writeTargets(%q): строка не разобрана (%v), а цели названы: %q %q %q", text, err, over, touch, opaque)
		}
		return
	}
	if len(over) > len(words) || len(touch) > len(words) {
		t.Fatalf("writeTargets(%q): целей (%d, %d) больше слов (%d)", text, len(over), len(touch), len(words))
	}
	if opaque != "" {
		dashed := false
		for _, w := range words[1:] {
			dashed = dashed || strings.HasPrefix(w, "-")
		}
		if !dashed {
			t.Fatalf("writeTargets(%q): opaque %q без единого ключа среди доводов", text, opaque)
		}
	}
	prog := programName(words[0])
	for _, ro := range fuzzWTReadOnly {
		if prog == ro && (len(over) != 0 || len(touch) != 0 || opaque != "") {
			t.Fatalf("writeTargets(%q): %s назван пишущим: %q %q %q", text, ro, over, touch, opaque)
		}
	}
}

// fuzzWTSafeName — имя, которое разбор слов и ключи оставят одним доводом как
// есть: без пробелов, кавычек, подстановок, управляющих знаков и ведущего «-».
func fuzzWTSafeName(name string) bool {
	if name == "" || len(name) > 200 || !utf8.ValidString(name) || name[0] == '-' {
		return false
	}
	for _, c := range name {
		if c < 0x21 || c == 0x7f || strings.ContainsRune(`\'"$`+"`"+`;|&<>()*?[]{}~#!=`, c) {
			return false
		}
	}
	return true
}

func fuzzWTHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
