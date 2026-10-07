package kbserve

import (
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Защита от чужих веб-страниц (аудит 07.10.2026, №14).
//
// Служба без ключа пускает всех, а «все» на петле — это ещё и браузер
// человека: любая открытая им страница может слать запросы на 127.0.0.1.
// «Простой» POST (text/plain, форма) браузер отправляет на чужой адрес без
// спроса: ответ странице он не покажет, но запрос исполнится. А через подмену
// DNS (DNS rebinding) страница attacker.example, чьё имя вдруг указывает на
// 127.0.0.1, читает и ответ: для браузера это её собственный адрес. Спецификация
// MCP (Streamable HTTP) прямо требует проверять Origin.
//
// Проверки три, и все дешёвые:
//   - Origin, если он есть, совпадает с Host. Браузер ставит его на всякий
//     межсайтовый запрос, а свои клиенты (ollchat, curl, клиенты MCP) его
//     не шлют вовсе.
//   - На петле Host — петлевое имя: localhost, 127.0.0.1, [::1]. Подменённое
//     имя приходит в Host как есть, и только по нему подмену DNS и видно:
//     Origin у такой страницы со своим Host совпадает.
//   - Тело POST — только application/json. Такой запрос браузер без
//     предварительного OPTIONS не пошлёт, а на OPTIONS служба разрешения
//     не даёт.

// Protect оборачивает обработчик службы проверками от чужих веб-страниц.
// loopback — служба слушает только петлю (Listen).
func Protect(h http.Handler, loopback bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopback && !loopbackHost(r.Host) {
			apiError(w, http.StatusForbidden, fmt.Errorf("служба слушает только петлю и отвечает "+
				"на имена localhost, 127.0.0.1 и [::1], а запрос пришёл на %q: так выглядит "+
				"подмена DNS из браузера", r.Host))
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !sameHost(o, r.Host) {
			apiError(w, http.StatusForbidden, fmt.Errorf("запрос со страницы %q, чужой для службы: "+
				"веб-страницам служба не отвечает", o))
			return
		}
		if r.Method == http.MethodPost && !isJSON(r.Header.Get("Content-Type")) {
			apiError(w, http.StatusUnsupportedMediaType,
				errors.New("тело запроса — только JSON: нужен заголовок Content-Type: application/json"))
			return
		}
		h.ServeHTTP(w, r)
	})
}

// NewHTTPServer — HTTP-сервер службы: обработчик под защитой Protect.
// Один на обе программы, чтобы защита и сроки у них не разошлись.
func NewHTTPServer(h http.Handler, loopback bool) *http.Server {
	return &http.Server{
		Handler:           Protect(h, loopback),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// loopbackHost — петлевое ли имя в заголовке Host (порт любой).
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// sameHost — указывает ли Origin на ту же машину и порт, что и Host.
// «null» (песочница, file://) хоста не имеет и своим не считается.
func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// isJSON — объявлено ли тело как JSON (параметры вроде charset допустимы).
func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "application/json"
}
