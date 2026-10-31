// Package ridedata — общие правила расчёта времени и пауз по сэмплам.
// Ими пользуются и импорт (NP), и веб (распределения): зоны и TSS должны
// считаться по одной физике, иначе один и тот же разрыв оказывается паузой
// в одном месте и движением в другом.
package ridedata

import (
	"math"

	"cycling-dashboard/internal/store"
)

const (
	// PauseGapSec — разрыв между записями больше этого может быть авто-паузой.
	PauseGapSec = 5
	// MaxGapSec — потолок времени одной записи при движении (прореженная запись).
	MaxGapSec = 60
	// MinMovingMs — 1 км/ч в м/с: медленнее считаем, что велосипед стоял.
	MinMovingMs = 1.0 / 3.6
)

// SampleTime — сколько времени относится к записи и была ли после неё пауза.
type SampleTime struct {
	Secs  float64 // длительность записи (дубли offset делят время поровну)
	Pause bool    // после записи разрыв-стоянка: окна и данные через него не тянем
}

// Timings — время каждой записи. Время записи — интервал до следующей; записи
// с одинаковым ts_offset (запись 2 Гц) делят интервал до следующего РАЗЛИЧНОГО
// offset поровну. Разрыв больше PauseGapSec — либо авто-пауза (1 с), либо
// прореживание записи при движении (полный интервал, но не больше MaxGapSec).
// Pause ставится только последней записи группы: по нему скользящие окна (NP)
// обрываются на стоянке.
func Timings(samples []store.Sample) []SampleTime {
	out := make([]SampleTime, len(samples))
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && samples[end].Offset == samples[start].Offset {
			end++
		}
		dt, pause := groupSeconds(samples, start, end)
		share := dt / float64(end-start)
		for i := start; i < end; i++ {
			out[i] = SampleTime{Secs: share}
		}
		out[end-1].Pause = pause
		start = end
	}
	return out
}

// groupSeconds — время группы записей с одинаковым offset и признак паузы после неё.
func groupSeconds(samples []store.Sample, start, end int) (float64, bool) {
	if end >= len(samples) {
		return 1, false // после последней записи времени нет
	}
	dt := float64(samples[end].Offset - samples[start].Offset)
	if dt <= 0 {
		return 1, false // записи не по порядку — не размазываем
	}
	if dt > PauseGapSec && isPause(samples, start, end) {
		return 1, true
	}
	return math.Min(dt, MaxGapSec), false
}

// isPause — разрыв это стоянка, а не прореживание записи (smart recording).
// Приоритет признаков:
//  1. дистанция — если известна с обеих сторон, решает её прирост: перед
//     авто-паузой в записи остаётся последняя «ещё ехавшая» скорость (7–8 км/ч),
//     а пройденное расстояние уже не растёт;
//  2. скорость на границе разрыва — от 1 км/ч ехали (медленнее или нет данных —
//     решает следующий пункт);
//  3. педалирование: мощность или каденс есть — ехали, иначе стояли.
func isPause(samples []store.Sample, start, end int) bool {
	next := samples[end]
	dt := float64(next.Offset - samples[start].Offset)
	if samples[start].HasDist && next.HasDist && dt > 0 {
		return (next.DistanceM-samples[start].DistanceM)/dt < MinMovingMs
	}
	for j := max(0, start-1); j <= end; j++ {
		if samples[j].SpeedMs >= MinMovingMs {
			return false
		}
	}
	return !pedaling(samples, start, end)
}

// pedaling — в группе записей есть мощность или каденс.
func pedaling(samples []store.Sample, start, end int) bool {
	for i := start; i < end; i++ {
		if samples[i].Power != 0 || samples[i].Cadence != 0 {
			return true
		}
	}
	return false
}
