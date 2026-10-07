package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"strings"
	"testing"
)

// Хеш ревизии 5 — простой SHA-256 от пароля и соли.
//
// Проверка не формальная: у ревизии 6 тот же вход даёт другой результат,
// и перепутать их значит не открыть ни одной книги, ничего при этом
// не сломав явно.
func TestHash2BRevision5(t *testing.T) {
	salt := []byte("12345678")
	want := sha256.Sum256(append([]byte(nil), salt...))
	if got := hash2B(nil, salt, nil, 5); !bytes.Equal(got, want[:]) {
		t.Errorf("ревизия 5 должна быть простым SHA-256")
	}
}

// Хеш ревизии 6 отличается от ревизии 5 и всегда даёт 32 байта.
func TestHash2BRevision6(t *testing.T) {
	salt := []byte("87654321")
	r5 := hash2B(nil, salt, nil, 5)
	r6 := hash2B(nil, salt, nil, 6)
	if len(r6) != 32 {
		t.Fatalf("длина хеша %d, ожидалось 32", len(r6))
	}
	if bytes.Equal(r5, r6) {
		t.Error("ревизии 5 и 6 не должны давать одинаковый хеш")
	}
	// Повторяемость: тот же вход — тот же выход, иначе ключ не соберётся.
	if !bytes.Equal(r6, hash2B(nil, salt, nil, 6)) {
		t.Error("хеш не повторяется на том же входе")
	}
}

// Расшифровка снимает вектор инициализации и дополнение.
func TestDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plain := []byte("текст страницы книги")

	// Собираем то, что лежало бы в файле: IV + шифротекст с дополнением.
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := bytes.Repeat([]byte{3}, aes.BlockSize)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, padded)

	c := &crypt{key: key}
	if got := c.decrypt(append(append([]byte(nil), iv...), enc...)); string(got) != string(plain) {
		t.Errorf("расшифровано %q, ожидалось %q", got, plain)
	}
}

// Испорченные данные не роняют разбор: кусок возвращается как есть.
//
// Книга на девятьсот страниц не должна теряться из-за одного повреждённого
// потока — остальные страницы прочитаются.
func TestDecryptToleratesBadData(t *testing.T) {
	c := &crypt{key: bytes.Repeat([]byte{1}, 32)}
	for _, bad := range [][]byte{nil, []byte("коротко"), bytes.Repeat([]byte{9}, 20)} {
		if got := c.decrypt(bad); !bytes.Equal(got, bad) {
			t.Errorf("испорченные данные должны возвращаться как есть")
		}
	}
	// Пустая расшифровка на nil-приёмнике тоже безопасна.
	var nilCrypt *crypt
	if got := nilCrypt.decrypt([]byte("данные")); string(got) != "данные" {
		t.Error("без расшифровки данные должны проходить насквозь")
	}
}

// aesSealed — то, что лежит в зашифрованном файле вместо data: вектор
// инициализации и шифротекст AES-256 с дополнением PKCS#7.
func aesSealed(key, data []byte) []byte {
	pad := aes.BlockSize - len(data)%aes.BlockSize
	padded := append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := bytes.Repeat([]byte{5}, aes.BlockSize)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return append(iv, out...)
}

// encryptedDoc собирает книгу AES-256 (/V 5 /R 6) с пустым паролем
// пользователя: зашифрованы содержимое, строки /Info, ActualText из раздела
// Properties и картинка JPEG на всю страницу. Возвращает и сам JPEG.
func encryptedDoc(t testing.TB) (doc, jpg []byte) {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, 32)
	valSalt, keySalt := []byte("valsalt1"), []byte("keysalt2")
	u := append(append(hash2B(nil, valSalt, nil, 6), valSalt...), keySalt...)
	block, err := aes.NewCipher(hash2B(nil, keySalt, nil, 6))
	if err != nil {
		t.Fatal(err)
	}
	ue := make([]byte, 32)
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(ue, key)
	hexOf := func(b []byte) string { return "<" + hex.EncodeToString(b) + ">" }
	sealed := func(s string) string { return hexOf(aesSealed(key, []byte(s))) }

	var img bytes.Buffer
	gray := image.NewGray(image.Rect(0, 0, 32, 32))
	for i := range gray.Pix {
		gray.Pix[i] = byte(i * 7)
	}
	if err := jpeg.Encode(&img, gray, nil); err != nil {
		t.Fatal(err)
	}
	jpg = img.Bytes()

	content := "BT /F1 12 Tf 72 720 Td (con) Tj /Span /P0 BDC (X) Tj EMC (gure) Tj ET q 612 0 0 792 0 0 cm /Im0 Do Q"
	doc = build(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> "+
			"/XObject << /Im0 6 0 R >> /Properties << /P0 << /ActualText "+sealed("fi")+" >> >> >> /Contents 4 0 R >>",
		stream("", string(aesSealed(key, []byte(content)))),
		helvetica,
		stream("<< /Type /XObject /Subtype /Image /Width 32 /Height 32 /ColorSpace /DeviceGray "+
			"/BitsPerComponent 8 /Filter /DCTDecode >>", string(aesSealed(key, jpg))),
		"<< /Title "+sealed("Encrypted Title")+" /Author "+sealed("Anon Author")+" >>",
		"<< /Filter /Standard /V 5 /R 6 /Length 256 /CF << /StdCF << /CFM /AESV3 /AuthEvent /DocOpen /Length 32 >> >> "+
			"/StmF /StdCF /StrF /StdCF /U "+hexOf(u)+" /UE "+hexOf(ue)+" /P -4 >>")
	doc = bytes.Replace(doc, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Info 7 0 R /Encrypt 8 0 R"), 1)
	return doc, jpg
}

// Книга AES-256 с пустым паролем читается целиком: не только потоки, но и
// строки вне потоков (/Info, ActualText из Properties) и картинки JPEG.
// Прежде строки оставались шифротекстом, а JPEG брался мимо расшифровки,
// и модели уходил шифротекст под видом картинки.
func TestEncryptedDocumentReadable(t *testing.T) {
	doc, jpg := encryptedDoc(t)
	res, err := Extract(doc, Options{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if res.Title != "Encrypted Title" || res.Author != "Anon Author" {
		t.Errorf("сведения о книге: %q / %q", res.Title, res.Author)
	}
	if got := res.Pages[0].Text; !strings.Contains(got, "configure") {
		t.Errorf("ActualText из Properties не расшифрован: %q", got)
	}
	imgs, err := ExtractImages(doc, ImageOptions{})
	if err != nil || len(imgs) != 1 || !bytes.Equal(imgs[0].Data, jpg) {
		t.Errorf("JPEG не расшифрован: %v, картинок %d", err, len(imgs))
	}
	pages, err := ScanPages(doc)
	if err != nil || len(pages) != 1 || pages[0].Image == nil {
		t.Errorf("страница скана: %v, %+v", err, pages)
	}
}

// Документ, зашифрованный незнакомым способом, отвергается, а не читается
// как мусор.
func TestUnsupportedEncryptionRefused(t *testing.T) {
	d := &Document{
		cache:   map[int]Object{},
		trailer: Dict{"Encrypt": Dict{"Filter": Name("Standard"), "V": int64(2), "R": int64(3)}},
	}
	if c := d.setupCrypt(); c != nil {
		t.Error("RC4 пока не поддержан — должен быть честный отказ")
	}
}
