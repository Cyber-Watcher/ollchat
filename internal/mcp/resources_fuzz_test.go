package mcp

import (
	"strings"
	"testing"
)

// Фаззинг chunkID — разбор адреса ресурса kb://коллекция/документ/кусок.
//
// Настоящий прогон — с пределом времени и памяти; после него смотреть execs/sec
// в строке прогресса (ниже тысячи на ядро — цель зависла или вход тяжёл):
//
//	export PATH=$HOME/sdk/go1.26.9/bin:$PATH
//	GOMEMLIMIT=512MiB go test ./internal/mcp/ -run '^$' -fuzz '^FuzzChunkID$' -fuzztime 60s
//
// Без -fuzz прогоняются только затравки — обычный быстрый тест. Найденный вход
// go сам кладёт в internal/mcp/testdata/fuzz/FuzzChunkID/.
//
// Свойства:
//  1. детерминизм; при false id пуст;
//  2. при true адрес начинается с kb://, а id = голова + "#" + хвост, где адрес
//     = kb:// + голова + "/" + хвост, хвост непуст и без "/" (номер куска — ПОСЛЕДНИЙ
//     сегмент пути, так записано в комментарии к ресурсам);
//  3. по шаблону kb://{collection}/{doc}/{chunk} ни один сегмент головы не пуст;
//  4. если в адресе нет "#", обратный путь chunkURI(id) возвращает тот же адрес.
//
// Память: разбор не выделяет по числу из входа; вход длиннее 1<<16 отбрасывается.

func FuzzChunkID(f *testing.F) {
	// Затравки — из resources_test.go (TestChunkURIRoundTrip) и комментариев resources.go.
	f.Add("kb://books/167/1")
	f.Add("kb://projectdocs/7131/413")
	f.Add("kb://" + "status")
	f.Add("kb://books/167/")
	f.Add("kb://books/167")
	f.Add("kb://books")
	f.Add("kb://")
	f.Add("")
	f.Add("http://example.test/books/167/1")
	f.Add("KB://books/167/1")
	f.Add("kb://books/167/1/2")
	f.Add("kb://books/167#1")
	f.Add("kb://books/16\x007/1\n")
	f.Add("kb://книги/167/1")

	f.Fuzz(func(t *testing.T, uri string) {
		if len(uri) > 1<<16 {
			t.Skip()
		}
		id, ok := chunkID(uri)
		if id2, ok2 := chunkID(uri); id2 != id || ok2 != ok {
			t.Fatalf("chunkID(%q) недетерминирован: (%q,%v) и (%q,%v)", uri, id, ok, id2, ok2)
		}
		if !ok {
			if id != "" {
				t.Fatalf("chunkID(%q) = (%q, false): при отказе id должен быть пуст", uri, id)
			}
			return
		}
		rest, has := strings.CutPrefix(uri, resourceScheme)
		if !has {
			t.Fatalf("chunkID(%q) принял адрес не со схемой %s: %q", uri, resourceScheme, id)
		}
		cut := strings.LastIndex(rest, "/")
		if cut < 0 {
			t.Fatalf("chunkID(%q) = %q: в адресе нет «/» перед номером куска", uri, id)
		}
		head, tail := rest[:cut], rest[cut+1:]
		if id != head+"#"+tail {
			t.Fatalf("chunkID(%q) = %q, ждали %q", uri, id, head+"#"+tail)
		}
		if tail == "" {
			t.Fatalf("chunkID(%q) = %q: номер куска пуст", uri, id)
		}
		for _, seg := range strings.Split(head, "/") {
			if seg == "" {
				t.Fatalf("chunkID(%q) = %q: пустой сегмент в голове %q — по шаблону коллекция/документ/кусок такого быть не должно", uri, id, head)
			}
		}
		if !strings.Contains(uri, "#") {
			if back := chunkURI(id); back != uri {
				t.Fatalf("chunkURI(chunkID(%q)) = %q — обратный путь не замкнут", uri, back)
			}
		}
	})
}
