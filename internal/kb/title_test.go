package kb

import "testing"

// Технические заголовки из метаданных распознаются, настоящие — нет.
//
// Живой случай 08.09.2026: книга «Machine Learning in Social Networks» приехала
// с заголовком «502879_1_En_Print.indd» — именем вёрсточного файла Springer.
func TestTechnicalTitleRecognised(t *testing.T) {
	technical := []string{
		"502879_1_En_Print.indd",
		"978-3-030-12345-6_Book.indd",
		"Microsoft Word - глава7.doc",
		"untitled",
		"book.docx",
		"",
		"   ",
		"12345678",
	}
	for _, s := range technical {
		if !technicalTitle(s) {
			t.Errorf("должно считаться техническим: %q", s)
		}
	}

	real := []string{
		"Machine Learning in Social Networks",
		"Graph Algorithms for Data Science",
		"Kubernetes в действии",
		"Head First C#",
		"Practical MLOps",
		"Go Web Programming, 2nd Edition",
		"Refactoring UI",
	}
	for _, s := range real {
		if technicalTitle(s) {
			t.Errorf("настоящее название принято за техническое: %q", s)
		}
	}
}

// Имя файла становится названием как есть — с расширением, как у всех книг
// без метаданных: два вида названий в одной выдаче хуже одного некрасивого.
func TestTitleFromFile(t *testing.T) {
	cases := map[string]string{
		"/books/AI/Machine Learning in Social Networks Embedding Nodes (2021).pdf": "Machine Learning in Social Networks Embedding Nodes (2021).pdf",
		"/books/Kubernetes в действии.epub":                                        "Kubernetes в действии.epub",
		"/books/без расширения":                                                    "без расширения",
	}
	for path, want := range cases {
		if got := titleFromFile(path); got != want {
			t.Errorf("titleFromFile(%q) = %q, ожидалось %q", path, got, want)
		}
	}
}
