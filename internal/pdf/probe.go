package pdf

import "fmt"

// FragDump — временный пробник: показывает куски первой строки таблицы.
func (d *Document) FragDump(pageNo int, contains string) []string {
	pages := d.Pages()
	if pageNo > len(pages) {
		return nil
	}
	e := newExtractor(d)
	e.page(pages[pageNo-1])
	var out []string
	for _, f := range e.frags {
		if contains != "" && !containsStr(f.text, contains) {
			continue
		}
		out = append(out, fmt.Sprintf("x=%7.1f y=%7.1f w=%6.1f size=%5.1f %q", f.x, f.y, f.w, f.size, f.text))
	}
	return out
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
