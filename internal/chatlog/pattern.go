package chatlog

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Pattern — разобранный шаблон имени файла журнала.
//
// Основная форма — шаблон в духе strftime: `chat-%Y-%m-%d_%H-%M-%S.md`.
// Разбор идёт по директивам, а не через time.Format, поэтому литеральная
// часть имени остаётся литеральной: шаблон `log2006-%Y.md` даст `log2006-2026.md`,
// а не подставит год дважды.
//
// Наличие в шаблоне часов, минут или секунд означает «файл на запуск»:
// имя вычисляется один раз по времени старта и больше не меняется, сколько бы
// суток ни работал экземпляр. Шаблон только с датой даёт привычную ротацию
// по дням, шаблон без директив — один общий файл.
type Pattern struct {
	src        string
	parts      []patternPart
	perSession bool

	// layout непуст у устаревшего шаблона из настройки log.pattern:
	// там записана раскладка времени Go и имя строится через time.Format.
	layout string
}

// patternPart — либо литеральный кусок имени (verb == 0), либо директива.
type patternPart struct {
	lit  string
	verb byte
}

// Поддерживаемые директивы шаблона.
const patternVerbs = "YymdHMS"

// ParsePattern разбирает шаблон имени файла в форме strftime.
//
// Директивы: %Y — год (2026), %y — год двумя знаками, %m — месяц, %d — день,
// %H — часы в 24-часовом виде, %M — минуты, %S — секунды, %% — знак процента.
func ParsePattern(s string) (*Pattern, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("пустой шаблон имени файла")
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "/") {
		return nil, fmt.Errorf("шаблон %q должен быть относительным именем файла", s)
	}
	for _, seg := range strings.Split(filepath.ToSlash(s), "/") {
		if seg == ".." {
			return nil, fmt.Errorf("шаблон %q не должен содержать %q", s, "..")
		}
	}

	p := &Pattern{src: s}
	var lit strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			lit.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			return nil, fmt.Errorf("шаблон %q оборван на знаке %%", s)
		}
		i++
		c := s[i]
		if c == '%' {
			lit.WriteByte('%')
			continue
		}
		if !strings.ContainsRune(patternVerbs, rune(c)) {
			return nil, fmt.Errorf("неизвестная директива %%%c в шаблоне %q "+
				"(допустимы %%Y, %%y, %%m, %%d, %%H, %%M, %%S, %%%%)", c, s)
		}
		if lit.Len() > 0 {
			p.parts = append(p.parts, patternPart{lit: lit.String()})
			lit.Reset()
		}
		p.parts = append(p.parts, patternPart{verb: c})
		if c == 'H' || c == 'M' || c == 'S' {
			p.perSession = true
		}
	}
	if lit.Len() > 0 {
		p.parts = append(p.parts, patternPart{lit: lit.String()})
	}
	return p, nil
}

// LegacyPattern оборачивает устаревшую настройку log.pattern — раскладку
// времени Go вида "chat-2006-01-02.md". Такой шаблон работает как раньше:
// имя пересчитывается на каждой записи, ротация идёт по дате.
func LegacyPattern(layout string) *Pattern {
	if strings.TrimSpace(layout) == "" {
		layout = "chat-2006-01-02.md"
	}
	return &Pattern{src: layout, layout: layout}
}

// FixedName оборачивает готовое имя файла — без директив и без проверки
// на относительность. Это не настройка, а путь, названный при запуске
// (ключ --steps-file): он может быть абсолютным и лежать вне каталога
// журналов. Имя фиксировано на время запуска, как у шаблона с часами.
func FixedName(path string) *Pattern {
	return &Pattern{src: path, parts: []patternPart{{lit: path}}, perSession: true}
}

// PerSession сообщает, что имя файла фиксируется на время запуска:
// в шаблоне есть часы, минуты или секунды.
func (p *Pattern) PerSession() bool { return p != nil && p.perSession }

// String возвращает исходный шаблон.
func (p *Pattern) String() string {
	if p == nil {
		return ""
	}
	return p.src
}

// Name строит имя файла для указанного времени.
func (p *Pattern) Name(t time.Time) string {
	if p == nil {
		return ""
	}
	if p.layout != "" {
		return t.Format(p.layout)
	}
	var b strings.Builder
	for _, part := range p.parts {
		if part.verb == 0 {
			b.WriteString(part.lit)
			continue
		}
		switch part.verb {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		}
	}
	return b.String()
}
