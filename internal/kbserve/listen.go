package kbserve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// Кому открыт порт службы.
//
// **Без ключа — только петля** (решение владельца 07.10.2026). Служба стоит
// на рабочей машине владельца и отдаёт его библиотеку, а `ollchat --serve`
// к тому же исполняет инструменты графа. Пустой OLLMCP_TOKEN значит «пускать
// всех», и пока адрес петлевой, «все» — это процессы самой машины. Раньше
// служба поднималась без ключа на любом адресе и только писала предупреждение
// в поток ошибок: пример из README (`--serve 0.0.0.0:8377`) с забытым ключом
// открывал библиотеку всей сети, и заметить это было не по чему. Теперь такой
// запуск — отказ, который говорит, что делать.
//
// Правило одно на обе программы (`ollchat --serve` и `ollmcp --http`):
// двух представлений о том, когда можно без ключа, быть не должно.

// Listen открывает порт службы.
//
// loopback — слушает ли служба только петлю: от него зависит, какие имена
// машины она принимает в запросе (Protect).
func Listen(addr, token string) (ln net.Listener, loopback bool, err error) {
	loopback, why := loopbackOnly(addr, net.DefaultResolver.LookupNetIP)
	if token == "" && !loopback {
		return nil, false, noTokenError(addr, why)
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		return nil, false, err
	}
	// Сверка с тем, что открылось на деле: имя машины при открытии разрешается
	// заново, и ответ мог смениться между двумя вопросами.
	if token == "" {
		if a, ok := ln.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() {
			got := ln.Addr().String()
			_ = ln.Close()
			return nil, false, noTokenError(addr, fmt.Errorf("открылся %s", got))
		}
	}
	return ln, loopback, nil
}

// noTokenError — отказ, который объясняет, что делать.
func noTokenError(addr string, why error) error {
	return fmt.Errorf("без ключа доступа служба слушает только петлевой адрес, "+
		"а %q открыт сети (%v).\n"+
		"Задайте ключ — OLLMCP_TOKEN=$(openssl rand -hex 32) — "+
		"или слушайте петлю: 127.0.0.1:<порт>", addr, why)
}

// lookupFunc — как разрешать имя машины; в тестах подменяется, чтобы не
// зависеть от настроек разрешателя.
type lookupFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

// loopbackOnly — слушал бы адрес только петлю. Второе значение объясняет «нет».
//
// Пустой хост («:8377»), 0.0.0.0 и «::» — это все интерфейсы машины. Имя
// годится, только если ВСЕ его адреса петлевые: имя, которое ведёт и на
// петлю, и в сеть, открыло бы порт наружу при другом ответе разрешателя.
// Мёртвая loopback() из internal/mcp считала петлёй и пустой хост — то есть
// ровно «все интерфейсы».
func loopbackOnly(addr string, lookup lookupFunc) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if host == "" {
		return false, errors.New("пустой хост — это все сетевые интерфейсы")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.Unmap().IsLoopback() {
			return false, fmt.Errorf("%s — не петлевой адрес", host)
		}
		return true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := lookup(ctx, "ip", host)
	if err != nil {
		return false, fmt.Errorf("имя %s не разрешилось: %w", host, err)
	}
	if len(ips) == 0 {
		return false, fmt.Errorf("у имени %s нет адресов", host)
	}
	for _, ip := range ips {
		if !ip.Unmap().IsLoopback() {
			return false, fmt.Errorf("имя %s ведёт на %s", host, ip)
		}
	}
	return true, nil
}
