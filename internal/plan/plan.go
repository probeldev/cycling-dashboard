// Package plan — чистая рекомендация на тренировочный день. Пакет не ходит
// в БД и не смотрит на время: форма, неделя и история приходят в Input.
package plan

import (
	"fmt"
	"math"
	"strings"

	"cycling-dashboard/internal/thresholds"
)

// Input — всё, что нужно правилам. DaysSinceHard/DaysSinceTest — целые дни
// с последней тяжёлой (TSS ≥ 80 или IF ≥ 0.85) и тестовой тренировки; -1 —
// такой тренировки не было.
type Input struct {
	DataDays      int
	TSB, CTL, ATL float64
	DaysSinceHard int
	DaysSinceTest int
	WeekTSS       float64
	TargetTSS     float64
	DayOfWeek     int     // 1..7 (пн..вс)
	GrayPct       float64 // за 4 недели
	FTPTrainer    float64
	FTPOutdoor    float64
	TrainerShare  float64 // доля станковых тренировок за 4 недели, 0..1
}

// Factor — короткая пара для карточки: «TSB — −10.0».
type Factor struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Recommendation — готовая тренировка дня. Для отдыха и теста зона/ватты
// пустые: их нельзя выразить одним диапазоном Z1–Z7.
type Recommendation struct {
	RuleID      int      `json:"ruleId"`
	Kind        string   `json:"kind"` // rest|z1|z1z2|z2|z2long|ftp_test
	DurationMin [2]int   `json:"durationMin"`
	Zone        string   `json:"zone"` // Z1|Z1-Z2|Z2|Z4-Z5; у теста пусто
	Watts       [2]int   `json:"watts"`
	PctFTP      [2]int   `json:"pctFTP"`
	Cadence     [2]int   `json:"cadence"`
	Rationale   string   `json:"rationale"`
	Factors     []Factor `json:"factors"`
}

// RuleCheck — результат проверки одного правила для ?debug=1.
type RuleCheck struct {
	RuleID  int    `json:"ruleId"`
	Applied bool   `json:"applied"`
	Reason  string `json:"reason"`
}

// Recommend применяет правила по порядку и возвращает первое совпадение.
func Recommend(in Input) Recommendation {
	rec, _ := Explain(in)
	return rec
}

