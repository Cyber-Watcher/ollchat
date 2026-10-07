package permissions

import (
	"strings"
	"testing"
)

// denyBypasses — записи одной и той же опасной команды, которые раньше
// проходили мимо `Bash(rm:*)`: правило сверялось с сырой строкой по префиксу.
// В режиме noask и после «разрешить инструмент целиком» каждая из них
// выполнялась без вопроса.
var denyBypasses = []string{
	`/bin/rm -rf x`,
	`./rm -rf x`,
	`\rm -rf x`,
	`r\m -rf x`,
	`"rm" -rf x`,
	`'rm' -rf x`,
	`"r"m -rf x`,
	`$'\x72m' -rf x`,
	`$'\162m' -rf x`,
	`FOO=1 rm -rf x`,
	`FOO=1 BAR="a b" /usr/bin/rm -rf x`,
	`env rm -rf x`,
	`env -i FOO=1 rm -rf x`,
	`env -u HOME rm -rf x`,
	`env - rm -rf x`,
	`env -S 'rm -rf x'`,
	`env -u #X rm -rf x`,
	`command rm -rf x`,
	`command -p rm -rf x`,
	`exec rm -rf x`,
	`exec -a name rm -rf x`,
	`nohup rm -rf x`,
	`nice rm -rf x`,
	`nice -n 5 rm -rf x`,
	`nice -n5 rm -rf x`,
	`nice --adjustment=5 rm -rf x`,
	`time rm -rf x`,
	`time -p rm -rf x`,
	`timeout 5 rm -rf x`,
	`timeout -s KILL 5 rm -rf x`,
	`timeout --signal=KILL -k 1 5 rm -rf x`,
	`stdbuf -oL rm -rf x`,
	`setsid rm -rf x`,
	`busybox rm -rf x`,
	`nohup nice -n 5 timeout 5 env FOO=1 /bin/rm -rf x`,
	`echo $(rm -rf x)`,
	`echo "$(rm -rf x)"`,
	"echo `rm -rf x`",
	"echo \"`rm -rf x`\"",
	`echo $(echo $(rm -rf x))`,
	`X=$(rm -rf x) make`,
	`echo ${X:-$(rm -rf x)}`,
	`echo $(( $(rm -rf x) + 1 ))`,
	`cat <(rm -rf x)`,
	`tee >(rm -rf x) < /dev/null`,
	`echo x | xargs rm -rf`,
	`echo x | xargs -0 -n1 rm -rf`,
	`echo x | xargs -I{} rm -rf {}`,
	`echo x | xargs -I {} -P 4 rm -rf {}`,
	`find . -exec rm {} \;`,
	`find . -name '*.tmp' -exec rm -f {} +`,
	`find . -execdir rm {} \;`,
	`find . -ok rm {} \;`,
	`find . -okdir rm {} \;`,
	`find . -exec sh -c 'rm -rf "$1"' _ {} \;`,
	`sh -c 'rm -rf x'`,
	`bash -c "rm -rf x"`,
	`bash -lc "rm -rf x"`,
	`bash -o pipefail -c 'rm -rf x'`,
	`/bin/sh -c -- 'rm -rf x'`,
	`zsh -c 'rm -rf x'`,
	`sh -c "sh -c 'rm -rf x'"`,
	`sh -c 'echo $(rm -rf x)'`,
	`eval "rm -rf x"`,
	`eval rm -rf x`,
	`builtin eval 'rm -rf x'`,
	`trap 'rm -rf x' EXIT`,
	`watch -n 1 rm -rf x`,
	`flock /tmp/lock rm -rf x`,
	`flock /tmp/lock -c 'rm -rf x'`,
	`if true; then rm -rf x; fi`,
	`while false; do rm -rf x; done`,
	`! rm -rf x`,
	`{ rm -rf x; }`,
	`(rm -rf x)`,
	`f() { rm -rf x; }; f`,
	`2>/dev/null rm -rf x`,
	`>out rm -rf x`,
	`ls; rm -rf x`,
	`ls && rm -rf x`,
	`ls || rm -rf x`,
	`ls | rm -rf x`,
	`ls & rm -rf x`,
	"ls\nrm -rf x",
	"echo hi # don't\nrm -rf x",
	`arr=( # it's
); rm -rf x # '`,
	"cat <<EOF\n$(rm -rf x)\nEOF",
	"cat <<EOF\nтекст\nEOF\nrm -rf x",
	// «)», которая у оболочки тело $(…) не закрывает: образец case,
	// комментарий, встроенный документ, $'…'. Закрой её подсчёт скобок —
	// и остаток тела в двойных кавычках читался бы как простой текст.
	`echo "$(case y in *) rm -rf x;; esac)"`,
	`echo "$(if true; then case y in *) rm -rf x;; esac; fi)"`,
	"echo \"$( # )\nrm -rf x)\"",
	"echo \"$(cat <<EOF\n)\nEOF\nrm -rf x)\"",
	`echo "$(echo $'\')'; rm -rf x)"`,
	`echo "${X:-$(echo }; rm -rf x)}"`,
	`a=(one $(rm -rf x) three)`,
	`sudo rm -rf x`,
	`sudo -u root rm -rf x`,
	`alias del='rm -rf'`,
}

