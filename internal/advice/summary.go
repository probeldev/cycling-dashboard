package advice

import (
	"fmt"
	"strings"

	"cycling-dashboard/internal/store"
	"cycling-dashboard/internal/thresholds"
)

// RecordFact — рекорд тренировки по окну MMP. Подписи окна и периода
// готовит web-слой (из уже посчитанных PR-флагов), движок только собирает
// из них фразу.
type RecordFact struct {
	Window string // "5 мин"
	Period string // "за месяц"
	Watts  int    // значение рекорда (0 — неизвестно)
}

// Пороги оценки «что за тренировка» для связного вывода.
const (
	ThresholdZonePct = 30   // больше % времени в Z4+ при IF > ThresholdIF — пороговая работа
	ThresholdIF      = 0.95 // выше — почти гоночная интенсивность
	IntensiveIF      = 0.85 // не ниже — интенсивная тренировка
	RecoveryIF       = 0.65 // ниже — восстановительная
	SteadyLowZonePct = 70   // не меньше % времени в Z1–Z2 при ровном VI — аэробная работа
	MaxPhrases       = 3    // связный вывод — 2–3 фразы, излишек уходит в инсайты
)

// Summary — связный вывод из 2–3 фраз для шапки деталки тренировки
// (TODO-UI §4.3, §6): что за тренировка, что рекордное, что дальше.
// Приоритет фраз: оценка → рекорд → тренд → рекомендация. Тон — сдержанный
// тренер: факты о данных, без восклицаний и оценок человека.
func Summary(w store.Workout, ctx Context) []string {
	phrases := make([]string, 0, 4)
	if s := assess(w, ctx); s != "" {
		phrases = append(phrases, s)
	}
	if s := recordPhrase(ctx.Records); s != "" {
		phrases = append(phrases, s)
	}
	if s := trendPhrase(w, ctx); s != "" {
		phrases = append(phrases, s)
	}
	if s := recommendPhrase(w, ctx); s != "" {
		phrases = append(phrases, s)
	}
	if len(phrases) > MaxPhrases {
		phrases = phrases[:MaxPhrases]
	}
	return phrases
}

// assess — что за тренировка: тип работы по зонам, IF и VI.
func assess(w store.Workout, ctx Context) string {
	total, high := zoneTotals(ctx.Zones)
	var lowPct, highPct float64
	if total > 0 {
		lowPct = pct(float64(ctx.Zones[0]+ctx.Zones[1]), float64(total))
		highPct = pct(float64(high), float64(total))
	}
	switch {
	case w.HasIF && w.IF > ThresholdIF && highPct > ThresholdZonePct:
		return fmt.Sprintf("Пороговая работа: %.0f%% времени педалирования в Z4 и выше, IF %.2f.", highPct, w.IF)
	case w.HasVI && w.VI > HighVI:
		return fmt.Sprintf("Рваная езда: много разгонов и остановок, VI %.2f.", w.VI)
	case w.HasIF && w.IF >= IntensiveIF:
		if w.HasTSS {
			return fmt.Sprintf("Интенсивная тренировка: IF %.2f, нагрузка %.0f TSS.", w.IF, w.TSS)
		}
		return fmt.Sprintf("Интенсивная тренировка: IF %.2f.", w.IF)
	case total > 0 && lowPct >= SteadyLowZonePct && w.HasVI && w.VI < GoodVI:
		return fmt.Sprintf("Ровная аэробная работа: %.0f%% времени в Z1–Z2.", lowPct)
	case w.HasIF && w.IF < RecoveryIF:
		return "Лёгкая восстановительная поездка."
	case w.HasPower:
		return fmt.Sprintf("Поездка %.0f мин, средняя мощность %d Вт.", w.Duration.Minutes(), w.AvgPower)
	}
	return fmt.Sprintf("Поездка %.0f мин.", w.Duration.Minutes())
}

// recordPhrase — рекордные отрезки, сгруппированные по периоду в порядке
// первого появления: «Рекордные отрезки: 5 мин — 312 Вт (за месяц);
// 1 мин и 5 мин (за год).»
func recordPhrase(recs []RecordFact) string {
	if len(recs) == 0 {
		return ""
	}
	var order []string
	byPeriod := map[string][]RecordFact{}
	for _, r := range recs {
		if _, ok := byPeriod[r.Period]; !ok {
			order = append(order, r.Period)
		}
		byPeriod[r.Period] = append(byPeriod[r.Period], r)
	}
	parts := make([]string, 0, len(order))
	for _, p := range order {
		g := byPeriod[p]
		if len(g) == 1 && g[0].Watts > 0 {
			parts = append(parts, fmt.Sprintf("%s — %d Вт (%s)", g[0].Window, g[0].Watts, p))
			continue
		}
		windows := make([]string, len(g))
		for i, r := range g {
			windows[i] = r.Window
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", joinRU(windows), p))
	}
	return "Рекордные отрезки: " + strings.Join(parts, "; ") + "."
}

// trendPhrase — фон, на котором прошла тренировка: усталость, серия
// тяжёлых дней или стабильность пульса и мощности.
func trendPhrase(w store.Workout, ctx Context) string {
	switch {
	case w.HasTSS && w.TSS > HeavyTSS && ctx.YesterdayTSS > PreviousHeavyTSS:
		return "Второй тяжёлый день подряд по нагрузке."
	case ctx.TSB < thresholds.TSBLoadedMin:
		return fmt.Sprintf("TSB %.0f — накопленная усталость, форма просела.", ctx.TSB)
	case ctx.Decoupling != nil && *ctx.Decoupling < GoodDecouplingPct && w.HasVI && w.VI < GoodVI:
		return fmt.Sprintf("Пульс и мощность стабильны всю поездку (аэробный дрейф %.0f%%).", *ctx.Decoupling)
	}
	return ""
}

// recommendPhrase — что делать дальше. Пустая строка, если данные не дают
// основания для совета: выдумывать рекомендацию не надо.
func recommendPhrase(w store.Workout, ctx Context) string {
	switch {
	case w.HasTSS && w.TSS > HeavyTSS && ctx.YesterdayTSS > PreviousHeavyTSS:
		return "Завтра — лёгкий день Z1 или отдых."
	case ctx.TSB < thresholds.TSBLightBelow:
		return "Дальше — только лёгкая Z1–Z2, пока TSB не вернётся к нулю."
	case w.RPE == 0:
		return "Отметь RPE: субъективная оценка помогает контролировать нагрузку."
	case ctx.WeekGrayPct > GrayZonePct:
		return "На неделе много серой зоны — добавь лёгких тренировок или убери темповые."
	}
	return ""
}

// joinRU — перечисление через запятую с «и» перед последним элементом.
func joinRU(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " и " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " и " + items[len(items)-1]
}
