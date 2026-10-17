package chatlog

import (
	"path/filepath"
	"testing"
	"time"
)

// Glob — образец всех имён шаблона: имя, построенное Name, под него подходит,
// чужие имена нет; у шаблона без директив, у устаревшего и у FixedName образца
// нет (чистить по нему нечего или нельзя).
func TestPatternGlob(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 5, 3, 0, time.Local)
	for _, tc := range []struct {
		pattern string
		match   []string
		miss    []string
	}{
		{"steps-%Y-%m-%d_%H-%M-%S.jsonl", nil,
			[]string{"steps-old.jsonl", "steps-2026-10-17.jsonl", "chat-2026-10-17_09-05-03.md", "steps-2026-1x-17_09-05-03.jsonl"}},
		{"chat-%y%m%d.md", []string{"chat-991231.md"}, []string{"chat-2026.md"}},
		{"a[1]*-%Y.log", []string{"a[1]*-1999.log"}, []string{"a1x-1999.log"}},
	} {
		p, err := ParsePattern(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		g := p.Glob()
		for _, name := range append([]string{p.Name(at)}, tc.match...) {
			if ok, err := filepath.Match(g, name); !ok || err != nil {
				t.Errorf("%s: %q не подошло под %q (%v)", tc.pattern, name, g, err)
			}
		}
		for _, name := range tc.miss {
			if ok, _ := filepath.Match(g, name); ok {
				t.Errorf("%s: чужое имя %q подошло под %q", tc.pattern, name, g)
			}
		}
	}
	plain, _ := ParsePattern("chat.md")
	for name, p := range map[string]*Pattern{"без директив": plain, "устаревший": LegacyPattern("chat-2006-01-02.md"),
		"FixedName": FixedName("/x/steps.jsonl"), "nil": nil} {
		if g := p.Glob(); g != "" {
			t.Errorf("%s: образец %q, ждали пусто", name, g)
		}
	}
}
