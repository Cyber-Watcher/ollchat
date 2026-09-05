package ui

import (
	"fmt"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Скорость ответа в строке состояния.
//
// Итоговая скорость приходит от сервера в конце обмена и точна: там настоящие
// счётчики токенов и наносекунды. Но пока модель отвечает, её ещё нет, а именно
// в это время цифра нужнее всего — по ней сразу видно, что модель поехала
// в оперативную память, что на общем сервере стоит чужая очередь или что
// первая просьба этого сеанса ждёт загрузки весов с диска.
//
// Поэтому счётчиков два. Живой считает куски потока: Ollama шлёт их по одному
// токену, так что это близкая оценка — и она честно помечена знаком «≈», как
// и оценка заполнения контекста до ответа сервера. Итоговый берётся из ответа
// сервера, без домыслов.

// TokenSpeedMode — что показывать в строке состояния.
const (
	SpeedOff   = "off"   // ничего
	SpeedLive  = "live"  // только текущую скорость во время ответа
	SpeedFinal = "final" // только итог по последнему ответу
	SpeedFull  = "full"  // и то и другое плюс время до первого токена
)

// liveSpeed — счётчик скорости, пока идёт ответ.
type liveSpeed struct {
	askedAt time.Time     // когда ушёл запрос
	firstAt time.Time     // когда пришёл первый кусок
	ttft    time.Duration // время до первого куска
	chunks  int
}

// Start отмечает начало обмена.
func (s *liveSpeed) Start(now time.Time) {
	*s = liveSpeed{askedAt: now}
}

// Tick отмечает пришедший кусок потока.
//
// Считаются и рассуждения, и сам ответ: карта работает одинаково над тем
// и другим, а человеку важно видеть, что она работает вообще.
func (s *liveSpeed) Tick(now time.Time) {
	if s.askedAt.IsZero() {
		return
	}
	if s.chunks == 0 {
		s.firstAt = now
		s.ttft = now.Sub(s.askedAt)
	}
	s.chunks++
}

// TTFT — время до первого куска. Ноль, если ответ ещё не начинался.
func (s *liveSpeed) TTFT() time.Duration { return s.ttft }

// Rate — оценка скорости в токенах в секунду.
//
// Отсчёт идёт от первого куска, а не от отправки запроса: иначе ожидание
// загрузки модели с диска размазывалось бы по всей строке и скорость первые
// секунды выглядела бы втрое ниже настоящей.
func (s *liveSpeed) Rate(now time.Time) float64 {
	if s.chunks < 2 || s.firstAt.IsZero() {
		return 0
	}
	elapsed := now.Sub(s.firstAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(s.chunks-1) / elapsed
}

// speedStatus собирает кусок строки состояния о скорости.
//
// mode — значение general.token_speed, streaming — идёт ли ответ прямо сейчас.
// Пустая строка означает «показывать нечего»: так и при выключенной настройке,
// и до первого ответа.
func speedStatus(mode string, streaming bool, live *liveSpeed, stats ollama.Stats, now time.Time) string {
	switch mode {
	case SpeedOff:
		return ""
	case SpeedLive:
		if !streaming {
			return ""
		}
		return liveText(live, now)
	case SpeedFinal:
		if streaming {
			return ""
		}
		return finalText(stats, live, false)
	case SpeedFull:
		if streaming {
			s := liveText(live, now)
			if t := live.TTFT(); t > 0 {
				if s != "" {
					s += " · "
				}
				s += "первый токен " + shortDuration(t)
			}
			return s
		}
		return finalText(stats, live, true)
	}
	return ""
}

// liveText — текущая скорость со знаком оценки.
func liveText(live *liveSpeed, now time.Time) string {
	r := live.Rate(now)
	if r <= 0 {
		return ""
	}
	return fmt.Sprintf("≈%.0f ток/с", r)
}

// finalText — итог по цифрам сервера. Домыслов здесь нет: если сервер
// не прислал счётчиков, показывать нечего.
func finalText(stats ollama.Stats, live *liveSpeed, withTTFT bool) string {
	tps := stats.TokensPerSecond()
	if tps <= 0 {
		return ""
	}
	s := fmt.Sprintf("%.0f ток/с", tps)
	if withTTFT && live.TTFT() > 0 {
		s += " · первый токен " + shortDuration(live.TTFT())
	}
	return s
}

// shortDuration печатает время коротко: «0.4 с», «12 с», «2м05с».
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%.1f с", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%.0f с", d.Seconds())
	default:
		return fmt.Sprintf("%dм%02dс", int(d.Minutes()), int(d.Seconds())%60)
	}
}