// Explain — то же, что Recommend, но ещё и след проверки правил: какие
// отклонены и почему. Нужен для отладки, в обычном ответе не используется.
func Explain(in Input) (Recommendation, []RuleCheck) {
	checks := make([]RuleCheck, 0, 8)
	reject := func(id int, reason string) {
		checks = append(checks, RuleCheck{RuleID: id, Reason: reason})
	}
	apply := func(rec Recommendation) (Recommendation, []RuleCheck) {
		checks = append(checks, RuleCheck{RuleID: rec.RuleID, Applied: true, Reason: "сработало"})
		return rec, checks
	}

	if in.DataDays < 21 {
		return apply(z2(in, 0, [2]int{60, 60}, fmt.Sprintf(
			"Данных пока мало: %d дн. истории. Работай ровно, без интервалов.", in.DataDays)))
	}
	reject(0, fmt.Sprintf("данных %d дн. (нужно < 21)", in.DataDays))

	if in.TSB < thresholds.TSBLoadedMin {
		return apply(Recommendation{
			RuleID:      1,
			Kind:        "rest",
			DurationMin: [2]int{0, 0},
			Zone:        "",
			Rationale:   fmt.Sprintf("TSB %s: усталость накоплена. Полный отдых или Z1 30–45 мин.", num(in.TSB)),
			Factors: []Factor{
				{Label: "TSB", Value: num(in.TSB)},
				{Label: "CTL", Value: num(in.CTL)},
				{Label: "ATL", Value: num(in.ATL)},
			},
		})
	}
	reject(1, fmt.Sprintf("TSB %s (нужно < %s)", num(in.TSB), num(thresholds.TSBLoadedMin)))

	if in.TSB < thresholds.TSBLightBelow {
		return apply(z1z2(in, 2, [2]int{40, 60},
			fmt.Sprintf("TSB %s: форма просела. Лёгкая Z1–Z2 без интервалов.", num(in.TSB))))
	}
	reject(2, fmt.Sprintf("TSB %s (нужно %s…<%s)", num(in.TSB), num(thresholds.TSBLoadedMin), num(thresholds.TSBLightBelow)))

	if in.DaysSinceHard >= 0 && in.DaysSinceHard <= 1 && in.TSB < 0 {
		return apply(Recommendation{
			RuleID:      3,
			Kind:        "z1z2",
			DurationMin: [2]int{45, 60},
			Zone:        "Z1-Z2",
			Watts:       watts(in, 55, 75),
			PctFTP:      [2]int{55, 75},
			Cadence:     [2]int{80, 90},
			Rationale: fmt.Sprintf("%s тяжёлая тренировка, TSB %s: сегодня только Z1–Z2.",
				hardWhen(in.DaysSinceHard), num(in.TSB)),
			Factors: []Factor{
				{Label: "Тяжёлая", Value: hardWhen(in.DaysSinceHard)},
				{Label: "TSB", Value: num(in.TSB)},
				{Label: "Неделя", Value: weekFactor(in)},
			},
		})
	}
	reject(3, fmt.Sprintf("TSB %s, тяжёлая %s", num(in.TSB), hardWhen(in.DaysSinceHard)))

	if in.TSB > 15 && in.DaysSinceTest > 21 {
		return apply(ftpTest(in))
	}
	reject(4, fmt.Sprintf("TSB %s (нужно > +15), тест %s (нужно > 21 дн.)",
		num(in.TSB), daysOrNever(in.DaysSinceTest)))

	if in.TSB > 5 && in.WeekTSS < 0.7*in.TargetTSS {
		return apply(Recommendation{
			RuleID:      5,
			Kind:        "z2long",
			DurationMin: [2]int{90, 120},
			Zone:        "Z2",
			Watts:       watts(in, 60, 75),
			PctFTP:      [2]int{60, 75},
			Cadence:     [2]int{85, 90},
			Rationale: fmt.Sprintf("TSB %s, неделя %s из %s TSS: можно добавить длинную Z2.",
				num(in.TSB), tss(in.WeekTSS), tss(in.TargetTSS)),
			Factors: []Factor{
				{Label: "TSB", Value: num(in.TSB)},
				{Label: "Неделя", Value: weekFactor(in)},
				{Label: "Осталось", Value: tss(math.Max(0, in.TargetTSS-in.WeekTSS)) + " TSS"},
			},
		})
	}
	reject(5, fmt.Sprintf("TSB %s (нужно > +5), неделя %s из %s (нужно < 70%%)",
		num(in.TSB), tss(in.WeekTSS), tss(in.TargetTSS)))

	if in.GrayPct > 20 {
		return apply(z2(in, 6, [2]int{60, 75},
			fmt.Sprintf("Серая зона %s%% за 4 недели: замени темп на ровную Z2.", pct(in.GrayPct))))
	}
	reject(6, fmt.Sprintf("серая зона %s%% (нужно > 20%%)", pct(in.GrayPct)))

	return apply(z2(in, 7, [2]int{60, 75},
		"Форма в норме, недельный объём выполняется."))
}

func z2(in Input, rule int, duration [2]int, rationale string) Recommendation {
	if rule == 0 {
		rationale += " Начни с ровной Z2, 60–75% FTP."
	}
	factors := []Factor{
		{Label: "TSB", Value: num(in.TSB)},
		{Label: "Неделя", Value: weekFactor(in)},
		{Label: "CTL", Value: num(in.CTL)},
		{Label: "ATL", Value: num(in.ATL)},
	}
	if rule == 0 {
		factors = []Factor{
			{Label: "Данные", Value: fmt.Sprintf("%d дн.", in.DataDays)},
			{Label: "TSB", Value: num(in.TSB)},
			{Label: "Неделя", Value: weekFactor(in)},
		}
	}
	return Recommendation{
		RuleID:      rule,
		Kind:        "z2",
		DurationMin: duration,
		Zone:        "Z2",
		Watts:       watts(in, 60, 75),
		PctFTP:      [2]int{60, 75},
		Cadence:     [2]int{85, 90},
		Rationale:   rationale,
		Factors:     factors,
	}
}

