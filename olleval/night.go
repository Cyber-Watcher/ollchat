package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Какую ночь продолжать.
//
// Имя ночи — это дата, и до 23.08.2026 обвязка брала её просто как «сегодня».
// На больших наборах это оказалось дефектом: набор задач вырос до сорока,
// полный обход стал занимать больше суток, и каждую полночь тик заводил новую
// ночь и начинал **с нуля**. Замерено на стенде: ночь 2026-08-22 остановилась
// на 824 попытках из 1320, а в 00:00 воскресенья началась ночь 2026-08-23,
// снова с первой задачи. Полный обход при таком устройстве не заканчивается
// никогда.
//
// Отсюда правило: если у прошлой ночи осталась работа и она не слишком стара —
// продолжаем её, а не заводим новую. «Слишком стара» нужно потому, что состав
// моделей на стенде меняется, и докатывать позапрошлую неделю бессмысленно:
// сравнивать её цифры будет не с чем.

// nightPattern — имена ночей, которые считаются датами. Ручные прогоны
// («проба», «smoke-test») в докатку не берутся: их заводили руками и руками же
// закончат.
var nightPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(-[a-z0-9]+)?$`)

// NightChoice — что решено про имя ночи.
type NightChoice struct {
	Name     string // какую ночь брать
	Carried  bool   // это продолжение прошлой, а не новая
	Pending  int    // сколько попыток в ней осталось
	Previous string // имя рассмотренной прошлой ночи (для объяснения)
}

// ResolveNight решает, какую ночь брать: продолжить незаконченную или завести
// новую по сегодняшней дате.
//
// pending считает оставшиеся попытки конкретной ночи; он передаётся отдельно,
// чтобы решение можно было проверить тестом без сервера и без наборов задач.
func ResolveNight(root, today string, maxAge time.Duration, now time.Time,
	pending func(night string) (int, error)) (NightChoice, error) {

	choice := NightChoice{Name: today}
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return choice, nil
		}
		return choice, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && nightPattern.MatchString(e.Name()) && e.Name() != today {
			names = append(names, e.Name())
		}
	}
	// От свежих к старым: продолжать имеет смысл последнюю незаконченную.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	for _, name := range names {
		day, err := time.ParseInLocation("2006-01-02", name[:10], now.Location())
		if err != nil {
			continue
		}
		if maxAge > 0 && now.Sub(day) > maxAge {
			break // дальше только старее
		}
		choice.Previous = name
		left, err := pending(name)
		if err != nil {
			return choice, err
		}
		if left > 0 {
			return NightChoice{Name: name, Carried: true, Pending: left, Previous: name}, nil
		}
		break // последняя ночь доделана — начинаем новую
	}
	return choice, nil
}

// pendingCounter собирает счётчик оставшихся попыток для конкретной ночи.
//
// Считает ровно то же, что и команда pending: те же наборы, те же модели,
// те же пропуски по возможностям. Иначе решение «продолжать ли ночь»
// разошлось бы с тем, что прогон потом сделает.
func pendingCounter(ctx context.Context, root string, client *ollama.Client,
	suiteNames, exclude, models string, repeats int) func(string) (int, error) {

	var cards []ModelCard
	return func(night string) (int, error) {
		store, err := NewStore(root, night)
		if err != nil {
			return 0, err
		}
		suites, err := pickSuites(store.SuitesDir(), suiteNames)
		if err != nil {
			return 0, err
		}
		if cards == nil {
			tags, err := client.Tags(ctx)
			if err != nil {
				return 0, err
			}
			cards = keepOnly(SelectModels(tags, strings.Split(exclude, ",")), models)
		}
		n := &Night{Store: store, Client: client, Suites: suites, Models: cards, Repeats: repeats}
		return n.Pending(), nil
	}
}
