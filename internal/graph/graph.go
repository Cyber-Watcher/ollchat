// Package graph — граф понятий поверх коллекции книг.
//
// Обычный поиск по библиотеке (internal/kb) отвечает на вопрос «где про это
// написано». Граф отвечает на вопрос «как это связано с тем» — из кусков
// извлекаются сущности и связи между ними, связи сшиваются в один граф на всю
// библиотеку, а граф режется на сообщества с краткими резюме.
//
// Граф — слой **над** коллекцией, а не вторая база. Он живёт внутри каталога
// коллекции и ссылается на её же куски:
//
//	<kb.dir>/collections/<имя>/graph/
//	  graph.meta        версия формата, модель извлечения, привязка к коллекции
//	  entities.jsonl    реестр сущностей, только дозапись
//	  mentions.log      упоминания «сущность в куске», только дозапись
//	  edges.log         связи, только дозапись
//	  mentions.dat/.idx готовый к чтению вид: сущность → куски
//	  edges.dat/.idx    готовый к чтению вид: сущность → связи
//	  progress.log      какие куски уже разобраны — для докатки
//	  LOCK              признак идущей сборки
//
// Два решения приняты сразу и определяют весь формат.
//
// **Переносимость.** Граф лежит внутри коллекции, поэтому копирование каталога
// переносит и его. Абсолютных путей в файлах графа нет ни одного: книги
// адресуются номером в реестре коллекции, куски — парой «книга, номер внутри
// книги». Собранный здесь граф работает на другой машине без пересборки.
//
// **Устойчивость к уплотнению.** `/kb merge` переписывает хранилище кусков,
// и сквозная нумерация внутри файла меняется. Пара «книга + порядковый номер
// куска внутри книги» её переживает — это прямо записано в internal/kb/merge.go
// и там же названо причиной, по которой внешние ссылки вида «go/12#37» остаются
// верными. Граф держится на той же паре, поэтому уплотнение его не убивает.
package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// FormatVersion — версия, которой пишется НОВЫЙ граф, если правила не
// просят иной (Rules.Format). Это рабочий формат: у него нет журнала
// синонимов, и его читатель не менялся с первой сборки.
const FormatVersion = 1

// FormatV2 — формат 2 (GraphSchemaV2.md): те же файлы плюс aliases.log
// с источником каждого синонима и свой промпт извлечения. Заводится только
// у именованного графа (Rules.Validate).
const FormatV2 = 2

// SupportedVersions — версии формата, которые читатель понимает.
//
// **Зачем список, а не одно число.** Над одной библиотекой живут несколько
// графов (см. named.go): рабочий и опытный. Опыт меняет схему извлечения, то
// есть формат, но рабочий граф при этом обязан читаться тем же самым бинарём —
// иначе переключение на опыт означает потерю недель работы видеокарты.
// Требование владельца 03.09.2026: **один ollchat читает оба формата**.
//
// Незнакомая версия по-прежнему отвергается: разбирать наугад файл, стоивший
// недель счёта, хуже, чем честно отказаться.
var SupportedVersions = []int{1, FormatV2}

// KnownVersion — понимает ли читатель эту версию формата.
func KnownVersion(v int) bool {
	for _, k := range SupportedVersions {
		if k == v {
			return true
		}
	}
	return false
}

// versionsHuman — перечень поддерживаемых версий для сообщения человеку.
func versionsHuman() string {
	out := make([]string, 0, len(SupportedVersions))
	for _, v := range SupportedVersions {
		out = append(out, strconv.Itoa(v))
	}
	return strings.Join(out, ", ")
}

// DirName — имя подкаталога графа внутри каталога коллекции.
const DirName = "graph"

// Ошибки, по которым вызывающий код принимает решения.
var (
	// ErrNoGraph — графа рядом с коллекцией нет.
	ErrNoGraph = errors.New("граф не собран")
	// ErrVersion — граф собран другой версией формата.
	ErrVersion = errors.New("несовместимая версия формата графа")
	// ErrCompacted — коллекцию уплотнили после сборки графа: сквозная
	// нумерация кусков изменилась, часть ссылок графа может указывать в пустоту.
	ErrCompacted = errors.New("коллекция уплотнена после сборки графа")
	// ErrLocked — сборка уже идёт.
	ErrLocked = errors.New("сборка графа уже идёт")
)

