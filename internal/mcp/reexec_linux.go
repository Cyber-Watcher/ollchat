package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Подмена себя новым бинарём в режиме stdio (этап 109, А1).
//
// Процесс stdio — потомок клиента, и связь с ним — потоки, открытые при запуске
// сеанса. Перезапустить его снаружи нельзя: новый процесс получил бы чужие
// потоки, и клиент его не увидел бы (ради этого 11.09.2026 и заводилась служба
// по HTTP). А exec сохраняет номер процесса и открытые потоки — клиент
// продолжает говорить с тем же собеседником, только уже на новом коде.
//
// Главная тонкость — не потерять запрос. exec выбрасывает всё, что процесс
// успел прочитать и не обработал. Поэтому чтение здесь без опережающей
// горутины: ждём данных через poll с таймаутом, и подмена делается только
// когда буфер пуст и ни одно чтение не идёт. Таймаут заодно будит цикл
// для проверки сигнала останова (см. длинный комментарий в Serve).

// watchInterval — как часто сверять свой файл; столько же ждёт poll.
const watchInterval = time.Second

// binaryWatch сравнивает файл, из которого процесс запущен, с тем, что сейчас
// лежит по его пути, а файлы настроек — с тем, какими они были при старте.
type binaryWatch struct {
	path string
	dev  uint64
	ino  uint64

	files    []fileSig
	validate func() error

	// refused — файл, уже отвергнутый trustedStat: о нём сказано один раз,
	// а проверка идёт каждую секунду.
	refused struct{ dev, ino uint64 }
}

// trustedStat — можно ли запускать этот файл вместо себя.
//
// Подмену служба замечает сама и сама же exec-ает новый файл — со своими
// правами и своим окружением, где лежит OLLMCP_TOKEN. Раньше годился любой
// исполняемый файл по пути бинаря: подложенный другим пользователем (чужой
// владелец, запись для группы или для всех) он означал бы чужой код от нашего
// имени. Теперь — только свой или root-а, и писать в него может лишь владелец.
func trustedStat(st *syscall.Stat_t) error {
	if uid := uint32(os.Getuid()); st.Uid != uid && st.Uid != 0 {
		return fmt.Errorf("владелец — uid %d, а не мы (uid %d) и не root", st.Uid, uid)
	}
	if st.Mode&0o022 != 0 {
		return fmt.Errorf("права %o: писать в файл может не только владелец", st.Mode&0o777)
	}
	return nil
}

// trustedBinary — trustedStat для файла по пути: последняя сверка перед exec.
func trustedBinary(path string) error {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return err
	}
	return trustedStat(&st)
}

// fileSig — приметы файла настроек: правка на месте меняет размер или mtime,
// запись через переименование (так сохраняют редакторы) — inode.
type fileSig struct {
	path   string
	exists bool
	ino    uint64
	size   int64
	mtime  time.Time
}

func sigOf(path string) fileSig {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return fileSig{path: path}
	}
	return fileSig{path: path, exists: true, ino: uint64(st.Ino), size: st.Size,
		mtime: time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec))}
}

func newBinaryWatch(srv *Server) *binaryWatch {
	path, err := os.Executable() // " (deleted)" Go отрезает сам
	if err != nil {
		return nil
	}
	var st syscall.Stat_t
	if syscall.Stat("/proc/self/exe", &st) != nil {
		return nil
	}
	w := &binaryWatch{path: path, dev: uint64(st.Dev), ino: uint64(st.Ino), validate: srv.Validate}
	for _, f := range srv.WatchFiles {
		w.files = append(w.files, sigOf(f))
	}
	return w
}

// replaced — пора ли перезапускаться: бинарь подменён или правлены настройки.
func (w *binaryWatch) replaced() bool {
	return w.binaryReplaced() || w.settingsChanged()
}

// binaryReplaced — лежит ли по пути уже другой файл, готовый к запуску.
//
// Подмена через mv атомарна, а копия поверх работающего бинаря невозможна
// вовсе (ETXTBSY). Но файл, только что появившийся под другим именем и
// переименованный, мог ещё дописываться до переименования чужим cp без
// .new — поэтому полсекунды тишины по mtime.
func (w *binaryWatch) binaryReplaced() bool {
	var st syscall.Stat_t
	if syscall.Stat(w.path, &st) != nil {
		return false
	}
	if uint64(st.Dev) == w.dev && uint64(st.Ino) == w.ino {
		return false
	}
	mtime := time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec))
	if st.Mode&0o111 == 0 || st.Size <= 0 || time.Since(mtime) <= 500*time.Millisecond {
		return false
	}
	if err := trustedStat(&st); err != nil {
		if w.refused.dev != uint64(st.Dev) || w.refused.ino != uint64(st.Ino) {
			w.refused.dev, w.refused.ino = uint64(st.Dev), uint64(st.Ino)
			fmt.Fprintf(os.Stderr, "ollmcp: новый бинарь %s не принят (%v), остаюсь на прежнем\n", w.path, err)
		}
		return false
	}
	return true
}

