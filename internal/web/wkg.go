package web

import (
	"math"
	"strconv"
	"strings"
	"time"

	"cycling-dashboard/internal/store"
)

// wkgTrendDays — за сколько дней показывать изменение Вт/кг на /progress.
var wkgTrendDays = []int{30, 90, 365}

// wkgTrendView — текущее Вт/кг (станок) и изменения к точкам N дней назад.
// Значение и изменения пустые, если истории не хватает: прочерк вместо нуля.
type wkgTrendView struct {
	Value    float64
	HasValue bool
	Text     string // «30 дн +0.13 · 90 дн +0.10 · 365 дн —»
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
	parts := make([]string, 0, len(wkgTrendDays))
	for _, days := range wkgTrendDays {
		text := "—"
		if prev, ok := byDate[today.AddDate(0, 0, -days)]; ok && prev != nil {
			text = signedDelta(*cur - *prev)
		}
		parts = append(parts, strconv.Itoa(days)+" дн "+text)
	}
	return wkgTrendView{Value: *cur, HasValue: true, Text: strings.Join(parts, " · ")}
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
