package permissions

// Контрактные тесты класса «необратимое» и защищённых каталогов (аудит
// 16.10.2026, находки 1–3; таблица обходов — из проб аудита, на коде до правки
// в noask без вопроса проходило 33 из 60 строк).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var classDefaultIrreversible = []string{
	"Bash(git push:*)", "Bash(git reset --hard:*)", "Bash(git clean:*)", "Bash(git restore:*)",
	"Bash(git checkout --:*)", "Bash(git branch -D:*)", "Bash(git stash drop:*)",
	"Bash(git stash clear:*)", "Bash(git rebase:*)",
	"Bash(mv:*)", "Bash(truncate:*)", "Bash(shred:*)",
	"Bash(psql:*)", "Bash(mysql:*)", "Bash(sqlite3:*)", "Bash(mongosh:*)", "Bash(redis-cli:*)",
	"Bash(docker:*)", "Bash(podman:*)", "Bash(kubectl:*)", "Bash(terraform:*)", "Bash(ansible-playbook:*)",
}

// classGuard — охранник с умолчаниями конфига, существующим файлом
// «есть.txt», каталогами .ollchat и .git в корне песочницы.
func classGuard(t *testing.T, mode string) *Guard {
	t.Helper()
	g := guardWith(t,
		[]string{"Read(./**)", "Bash(go build:*)", "Bash(go test:*)", "Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(ls:*)"},
		[]string{"Bash(rm:*)", "Bash(rmdir:*)", "Bash(dd:*)", "Bash(sudo:*)", "Bash(chmod:*)", "Bash(curl:*)", "Bash(wget:*)", "Bash(ssh:*)"},
		mode)
	if err := g.set.SetIrreversible(classDefaultIrreversible, g.sandbox.Root()); err != nil {
		t.Fatal(err)
	}
	root := g.sandbox.RealRoot()
	for _, d := range []string{filepath.Join(OwnDirName, "backups"), filepath.Join(GitDirName, "hooks"), "sub"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"есть.txt", "main.go", filepath.Join("sub", "есть.txt"), filepath.Join(OwnDirName, "config.toml"), filepath.Join(GitDirName, "config")} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("данные\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// Каждая строка — действие, которое по смыслу класса (откат дольше 10 минут
// или невозможен) обязано остановиться вопросом либо отказом в noask.
func TestIrreversibleClassAsksInNoask(t *testing.T) {
	g := classGuard(t, ModeNoAsk)
	cases := []string{
		// перезапись существующего файла программой
		"cp /dev/null есть.txt", "cp -f новый.txt есть.txt", "cp есть.txt sub", "cp -t sub есть.txt",
		"cat новый.txt | tee есть.txt", "tee есть.txt < /dev/null", "echo x | tee есть.txt > /dev/null",
		"install -m 644 новый.txt есть.txt", "ln -sf новый.txt есть.txt", "ln -f новый.txt есть.txt",
		"rsync -a --delete src/ dst/", "rsync -a новый.txt есть.txt", "tar -xf a.tar", "tar xf a.tar", "tar --extract -f a.tar",
		"python3 -c \"open('есть.txt','w')\"", "perl -e 'unlink \"есть.txt\"'", "ruby -e 'File.delete(\"есть.txt\")'", "node -e 'require(\"fs\").rmSync(\"есть.txt\")'",
		"awk -i inplace '{print}' есть.txt", "gawk --in-place '{print}' есть.txt", "ed -s есть.txt", "sort -o есть.txt есть.txt", "sort --output=есть.txt есть.txt",
		// обёртки вокруг sed -i
		"env sed -i s/a/b/ есть.txt", "command sed -i s/a/b/ есть.txt", "nohup sed -i s/a/b/ есть.txt", "timeout 5 sed -i s/a/b/ есть.txt",
		"xargs sed -i s/a/b/ < list", "find . -name '*.txt' -exec sed -i s/a/b/ {} +", "sh -c 'sed -i s/a/b/ есть.txt'",
		"bash -c 'git push'", "echo $(sed -i s/a/b/ есть.txt)", "sed --in-place=.bak s/a/b/ есть.txt", "sed -e s/a/b/ -i есть.txt",
		// git
		"git commit --amend --no-edit", "git tag -d v1", "git switch -f main", "git switch --discard-changes main",
		"git update-ref -d refs/heads/x", "git reflog expire --expire=now --all", "git gc --prune=now", "git -c alias.p=push p",
		"git checkout main.go", "git rm -f есть.txt", "git push --force", "git --git-dir=.git push", "git -C . push",
		"git -C sub push", "git stash drop", "git branch -D old", "git reset HEAD~1 --hard", "git checkout -- main.go",
		// `>` в разных написаниях
		"echo x 1> есть.txt", "echo x &> есть.txt", "echo x > ./есть.txt", "echo x > есть.txt 2>&1", "echo $(echo x > есть.txt)",
		"{ echo x; } > есть.txt", "( echo x ) > есть.txt", "echo x >есть.txt", "true && echo x > есть.txt", "ls; echo x > есть.txt",
	}
	var passed []string
	for _, cmd := range cases {
		got := decide(g, cmd)
		if got.Decision == DecisionAllow {
			passed = append(passed, cmd+" ("+got.Reason+")")
		}
	}
	if len(passed) > 0 {
		t.Errorf("в noask без вопроса прошло %d из %d:\n  %s", len(passed), len(cases), strings.Join(passed, "\n  "))
	}
}

// Обратимое и чтение остаются без вопроса: класс не должен спрашивать про всё.
func TestIrreversibleClassLeavesReadsAlone(t *testing.T) {
	g := classGuard(t, ModeNoAsk)
	for _, cmd := range []string{
		"cp есть.txt новый.txt", "cp -r src backup_dir", "tee новый.txt < есть.txt", "tee -a есть.txt < новый.txt",
		"ln -s есть.txt новый.txt", "sort есть.txt", "sort -o новый.txt есть.txt", "tar -cf a.tar src", "tar -tf a.tar", "tar -xkf a.tar",
		"python3 script.py", "python3 -m pytest", "perl script.pl", "node app.js", "awk '{print}' есть.txt", "sed -n '1,5p' есть.txt",
		"git status", "git stash list", "git branch -d merged", "git checkout -b new", "git checkout feature", "git reset --soft HEAD~1",
		"git commit -m x", "git tag v2", "git switch main", "git rm --cached есть.txt", "git gc", "git log --oneline",
		"echo x > новый.txt", "echo x >> есть.txt", "go test ./...", "ls -la", "cat есть.txt | head",
	} {
		got := decide(g, cmd)
		if got.Irreversible {
			t.Errorf("%q помечено необратимым зря: %s", cmd, got.Reason)
		}
		if got.Decision == DecisionDeny {
			t.Errorf("%q запрещено зря: %s", cmd, got.Reason)
		}
	}
}

// Каталоги .ollchat и .git закрыты для записи ЛЮБЫМ путём и в любом режиме,
// включая yolo: `>>`, пишущие программы, код в строке, cd внутрь, подстановка в пути.
func TestProtectedDirsDeniedByEveryWritePath(t *testing.T) {
	for _, mode := range []string{ModeSafe, ModeNoAsk, ModeYolo} {
		g := classGuard(t, mode)
		for _, dir := range []string{OwnDirName, GitDirName} {
			for _, tpl := range []string{
				"echo 'mode = \"yolo\"' > %s/config.toml", "echo 'mode = \"yolo\"' >> %s/config.toml",
				"cp новый.txt %s/config.toml", "cp -t %s новый.txt", "cat новый.txt | tee %s/config.toml", "tee -a %s/config.toml < новый.txt",
				"install -m 600 новый.txt %s/config.toml", "ln -sf /tmp/evil.toml %s/config.toml", "mv новый.txt %s/config.toml",
				"sed -i 's/safe/yolo/' %s/config.toml", "mkdir -p %s/hooks", "touch %s/hooks/pre-commit", "chmod +x %s/hooks/pre-commit",
				"echo x > %s/../%s/config.toml", "echo x > \"$PWD/%s/config.toml\"", "cd %s && echo x > config.toml", "tar -xf a.tar -C %s",
				"sort -o %s/config.toml есть.txt", "dd if=/dev/zero of=%s/config.toml",
			} {
				cmd := strings.ReplaceAll(tpl, "%s", dir)
				got := decide(g, cmd)
				if got.Decision != DecisionDeny {
					t.Errorf("%s: %q — %v (%s), ожидался отказ", mode, cmd, got.Decision, got.Reason)
				}
			}
			root := g.sandbox.RealRoot()
			for _, p := range []string{filepath.Join(root, dir, "config"), filepath.Join(root, "sub", "..", dir, "hooks", "pre-commit")} {
				got := g.Check(Request{Kind: KindWrite, Target: p, Tool: "write_file"})
				if got.Decision != DecisionDeny {
					t.Errorf("%s: write_file %q — %v (%s), ожидался отказ", mode, p, got.Decision, got.Reason)
				}
			}
		}
		// соседи и сами команды git не задеты
		for _, cmd := range []string{"echo x > .ollchatx/f", "echo x > .gitignore", "echo x >> .gitattributes", "git status", "git add .", "cd sub && ls"} {
			if got := decide(g, cmd); got.Decision == DecisionDeny {
				t.Errorf("%s: %q запрещено зря: %s", mode, cmd, got.Reason)
			}
		}
	}
}

// Код в строке интерпретатора не разобрать: считается необратимым (вопрос
// в noask), но обычный запуск скрипта — нет.
func TestInlineInterpreterCodeIsIrreversible(t *testing.T) {
	g := classGuard(t, ModeNoAsk)
	for cmd, want := range map[string]bool{
		"python3 -c 'print(1)'":        true,
		"python3 -c \"open('x','w')\"": true,
		"perl -e 'print 1'":            true,
		"ruby -e 'puts 1'":             true,
		"node -e 'console.log(1)'":     true,
		"php -r 'echo 1;'":             true,
		"python3 app.py":               false,
		"python3 -m http.server":       false,
		"node app.js":                  false,
		// второй круг: ключи самого скрипта после его имени — не код в строке
		"python3 -u -c 'print(1)'":                     true,
		"node --eval 'console.log(1)'":                 true,
		"python3 privatescripts/foo.py -c config.yaml": false,
		"python3 -m pip install -c constraints.txt":    false,
		"node server.js -p 8080":                       false,
		"ruby build.rb -e production":                  false,
	} {
		got := decide(g, cmd)
		if got.Irreversible != want {
			t.Errorf("%q: необратимо %v, ждали %v (%s)", cmd, got.Irreversible, want, got.Reason)
		}
	}
}

// Второй круг (ревью ветки): `~/проект/.git/…` — тот же каталог через дом;
// `proj.git` зеркала — не наш `.git`; `$PWD/.git/config` — наш.
func TestProtectedDirsViaHomeAndMirrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(root, GitDirName, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	sb, err := NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile([]string{"Bash(*)"}, nil, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	g := NewGuard(set, sb, ModeYolo)
	for _, cmd := range []string{
		"echo 'touch /tmp/pwn' > ~/proj/.git/hooks/pre-commit",
		"cp x ~/proj/.ollchat/config.toml",
		"echo x > \"$PWD/.git/config\"",
		"echo x >> \"$HOME/proj/.ollchat/config.toml\"",
	} {
		if got := decide(g, cmd); got.Decision != DecisionDeny {
			t.Errorf("%q — %v (%s), ожидался отказ", cmd, got.Decision, got.Reason)
		}
	}
	for _, cmd := range []string{
		"git clone --mirror x \"$OUT/proj.git\"",
		"tar cf - . > \"$BACKUPS/proj.git/dump.tar\"",
		"echo x > \"$OUT/ollchat.git\"",
		"echo x > ~/other/.git/config",
	} {
		if got := decide(g, cmd); got.Decision == DecisionDeny {
			t.Errorf("%q запрещено зря: %s", cmd, got.Reason)
		}
	}
}
