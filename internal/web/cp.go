package web

import (
	"math"

	"cycling-dashboard/internal/store"
)

// cpResult — модель критической мощности по MMP: W(t) = CP + W'/t.
// Указатели, чтобы в JSON честно отдавать null, когда данных мало.
type cpResult struct {
	CP            *float64 `json:"cp"`
	WPrime        *float64 `json:"wPrime"` // Дж
	R2            *float64 `json:"r2"`
	Points        int      `json:"points"`
	FTPEst        *float64 `json:"ftpEst"` // 0.95 × лучшее 20 мин
	LowConfidence bool     `json:"lowConfidence"`
}

// powerPoints — окна из рекордов периода (для фита нужны только secs/watts).
func powerPoints(recs []store.Record) []store.PowerRecord {
	out := make([]store.PowerRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, store.PowerRecord{WindowSec: r.WindowSec, Watts: r.Watts})
	}
	return out
}

// fitCP фитирует CP и W' линейной регрессией W от 1/t по окнам 120–1200 с.
// Нужно не меньше трёх точек. FTP-оценка считается из окна 1200 с (20 мин)
// независимо от фита: для коротких записей её просто нет.
func fitCP(records []store.PowerRecord) cpResult {
	res := cpResult{}
	var xs, ys []float64
	for _, r := range records {
		if r.WindowSec == 1200 {
			v := math.Round(0.95*r.Watts*10) / 10
			res.FTPEst = &v
		}
		if r.WindowSec < 120 || r.WindowSec > 1200 {
			continue
		}
		xs = append(xs, 1/float64(r.WindowSec))
		ys = append(ys, r.Watts)
	}
	res.Points = len(xs)
	if len(xs) < 3 {
		return res
	}

	var sx, sy, sxx, sxy float64
	n := float64(len(xs))
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	den := n*sxx - sx*sx
	// вырожденный фит (все окна одинаковые): den отличается от нуля только
	// ошибкой округления — сравниваем относительно масштаба
	scale := math.Max(math.Abs(n*sxx), math.Abs(sx*sx))
	if scale == 0 || math.Abs(den) <= 1e-12*scale {
		res.LowConfidence = true
		return res
	}
	slope := (n*sxy - sx*sy) / den // W'
	intercept := (sy - slope*sx) / n
	// CP и W' должны быть положительными: плоская/убывающая кривая — не модель
	if intercept <= 0 || slope <= 0 {
		res.LowConfidence = true
		return res
	}

	// R² — насколько модель объясняет разброс точек (порог — по неокруглённому)
	my := sy / n
	var ssRes, ssTot float64
	for i := range xs {
		pred := intercept + slope*xs[i]
		ssRes += (ys[i] - pred) * (ys[i] - pred)
		ssTot += (ys[i] - my) * (ys[i] - my)
	}
	r2raw := 1.0
	if ssTot > 0 {
		r2raw = 1 - ssRes/ssTot
	}

	cp := math.Round(intercept*10) / 10
	wp := math.Round(slope)
	r2 := math.Round(r2raw*1000) / 1000
	res.CP = &cp
	res.WPrime = &wp
	res.R2 = &r2
	res.LowConfidence = r2raw < 0.95
	return res
}

// cpView — данные для карточки CP на странице прогресса (без указателей).
func cpView(res cpResult) map[string]any {
	v := map[string]any{
		"HasFit": res.CP != nil,
		"Points": res.Points,
		"Low":    res.LowConfidence,
		"FTP":    nil,
		"R2":     nil,
		"Reason": "",
	}
	if res.CP != nil {
		v["CP"] = *res.CP
	}
	if res.WPrime != nil {
		v["WPrime"] = *res.WPrime / 1000 // кДж
	}
	if res.FTPEst != nil {
		v["FTP"] = *res.FTPEst
	}
	if res.R2 != nil {
		v["R2"] = *res.R2
	}
	if res.CP == nil {
		// различаем «мало точек» и «кривая не описывается моделью»
		if res.Points < 3 {
			v["Reason"] = "few"
		} else {
			v["Reason"] = "model"
		}
	}
	return v
}
