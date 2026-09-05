package kb

import "testing"

// Умолчание надбавки — то, на котором сделан замер. Если его поменяют, замер
// 03.09.2026 (recall 0.325 → 0.358 на наборе терминов) перестанет описывать
// поведение, и об этом надо узнать здесь, а не через месяц по ухудшившейся выдаче.
func TestDefaultTableBoostIsMeasured(t *testing.T) {
	if DefaultTableBoost != 1.5 {
		t.Fatalf("умолчание надбавки изменилось: %v — замер сделан на 1.5", DefaultTableBoost)
	}
	if got := DefaultSearchOpts().TableBoost; got != DefaultTableBoost {
		t.Fatalf("поиск по умолчанию не берёт надбавку: %v", got)
	}
}

// Настройка обязана действовать, иначе она украшение конфига.
func TestTableBoostIsSettable(t *testing.T) {
	if got := (SearchOpts{TableBoost: 2.25}).tableBoost(); got != 2.25 {
		t.Fatalf("надбавка не применилась: %v", got)
	}
	// Ноль означает умолчание, а не «выключить»; «без надбавки» — это 1.0.
	if got := (SearchOpts{}).tableBoost(); got != DefaultTableBoost {
		t.Fatalf("ноль не вернул умолчание: %v", got)
	}
	if got := (SearchOpts{TableBoost: 1}).tableBoost(); got != 1 {
		t.Fatalf("единица должна означать «без надбавки»: %v", got)
	}
}
