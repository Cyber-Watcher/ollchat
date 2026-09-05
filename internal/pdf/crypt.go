package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/sha512"
)

// Зашифрованные PDF с пустым паролем пользователя.
//
// **Почему их надо читать, а не отвергать.** Огромная доля покупных книг
// зашифрована — но не от читателя: пароль пользователя пустой, а шифрование
// стоит ради ограничений на печать и копирование, которые адресованы
// программе, а не человеку. Любой просмотрщик открывает такую книгу молча.
//
// Замер 30.08.2026: книга «Dependency Injection Principles, Practices,
// and Patterns» открывалась просмотрщиком, а ollchat объявлял её нечитаемой
// и терял целиком. Со стороны это выглядело как дефект нашего разборщика —
// им и было.
//
// **Что поддержано.** Стандартный обработчик безопасности, версия 5 (`/V 5`,
// `/R 5` и `/R 6`) с AES-256 — то, чем зашифрованы современные книги. У него
// ключ файла общий, без примешивания номера объекта, и потому расшифровка
// не требует протаскивать номера объектов через весь разбор.
//
// **Чего нет.** Старые RC4 и AES-128 (`/V 1`, `/V 2`, `/V 4`): там ключ
// свой у каждого объекта, и это отдельная работа. Такие файлы по-прежнему
// отвергаются — честно и с объяснением, а не молча.
//
// **Пароль подбирать не пытаемся.** Пустой — и только он: книга, закрытая
// настоящим паролем, закрыта намеренно, и обходить это не наше дело.

// crypt расшифровывает содержимое документа.
type crypt struct {
	key []byte // ключ файла, 32 байта
}

// setupCrypt разбирает словарь /Encrypt и готовит расшифровку.
//
// Возвращает nil, если документ зашифрован способом, которого мы не понимаем:
// вызывающий тогда отказывается от файла с объяснением.
func (d *Document) setupCrypt() *crypt {
	enc, ok := d.Resolve(d.trailer["Encrypt"]).(Dict)
	if !ok {
		return nil
	}
	if name, _ := d.Resolve(enc["Filter"]).(Name); name != "Standard" {
		return nil // чужой обработчик безопасности
	}
	v, _ := toInt(d.Resolve(enc["V"]))
	r, _ := toInt(d.Resolve(enc["R"]))
	if v != 5 || (r != 5 && r != 6) {
		return nil // RC4 и AES-128 — отдельная работа, см. заголовок файла
	}
	if cfm := d.cryptMethod(enc); cfm != "AESV3" {
		return nil
	}

	u, _ := d.Resolve(enc["U"]).(String)
	ue, _ := d.Resolve(enc["UE"]).(String)
	if len(u) < 48 || len(ue) < 32 {
		return nil
	}
	valSalt, keySalt := []byte(u)[32:40], []byte(u)[40:48]

	// Проверка пустого пароля пользователя: совпал ли отпечаток.
	if !bytes.Equal(hash2B(nil, valSalt, nil, r), []byte(u)[:32]) {
		return nil // пароль не пустой — книга закрыта намеренно
	}
	// Ключ файла лежит в /UE, зашифрованный промежуточным ключом.
	inter := hash2B(nil, keySalt, nil, r)
	block, err := aes.NewCipher(inter)
	if err != nil {
		return nil
	}
	key := make([]byte, 32)
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(key, []byte(ue)[:32])
	return &crypt{key: key}
}

// cryptMethod достаёт способ шифрования потоков из /CF /StdCF /CFM.
func (d *Document) cryptMethod(enc Dict) Name {
	cf, ok := d.Resolve(enc["CF"]).(Dict)
	if !ok {
		return ""
	}
	std, ok := d.Resolve(cf["StdCF"]).(Dict)
	if !ok {
		return ""
	}
	name, _ := d.Resolve(std["CFM"]).(Name)
	return name
}

// decrypt расшифровывает данные потока или строки.
//
// Первые шестнадцать байт — вектор инициализации, остальное — шифротекст.
// Данные короче или не кратные блоку возвращаются как есть: испорченный
// кусок не должен ронять разбор всей книги.
func (c *crypt) decrypt(data []byte) []byte {
	if c == nil || len(data) <= aes.BlockSize {
		return data
	}
	body := data[aes.BlockSize:]
	if len(body)%aes.BlockSize != 0 {
		return data
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return data
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, data[:aes.BlockSize]).CryptBlocks(out, body)

	// Дополнение PKCS#7: последний байт говорит, сколько лишнего.
	if n := int(out[len(out)-1]); n > 0 && n <= aes.BlockSize && n <= len(out) {
		out = out[:len(out)-n]
	}
	return out
}

// hash2B — хеш пароля по алгоритму 2.B из спецификации.
//
// У ревизии 5 это простой SHA-256; у ревизии 6 — намеренно дорогой цикл,
// чтобы подбор пароля стоил времени. Цикл дословно по спецификации: он
// не «примерно такой», в нём каждая мелочь влияет на результат.
func hash2B(password, salt, udata []byte, r int) []byte {
	sum := sha256.Sum256(concat(password, salt, udata))
	k := sum[:]
	if r == 5 {
		return k
	}
	for round := 0; ; round++ {
		k1 := bytes.Repeat(concat(password, k, udata), 64)
		block, err := aes.NewCipher(k[:16])
		if err != nil {
			return k
		}
		e := make([]byte, len(k1))
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1)

		var mod int
		for _, b := range e[:16] {
			mod += int(b)
		}
		switch mod % 3 {
		case 0:
			s := sha256.Sum256(e)
			k = s[:]
		case 1:
			s := sha512.Sum384(e)
			k = s[:]
		default:
			s := sha512.Sum512(e)
			k = s[:]
		}
		// Условие остановки: не раньше 64 витков и пока последний байт
		// не станет достаточно малым.
		if round >= 63 && int(e[len(e)-1]) <= round-31 {
			break
		}
	}
	return k[:32]
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