func z1z2(in Input, rule int, duration [2]int, rationale string) Recommendation {
	return Recommendation{
		RuleID:      rule,
		Kind:        "z1z2",
		DurationMin: duration,
		Zone:        "Z1-Z2",
		Watts:       watts(in, 55, 75),
		PctFTP:      [2]int{55, 75},
		Cadence:     [2]int{80, 90},
		Rationale:   rationale,
		Factors: []Factor{
			{Label: "TSB", Value: num(in.TSB)},
			{Label: "CTL", Value: num(in.CTL)},
			{Label: "ATL", Value: num(in.ATL)},
		},
	}
}

// ftpTest выбирает формат теста по доле станка: на улице — 20 минут,
// на станке — ramp. Зона у теста не задаётся: это замер, а не работа в зоне.
func ftpTest(in Input) Recommendation {
	outdoor := in.TrainerShare < 0.5
	where := "ramp-тест на станке"
	if outdoor {
		where = "20 минут на улице"
	}
	return Recommendation{
		RuleID:      4,
		Kind:        "ftp_test",
		DurationMin: [2]int{20, 20},
		Zone:        "",
		Rationale: fmt.Sprintf("TSB %s и теста не было %s: пора проверить FTP — %s.",
			num(in.TSB), daysOrNever(in.DaysSinceTest), where),
		Factors: []Factor{
			{Label: "TSB", Value: num(in.TSB)},
			{Label: "Без теста", Value: daysOrNever(in.DaysSinceTest)},
			{Label: "Станок", Value: pct(in.TrainerShare*100) + "%"},
			{Label: "FTP", Value: wattsRange(in)},
		},
	}
}

// activeFTP — действующий FTP по доле станка; без уличного значения
// возвращаемся к станковому. Ноль означает, что FTP вообще не задан.
func activeFTP(in Input) float64 {
	if in.TrainerShare >= 0.5 {
		return in.FTPTrainer
	}
	if in.FTPOutdoor > 0 {
		return in.FTPOutdoor
	}
	return in.FTPTrainer
}

func watts(in Input, fromPct, toPct int) [2]int {
	ftp := activeFTP(in)
	if ftp <= 0 {
		return [2]int{0, 0}
	}
	return [2]int{
		int(math.Round(float64(fromPct) / 100 * ftp)),
		int(math.Round(float64(toPct) / 100 * ftp)),
	}
}

func wattsRange(in Input) string {
	w := watts(in, 60, 75)
	if w[0] == 0 && w[1] == 0 {
		return "нет"
	}
	return fmt.Sprintf("%d–%d Вт", w[0], w[1])
}

func weekFactor(in Input) string {
	return tss(in.WeekTSS) + " / " + tss(in.TargetTSS) + " TSS"
}

func hardWhen(days int) string {
	switch {
	case days < 0:
		return "нет"
	case days == 0:
		return "сегодня"
	case days == 1:
		return "вчера"
	default:
		return fmt.Sprintf("%d дн. назад", days)
	}
}

func daysOrNever(days int) string {
	if days < 0 {
		return "не было"
	}
	return fmt.Sprintf("%d дн.", days)
}

// num — одно число после запятой с типографским минусом, как в подписях
// дашборда. Ноль без знака.
func num(v float64) string {
	if v == 0 {
		v = 0
	}
	return strings.ReplaceAll(fmt.Sprintf("%.1f", v), "-", "−")
}

func tss(v float64) string {
	return fmt.Sprintf("%.0f", v)
}

func pct(v float64) string {
	return fmt.Sprintf("%.1f", v)
}
