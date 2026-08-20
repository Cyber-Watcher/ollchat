package chatlog

import (
	"crypto/rand"
	"fmt"
)

// Идентификатор обмена: четыре знака на запуск программы плюс номер обмена
// внутри него — `k7f3-01`. Так по журналу видно и то, какие записи сделаны
// одним запуском, и в каком порядке шли вопросы.
//
// Алфавит без знаков, которые путают глазом и при диктовке: нет 0 и o, 1 и l,
// i, u. Тридцать символов на четыре знака — 810 тысяч вариантов на запуск,
// а внутри одного файла журнала совпадение невозможно по построению.
const (
	idAlphabet   = "23456789abcdefghjkmnpqrstvwxyz"
	sessionIDLen = 4
)

// NewSessionID выдаёт идентификатор запуска. Берётся crypto/rand: он не требует
// засева, а цена одного вызова за весь сеанс работы неощутима.
func NewSessionID() string {
	buf := make([]byte, sessionIDLen)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand на Linux не отказывает, но молча отдать пустой
		// идентификатор нельзя: без него записи журнала не собрать в обмен.
		panic("chatlog: нет источника случайных чисел: " + err.Error())
	}
	out := make([]byte, sessionIDLen)
	for i, b := range buf {
		out[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return string(out)
}

// FormatTurnID собирает идентификатор обмена. Номер 0 означает «вне обмена»:
// начало сеанса, смена сервера или модели, приложение файла к контексту.
// Пустой идентификатор сеанса даёт пустую строку — записи без пометки.
func FormatTurnID(session string, turn int) string {
	if session == "" {
		return ""
	}
	return fmt.Sprintf("%s-%02d", session, turn)
}
