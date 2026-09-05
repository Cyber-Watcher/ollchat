package graph

import (
	"fmt"
	"path/filepath"
)

// Rules — правила одного открытого графа: какой подкаталог, как входить
// по слову и по смыслу, как показывать связи, действуют ли группы и склейки.
//
// До этапа 91 (R3) всё это лежало в атомарных переменных пакета, которые
// выставлял config.UseGraph при запуске. Два графа с разными правилами
// в одном процессе были невозможны, а тесты зависели от порядка вызовов.
// Теперь правила — параметр открытия: их несёт сам *Graph, и компилятор
// не даёт открыть граф, не сказав, по каким правилам.
//
// Нулевое значение — рабочий граф с умолчаниями из замеров (см. lexrules.go),
// группы выключены, склейки действуют. Так `Rules{}` годится везде, где
// настроек нет: в тестах, в службах без конфига.
type Rules struct {
	// Name — имя графа: "" — рабочий (каталог graph), иначе graph-<имя>.
	Name string

	// Словесный вход: длина основы и число книг, см. DefaultStemMinLen.
	StemMinLen   int
	StemMinBooks int

	// Смысловой вход и показ связей, см. DefaultSenseTie и соседей.
	SenseTie      float64
	SenseMargin   float64
	VectorAliases int
	MaxEvidences  int

	// Groups — режим групп «про одно»: GroupOff, GroupUnion, GroupExpand.
	// Пусто или незнакомое — off: молчаливо ничего не делать безопаснее,
	// чем гадать.
	Groups string

	// MergesOff выключает действие склеек двойников (настройка
	// merges_enabled = false). Нулевое значение — склейки действуют.
	MergesOff bool

	// Format — версия формата, которой ЗАВОДИТСЯ новый граф (настройка
	// format). Нулевое значение — FormatVersion, то есть рабочий формат 1.
	// На уже собранный граф не действует: его версия записана в паспорте.
	//
	// Формат 2 допустим только у именованного графа: в каталог `graph`
	// рабочего графа он не попадает никогда — решение владельца 04.09.2026,
	// рабочий граф неприкосновенен.
	Format int
}

// DefaultRules — правила рабочего графа с умолчаниями из замеров.
func DefaultRules() Rules { return Rules{}.norm() }

// Normalized — правила с заполненными умолчаниями: то, что действует в графе.
func (r Rules) Normalized() Rules { return r.norm() }

// norm заполняет нули умолчаниями и проверяет режим групп.
func (r Rules) norm() Rules {
	if r.StemMinLen <= 0 {
		r.StemMinLen = DefaultStemMinLen
	}
	if r.StemMinBooks <= 0 {
		r.StemMinBooks = DefaultStemMinBooks
	}
	if r.SenseTie <= 0 {
		r.SenseTie = DefaultSenseTie
	}
	if r.SenseMargin <= 0 {
		r.SenseMargin = DefaultSenseMargin
	}
	if r.VectorAliases <= 0 {
		r.VectorAliases = DefaultVectorAliases
	}
	if r.MaxEvidences <= 0 {
		r.MaxEvidences = DefaultMaxEvidences
	}
	switch r.Groups {
	case GroupOff, GroupUnion, GroupExpand:
	default:
		r.Groups = GroupOff
	}
	if r.Format <= 0 {
		r.Format = FormatVersion
	}
	return r
}

// Validate проверяет имя графа: строчные латинские буквы, цифры и дефис.
func (r Rules) Validate() error {
	if !ValidName(r.Name) {
		return fmt.Errorf("недопустимое имя графа %q: строчные латинские буквы, цифры и дефис, до 32 знаков", r.Name)
	}
	if r.Format > 0 && !KnownVersion(r.Format) {
		return fmt.Errorf("формат графа %d не поддерживается (известны: %s)", r.Format, versionsHuman())
	}
	if r.Format >= FormatV2 && r.Name == "" {
		return fmt.Errorf("формат %d допустим только у именованного графа (graph.name), "+
			"а не у рабочего в каталоге %s: рабочий граф формата 1 неприкосновенен", r.Format, DirName)
	}
	return nil
}

// Dir — каталог графа внутри каталога коллекции.
func (r Rules) Dir(collDir string) string { return filepath.Join(collDir, DirFor(r.Name)) }

// Rules отдаёт правила, по которым открыт граф (уже с умолчаниями).
func (g *Graph) Rules() Rules {
	if g == nil {
		return DefaultRules()
	}
	return g.rules
}
