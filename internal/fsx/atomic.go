// Package fsx — общие мелочи файловой системы: атомарная запись, раскрытие
// домашнего каталога, человеческий размер. Заведён этапом 91 (R0.11), чтобы
// пять мест с прямым os.WriteFile и четыре копии раскрытия `~` жили в одном.
package fsx

import (
	"fmt"
	"os"
	"path/filepath"
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
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
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
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return fmt.Errorf("права %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("переименование в %s: %w", path, err)
	}
	return syncDir(dir)
}

// syncDir просит диск записать сам каталог: иначе переименование может
// остаться только в памяти системы.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil // каталог только что использовался; не открылся — не наша беда
	}
	defer d.Close()
	// На некоторых файловых системах Sync каталога не поддерживается и
	// возвращает ошибку; данные при этом уже на месте, поэтому её не поднимаем.
	_ = d.Sync()
	return nil
}
