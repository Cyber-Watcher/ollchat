package kb

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// epubWithTOC собирает книгу EPUB: оглавление без номеров страниц (toc.xhtml),
// две главы прозы и указатель (index.xhtml).
func epubWithTOC(t *testing.T) []byte {
	t.Helper()
	chapter := func(title, sentence string) string {
		return `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>` + title + `</h1>` +
			strings.Repeat(`<p>`+sentence+`</p>`, 40) + `</body></html>`
	}
	var toc strings.Builder
	toc.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Table of Contents</h1><ol>`)
	for i := 0; i < 60; i++ {
		toc.WriteString(`<li><a href="ch1.xhtml">Chapter about kubernetes pods and nodes</a></li>`)
	}
	toc.WriteString(`</ol></body></html>`)
	var index strings.Builder
	index.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Index</h1>`)
	for i := 0; i < 60; i++ {
		index.WriteString(`<p>kubelet, Node Agent</p><p>pods, Scheduling</p>`)
	}
	index.WriteString(`</body></html>`)
	files := []struct{ name, body string }{
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/book.opf", `<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" version="3.0">
			<metadata><dc:title>Kubernetes</dc:title><dc:creator>Автор</dc:creator></metadata>
			<manifest>
			<item href="toc.xhtml" id="toc" media-type="application/xhtml+xml"/>
			<item href="ch1.xhtml" id="c1" media-type="application/xhtml+xml"/>
			<item href="ch2.xhtml" id="c2" media-type="application/xhtml+xml"/>
			<item href="index.xhtml" id="ix" media-type="application/xhtml+xml"/>
			</manifest>
			<spine><itemref idref="toc"/><itemref idref="c1"/><itemref idref="c2"/><itemref idref="ix"/></spine></package>`},
		{"OEBPS/toc.xhtml", toc.String()},
		{"OEBPS/ch1.xhtml", chapter("Pods", "Kubernetes schedules pods onto nodes, and each pod gets its own address.")},
		{"OEBPS/ch2.xhtml", chapter("Services", "A service exposes pods by a stable name, and kube-proxy programs the rules.")},
		{"OEBPS/index.xhtml", index.String()},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	w.Write([]byte("application/epub+zip"))
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Оглавление и указатель EPUB — без номеров страниц — помечаются служебными
// по разделу книги: при нарезке и проходом --kb-flag-toc по старой книге.
// Куски глав признака не получают, и кусок не тянется из оглавления в главу.
func TestEPUBServiceSectionsFlagged(t *testing.T) {
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "k8s.epub"), epubWithTOC(t), 0o644); err != nil {
		t.Fatal(err)
	}
	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	service := map[int]bool{1: true, 4: true} // toc.xhtml и index.xhtml
	var flagged, prose int
	if err := coll.EachChunk(ChunkFilter{}, func(ci ChunkInfo) error {
		inService := service[ci.UnitFrom]
		if service[ci.UnitFrom] != service[ci.UnitTo] {
			t.Errorf("кусок %d#%d тянется из раздела %d в %d: служебный раздел пакуется отдельно", ci.Doc, ci.Ord, ci.UnitFrom, ci.UnitTo)
		}
		if ci.TOC != inService {
			t.Errorf("кусок %d#%d разделов %d–%d: признак %v, ожидалось %v: %.60q", ci.Doc, ci.Ord, ci.UnitFrom, ci.UnitTo, ci.TOC, inService, ci.Text)
		}
		if ci.TOC {
			flagged++
		} else {
			prose++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if flagged == 0 || prose == 0 {
		t.Fatalf("служебных кусков %d, прозы %d — проверять нечего", flagged, prose)
	}

	// Книга проиндексирована до 18.09.2026: признаков нет. Проход обязан
	// вернуть их по разделам книги, а не по строению текста.
	for i := range coll.store.recs {
		coll.store.recs[i].Flags &^= uint16(FlagTOC)
	}
	res, err := coll.FlagTOC(context.Background(), 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged != flagged || res.FlaggedUnits != flagged || res.Changed != flagged || res.EPUBRead != 1 {
		t.Fatalf("проход по разделам EPUB: %+v при %d служебных кусках", res, flagged)
	}
	// Книги нет на диске — проход не падает, а считает её.
	if err := os.Remove(filepath.Join(books, "k8s.epub")); err != nil {
		t.Fatal(err)
	}
	res, err = coll.FlagTOC(context.Background(), 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.EPUBMissing != 1 || res.EPUBRead != 0 {
		t.Fatalf("без файла: %+v", res)
	}
}
