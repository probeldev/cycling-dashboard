package web

import (
	"math"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// Презентационные хелперы деталки тренировки (TODO-UI §4.3): микро-выводы
// смысла у аббревиатур и сборка данных для сводки. Расчётов здесь нет —
// только перевод уже посчитанных чисел в подписи.

// ifVerdict — микро-вывод смысла Intensity Factor (NP / FTP).
func ifVerdict(v float64) string {
	switch {
	case v >= 1.0:
		return "выше FTP — гоночная интенсивность"
	case v >= 0.9:
		return "почти гоночная интенсивность"
	case v >= 0.8:
		return "твёрдая аэробная работа"
	case v >= 0.7:
		return "умеренная нагрузка"
	default:
		return "лёгкая езда"
	}
}

// viVerdict — микро-вывод смысла Variability Index (NP / средняя).
func viVerdict(v float64) string {
	switch {
	case v > advice.HighVI:
		return "очень рвано, много разгонов"
	case v > 1.10:
		return "рвано, много разгонов"
	case v > 1.05:
		return "умеренно рваная езда"
	default:
		return "ровная работа"
	}
}

// tssVerdict — микро-вывод смысла TSS; граница «тяжёлой» — та же, что
// в правилах советов (advice.HeavyTSS).
func tssVerdict(v float64) string {
	switch {
	case v > 150:
		return "очень тяжёлая тренировка"
	case v > advice.HeavyTSS:
		return "тяжёлая тренировка"
	case v >= 50:
		return "средняя нагрузка"
	default:
		return "лёгкая нагрузка"
	}
}

// pauseVerdict — чем объясняются паузы: на улице это остановки в городе,
// на станке — перерывы между интервалами.
func pauseVerdict(trainer bool) string {
	if trainer {
		return "остановки между интервалами"
	}
	return "светофоры и перекрёстки"
}

// insightLevel — уровень карточки advice в модификатор .insight (§3).
func insightLevel(level string) string {
	switch level {
	case advice.LevelWarn:
		return "warn"
	case advice.LevelGood:
		return "ok"
	default:
		return "info"
	}
}

// recordFacts — рекордные отрезки тренировки для движка сводки §6. Берёт
// готовые PR-флаги из карточек MMP: новых расчётов не появляется.
func recordFacts(cards []mmpCard) []advice.RecordFact {
	var out []advice.RecordFact
	for _, c := range cards {
		if c.PRPeriod == "" {
			continue
		}
		out = append(out, advice.RecordFact{
			Window: c.Label,
			Period: c.PRPeriod,
			Watts:  int(math.Round(c.Watts)),
		})
	}
	return out
}

// attachRecordTrends проставляет карточкам тренд: разницу между значением
// ЭТОЙ тренировки (cards[i].Watts) и лучшим значением того же окна за
// предыдущий период до даты тренировки (запрос тот же, что у
// /api/records?period=30d, но относительно её даты, а не now). Карточки
// без данных (Watts <= 0) и окна без прошлого периода тренда не получают.
// Тренд — подпись к готовым числам, а не новый расчёт; GPS в рекордах
// мощности не участвует.
func attachRecordTrends(cards []mmpCard, prev []store.Record) {
	prevByWindow := make(map[int]float64, len(prev))
	for _, r := range prev {
		prevByWindow[r.WindowSec] = r.Watts
	}
	for i := range cards {
		if cards[i].Watts <= 0 {
			continue
		}
		p, ok := prevByWindow[cards[i].Secs]
		if !ok {
			continue
		}
		if d := int(math.Round(cards[i].Watts - p)); d != 0 {
			cards[i].Trend = d
		}
	}
}

// zoneEdgesW — границы зон в ваттах для фоновых полос графика мощности.
// Те же доли FTP, что в подписях зон (ridedata.ZoneBounds).
func zoneEdgesW(ftp int) []float64 {
	if ftp <= 0 {
		return nil
	}
	bounds := ridedata.ZoneBounds()
	out := make([]float64, len(bounds))
	for i, f := range bounds {
		// 0.56*200 в float64 — 112.00000000000001; для полос на графике
		// хватает десятых
		out[i] = math.Round(f*float64(ftp)*10) / 10
	}
	return out
}