// TestDenyCannotBeBypassed — главный инвариант: запрет сильнее любого
// режима и любого сеансового разрешения, как бы ни была записана команда.
func TestDenyCannotBeBypassed(t *testing.T) {
	type setup struct {
		name  string
		mode  string
		grant bool
	}
	setups := []setup{
		{"safe", ModeSafe, false},
		{"auto-edit", ModeAutoEdit, false},
		{"noask", ModeNoAsk, false},
		{"инструмент разрешён на сеанс", ModeSafe, true},
		{"noask и инструмент разрешён", ModeNoAsk, true},
	}
	for _, st := range setups {
		g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)", "Bash(sudo:*)"}, st.mode)
		if st.grant {
			if err := g.GrantSessionTool("bash"); err != nil {
				t.Fatal(err)
			}
		}
		for _, cmd := range denyBypasses {
			if got := decide(g, cmd); got.Decision != DecisionDeny {
				t.Errorf("%s: %q: вышло %v (%s), ожидался запрет", st.name, cmd, got.Decision, got.Reason)
			}
		}
	}
}

// Сеансовое разрешение «эту команду до конца сеанса» не выдаётся на строку,
// внутри которой запрещённая команда.
func TestGrantSessionRefusesHiddenDeny(t *testing.T) {
	g := guardWith(t, nil, []string{"Bash(rm:*)"}, ModeSafe)
	for _, cmd := range []string{`env rm -rf x`, `echo $(rm -rf x)`, `/bin/rm x`} {
		if err := g.GrantSession(KindBash, cmd); err == nil {
			t.Errorf("%q: разрешение на сеанс выдано, хотя внутри запрещённая команда", cmd)
		}
	}
}

// Запрет, записанный с путём к программе, ловит и голое имя, и другой путь.
func TestDenyRuleWithPathMatchesAnyPath(t *testing.T) {
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(/usr/bin/curl:*)"}, ModeNoAsk)
	for _, cmd := range []string{`curl http://x`, `/bin/curl http://x`, `/usr/bin/curl http://x`} {
		if got := decide(g, cmd); got.Decision != DecisionDeny {
			t.Errorf("%q: вышло %v (%s), ожидался запрет", cmd, got.Decision, got.Reason)
		}
	}
}

// Многословный запрет сверяется с командой после разбора: `git push`
// ловится и в обёртке, и с кавычками.
func TestMultiWordDenyAfterUnwrap(t *testing.T) {
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(git push:*)"}, ModeNoAsk)
	for _, cmd := range []string{`env git push origin`, `git "push" origin`, `sh -c 'git push'`} {
		if got := decide(g, cmd); got.Decision != DecisionDeny {
			t.Errorf("%q: вышло %v (%s), ожидался запрет", cmd, got.Decision, got.Reason)
		}
	}
	if got := decide(g, "git pull origin"); got.Decision != DecisionAllow {
		t.Errorf("git pull не под запретом: вышло %v (%s)", got.Decision, got.Reason)
	}
}