// ChunkKey — устойчивый номер куска: книга и порядковый номер внутри неё.
//
// Именно пара, а не сквозной номер: сквозной меняется при уплотнении
// коллекции, и граф после первого же `/kb merge` указывал бы не туда.
type ChunkKey struct {
	Doc uint32
	Ord uint32
}

// Pack складывает пару в одно число — так её дешевле хранить и сравнивать.
func (k ChunkKey) Pack() uint64 { return uint64(k.Doc)<<32 | uint64(k.Ord) }

// UnpackChunk разбирает упакованный номер обратно в пару.
func UnpackChunk(v uint64) ChunkKey {
	return ChunkKey{Doc: uint32(v >> 32), Ord: uint32(v)}
}

// String печатает кусок так же, как это делает база знаний: «12#37».
func (k ChunkKey) String() string { return fmt.Sprintf("%d#%d", k.Doc, k.Ord) }

// Meta — паспорт графа. Лежит в graph.meta и связывает граф с коллекцией.
type Meta struct {
	Version    int    `json:"version"`
	Collection string `json:"collection"`

	// Model — чем извлекались сущности. Смена модели меняет качество графа,
	// поэтому она записана: иначе через месяц не ответить, что именно собрано.
	Model string `json:"model,omitempty"`

	// Kind — рабочий граф или опытный. Пусто у собранных до 03.09.2026 —
	// такие считаются рабочими: раньше других и не было.
	//
	// На опытный граф не срабатывает доктор: он заведомо неполон, а сторож,
	// кричащий не по делу, перестаёт читаться.
	Kind Kind `json:"kind,omitempty"`

	// Note — чем этот граф отличается: другая схема, другой промпт, включённое
	// связывание новых сущностей. Без этой строки через месяц не воспроизвести,
	// что именно сравнивалось.
	Note string `json:"note,omitempty"`

	// PromptID — версия промптов графа (graph.PromptID), которой он собран.
	// Пусто у графов, собранных до 04.09.2026: тогда версия не записывалась.
	PromptID string `json:"prompt_id,omitempty"`
	// PromptHistory — когда и при каком покрытии промпт записан или сменён.
	PromptHistory []PromptStamp `json:"prompt_history,omitempty"`

	// Chunks — сколько кусков было в коллекции на момент последней сборки.
	// По нему видно, сколько книг долито после и осталось без графа.
	Chunks int `json:"chunks"`
	// Covered — сколько кусков разобрано.
	Covered int `json:"covered"`

	Entities    int `json:"entities"`
	Edges       int `json:"edges"`
	Mentions    int `json:"mentions"`
	Communities int `json:"communities"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`

	// BuildSeconds — сколько всего секунд работала модель извлечения,
	// накопительно по всем заходам.
	//
	// **Зачем хранить.** Единственный честный ответ на вопрос «во что обойдётся
	// пересборка» — «во столько же, во сколько обошлась сборка». Считать его
	// по разнице Created и Updated нельзя: между заходами граф простаивает
	// сутками, и такая «оценка» завысила бы срок в разы. Взять число из чужого
	// замера — тем более: скорость зависит от карты и модели.
	BuildSeconds float64 `json:"build_seconds,omitempty"`
}

// PromptStamp — отметка о версии промпта в истории графа.
type PromptStamp struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Covered int       `json:"covered"` // сколько кусков было разобрано на этот момент
	Note    string    `json:"note,omitempty"`
}

// PromptLine описывает версию промпта графа для отчётов: доктора, статуса.
func (g *Graph) PromptLine() string {
	m := g.Meta()
	switch {
	case m.PromptID == "":
		return "промпт извлечения не записан (граф собран до 04.09.2026); запишется при следующей сборке"
	case m.PromptID == PromptID:
		return "промпт извлечения " + m.PromptID
	default:
		return fmt.Sprintf("промпт извлечения %s, а в этом бинаре %s — ДРУГОЙ: сборка этим бинарём "+
			"смешает две схемы (--graph-allow-prompt-change, если это осознанно)", m.PromptID, PromptID)
	}
}

