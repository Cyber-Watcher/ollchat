package kbserve

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// fakeLookup — разрешатель с заранее заданными ответами: тест не должен
// зависеть от /etc/hosts и DNS машины, на которой идёт.
func fakeLookup(names map[string][]string) lookupFunc {
	return func(_ context.Context, _, host string) ([]netip.Addr, error) {
		list, ok := names[host]
		if !ok {
			return nil, errors.New("нет такого имени")
		}
		var out []netip.Addr
		for _, s := range list {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

// Какие адреса считаются «только петлёй». Главное — пустой хост: мёртвая
// loopback() из internal/mcp считала его петлёй, а это все интерфейсы машины.
func TestLoopbackOnly(t *testing.T) {
	lookup := fakeLookup(map[string][]string{
		"localhost": {"127.0.0.1", "::1"},
		"kb.corp":   {"10.0.0.5"},
		"mixed":     {"127.0.0.1", "192.168.1.7"},
		"mapped":    {"::ffff:127.0.0.1"},
	})
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8377", true},
		{"127.0.0.2:8377", true},
		{"[::1]:8377", true},
		{"localhost:8377", true},
		{"mapped:8377", true},
		{":8377", false},
		{"0.0.0.0:8377", false},
		{"[::]:8377", false},
		{"192.168.1.5:8377", false},
		{"[::ffff:10.0.0.1]:8377", false},
		{"kb.corp:8377", false},
		{"mixed:8377", false},   // хоть один адрес в сети — уже не петля
		{"unknown:8377", false}, // не разрешилось — не петля
		{"127.0.0.1", false},    // без порта — не адрес вовсе
		{"просто текст", false},
	}
	for _, c := range cases {
		got, why := loopbackOnly(c.addr, lookup)
		if got != c.want {
			t.Errorf("%q: получено %v (%v), ожидалось %v", c.addr, got, why, c.want)
		}
		if !got && why == nil {
			t.Errorf("%q: отказ без объяснения", c.addr)
		}
	}
}

// Без ключа служба не открывает порт сети, а отказ говорит, что делать.
func TestListenRefusesNetworkWithoutToken(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0"} {
		ln, _, err := Listen(addr, "")
		if err == nil {
			ln.Close()
			t.Fatalf("%s без ключа: порт открыт", addr)
		}
		for _, want := range []string{"OLLMCP_TOKEN", "127.0.0.1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: в отказе нет подсказки %q: %v", addr, want, err)
			}
		}
	}
}

// Петля без ключа и сеть с ключом — законные запуски.
func TestListenAllowsLoopbackOrToken(t *testing.T) {
	ln, loop, err := Listen("127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("петля без ключа: %v", err)
	}
	ln.Close()
	if !loop {
		t.Error("127.0.0.1 не признан петлёй")
	}

	ln, loop, err = Listen("0.0.0.0:0", "ключ")
	if err != nil {
		t.Fatalf("сеть с ключом: %v", err)
	}
	ln.Close()
	if loop {
		t.Error("0.0.0.0 признан петлёй")
	}
}