// Обычные команды разбор не задевает: имя запрещённой программы в роли
// аргумента, строки в одинарных кавычках, тело встроенного документа
// и `command -v` ничего не запускают.
func TestDenyLeavesOrdinaryCommandsAlone(t *testing.T) {
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)", "Bash(curl:*)", "Bash(kill:*)"}, ModeNoAsk)
	for _, cmd := range []string{
		`git rm --cached file.txt`,
		`docker rm old`,
		`echo rm -rf x`,
		`grep -rn "rm -rf" .`,
		`grep -E 'rm|curl' notes.txt`,
		`echo 'echo $(rm -rf x)'`,
		`sh -c "echo 'rm -rf x'"`,
		`command -v rm`,
		`find . -name '*.go' -exec gofmt -l {} +`,
		`git commit -m "kill: убрать лишнее"`,
		`ps aux | grep kill`,
		`env | grep PATH`,
		`go test ./... 2>&1 | tail -20`,
		`for f in *.go; do gofmt -l "$f"; done`,
		"cat <<'EOF' > s.sh\nrm -rf build\nEOF",
		"cat <<EOF > s.sh\nrm -rf build\nEOF",
		`ls # rm -rf x`,
		`[ -f go.mod ] && go build ./...`,
		`echo $((1 + 2))`,
		`x=$(( $(wc -l < f) + 1 ))`,
		`make -j4 X=$(nproc)`,
		`case "$1" in start|stop) svc "$1";; (restart) svc stop;; *) echo usage;; esac`,
		`files=(rm.go kill.go); echo "${files[@]}"`,
	} {
		if got := decide(g, cmd); got.Decision != DecisionAllow {
			t.Errorf("%q: вышло %v (%s), ожидалось разрешение", cmd, got.Decision, got.Reason)
		}
	}
}

// Имя программы, вычисляемое при запуске, сверить с запретами нельзя —
// решает человек, даже в noask и при разрешённом инструменте.
func TestComputedProgramNameAsks(t *testing.T) {
	cmds := []string{
		`$X -rf x`,
		`X=rm; $X -rf x`,
		`"$X" -rf x`,
		`${X} -rf x`,
		`$(echo rm) -rf x`,
		"`echo rm` -rf x",
		// Шаблоны имён раскрывает только оболочка: простая команда идёт
		// без неё, и `/bin/r? -rf x` там просто не найдётся.
		`true; /bin/r? -rf x`,
		`true; /bin/r[m] -rf x`,
		`{rm,-rf,x}`,
		`env $X -rf x`,
		`sh -c "$CMD"`,
		`eval "$CMD"`,
	}
	for _, mode := range []string{ModeSafe, ModeNoAsk} {
		g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, mode)
		if err := g.GrantSessionTool("bash"); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range cmds {
			got := decide(g, cmd)
			if got.Decision != DecisionAsk {
				t.Errorf("%s: %q: вышло %v (%s), ожидался вопрос", mode, cmd, got.Decision, got.Reason)
			}
			if !strings.Contains(got.Reason, "при запуске") {
				t.Errorf("%s: %q: причина не объясняет вопрос: %s", mode, cmd, got.Reason)
			}
		}
	}

	// Без запретов на команды сверять не с чем — noask ведёт себя как раньше.
	g := guardWith(t, []string{"Bash(*)"}, []string{"Read(./.env)"}, ModeNoAsk)
	if got := decide(g, `$X -rf x`); got.Decision != DecisionAllow {
		t.Errorf("без запретов bash: вышло %v (%s), ожидалось разрешение", got.Decision, got.Reason)
	}
}