// stampPrompt записывает версию промпта перед сборкой.
//
// Та же логика, что у модели извлечения: пустая — записать; та же — идти;
// другая — отказ, если смену не разрешили явно. Отказ здесь важнее, чем
// у модели: промпт меняется одной правкой файла, и без этого сторожа граф
// начал бы тихо собираться двумя схемами.
func (g *Graph) stampPrompt(id string, allowChange bool) error {
	have := g.meta.PromptID
	switch {
	case have == id:
		return nil
	case have == "":
		g.meta.PromptID = id
		g.meta.PromptHistory = append(g.meta.PromptHistory, PromptStamp{
			ID: id, At: time.Now(), Covered: g.meta.Covered,
			Note: "записан задним числом: куски до этой даты разобраны промптом, совпадающим с ним по тексту",
		})
		return g.saveMeta()
	case !allowChange:
		return fmt.Errorf(
			"граф собран промптом %s, а в этом бинаре промпт %s.\n"+
				"Продолжать другим промптом нельзя: граф окажется собран двумя схемами, "+
				"и различить их потом будет невозможно.\n"+
				"Либо верните прежний текст в internal/graph/prompts/, либо соберите граф заново, "+
				"либо разрешите смену осознанно: --graph-allow-prompt-change", have, id)
	default:
		g.meta.PromptID = id
		g.meta.PromptHistory = append(g.meta.PromptHistory, PromptStamp{
			ID: id, At: time.Now(), Covered: g.meta.Covered,
			Note: "смена промпта разрешена ключом --graph-allow-prompt-change",
		})
		return g.saveMeta()
	}
}

// Graph — открытый граф коллекции.
type Graph struct {
	dir  string
	meta Meta
	// rules — правила этого графа: имя каталога, вход по слову и смыслу,
	// группы, склейки. Заданы при открытии, см. rules.go.
	rules Rules

	ents *Entities
	ment *Mentions
	edge *Edges
	prog *Progress
	vecs *EntityVectors // смысловой вход; пусто, пока векторы не посчитаны

	// alias — журнал синонимов с источником; есть только у формата 2,
	// у формата 1 остаётся nil.
	alias *Aliases

	// dropped — книги, вклад которых скрыт из выдачи (но не стёрт).
	dropped *DroppedBooks
	links   *Links // связывания при сборке (links.jsonl), см. link.go

	// groups — группы понятий «про одно», объединяющие выдачу без слияния.
	groups *Groups

	// merges — решения о склейке двойников, надеваемые на граф при чтении.
	// Отдельно от реестра нарочно: убрать файл — и граф прежний.
	merges *Merges

	// opened — во что обошлось открытие: время и занятая память.
	//
	// Меряется затем, что обе величины растут вместе с библиотекой и однажды
	// упрутся: замер 29.08.2026 на четверти библиотеки — 11.5 с, 160 МБ удержано,
	// 1.03 ГБ пик процесса при открытии.
	// Порог, за которым стоит смотреть в сторону встраиваемой базы, назван
	// в HowGraphBuildRuns.md; чтобы его заметить, надо видеть числа.
	opened OpenStats

	lock *os.File

	// staleLock — что было написано в признаке сборки, снятом с неживого
	// процесса. Пусто, если снимать было нечего.
	staleLock string
}

// Dir возвращает каталог графа.
func (g *Graph) Dir() string { return g.dir }

// Meta возвращает паспорт графа.
func (g *Graph) Meta() Meta { return g.meta }

// Entities отдаёт реестр сущностей.
func (g *Graph) Entities() *Entities { return g.ents }

// Mentions отдаёт упоминания.
func (g *Graph) Mentions() *Mentions { return g.ment }

// Aliases — журнал синонимов с источником. У графа формата 1 его нет: nil.
func (g *Graph) Aliases() *Aliases { return g.alias }

