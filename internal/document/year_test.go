package document

import (
	"testing"
	"time"
)

// TestYearFromFilename — имена взяты из настоящей библиотеки владельца.
func TestYearFromFilename(t *testing.T) {
	cases := map[string]int{
		"Фундаментальный подход к программной архитектуре. 2-е издание (2026).pdf": 2026,
		"Мониторинг PostgreSQL 2024.pdf":                                    2024,
		"The Software Developers’ Guidebook 2026.pdf":                       2026,
		"Как делать полезные заметки (2022).epub":                           2022,
		"Agentic GraphRAG Integrating Knowledge Graphs (Final Release).pdf": 0,
		"Книга без года.pdf":                                                0,
		// Год в названии произведения не должен считаться годом издания:
		// диапазон намеренно начинается с 1990.
		"1984.epub": 0,
		// Из нескольких лет берётся наибольший: переиздание новее оригинала.
		"Учебник 2-е издание 2019 переработано 2021.pdf": 2021,
	}
	for name, want := range cases {
		if got := YearFromFilename("/mnt/books/" + name); got != want {
			t.Errorf("%s: год %d, ожидался %d", name, got, want)
		}
	}
}

func TestYearFromCopyright(t *testing.T) {
	cases := map[string]int{
		"Copyright © 2019 Packt Publishing":           2019,
		"© 2021 Иванов И. И.":                         2021,
		"First published 2015 by O’Reilly":            2015,
		"Подписано в печать 12.03.2018":               2018,
		"Переиздание: copyright 2011, copyright 2020": 2020,
		"В книге упоминается стандарт 2016 года":      0,
	}
	for text, want := range cases {
		if got := YearFromCopyright(text); got != want {
			t.Errorf("%q: год %d, ожидался %d", text, got, want)
		}
	}
}

// TestPickYearOrder — порядок источников и отсечение невозможных лет.
func TestPickYearOrder(t *testing.T) {
	// Имя файла старше копирайта и метаданных, но верят ему первым.
	if y, src := PickYear("/b/Книга 2019.pdf", "Copyright © 2021", 2024); y != 2019 || src != YearFromName {
		t.Fatalf("имя файла не в приоритете: %d %s", y, src)
	}
	// Без года в имени берётся копирайт.
	if y, src := PickYear("/b/Книга.pdf", "Copyright © 2021", 2024); y != 2021 || src != YearFromText {
		t.Fatalf("копирайт не подхватился: %d %s", y, src)
	}
	// Без копирайта — метаданные, и они помечены приблизительными.
	y, src := PickYear("/b/Книга.pdf", "текст без копирайта", 2024)
	if y != 2024 || src != YearFromMeta || !src.Approximate() {
		t.Fatalf("метаданные не подхватились: %d %s", y, src)
	}
	// Год из будущего дальше следующего — не год издания.
	if y, _ := PickYear("/b/Книга.pdf", "", time.Now().Year()+5); y != 0 {
		t.Fatalf("принят невозможный год: %d", y)
	}
	if y, src := PickYear("/b/Книга.pdf", "", 0); y != 0 || src != YearNone {
		t.Fatalf("год взялся из ниоткуда: %d %s", y, src)
	}
}

// TestYearNote — оговорка появляется только там, где она чего-то стоит.
func TestYearNote(t *testing.T) {
	now := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	if s := YearNote(2026, now); s != "" {
		t.Errorf("оговорка на свежей книге: %q", s)
	}
	if s := YearNote(2024, now); s != "" {
		t.Errorf("оговорка на книге двух лет: %q", s)
	}
	if s := YearNote(2023, now); s != "книге 3 года" {
		t.Errorf("оговорка на книге трёх лет: %q", s)
	}
	if s := YearNote(2019, now); s != "книге 7 лет" {
		t.Errorf("склонение: %q", s)
	}
	if s := YearNote(2005, now); s != "книге 21 год" {
		t.Errorf("склонение на 21: %q", s)
	}
	if s := YearNote(0, now); s != "" {
		t.Errorf("оговорка без года: %q", s)
	}
}
