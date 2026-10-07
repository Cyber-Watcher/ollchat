package graph

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Подмена журнала переписанным, с копией прежнего.
//
// **Беда.** До 07.10.2026 подмена шла двумя переименованиями: прежний файл —
// в «.bak-…», затем новый — на его место. Обрыв (питание, kill -9) между
// ними оставлял каталог графа без журнала: реестр понятий открывался пустым,
// и новые номера пошли бы с единицы поверх существующих связей и векторов
// (аудит, 4.5). Второе переименование к тому же молча затирало копию с тем же
// именем, если две подмены попадали в одну секунду.
//
// **Решение.** Копия заводится жёсткой ссылкой на прежний файл, а новый
// встаёт на место ОДНИМ переименованием: в любой миг под прежним именем лежит
// целый журнал — прежний или новый. Файловая система без жёстких ссылок
// получает копию байт. Занятое имя копии не затирается: берётся следующее.

// swapIn ставит готовый файл tmp на место path, оставив прежний файл копией
// под именем bak (или bak.1, bak.2…, если оно занято). Возвращает имя копии.
// Не вышло — path остаётся прежним, копия убирается, а заготовку tmp убирает
// вызывающий: переносу номеров книг она нужна и после неудачи (bookmap.go).
func swapIn(path, tmp, bak string) (string, error) {
	used, err := keepBackup(path, bak)
	if err != nil {
		return "", fmt.Errorf("копия %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(used)
		return "", err
	}
	return used, nil
}

// keepBackup делает копию path под свободным именем от bak: жёсткой ссылкой,
// а где их нет — копией байт с записью на диск.
func keepBackup(path, bak string) (string, error) {
	for i := 0; i < 100; i++ {
		name := bak
		if i > 0 {
			name = fmt.Sprintf("%s.%d", bak, i)
		}
		err := os.Link(path, name)
		if err == nil {
			return name, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		// Жёстких ссылок нет (или нельзя) — копия байт; занятое имя
		// и здесь не затирается.
		err = copyFileExcl(path, name)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("свободного имени для копии %s нет", bak)
}

// copyFileExcl копирует файл в новый, которого ещё нет, и пишет его на диск.
func copyFileExcl(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(to)
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		os.Remove(to)
		return err
	}
	return dst.Close()
}

// writeSynced пишет новый файл целиком и на диск — заготовку для swapIn.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}