// Edges отдаёт связи.
func (g *Graph) Edges() *Edges { return g.edge }

// Progress отдаёт отметки о разобранных кусках.
func (g *Graph) Progress() *Progress { return g.prog }

// Create заводит пустой граф рядом с коллекцией.
//
// collDir — каталог коллекции, name — её имя, chunks — сколько в ней кусков
// сейчас. Существующий граф не затирается: это ошибка, а не тихая потеря
// нескольких суток работы модели.
func Create(collDir, name string, chunks int, rules Rules) (*Graph, error) {
	return CreateKind(collDir, name, chunks, KindProduction, "", rules)
}

// CreateKind заводит граф с заданным назначением и пометкой об отличиях.
func CreateKind(collDir, name string, chunks int, kind Kind, note string, rules Rules) (*Graph, error) {
	if err := rules.Validate(); err != nil {
		return nil, err
	}
	dir := rules.Dir(collDir)
	if _, err := os.Stat(filepath.Join(dir, metaFile)); err == nil {
		return nil, fmt.Errorf("граф в %s уже есть", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	now := time.Now()
	if kind == "" {
		kind = KindProduction
	}
	version := rules.norm().Format
	pid := PromptIDFor(version)
	m := Meta{
		Version:       version,
		Collection:    name,
		Chunks:        chunks,
		Kind:          kind,
		Note:          note,
		PromptID:      pid,
		PromptHistory: []PromptStamp{{ID: pid, At: now}},
		Created:       now,
		Updated:       now,
	}
	if err := writeMeta(dir, m); err != nil {
		return nil, err
	}
	return open(dir, m, rules)
}

// Open открывает граф коллекции.
//
// chunks — сколько кусков в коллекции сейчас. Если их стало **меньше**, чем
// было при сборке, коллекцию уплотнили: сквозная нумерация изменилась, и часть
// ссылок графа может указывать не туда. Такой граф не открывается —
// возвращается ErrCompacted. Лучше честный отказ, чем тихий неверный ответ
// со ссылкой на чужую страницу.
func Open(collDir string, chunks int, rules Rules) (*Graph, error) {
	return openCollection(collDir, chunks, rules, nil)
}

func openCollection(collDir string, chunks int, rules Rules, cb func(OpenProgress)) (*Graph, error) {
	if err := rules.Validate(); err != nil {
		return nil, err
	}
	dir := rules.Dir(collDir)
	m, err := readMeta(dir)
	if err != nil {
		return nil, err
	}
	if !KnownVersion(m.Version) {
		return nil, fmt.Errorf("%w: в файле %d, программа читает %s",
			ErrVersion, m.Version, versionsHuman())
	}
	if chunks > 0 && chunks < m.Chunks {
		return nil, fmt.Errorf("%w: было %d кусков, стало %d — граф надо пересобрать",
			ErrCompacted, m.Chunks, chunks)
	}
	return openWith(dir, m, rules, cb)
}

// OpenOrCreate открывает граф, а если его нет — заводит пустой.
func OpenOrCreate(collDir, name string, chunks int, rules Rules) (*Graph, error) {
	return OpenOrCreateKind(collDir, name, chunks, KindProduction, "", rules)
}

// OpenOrCreateKind — то же, но у создаваемого графа проставляются назначение
// и пометка об отличиях. У существующего графа паспорт не трогается: назначение
// задаётся один раз, при сборке.
func OpenOrCreateKind(collDir, name string, chunks int, kind Kind, note string, rules Rules) (*Graph, error) {
	g, err := Open(collDir, chunks, rules)
	if errors.Is(err, ErrNoGraph) {
		return CreateKind(collDir, name, chunks, kind, note, rules)
	}
	return g, err
}

// OpenProgress — ход открытия графа: какой шаг идёт и сколько байт прочитано.
//
// Нужен потому, что открытие большого графа это десятки секунд (41 с на
// библиотеке из 465 тысяч кусков, замер 02.09.2026), и всё это время человеку
// нечего показать, кроме крутилки. Крутилка не отличает «идёт медленно»
// от «повисло», а полоса отличает.
type OpenProgress struct {
	Stage string // шаг человеческими словами: «реестр понятий», «связи»
	Done  int64  // прочитано байт на этом шаге
	Total int64  // сколько на нём всего; ноль — шаг без счёта байт
}

// OpenWithProgress открывает граф, сообщая о ходе.
//
// Обратный вызов идёт из горутины открытия и обязан быть быстрым: он зовётся
// на каждые несколько мегабайт чтения. Блокирующая отправка в канал без буфера
// замедлит открытие ровно на то время, пока её никто не читает.
func OpenWithProgress(collDir string, chunks int, rules Rules, cb func(OpenProgress)) (*Graph, error) {
	return openCollection(collDir, chunks, rules, cb)
}

// OpenStats — во что обошлось открытие графа.
type OpenStats struct {
	Elapsed time.Duration // сколько заняло чтение файлов в память
	Heap    uint64        // сколько памяти граф удерживает, пока открыт
	Peak    uint64        // сколько было занято в конце чтения, до сборки мусора
}

// Opened отдаёт замер открытия. Нулевое время означает, что граф не открывали
// (создали пустым) — показывать такое человеку незачем.
func (g *Graph) Opened() OpenStats { return g.opened }

func open(dir string, m Meta, rules Rules) (*Graph, error) { return openWith(dir, m, rules, nil) }

func openWith(dir string, m Meta, rules Rules, cb func(OpenProgress)) (*Graph, error) {
	// Сколько занял граф — это прирост ЖИВОЙ кучи, поэтому перед обоими
	// замерами вызывается сборка мусора.
	//
	// Без неё число врёт втрое: первая редакция показывала 72 МБ там, где граф
	// на деле занимает около гигабайта. Причина простая — за одиннадцать секунд
	// чтения сборщик успевает отработать сам, и разница HeapAlloc отражает
	// не занятое, а случайный момент цикла сборки.
	//
	// Две сборки мусора на открытие — цена приемлемая: открытие и так идёт
	// секунды и случается редко.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()

	g := &Graph{dir: dir, meta: m, rules: rules.norm()}
	var err error
	// Шаги названы так, как их стоит показать человеку. Первый — самый долгий:
	// реестр понятий это сотни мегабайт JSONL, остальные вместе занимают
	// заметно меньше, поэтому только у него считаются байты.
	step := func(stage string) {
		if cb != nil {
			cb(OpenProgress{Stage: stage})
		}
	}
	if g.ents, err = openEntitiesWith(dir, g.rules.StemMinLen, cb); err != nil {
		return nil, err
	}
	step("упоминания")
	if g.ment, err = openMentions(dir); err != nil {
		return nil, err
	}
	step("связи")
	if g.edge, err = openEdges(dir); err != nil {
		return nil, err
	}
	step("ход сборки")
	if g.prog, err = openProgress(dir); err != nil {
		return nil, err
	}
	if m.Version >= FormatV2 {
		step("синонимы с источником")
		if g.alias, err = openAliases(dir); err != nil {
			return nil, err
		}
	}
	step("векторы понятий")
	// Векторов может не быть — это обычное состояние, а не ошибка: граф
	// работает и по написанию.
	g.vecs = openEntityVectors(dir)
	step("склейки двойников")
	if g.merges, err = openMerges(dir); err != nil {
		return nil, err
	}
	g.merges.off = g.rules.MergesOff
	if g.links, err = openLinks(dir); err != nil {
		return nil, fmt.Errorf("журнал связываний: %w", err)
	}
	if g.dropped, err = openDroppedBooks(dir); err != nil {
		return nil, err
	}
	if g.groups, err = openGroups(dir); err != nil {
		return nil, err
	}
	// Склейки надеваются на реестр и на связи: поиск обязан вести к выжившему,
	// а его окружение — включать окружение поглощённых.
	g.ents.useMerges(g.merges)
	g.alias.useMerges(g.merges)
	g.edge.useMerges(g.merges)
	g.ment.useMerges(g.merges)

	elapsed := time.Since(started) // время меряется до сборки мусора: она не часть чтения

	// Две разные величины, и путать их дорого:
	//   peak — сколько занято в конце чтения, вместе с мусором разбора; по нему
	//          видно, сколько памяти НУЖНО, чтобы граф вообще открылся;
	//   heap — сколько остаётся занято, пока граф открыт; по нему видно, во что
	//          обходится держать его в памяти между вопросами.
	// Первая величина всегда больше и растёт быстрее: разбор 118 МБ JSONL
	// оставляет много временного мусора.
	var peak runtime.MemStats
	runtime.ReadMemStats(&peak)
	runtime.GC()
	runtime.ReadMemStats(&after)

	g.opened = OpenStats{Elapsed: elapsed}
	if after.HeapAlloc > before.HeapAlloc {
		g.opened.Heap = after.HeapAlloc - before.HeapAlloc
	}
	if peak.HeapAlloc > before.HeapAlloc {
		g.opened.Peak = peak.HeapAlloc - before.HeapAlloc
	}
	return g, nil
}

// Close закрывает файлы графа и снимает признак сборки, если он ставился.
func (g *Graph) Close() error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if g.ents != nil {
		keep(g.ents.Close())
	}
	if g.ment != nil {
		keep(g.ment.Close())
	}
	if g.edge != nil {
		keep(g.edge.Close())
	}
	if g.prog != nil {
		keep(g.prog.Close())
	}
	keep(g.alias.Close())
	keep(g.Unlock())
	return first
}

// Stats — сводка по графу, пригодная для показа пользователю.
type Stats struct {
	Meta
	// Pending — сколько кусков коллекции ещё без графа. Считается по числу
	// кусков, переданному при открытии: книги доливают, и честнее показать
	// «граф знает 19 630 кусков из 268 686», чем промолчать.
	Pending int

	// Merged — сколько понятий поглощено склейкой двойников.
	//
	// **Зачем отдельно от Entities.** В реестре запись остаётся навсегда:
	// склейка лежит отдельным журналом и надевается при чтении, чтобы её можно
	// было снять. Поэтому `Entities` — число записей реестра, а живых понятий
	// на `Merged` меньше. До 02.09.2026 доктор показывал только реестр и после
	// склейки 2693 двойников уверял, что понятий по-прежнему 161 239 — то есть
	// прибор, которому верят о состоянии графа, не видел того, что сам же
	// и советовал сделать.
	Merged int
}

// Live — сколько понятий в графе на самом деле, за вычетом поглощённых.
func (s Stats) Live() int { return s.Entities - s.Merged }

// Stats собирает сводку. chunks — сколько кусков в коллекции сейчас.
func (g *Graph) Stats(chunks int) Stats {
	m := g.meta
	m.Entities = g.ents.Count()
	m.Mentions = g.ment.Count()
	m.Edges = g.edge.Count()
	m.Covered = g.prog.Count()
	st := Stats{Meta: m, Merged: g.merges.Count()}
	if chunks > m.Covered {
		st.Pending = chunks - m.Covered
	}
	return st
}

// SetModel записывает в паспорт модель извлечения.
func (g *Graph) SetModel(model string) error {
	g.meta.Model = model
	return g.saveMeta()
}

// SetChunks запоминает, сколько кусков было в коллекции при сборке.
func (g *Graph) SetChunks(chunks int) error {
	g.meta.Chunks = chunks
	return g.saveMeta()
}

// saveMeta переписывает паспорт целиком — он маленький и цельный,
// в отличие от журналов, которые только дозаписываются.
func (g *Graph) saveMeta() error {
	g.meta.Updated = time.Now()
	g.meta.Entities = g.ents.Count()
	g.meta.Mentions = g.ment.Count()
	g.meta.Edges = g.edge.Count()
	g.meta.Covered = g.prog.Count()
	return writeMeta(g.dir, g.meta)
}

// Save сохраняет паспорт с нынешними счётчиками.
func (g *Graph) Save() error { return g.saveMeta() }

// ── LOCK ─────────────────────────────────────────────────────────────────────

const lockFile = "LOCK"

// Lock ставит признак идущей сборки.
//
// Сборка графа идёт часами и пишет в те же журналы. Два прогона разом
// перемешали бы записи так, что разобрать их было бы нельзя.
func (g *Graph) Lock() error {
	// Идущий архив коллекции дожидаемся, а не отказываем: см. work.go.
	if err := kb.WaitArchive(filepath.Dir(g.dir), kb.ArchiveWait); err != nil {
		return err
	}
	path := filepath.Join(g.dir, lockFile)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if os.IsExist(err) {
		// Признак есть — но это ещё не значит, что сборка идёт. Прогон,
		// убитый по kill -9, при отключении питания или снятый OOM, снять
		// его за собой не успевает, и без разбора человек получал бы отказ
		// «сборка графа уже идёт» на пустом месте, без единой подсказки,
		// что делать. Поэтому смотрим, жив ли записанный процесс.
		owner := readLock(path)
		if owner.alive() {
			return &LockedError{Path: path, PID: owner.PID, Since: owner.Since}
		}
		// Хозяин мёртв — признак наш. Снимаем и берём себе.
		g.staleLock = owner.describe()
		if rmErr := os.Remove(path); rmErr != nil {
			return fmt.Errorf("остался признак сборки от неживого процесса, "+
				"и его не удалось убрать: %w", rmErr)
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		if os.IsExist(err) {
			// Кто-то успел занять признак между нашей уборкой и попыткой:
			// значит, сборка всё-таки идёт.
			return &LockedError{Path: path}
		}
		return err
	}

	fmt.Fprintf(f, "pid %d, начато %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	g.lock = f
	return nil
}

// StaleLock — описание снятого признака от неживого процесса, если он был.
// Пусто, когда снимать было нечего. Нужен, чтобы человек увидел: сборка
// не «сама завелась», а подобрала работу за упавшим прогоном.
func (g *Graph) StaleLock() string { return g.staleLock }

// lockOwner — что записано в признаке сборки.
type lockOwner struct {
	PID   int
	Since string
	Raw   string
}

// readLock разбирает «pid 12345, начато 2026-08-24T15:06:38+03:00».
//
// Разбор нестрогий намеренно: признак пишем мы сами, но файл мог быть
// затёрт, обрезан или создан руками. Не разобрали pid — считаем хозяина
// живым и отказываемся: помешать чужой работе хуже, чем попросить человека
// разобраться самому.
func readLock(path string) lockOwner {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockOwner{PID: -1}
	}
	o := lockOwner{PID: -1, Raw: strings.TrimSpace(string(b))}
	if _, err := fmt.Sscanf(o.Raw, "pid %d, начато %s", &o.PID, &o.Since); err != nil {
		o.PID = -1
	}
	return o
}

// alive — жив ли процесс, поставивший признак.
//
// Сигнал 0 проверяет существование процесса, ничего ему не посылая. Одного
// этого мало: номера процессов переиспользуются, и на месте упавшего прогона
// может оказаться чужая программа. Поэтому там, где есть /proc, сверяется
// ещё и имя: признак снимается только с нашего же ollchat.
func (o lockOwner) alive() bool {
	if o.PID <= 0 {
		return true // не разобрали — считаем живым и не трогаем
	}
	if o.PID == os.Getpid() {
		return true
	}
	p, err := os.FindProcess(o.PID)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false // процесса нет либо он чужой
	}
	// Процесс с таким номером есть. Наш ли это ollchat?
	cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", o.PID))
	if err != nil {
		return true // /proc недоступен — не рискуем
	}
	return strings.Contains(strings.ReplaceAll(string(cmd), "\x00", " "), "ollchat")
}

func (o lockOwner) describe() string {
	if o.PID > 0 {
		if o.Since != "" {
			return fmt.Sprintf("процесс %d, начатый %s, больше не работает", o.PID, o.Since)
		}
		return fmt.Sprintf("процесс %d больше не работает", o.PID)
	}
	return "прежний прогон больше не работает"
}

// LockedError — сборка действительно идёт: процесс-хозяин жив.
//
// Отдельный тип, а не строка: человеку нужны номер процесса и путь к файлу,
// иначе «сборка графа уже идёт» — это тупик.
type LockedError struct {
	Path  string
	PID   int
	Since string
}

func (e *LockedError) Error() string {
	var b strings.Builder
	b.WriteString("сборка графа уже идёт")
	if e.PID > 0 {
		fmt.Fprintf(&b, " (процесс %d", e.PID)
		if e.Since != "" {
			fmt.Fprintf(&b, ", начата %s", e.Since)
		}
		b.WriteString(")")
	}
	if e.Path != "" {
		fmt.Fprintf(&b, ".\nЕсли уверены, что она не идёт, удалите признак: rm %s", e.Path)
	}
	return b.String()
}

// Is — чтобы errors.Is(err, ErrLocked) продолжал работать.
func (e *LockedError) Is(target error) bool { return target == ErrLocked }

// Unlock снимает признак сборки. Чужой признак не трогает: его снимет тот,
// кто поставил, либо пользователь руками, разобравшись, что случилось.
func (g *Graph) Unlock() error {
	if g.lock == nil {
		return nil
	}
	err := g.lock.Close()
	g.lock = nil
	if rmErr := os.Remove(filepath.Join(g.dir, lockFile)); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Locked сообщает, идёт ли сборка.
func (g *Graph) Locked() bool {
	_, err := os.Stat(filepath.Join(g.dir, lockFile))
	return err == nil
}

// ── Паспорт ──────────────────────────────────────────────────────────────────

const metaFile = "graph.meta"

func writeMeta(dir string, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Переименование поверх — единственный способ не оставить обрезанный
	// паспорт, если запись оборвётся: полстроки JSON не читается никак.
	return fsx.WriteFileAtomic(filepath.Join(dir, metaFile), append(b, '\n'), 0o644)
}

// decodeMeta читает паспорт из потока — из архива, не с диска.
func decodeMeta(r io.Reader, m *Meta) error { return json.NewDecoder(r).Decode(m) }

func readMeta(dir string) (Meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, ErrNoGraph
		}
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, fmt.Errorf("%s: %w", metaFile, err)
	}
	return m, nil
}

