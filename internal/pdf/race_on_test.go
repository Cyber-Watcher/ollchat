//go:build race

package pdf

// raceEnabled — тесты собраны с детектором гонок. Он раздувает выделения
// памяти, и предел в мегабайтах, честный для обычной сборки, под ним
// меряет уже не разбор, а инструментирование (см. TestPageStateBounded).
const raceEnabled = true
