package clipboard

import "testing"

// Буфер обмена предлагает список типов, и выбрать надо тот, который Ollama
// действительно примет, а не первый попавшийся.
func TestPickPrefersPNG(t *testing.T) {
	cases := []struct {
		name    string
		offered []string
		want    string
	}{
		{"только PNG", []string{"TARGETS", "image/png"}, "image/png"},
		{"PNG предпочтительнее JPEG", []string{"image/jpeg", "image/png"}, "image/png"},
		{"JPEG, если PNG нет", []string{"TARGETS", "image/jpeg"}, "image/jpeg"},
		{"регистр и пробелы не мешают", []string{" IMAGE/PNG "}, "image/png"},
		{"только текст", []string{"TARGETS", "UTF8_STRING", "text/plain"}, ""},
		{"пусто", nil, ""},
		{"неподдерживаемая картинка", []string{"image/webp", "image/tiff"}, ""},
	}
	for _, c := range cases {
		if got := pick(c.offered); got != c.want {
			t.Errorf("%s: pick(%v) = %q, ожидалось %q", c.name, c.offered, got, c.want)
		}
	}
}
