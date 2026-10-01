package maint

import "testing"

// Отчёт по каталогу считает только книги ИЗ ИНДЕКСА, и файл, который лежит
// в каталоге библиотеки, но в индекс не попал, в «осталось» не входит вовсе.
// 01.10.2026 на этом молчании каталог /OS/Linux напечатал «осталось 0 (100%)»
// при 35 файлах на диске и 34 книгах в индексе, и я доложил владельцу, что
// разбирать нечего. Этот тест держит фильтр по каталогу: предупреждение
// должно считать свои файлы, а не чужие.
func TestOutsideIndexCountsOnlyThisFolder(t *testing.T) {
	files := []string{
		"/lib/OS/Linux/Mastering Ubuntu Server 5th ed (2026).pdf",
		"/lib/OS/Linux/Second Linux Book.pdf",
		"/lib/IoT/AI for IoT Cookbook 2026.pdf",
		"/lib/Coding/C# and .NET/Aspire 2026.pdf",
	}
	cases := []struct {
		folder string
		want   int
	}{
		{"/OS/Linux", 2},
		{"OS/Linux", 2},
		{"OS/Linux/", 2},
		{"Linux", 2}, // хвост пути тоже каталог
		{"/IoT", 1},
		{"", 4},         // вся библиотека
		{"/Infosec", 0}, // каталог без таких файлов — молчать
	}
	for _, c := range cases {
		if got := outsideIndex(files, c.folder); got != c.want {
			t.Errorf("outsideIndex(…, %q) = %d, ожидалось %d", c.folder, got, c.want)
		}
	}
}

// Каталог не подстрокой: «Linux» не должен захватывать «Linux-Hardening»
// соседнего каталога — иначе предупреждение назовёт чужие книги своими
// (та же болезнь, что в my-mistakes #46).
func TestOutsideIndexFolderIsNotSubstring(t *testing.T) {
	files := []string{
		"/lib/OS/Linux-Hardening/Book.pdf",
		"/lib/OS/Linux/Book.pdf",
	}
	if got := outsideIndex(files, "OS/Linux"); got != 1 {
		t.Errorf("outsideIndex(…, %q) = %d, ожидалось 1", "OS/Linux", got)
	}
}
