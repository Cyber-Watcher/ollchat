package graph

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Второй счёт векторов на тот же граф получает отказ, а не гонку файлов.
func TestVectorsLockExcludesSecondWriter(t *testing.T) {
	g, _ := graph(t)
	release, err := lockVectors(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lockVectors(g.dir)
	var locked *VectorsLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("второй писатель прошёл: %v", err)
	}
	if locked.PID != os.Getpid() {
		t.Errorf("в отказе процесс %d, а замок наш (%d)", locked.PID, os.Getpid())
	}
	if !strings.Contains(vectorsBusy(g.dir), "счёт векторов") {
		t.Errorf("занятость не видна: %q", vectorsBusy(g.dir))
	}
	release()
	if s := vectorsBusy(g.dir); s != "" {
		t.Errorf("после снятия замка занятость осталась: %q", s)
	}
	release2, err := lockVectors(g.dir)
	if err != nil {
		t.Fatalf("после снятия замка счёт не идёт: %v", err)
	}
	release2()
}

// Замок мёртвого процесса (kill -9, отключение питания) снимается сам:
// иначе один сбой требовал бы ручной уборки перед каждым счётом.
func TestVectorsLockTakesOverStale(t *testing.T) {
	g, _ := graph(t)
	path := filepath.Join(g.dir, vecLockFile)
	// Номер за пределом pid_max Linux (4 194 304): такого процесса нет.
	if err := os.WriteFile(path, []byte("pid 4194305, начато 2026-01-01T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := vectorsBusy(g.dir); s != "" {
		t.Errorf("мёртвый замок показан занятостью: %q", s)
	}
	release, err := lockVectors(g.dir)
	if err != nil {
		t.Fatalf("брошенный замок не снят: %v", err)
	}
	release()
}

// Оба писателя — полный счёт и догонщик — ходят через одну дверь.
func TestEmbedRefusesWhileVectorsLocked(t *testing.T) {
	g := growGraph(t, 3)
	release, err := lockVectors(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	emb := &countingEmbedder{model: "m", digest: "AAA", dim: 4}
	var locked *VectorsLockedError

	if err := g.EmbedEntities(t.Context(), emb, EmbedOpts{}, nil); !errors.As(err, &locked) {
		t.Fatalf("полный счёт под чужим замком: %v", err)
	}
	if _, err := g.EmbedNewEntities(t.Context(), emb, EmbedOpts{}, 0, nil); !errors.As(err, &locked) {
		t.Fatalf("догонщик под чужим замком: %v", err)
	}
	if emb.asked != 0 {
		t.Errorf("под замком спросили %d векторов", emb.asked)
	}
}
