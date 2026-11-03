package web

import "cycling-dashboard/internal/store"

// mmpCard — карточка «лучшего усилия» на деталке тренировки.
type mmpCard struct {
	Secs     int // окно, секунды
	Label    string
	Watts    float64
	PR       string // подпись рекорда, если это PR
	PRPeriod string // период рекорда для сводки («за месяц»), пусто — не PR
	Trend    int    // разница к лучшему значению окна за 30 дней до даты тренировки, Вт (0 — не считается)
}

// mmpCardWindows — окна, которые показываем на деталке.
var mmpCardWindows = []struct {
	secs  int
	label string
}{
	{5, "5 с"},
	{60, "1 мин"},
	{300, "5 мин"},
	{1200, "20 мин"},
}

// prTitles — как подписывать рекорд по периоду.
var prTitles = map[string]string{
	"all":  "рекорд за всё время",
	"365d": "рекорд за год",
	"90d":  "рекорд за 90 дней",
	"30d":  "рекорд за месяц",
}

// prPeriods — короткие подписи периодов для связной сводки: без слова
// «рекорд» и без уточнений в скобках, фраза и так начинается с «Рекордные».
var prPeriods = map[string]string{
	"all":  "за всё время",
	"365d": "за год",
	"90d":  "за 90 дней",
	"30d":  "за месяц",
}

// mmpCards собирает карточки лучших усилий; рекорд подписывается по самому
// широкому периоду из списка WorkoutPRs.
func mmpCards(records []store.PowerRecord, prs map[int][]string) []mmpCard {
	byWindow := make(map[int]float64, len(records))
	for _, r := range records {
		byWindow[r.WindowSec] = r.Watts
	}
	out := make([]mmpCard, 0, len(mmpCardWindows))
	for _, w := range mmpCardWindows {
		watts, ok := byWindow[w.secs]
		if !ok {
			continue
		}
		card := mmpCard{Secs: w.secs, Label: w.label, Watts: watts}
		for _, p := range prs[w.secs] {
			if title, ok := prTitles[p]; ok {
				card.PR = title
				card.PRPeriod = prPeriods[p]
				break
			}
		}
		out = append(out, card)
	}
	return out
}
