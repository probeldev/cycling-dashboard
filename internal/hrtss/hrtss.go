// Package hrtss оценивает TSS тренировок без поверметра по пульсу и длительности,
// обучаясь на ближайших по времени тренировках с одновременными данными мощности и пульса.
package hrtss

import "math"

// MinRides — минимальное число обучающих тренировок для построения модели.
// При 3–4 тренировках модель строится, но все оценки помечаются lowConfidence.
const MinRides = 3

// MinHRSpread — минимальный разброс пульса обучающего набора (maxHR − minHR).
// При меньшем разбросе регрессия вырождена: модель не строится.
const MinHRSpread = 15

// lowConfidenceRides — при обучающем наборе меньше этого размера все оценки
// помечаются lowConfidence, даже если пульс внутри диапазона обучения.
const lowConfidenceRides = 5

// Ride — тренировка с известными средним пульсом, длительностью и TSS.
type Ride struct {
	AvgHR       int
	DurationSec int64
	TSS         float64
}

// Model — линейная модель TSS в час = a + b·HR, построенная по обучающему набору.
type Model struct {
	a, b  float64
	minHR int
	maxHR int
	n     int
}

// Fit строит модель методом наименьших квадратов: y = TSS/часы, x = AvgHR.
// Возвращает ok=false, если данных мало (< MinRides), разброс пульса слишком
// узкий (< MinHRSpread) или модель вырождается.
func Fit(rides []Ride) (Model, bool) {
	if len(rides) < MinRides {
		return Model{}, false
	}
	var sumX, sumY, sumXY, sumX2 float64
	minHR, maxHR := 0, 0
	for _, r := range rides {
		if r.DurationSec <= 0 || r.AvgHR <= 0 || r.TSS <= 0 {
			return Model{}, false
		}
		x := float64(r.AvgHR)
		y := r.TSS / (float64(r.DurationSec) / 3600.0)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
		if minHR == 0 || r.AvgHR < minHR {
			minHR = r.AvgHR
		}
		if r.AvgHR > maxHR {
			maxHR = r.AvgHR
		}
	}
	if maxHR-minHR < MinHRSpread {
		return Model{}, false
	}
	n := float64(len(rides))
	meanX := sumX / n
	meanY := sumY / n
	sxx := sumX2 - n*meanX*meanX
	if sxx <= 0 {
		return Model{}, false
	}
	sxy := sumXY - n*meanX*meanY
	b := sxy / sxx
	if b <= 0 || math.IsNaN(b) || math.IsInf(b, 0) {
		return Model{}, false
	}
	a := meanY - b*meanX
	if math.IsNaN(a) || math.IsInf(a, 0) {
		return Model{}, false
	}
	return Model{a: a, b: b, minHR: minHR, maxHR: maxHR, n: len(rides)}, true
}

// Estimate возвращает приблизительный TSS для пульса и длительности.
// lowConfidence=true, если HR выходит за границы обучающего набора ±5 ударов
// или обучающий набор был мал (< 5 тренировок).
// ok=false, если входные данные некорректны или оценка нелепая.
func (m Model) Estimate(hr int, durationSec int64) (tss float64, lowConfidence bool, ok bool) {
	if durationSec <= 0 || hr <= 0 {
		return 0, false, false
	}
	tssPerHour := m.a + m.b*float64(hr)
	tss = tssPerHour * (float64(durationSec) / 3600.0)
	if tss <= 0 || tss > 500 || math.IsNaN(tss) || math.IsInf(tss, 0) {
		return 0, false, false
	}
	lowConfidence = hr < m.minHR-5 || hr > m.maxHR+5 || m.n < lowConfidenceRides
	return tss, lowConfidence, true
}
