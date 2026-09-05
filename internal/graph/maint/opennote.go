package maint

import (
	"fmt"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// OpenNote — строка о том, во что обошлось открытие графа: время и память,
// цветом по порогам graph.open_slow_seconds / open_hot_seconds.
//
// Живёт здесь, а не в интерфейсе, потому что открывают граф чаще всего команды
// обслуживания, а цвет порогов должен быть одним и тем же (этап 91, R4.7:
// раньше ради этой строки cmd тянул весь пакет ui).
//
// Пустая строка означает «показывать нечего»: граф не открывали (создали
// пустым) или замер не снят.
//
// Экспортирована ради команд командной строки: они открывают граф чаще всего,
// и цвет порогов должен быть тем же самым, а не переписанным заново.
//
// Пустая строка означает «показывать нечего»: граф не открывали (создали
// пустым) или замер не снят.
func OpenNote(st graph.OpenStats, cfg *config.Graph) string {
	if st.Elapsed <= 0 {
		return ""
	}
	text := fmt.Sprintf("граф открыт за %s, занимает %s", humanSeconds(st.Elapsed), humanBytes(st.Heap))
	// Пик показывается, только когда он заметно больше удержанного: иначе это
	// лишнее число. Разница важна тем, у кого мало памяти — открыть граф стоит
	// дороже, чем держать его открытым.
	if st.Peak > st.Heap+st.Heap/4 {
		text += fmt.Sprintf(", при открытии до %s", humanBytes(st.Peak))
	}
	return openStyle(st.Elapsed, cfg).Render(text)
}

// graphOpenStyle выбирает цвет по порогам: до первого — спокойный и тусклый,
// между порогами — оранжевый, выше второго — красный.
func openStyle(d time.Duration, cfg *config.Graph) lipgloss.Style {
	switch {
	case d >= cfg.OpenHot():
		return lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.ColorHot()))
	case d >= cfg.OpenSlow():
		return lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.ColorSlow()))
	default:
		// Тусклым: пока всё как обычно, строка не должна отвлекать.
		return lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.ColorOK())).Faint(true)
	}
}

// humanSeconds — время открытия словами человека: до минуты в секундах
// с десятой долей, дальше в минутах.
func humanSeconds(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1f с", d.Seconds())
	}
	return fmt.Sprintf("%d мин %d с", int(d.Minutes()), int(d.Seconds())%60)
}

// humanBytes — занятая память в мегабайтах или гигабайтах.
func humanBytes(n uint64) string {
	const mb = 1 << 20
	switch {
	case n == 0:
		return "меньше мегабайта"
	case n < mb:
		return fmt.Sprintf("%d КБ", n/1024)
	case n < 1024*mb:
		return fmt.Sprintf("%d МБ", n/mb)
	default:
		return fmt.Sprintf("%.2f ГБ", float64(n)/float64(1024*mb))
	}
}
