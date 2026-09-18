package epub

import (
	"strings"
	"testing"
)

// tocText — оглавление без номеров страниц, как в EPUB.
func tocText(n int) string {
	var b strings.Builder
	b.WriteString("Table of Contents\n")
	for i := 1; i <= n; i++ {
		b.WriteString("Chapter ")
		b.WriteString(strings.Repeat("I", i%4+1))
		b.WriteString(": Working with containers and pods\n")
	}
	return b.String()
}

const proseText = `Introduction
Kubernetes schedules pods onto nodes. Each pod gets an address.
Services expose pods by a stable name; the kube-proxy programs the rules.
This chapter explains both.
Further reading follows in the next chapter, where deployments are described in detail.
Deployments roll pods out gradually, and a failed rollout is rolled back.
Labels select pods; selectors are plain equality checks.
Namespaces divide a cluster between teams.
Quotas limit what a namespace may consume.
`

// Служебная глава узнаётся по манифесту, имени файла, заголовку и первой
// строке — и только при строении списка; проза с тем же именем не служебная.
func TestServiceSection(t *testing.T) {
	toc := tocText(20)
	cases := []struct {
		href, title, text string
		nav, want         bool
		why               string
	}{
		{"OEBPS/nav.xhtml", "", toc, true, true, "навигационный документ"},
		{"OEBPS/nav.xhtml", "", proseText, true, true, "nav — служебный по манифесту, даже если строение прозы"},
		{"OEBPS/toc.xhtml", "", toc, false, true, "имя файла toc"},
		{"OEBPS/toc1.xhtml", "", toc, false, true, "имя файла с номером"},
		{"OEBPS/B21159_TOC_ePub.xhtml", "", toc, false, true, "издательское имя с _TOC_"},
		{"OEBPS/ix01.html", "Index", "Index\n• ACI (Azure Container Instances), Key Terms\n" + strings.Repeat("• Amazon EKS, defined, Key Terms\n", 12), false, true, "указатель O'Reilly без номеров страниц"},
		{"OEBPS/part0003.xhtml", "part0003", toc, false, true, "оглавление под безликим именем — по первой строке"},
		{"OEBPS/c001.xhtml", "Title", "Go Programming\nAuthor\n2025\n\n" + toc, false, true, "титул и оглавление одним файлом"},
		{"OEBPS/chapter0001.html", "Copyright Page", "Copyright 2023\nWritten by the author\nContents\n" + strings.Repeat("Why Use The Command Line?\nLearning The Shell\nTerminal Emulators\nNavigation\nThe File System Tree\nThe Working Directory\n", 4), false, true, "оглавление после копирайта"},
		{"OEBPS/index.html", "", proseText, false, false, "index.html одной-файловой книги — проза"},
		{"OEBPS/index_split_003.html", "Unknown", proseText, false, false, "index_split_NNN — так зовут все главы"},
		{"OEBPS/ch05.xhtml", "Contents", proseText, false, false, "заголовок Contents у прозы не делает её оглавлением"},
		{"OEBPS/ch05.xhtml", "", "Contents\n" + proseText, false, false, "первая строка Contents у прозы"},
		{"OEBPS/text00146.html", "part0146", "Command Summary\nModule 1: Introduction\n" + strings.Repeat("• ls – list files\n• -l – long\n", 10), false, false, "список команд — не оглавление"},
		{"OEBPS/toc.xhtml", "", "Contents\nOne\nTwo\n", false, false, "слишком короткая"},
	}
	for _, c := range cases {
		if got := serviceSection(c.href, c.title, c.text, c.nav); got != c.want {
			t.Errorf("%s %q nav=%v → %v, ожидалось %v — %s", c.href, c.title, c.nav, got, c.want, c.why)
		}
	}
}

// Extract помечает служебные главы, отдаёт путь файла и признак nav.
func TestExtractMarksServiceSections(t *testing.T) {
	data := buildEPUB(t, true, map[string]string{
		"META-INF/container.xml": container,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" version="3.0" unique-identifier="bookid">
  <metadata><dc:title>Книга</dc:title></metadata>
  <manifest>
    <item href="nav.xhtml" id="nav" media-type="application/xhtml+xml" properties="nav"/>
    <item href="ch1.xhtml" id="one" media-type="application/xhtml+xml"/>
    <item href="index.xhtml" id="ix" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="nav"/><itemref idref="one"/><itemref idref="ix"/></spine>
</package>`,
		"OEBPS/nav.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><nav epub:type="toc"><ol>` +
			strings.Repeat(`<li><a href="ch1.xhtml">Chapter one about pods</a></li>`, 10) + `</ol></nav></body></html>`,
		"OEBPS/ch1.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Chapter one</h1>` +
			strings.Repeat(`<p>Kubernetes schedules pods onto nodes, and each pod gets an address.</p>`, 6) + `</body></html>`,
		"OEBPS/index.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Index</h1>` +
			strings.Repeat(`<p>pods, 12, 45</p><p>nodes, 13</p>`, 8) + `</body></html>`,
	})
	res, err := Extract(data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sections) != 3 {
		t.Fatalf("разделов %d, ожидалось 3", len(res.Sections))
	}
	want := []struct {
		href         string
		nav, service bool
	}{{"OEBPS/nav.xhtml", true, true}, {"OEBPS/ch1.xhtml", false, false}, {"OEBPS/index.xhtml", false, true}}
	for i, w := range want {
		s := res.Sections[i]
		if s.Href != w.href || s.Nav != w.nav || s.Service != w.service {
			t.Errorf("раздел %d: %q nav=%v service=%v, ожидалось %+v", i+1, s.Href, s.Nav, s.Service, w)
		}
	}
}
