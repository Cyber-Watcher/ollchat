package permissions

import (
	"path/filepath"
	"strings"
	"testing"
)

// benchGuard — сторож с песочницей и запретами для bash, как у newTestGuard:
// без песочницы protectedDir не разбирает строку, без запретов bashDeny
// не сверяет найденные команды — и замер мерил бы не рабочий путь.
func benchGuard(b *testing.B) *Guard {
	b.Helper()
	root, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatalf("EvalSymlinks: %v", err)
	}
	sb, err := NewSandbox(root, false, false, 512)
	if err != nil {
		b.Fatalf("NewSandbox: %v", err)
	}
	set, err := Compile(
		[]string{"Read(./**)", "Bash(go build:*)", "Bash(ls:*)", "Bash(grep:*)", "Bash(head:*)"},
		[]string{"Write(./**)", "Bash(*)"},
		[]string{"Bash(rm:*)", "Bash(sudo:*)", "Bash(curl:*)"},
		sb.Root())
	if err != nil {
		b.Fatalf("Compile: %v", err)
	}
	return NewGuard(set, sb, ModeSafe)
}

// longBashCommand — длинная составная команда: склейки, перенаправления,
// подстановки и тело sh -c, ни одна часть не под запретом — строка проходит
// все проверки checkBash до конца.
func longBashCommand() string {
	part := `grep -rn "func" ./internal | head -50 > out.txt; ls -la "$(pwd)/docs" >> log.txt && sh -c 'echo $(date) | tee -a stamp.txt'`
	return strings.Repeat(part+"; ", 20) + "go build ./..."
}

// BenchmarkCheckBashLong — одна проверка длинной команды bash сторожем.
func BenchmarkCheckBashLong(b *testing.B) {
	g := benchGuard(b)
	req := Request{Kind: KindBash, Target: longBashCommand(), Tool: "bash"}
	if res := g.Check(req); res.Decision == DecisionDeny {
		b.Fatalf("команда для замера не должна запрещаться: %s", res.Reason)
	}
	b.ReportAllocs()
	for b.Loop() {
		g.Check(req)
	}
}
