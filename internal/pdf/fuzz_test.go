package pdf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// Обстрел разбора: go test -fuzz=FuzzExtract ./internal/pdf/
//
// Без -fuzz прогоняются только затравки — это обычный быстрый тест. Затравки
// собраны из тех же документов, что и прочие тесты пакета: текст, таблица
// /ToUnicode, объектный поток, форма, картинки, ActualText, предикторы,
// встроенный шрифт TrueType.
//
// Находкой считается не только паника (её recover превращает в ErrDamaged,
// а разбор одного объекта гасит её сам — см. objectPanic), но и долгий
// разбор: бюджет работы на время обстрела урезан до 64 МБ, и вход, который
// разбирается дольше fuzzTimeout, значит, работает мимо бюджета.

// fuzzTimeout — сколько может разбираться один вход обстрела.
const fuzzTimeout = 10 * time.Second

// fuzzSeeds — затравки обстрела.
func fuzzSeeds() [][]byte {
	cmap := "begincmap\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
		"2 beginbfchar\n<0003> <0414>\n<0004> <0430>\nendbfchar\n" +
		"1 beginbfrange\n<0100> <01FF> <0410>\nendbfrange\nendcmap"
	pixels := string([]byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 255})
	png := zlibBytes([]byte{1, 10, 20, 30, 2, 1, 1, 1})
	font := ttfWithCmap(cmapFormat4('A', 'Z', 3))
	packedFont := zlibBytes(font)
	return [][]byte{
		docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td (Hello World) Tj ET", helvetica),
		docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td <000300040005> Tj [(a) -120 (b)] TJ ET",
			"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H "+
				"/DescendantFonts [7 0 R] /ToUnicode 6 0 R >>",
			stream("", cmap),
			"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /DW 500 /W [3 [600 700] 10 20 500] >>"),
		docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td (con) Tj /Span <</ActualText (fi)>> BDC (X) Tj EMC "+
			"/Artifact BMC (z) Tj EMC ET q 1 0 0 1 5 5 cm Q",
			"<< /Type /Font /Subtype /Type1 /Encoding << /Differences [65 /A /afii10017 /uni0416] >> "+
				"/FirstChar 65 /Widths [500 600 700] >>"),
		objStmDoc("2"),
		build(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm0 5 0 R /Im0 6 0 R /Im1 7 0 R >> "+
				"/Font << /F1 8 0 R >> >> /Contents 4 0 R >>",
			stream("", "q /Fm0 Do Q q 612 0 0 792 0 0 cm /Im0 Do Q q 200 0 0 100 50 50 cm /Im1 Do Q "+
				"BI /W 2 /H 2 /BPC 8 /CS /G ID xxxx EI"),
			stream("<< /Type /XObject /Subtype /Form /Resources << /Font << /F1 8 0 R >> >> >>",
				"BT /F1 12 Tf 10 700 Td (inside form) Tj ET"),
			stream("<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB "+
				"/BitsPerComponent 8 >>", pixels),
			fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 3 /Height 2 /ColorSpace [/Indexed /DeviceRGB 1 <000000FFFFFF>] "+
				"/BitsPerComponent 8 /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 3 >> /Length %d >>\nstream\n%s\nendstream",
				len(png), png),
			helvetica),
		docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td <00030004> Tj ET",
			"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [6 0 R] >>",
			"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /FontDescriptor 7 0 R >>",
			"<< /Type /FontDescriptor /FontFile2 8 0 R >>",
			fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(packedFont), packedFont)),
		scanDoc("<< /Type /XObject /Subtype /Image /Width 8 /Height 8 /ImageMask true "+
			// Group 4, восемь белых строк: каждая — «V0» (бит 1), затем EOFB.
			"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 8 /Rows 8 >> >>", []byte{0xFF, 0x00, 0x10, 0x01}),
		[]byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
			"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
			"3 0 obj\n<< /Type /Page /Contents 4 0 R >>\nendobj\n" +
			"4 0 obj\n<< /Length 9 /Filter [/ASCIIHexDecode /RunLengthDecode] >>\nstream\n0141FF42>\nendstream\nendobj\n" +
			"5 0 obj\n<< /Type /XRef /Root 1 0 R /Filter /ASCII85Decode /Length 7 >>\nstream\n87cUR~>\nendstream\nendobj\n"),
	}
}

// cmapFormat4 — подтаблица cmap формата 4 с одним сегментом from…to,
// отображённым на глифы начиная с gid, и обязательным завершающим.
func cmapFormat4(from, to, gid uint16) []byte {
	be := binary.BigEndian
	const seg = 2
	sub := make([]byte, 16+seg*8)
	be.PutUint16(sub[0:], 4)
	be.PutUint16(sub[2:], uint16(len(sub)))
	be.PutUint16(sub[6:], seg*2)
	be.PutUint16(sub[14:], to)
	be.PutUint16(sub[16:], 0xFFFF)
	be.PutUint16(sub[16+seg*2:], from)
	be.PutUint16(sub[18+seg*2:], 0xFFFF)
	be.PutUint16(sub[16+seg*4:], gid-from)
	be.PutUint16(sub[18+seg*4:], 1)
	return sub
}

func FuzzExtract(f *testing.F) {
	for _, seed := range fuzzSeeds() {
		f.Add(seed)
	}
	oldBase, oldPanic := workBase, objectPanic
	workBase = 64 << 20
	f.Cleanup(func() { workBase, objectPanic = oldBase, oldPanic })

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("вход больше мегабайта")
		}
		// Сбой, погашенный на одном объекте, — тоже находка. Отметка только
		// до конца этого входа: зависшая горутина не должна писать в чужой.
		var live atomic.Bool
		live.Store(true)
		objectPanic = func(r any) {
			if live.Load() {
				t.Errorf("паника при разборе объекта: %v", r)
			}
		}
		defer live.Store(false)

		done := make(chan struct{})
		go func() {
			defer close(done)
			for what, err := range map[string]error{
				"Extract":       second(Extract(data, Options{})),
				"ExtractImages": second(ExtractImages(data, ImageOptions{MaxCount: 4})),
				"ScanPages":     second(ScanPages(data)),
			} {
				if errors.Is(err, ErrDamaged) && live.Load() {
					t.Errorf("%s: %v", what, err)
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(fuzzTimeout):
			t.Fatalf("вход разбирается дольше %v", fuzzTimeout)
		}
	})
}

// FuzzTrueTypeCmap — таблица cmap встроенного шрифта отдельно: её счётчики
// ничем не ограничены, и именно на ней находились зацикливания.
func FuzzTrueTypeCmap(f *testing.F) {
	f.Add(ttfWithCmap(cmapFormat4('A', 'Z', 3)))
	f.Add(ttfWithCmap(cmapFormat12([3]uint32{'A', 'Z', 3}, [3]uint32{0x1F600, 0x1F64F, 40})))
	f.Fuzz(func(t *testing.T, data []byte) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if m := TrueTypeRunes(data); len(m) > maxCodePoint+1 {
				t.Errorf("символов %d", len(m))
			}
		}()
		select {
		case <-done:
		case <-time.After(fuzzTimeout):
			t.Fatalf("таблица разбирается дольше %v", fuzzTimeout)
		}
	})
}

// second отбрасывает первое значение: нужна только ошибка.
func second[T any](_ T, err error) error { return err }