// Разрешения не расширились: `Bash(ls:*)` по-прежнему разрешает только
// строку, которая начинается с ls, — путь, кавычки и обёртка спрашивают.
func TestAllowRulesNotLoosened(t *testing.T) {
	g := guardWith(t, []string{"Bash(ls:*)"}, nil, ModeSafe)
	if got := decide(g, "ls -la"); got.Decision != DecisionAllow {
		t.Errorf("ls -la: вышло %v (%s), ожидалось разрешение", got.Decision, got.Reason)
	}
	for _, cmd := range []string{`/bin/ls -la`, `\ls -la`, `"ls" -la`, `env ls -la`, `FOO=1 ls`} {
		if got := decide(g, cmd); got.Decision != DecisionAsk {
			t.Errorf("%q: вышло %v (%s), ожидался вопрос", cmd, got.Decision, got.Reason)
		}
	}
}

// На macOS файловая система не различает регистр, и `RM` — тот же rm.
func TestDenyFoldsCaseWhereFilesystemDoes(t *testing.T) {
	saved := foldNames
	t.Cleanup(func() { foldNames = saved })

	foldNames = true
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, ModeNoAsk)
	for _, cmd := range []string{`RM -rf x`, `/bin/Rm -rf x`} {
		if got := decide(g, cmd); got.Decision != DecisionDeny {
			t.Errorf("%q: вышло %v (%s), ожидался запрет", cmd, got.Decision, got.Reason)
		}
	}

	foldNames = false
	g = guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, ModeNoAsk)
	if got := decide(g, `RM -rf x`); got.Decision != DecisionAllow {
		t.Errorf("с различением регистра RM — другая программа: вышло %v (%s)", got.Decision, got.Reason)
	}
}

// Разбор находит каждую запускаемую команду — и только их.
func TestScanCommandsFindsEveryProgram(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`go build ./...`, []string{"go build ./..."}},
		{`FOO=1 /usr/bin/env -i rm -rf x`, []string{"env -i rm -rf x", "rm -rf x"}},
		{`echo "$(date)" | tee log`, []string{"date", "echo $(date)", "tee log"}},
		{`find . -exec grep -l x {} \;`, []string{"find . -exec grep -l x {} ;", "grep -l x {}"}},
		{`sh -c 'a; b' && c`, []string{"sh -c a; b", "a", "b", "c"}},
		{`xargs -n1`, []string{"xargs -n1"}},
		{"cat <<EOF\n$(date)\nEOF", []string{"date", "cat"}},
		{`timeout 5 sleep 1`, []string{"timeout 5 sleep 1", "sleep 1"}},
	}
	for _, c := range cases {
		scan := scanCommands(c.cmd)
		var got []string
		for _, f := range scan.found {
			got = append(got, f.text)
		}
		if strings.Join(got, " | ") != strings.Join(c.want, " | ") {
			t.Errorf("%q:\n  найдено  %q\n  ожидалось %q", c.cmd, got, c.want)
		}
		if scan.unknown != "" {
			t.Errorf("%q: имя сочтено вычисляемым: %q", c.cmd, scan.unknown)
		}
	}
}

// Простая команда запускается без оболочки, и разбивка у запуска и у сверки
// одна: `#` здесь не комментарий, а обычный знак.
func TestSplitWordsMatchesDirectRun(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`go test ./...`, []string{"go", "test", "./..."}},
		{`grep "a b" 'c d' e\ f`, []string{"grep", "a b", "c d", "e f"}},
		{`echo "r\m"`, []string{"echo", "rm"}},
		{`env -u #X rm`, []string{"env", "-u", "#X", "rm"}},
		{`echo ""`, []string{"echo", ""}},
	}
	for _, c := range cases {
		got, err := SplitWords(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("SplitWords(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
	if _, err := SplitWords(`echo "открыта`); err == nil {
		t.Error("незакрытая кавычка должна быть ошибкой")
	}
}

// Безумная вложенность не роняет разбор и не проходит молча.
func TestDeepNestingAsks(t *testing.T) {
	cmd := "rm -rf x"
	for i := 0; i < maxShellDepth+5; i++ {
		cmd = "echo $(" + cmd + ")"
	}
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(curl:*)"}, ModeNoAsk)
	if got := decide(g, cmd); got.Decision != DecisionAsk {
		t.Errorf("вышло %v (%s), ожидался вопрос", got.Decision, got.Reason)
	}
}
