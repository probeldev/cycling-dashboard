package web

import (
	"math"
	"strconv"
	"time"

	"cycling-dashboard/internal/store"
)

// wkgTrendDays — за сколько дней показывать изменение Вт/кг на /progress.
var wkgTrendDays = []int{30, 90, 365}

// wkgTrendWindow — изменение Вт/кг к точке N дней назад.
type wkgTrendWindow struct {
	Label  string // «30 дн»
	Change string // «+0.13» или «—»
}

// wkgTrendView — текущее Вт/кг (станок) и изменения к точкам N дней назад.
// Значение и изменения пустые, если истории не хватает: прочерк вместо нуля.
type wkgTrendView struct {
	Value    float64
	HasValue bool
	Windows  []wkgTrendWindow
	Note     string // пояснение к прочеркам
}

// buildWkgTrend считает изменения по дневному ряду профиля. Точки на дату
// N дней назад нет (история короче) или Вт/кг там null (не было веса/FTP) —
// изменение «—», а не ноль.
func buildWkgTrend(points []store.ProfilePoint, today time.Time) wkgTrendView {
	byDate := make(map[time.Time]*float64, len(points))
	var cur *float64
	for i := range points {
		byDate[points[i].Date] = points[i].WPerKgTrainer
		if points[i].Date.Equal(today) {
			cur = points[i].WPerKgTrainer
		}
	}
	if cur == nil {
		return wkgTrendView{}
	}
	windows := make([]wkgTrendWindow, 0, len(wkgTrendDays))
	missing := 0
	for _, days := range wkgTrendDays {
		change := "—"
		if prev, ok := byDate[today.AddDate(0, 0, -days)]; ok && prev != nil {
			change = signedDelta(*cur - *prev)
		} else {
			missing++
		}
		windows = append(windows, wkgTrendWindow{Label: strconv.Itoa(days) + " дн", Change: change})
	}
	note := ""
	switch {
	case missing == len(windows):
		note = "Изменения появятся, когда будут записи веса и FTP на дату 30, 90 и 365 дней назад."
	case missing > 0:
		note = "Прочерк — нет записи веса или FTP на эту дату."
	}
	return wkgTrendView{Value: *cur, HasValue: true, Windows: windows, Note: note}
}

// signedDelta — изменение с двумя знаками и явным плюсом; отрицательный ноль
// не показываем.
func signedDelta(v float64) string {
	if math.Abs(v) < 0.005 {
		return "0.00"
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if v > 0 {
		return "+" + s
	}
	return s
}