// settingsChanged — правлен ли файл настроек, и годится ли правка.
//
// Негодная правка (опечатка в ключе, предел выше потолка кода) перезапуска
// не вызывает: новый процесс упал бы на старте, и клиент stdio потерял бы
// службу насовсем. Служба остаётся на прежних настройках, причина уходит
// в поток ошибок, а следующая правка проверяется заново.
func (w *binaryWatch) settingsChanged() bool {
	changed := false
	for i, old := range w.files {
		cur := sigOf(old.path)
		if cur == old {
			continue
		}
		if cur.exists && time.Since(cur.mtime) < 500*time.Millisecond {
			return false // ещё пишется — посмотрим на следующем круге
		}
		w.files[i] = cur
		changed = true
	}
	if !changed {
		return false
	}
	if w.validate != nil {
		if err := w.validate(); err != nil {
			fmt.Fprintf(os.Stderr, "ollmcp: правка настроек не принята, остаюсь на прежних: %v\n", err)
			return false
		}
	}
	fmt.Fprintln(os.Stderr, "ollmcp: настройки службы изменились — перезапускаюсь на них")
	return true
}

// serveStdio — цикл режима stdio со слежкой за бинарём и настройками.
func serveStdio(ctx context.Context, srv *Server, verbose bool) error {
	w := newBinaryWatch(srv)
	if w == nil {
		return Serve(ctx, srv, os.Stdin, os.Stdout, verbose)
	}
	return serveWatched(ctx, srv, os.Stdin, os.Stdout, verbose, w, func() error {
		// Сверка ещё раз прямо перед exec: файл могли подменить снова.
		if err := trustedBinary(w.path); err != nil {
			return err
		}
		_ = srv.Steps.Close()
		return syscall.Exec(w.path, os.Args, append(os.Environ(), reexecEnv+"="+srv.Fingerprint()))
	})
}

// WatchBinary закрывает канал, когда файл, из которого запущен процесс,
// подменён новым или правлены настройки (srv.WatchFiles). Для службы по HTTP:
// ей не нужно беречь поток ввода, она дожидается конца текущих запросов
// и зовёт Reexec. nil — слежка недоступна.
func WatchBinary(ctx context.Context, srv *Server) <-chan struct{} {
	w := newBinaryWatch(srv)
	if w == nil {
		return nil
	}
	ch := make(chan struct{})
	go func() {
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if w.replaced() {
					close(ch)
					return
				}
			}
		}
	}()
	return ch
}

// Reexec заменяет процесс новым бинарём с теми же ключами и окружением.
// Возвращается только при неудаче.
func Reexec() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	if err := trustedBinary(path); err != nil {
		return fmt.Errorf("новый бинарь %s не принят: %w", path, err)
	}
	return syscall.Exec(path, os.Args, os.Environ())
}

// serveWatched — сам цикл; exec подставляется, чтобы тест не заменял себя.
//
// Вызовы инструментов идут в своих горутинах (session.go), поэтому подмена
// ждёт не только пустого буфера, но и конца начатых вызовов: их ответы exec
// выбросил бы вместе с процессом. Замеченная подмена запоминается (pending):
// правку настроек replaced сообщает один раз.
func serveWatched(ctx context.Context, srv *Server, in *os.File, outW io.Writer, verbose bool,
	w *binaryWatch, exec func() error) error {
	r := bufio.NewReaderSize(in, 1<<20)
	sess := newSession(srv, bufio.NewWriter(outW), verbose)
	defer sess.close()
	fds := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}}
	pending := false
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := sess.err(); err != nil {
			return err
		}
		if r.Buffered() == 0 {
			if w != nil && !pending && w.replaced() {
				pending = true
			}
			if pending && sess.idle() {
				pending = false
				if err := sess.flush(); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, "ollmcp: перезапускаюсь (новый бинарь или настройки)")
				err := exec()
				// Сюда попадаем, только если exec не удался: служим дальше
				// прежним кодом и больше не пробуем — новый бинарь негоден.
				fmt.Fprintf(os.Stderr, "ollmcp: перезапуск не удался (%v), остаюсь на прежнем бинаре\n", err)
				w = nil
			}
			n, err := unix.Poll(fds, int(watchInterval/time.Millisecond))
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return err
			}
			if n == 0 {
				continue
			}
		}
		line, err := readLine(r)
		if err == io.EOF {
			sess.wait()
			return sess.err()
		}
		if err != nil {
			return err
		}
		if err := sess.handle(ctx, line); err != nil {
			return err
		}
	}
}
