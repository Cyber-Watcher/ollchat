package ui

import (
	"strings"
	"testing"
)

// /permissions обещает ровно то, что держит проверка: deny сильнее режима
// и сеансовых разрешений и сверяется с каждой командой строки, но «не
// обходится ничем» было неправдой — это ограждение, а не изоляция (аудит
// 07.10.2026, находка 5).
func TestPermissionsReportPromisesOnlyWhatIsChecked(t *testing.T) {
	m := newTestModel(t)
	m.runCommand("/permissions")
	got := lastBlock(m).text
	if strings.Contains(got, "не обходится ничем") {
		t.Errorf("отчёт обещает больше, чем держит проверка:\n%s", got)
	}
	for _, want := range []string{"сильнее режима и сеансовых разрешений", "для каждой команды строки"} {
		if !strings.Contains(got, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, got)
		}
	}
}
