//go:build live

package live

import (
	"os"
	"testing"
)

// TestRealBreakdown печатает состав настоящей коллекции — то, что теперь
// показывает /kb stats.
func TestRealBreakdown(t *testing.T) {
	if os.Getenv("OLLCHAT_KB_LIVE") == "" {
		t.Skip("нужен OLLCHAT_KB_LIVE=1")
	}
	c := open(t)
	total := 0
	for _, f := range c.Breakdown() {
		name := f.Folder
		if name == "." {
			name = "(в корне)"
		}
		t.Logf("  %-24s %4d книг, кусков %6d, без текста %d", name, f.Books, f.Chunks, f.Scans)
		total += f.Books
	}
	t.Logf("  итого книг: %d", total)
	// Stats.Books — все неудалённые книги; сканы и сбойные это его подмножества,
	// а не слагаемые. Разбивка обязана давать ровно столько же.
	st := c.Stats()
	if total != st.Books {
		t.Errorf("разбивка даёт %d книг, а Stats.Books — %d", total, st.Books)
	}
	t.Logf("Stats: книг %d (с текстом %d, сканов %d, со сбоями %d, убрано %d), кусков %d, термов %d, сегментов %d",
		st.Books, st.Indexed, st.Scans, st.Broken, st.Deleted, st.Chunks, st.Terms, st.Segments)
}
