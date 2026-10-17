package steplog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
)

// Находка 16 (аудит 16.10.2026): журналы шагов старше срока удаляются при
// запуске, свежие, чужие по имени, свой текущий и названный --steps-file —
// остаются.
func TestPruneByKeepDays(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	pat, err := chatlog.ParsePattern("steps-%Y-%m-%d_%H-%M-%S.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	touch := func(name string, age time.Duration) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	day := 24 * time.Hour
	old := touch("steps-2026-10-01_10-00-00.jsonl", 16*day)
	edge := touch("steps-2026-10-10_11-00-00.jsonl", 7*day+time.Hour)
	fresh := touch("steps-2026-10-16_10-00-00.jsonl", 1*day)
	// Имя от даты старта, а писался файл до вчера: служба живёт неделями.
	longRun := touch("steps-2026-09-20_10-00-00.jsonl", 1*day)
	chat := touch("chat-2026-10-01_10-00-00.md", 16*day)
	other := touch("steps-old.jsonl", 16*day)

	w := New(dir, pat, now, "test", true)
	w.Write(Step{Kind: KindTurn})
	cur := w.Path()
	if err := os.Chtimes(cur, now.Add(-30*day), now.Add(-30*day)); err != nil {
		t.Fatal(err)
	}

	n, err := w.Prune(7, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("удалено %d, ждали 2 (старше 7 дней по времени изменения)", n)
	}
	for _, p := range []string{old, edge} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s: старый журнал не удалён", filepath.Base(p))
		}
	}
	for _, p := range []string{fresh, longRun, chat, other, cur} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: удалён, а не должен был: %v", filepath.Base(p), err)
		}
	}

	// Отрицательный срок и ноль у Writer — ничего не удалять.
	touch("steps-2026-09-01_10-00-00.jsonl", 40*day)
	for _, keep := range []int{0, -1} {
		if n, _ := w.Prune(keep, now); n != 0 {
			t.Errorf("срок %d: удалено %d, ждали 0", keep, n)
		}
	}
	// Путь, названный человеком (--steps-file), не чистится вовсе.
	fixed := New(dir, chatlog.FixedName(filepath.Join(dir, "probe.jsonl")), now, "test", true)
	if n, _ := fixed.Prune(1, now); n != 0 {
		t.Errorf("--steps-file: удалено %d, ждали 0", n)
	}
	// Выключенный журнал (nil) — без паники и без удаления.
	var off *Writer
	if n, err := off.Prune(1, now); n != 0 || err != nil {
		t.Errorf("nil: %d, %v", n, err)
	}
}
