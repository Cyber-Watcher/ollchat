// Package fsx — общие мелочи файловой системы: атомарная запись, раскрытие
// домашнего каталога, человеческий размер. Заведён этапом 91 (R0.11), чтобы
// пять мест с прямым os.WriteFile и четыре копии раскрытия `~` жили в одном.
package fsx

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// WriteFileAtomic записывает файл так, что после сбоя на диске остаётся либо
// прежний файл целиком, либо новый целиком — никогда обрезанный.
//
// Порядок: временный файл в том же каталоге → Sync → Close → Rename поверх →
// Sync каталога. Книга («Code a database in 45 steps», 2025, стр. 62)
// оговаривает оба условия: «after a crash, the target of rename() is either
// the old file or the new file. Provided that the data is fsynced before
// renaming» — «после сбоя цель rename() — либо старый файл, либо новый.
// При условии, что данные были fsync-нуты до переименования». Без Sync
// каталога само переименование может не пережить отказ питания.
//
// Права — как у os.WriteFile, которую функция заменяет: у существующего файла
// остаются его собственные, новый получает perm за вычетом umask. До 07.10.2026
// права ставились Chmod(perm) поверх всего, и это било дважды: файл 0600
// после перезаписи становился 0644 (вместе с содержимым, которое его владелец
// закрыл от чужих глаз), а umask 077 человека не действовал вовсе.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	keep, hasOld := existingPerm(path)
	tmp, err := createTemp(dir, filepath.Base(path), perm)
	if err != nil {
		return fmt.Errorf("временный файл рядом с %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("запись %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("закрытие %s: %w", path, err)
	}
	// Временный файл создан с perm, и umask к нему уже применило ядро.
	// Прежние права файла umask не трогает: os.WriteFile их бы тоже не тронула.
	if hasOld {
		if err := os.Chmod(tmpName, keep); err != nil {
			cleanup()
			return fmt.Errorf("права %s: %w", path, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("переименование в %s: %w", path, err)
	}
	return syncDir(dir)
}

// existingPerm — права файла, который сейчас лежит по пути; false — файла нет
// (или это не обычный файл, и подменять его права нечем).
func existingPerm(path string) (os.FileMode, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Mode().Perm(), true
}

// createTemp — os.CreateTemp, но с заданными правами: та всегда создаёт 0600,
// и новый файл не получил бы ни perm, ни umask. Права задаются при создании,
// а не после: так umask применяет само ядро, как у os.WriteFile.
func createTemp(dir, base string, perm os.FileMode) (*os.File, error) {
	for try := 0; ; try++ {
		name := filepath.Join(dir, "."+base+"."+strconv.FormatUint(uint64(rand.Uint32()), 10)+".tmp")
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) && try < 1000 {
			continue
		}
		return f, err
	}
}

// syncDir просит диск записать сам каталог: иначе переименование может
// остаться только в памяти системы.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		// Каталог, куда можно писать, но нельзя читать (права -wx), открыть
		// нечем. Ошибкой это не считаем: переименование уже сделано и файл
		// на месте; без сброса каталога под вопросом только то, переживёт ли
		// оно отказ питания, а исправить это отсюда нельзя.
		return nil
	}
	defer d.Close()
	return dirSyncErr(dir, d.Sync())
}

// dirSyncErr отделяет «файловая система сброс каталога не умеет» от настоящей
// беды. Первое — не ошибка: так отвечают некоторые сетевые и FUSE-системы,
// а данные при этом уже на месте. Второе (EIO и подобные) поднимается:
// запись каталога могла не дойти до диска, и вызывающий должен об этом знать,
// а не верить, что файл переживёт сбой.
func dirSyncErr(dir string, err error) error {
	if err == nil || errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return fmt.Errorf("sync каталога %s: %w", dir, err)
}