// CoversDoc сообщает, разбирал ли граф куски этой книги.
//
// **Зачем.** Проверка коллекции показывает книги, помеченные удалёнными, и
// человеку важно знать про каждую: успел ли граф её разобрать. Если успел,
// удаление книги оставляет в графе понятия и связи, опирающиеся на куски,
// которых в выдаче больше нет, — это не поломка, но знать об этом надо.
// Если не успел, книга ушла бесследно, и никаких следов чистить не придётся.
//
// Смотрим отметки разбора, а не упоминания: книга могла быть разобрана и не дать
// ни одного понятия (так бывает у оглавлений и указателей), и «разбирали»
// про неё вернее, чем «нашли в ней что-то».
func (g *Graph) CoversDoc(doc uint32) bool {
	if g == nil || g.prog == nil {
		return false
	}
	return g.prog.coversDoc(doc)
}

// Stat читает паспорт графа, не открывая сам граф.
//
// **Зачем отдельно от Open.** Открытие поднимает в память сущности и связи:
// на живой библиотеке это 16 секунд и сотни мегабайт. А чтобы сказать человеку,
// что он теряет, нужны одни только числа — и они целиком лежат в graph.meta,
// файле на триста байт. Заставлять ждать шестнадцать секунд ради предупреждения
// значит добиться того, что предупреждение начнут пролистывать.
//
// ErrNoGraph — графа нет; это не ошибка, а ответ.
func Stat(dir string) (Meta, error) { return readMeta(dir) }

// AddBuildTime прибавляет время захода сборки к накопленному.
// Зовётся из того же места, что и прочая запись паспорта, — своего замка
// у графа нет, потому что сборка однопоточна по построению.
func (g *Graph) AddBuildTime(d time.Duration) error {
	g.meta.BuildSeconds += d.Seconds()
	return g.saveMeta()
}
