package graph

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Отметки о разобранных кусках.
//
// Сборка графа по всей библиотеке — это дни работы модели, и она обязательно
// будет прервана: обрывом связи, перезагрузкой, занятой картой. Отметки нужны,
// чтобы продолжить с того же места, а не начать заново: на 249 тысячах кусков
// «начать заново» означает выбросить неделю.
//
// Формат тот же, что у остальных журналов графа: двоичные записи постоянной
// длины, только дозапись, оборванный хвост отбрасывается при чтении.

const (
	progressFile = "progress.log"
	progressSize = 12 // книга, номер куска, признак
)

// Признаки разбора куска.
const (
	MarkDone    uint32 = 1 // разобран, сущности записаны
	MarkEmpty   uint32 = 2 // разобран, ничего не нашлось — это тоже результат
	MarkSkipped uint32 = 3 // пропущен: модель не дала разбираемого ответа
)

// Progress — отметки о разобранных кусках.
type Progress struct {
	mu   sync.RWMutex
	f    *os.File
	w    *bufio.Writer
	mark map[uint64]uint32
}

func openProgress(dir string) (*Progress, error) {
	p := &Progress{mark: map[uint64]uint32{}}
	path := filepath.Join(dir, progressFile)
	if err := p.load(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	p.f, p.w = f, bufio.NewWriterSize(f, 32*1024)
	return p, nil
}

func (p *Progress) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 128*1024)
	buf := make([]byte, progressSize)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		key := ChunkKey{
			Doc: binary.LittleEndian.Uint32(buf[0:]),
			Ord: binary.LittleEndian.Uint32(buf[4:]),
		}.Pack()
		p.mark[key] = binary.LittleEndian.Uint32(buf[8:])
	}
}

// Mark отмечает кусок разобранным.
func (p *Progress) Mark(chunk ChunkKey, mark uint32) error {
	var buf [progressSize]byte
	binary.LittleEndian.PutUint32(buf[0:], chunk.Doc)
	binary.LittleEndian.PutUint32(buf[4:], chunk.Ord)
	binary.LittleEndian.PutUint32(buf[8:], mark)

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.w.Write(buf[:]); err != nil {
		return err
	}
	p.mark[chunk.Pack()] = mark
	return nil
}

// Done сообщает, разобран ли кусок. Пропущенный считается разобранным:
// второй заход даст тот же непонятный ответ модели и снова встанет.
func (p *Progress) Done(chunk ChunkKey) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.mark[chunk.Pack()]
	return ok
}

// Mark возвращает признак разбора куска.
func (p *Progress) MarkOf(chunk ChunkKey) (uint32, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	m, ok := p.mark[chunk.Pack()]
	return m, ok
}

// Count возвращает число разобранных кусков.
func (p *Progress) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.mark)
}

// Counts раскладывает разобранное по признакам: сколько дало сущности,
// сколько оказалось пустым, сколько пропущено. По этим числам видно качество
// извлечения, а не только его ход.
func (p *Progress) Counts() (done, empty, skipped int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.mark {
		switch m {
		case MarkDone:
			done++
		case MarkEmpty:
			empty++
		case MarkSkipped:
			skipped++
		}
	}
	return done, empty, skipped
}

// Docs возвращает номера книг, у которых разобран хотя бы один кусок.
func (p *Progress) Docs() map[uint32]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := map[uint32]int{}
	for packed := range p.mark {
		out[UnpackChunk(packed).Doc]++
	}
	return out
}

// Sync сбрасывает буфер и просит диск записать его: Flush отдаёт данные
// системе, а при отказе питания они всё ещё в её кеше. Журнал разобранных кусков.
func (p *Progress) Sync() error {
	if err := p.Flush(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f == nil {
		return nil
	}
	return p.f.Sync()
}

// Flush дописывает буфер на диск.
func (p *Progress) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w == nil {
		return nil
	}
	return p.w.Flush()
}

// Close закрывает журнал отметок.
func (p *Progress) Close() error {
	if err := p.Flush(); err != nil {
		return err
	}
	if p.f == nil {
		return nil
	}
	err := p.f.Close()
	p.f = nil
	return err
}

// coversDoc — есть ли хоть одна отметка разбора у кусков этой книги.
//
// Перебор по всем отметкам: их сотни тысяч, но вопрос задаётся десятки раз
// за отчёт, а не в горячем пути. Заводить ради этого второй указатель значило
// бы держать его в согласии с первым — лишний способ разойтись.
func (p *Progress) coversDoc(doc uint32) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for key := range p.mark {
		if uint32(key>>32) == doc {
			return true
		}
	}
	return false
}
