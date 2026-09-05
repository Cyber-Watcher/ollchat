package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
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
