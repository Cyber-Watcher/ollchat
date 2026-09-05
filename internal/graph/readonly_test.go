package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Графу нужен доступ на запись даже для чтения — и это надо знать.
//
// **Замер 30.08.2026.** Журналы графа (`entities.jsonl`, `mentions.log`,
// `edges.log`) открываются в режиме добавления уже при открытии графа, даже
// когда дописывать нечего. На каталоге, закрытом от записи, открытие падает
// с `permission denied`.
//
// Это не дефект, а следствие устройства: журналы открываются заранее, чтобы
// сборка могла продолжиться в любой миг. Но у него есть последствие для службы
// знаний под systemd: `ProtectSystem=strict` **обязан** сопровождаться
// `ReadWritePaths` на каталог библиотеки, иначе служба поднимется и будет
// отвечать по книгам, а на первом же вопросе к графу откажет.
//
// Проверка закрепляет факт, а не желаемое. Если однажды журналы станут
// открываться лениво — по первой записи, — этот тест упадёт и напомнит,
// что инструкцию по systemd можно ужесточить, убрав ReadWritePaths.
func TestGraphNeedsWritableDir(t *testing.T) {
	dir := collection(t)
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	gdir := filepath.Join(dir, DirName)
	entries, err := os.ReadDir(gdir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Chmod(filepath.Join(gdir, e.Name()), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(gdir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(gdir, 0o755)
		for _, e := range entries {
			_ = os.Chmod(filepath.Join(gdir, e.Name()), 0o644)
		}
	})

	g2, err := Open(dir, 100, Rules{})
	if err == nil {
		g2.Close()
		t.Fatal("граф открылся на каталоге только для чтения — устройство изменилось.\n" +
			"Это хорошая новость: значит службе знаний больше не нужен ReadWritePaths, " +
			"и инструкцию по systemd в README можно ужесточить.")
	}
	if !os.IsPermission(err) && !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("ожидался отказ по правам, получено: %v", err)
	}
}
