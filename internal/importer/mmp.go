package importer

import (
	"database/sql"
	"math"
	"sort"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// mmpVersion — версия расчёта MMP; поднимать при изменении формулы.
// 2 — накат (power == 0) входит в окно временем, а не выбрасывается.
const mmpVersion = 2

// mmpWindows — окна MMP в секундах.
var mmpWindows = []int{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 900, 1200, 1800, 2700, 3600, 7200}

// mmpOf — лучшие средние мощности по окнам. В окно входят все записи с
// HasPwr: power > 0 даёт работу, power == 0 (накат) даёт только время и честно
// разбавляет среднюю. Вес — ridedata.Timings.Secs. Пауза обрывает сегмент,
// поэтому окно не склеивает мощность до и после остановки.
// Дропаут датчика (HasPwr == false) просто пропускается, сегмент не рвём:
// время дропаута не засчитывается, средняя от этого не завышается, а разрыв
// на каждом дропауте сделал бы длинные окна невычислимыми (на 418 97 дропаутов
// и окно 600 с упало бы с 160 до 68 Вт).
// Окно — последовательность записей, набравшая не меньше window секунд; если
// запись короче — значения для окна нет. Окно не может начинаться с записи
// длиннее самого окна (иначе «1-секундный пик» стал бы средним за 60 с).
func mmpOf(samples []store.Sample) map[int]float64 {
	times := ridedata.Timings(samples)

	type segment struct{ t, p, dt []float64 } // накопленное время, работа, длительности
	var segments []segment
	cur := segment{t: []float64{0}, p: []float64{0}}
	for i, s := range samples {
		if s.HasPwr {
			cur.t = append(cur.t, cur.t[len(cur.t)-1]+times[i].Secs)
			cur.p = append(cur.p, cur.p[len(cur.p)-1]+float64(s.Power)*times[i].Secs)
			cur.dt = append(cur.dt, times[i].Secs)
		}
		if times[i].Pause {
			if len(cur.t) > 1 {
				segments = append(segments, cur)
			}
			cur = segment{t: []float64{0}, p: []float64{0}}
		}
	}
	if len(cur.t) > 1 {
		segments = append(segments, cur)
	}

	out := make(map[int]float64, len(mmpWindows))
	for _, w := range mmpWindows {
		wf := float64(w)
		best, found := 0.0, false
		for _, sg := range segments {
			for i := 0; i < len(sg.t)-1; i++ {
				if sg.dt[i] > wf {
					continue // запись длиннее окна — не может его представлять
				}
				j := sort.SearchFloat64s(sg.t, sg.t[i]+wf)
				if j >= len(sg.t) {
					break
				}
				avg := (sg.p[j] - sg.p[i]) / (sg.t[j] - sg.t[i])
				if !found || avg > best {
					best, found = avg, true
				}
			}
		}
		if found {
			out[w] = math.Round(best*10) / 10
		}
	}
	return out
}

// mmpRecords — MMP в виде строк таблицы, по возрастанию окна.
func mmpRecords(samples []store.Sample) []store.PowerRecord {
	m := mmpOf(samples)
	out := make([]store.PowerRecord, 0, len(m))
	for _, w := range mmpWindows {
		if v, ok := m[w]; ok {
			out = append(out, store.PowerRecord{WindowSec: w, Watts: v})
		}
	}
	return out
}

// BackfillMMP считает MMP для тренировок, импортированных до появления таблицы
// (или посчитанных старой версией). Идемпотентен, работает в одной транзакции.
func BackfillMMP(db *sql.DB) (int, error) {
	refs, err := store.WorkoutsNeedingMMP(db, mmpVersion)
	if err != nil {
		return 0, err
	}
	if len(refs) == 0 {
		return 0, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, ref := range refs {
		w, err := store.GetWorkout(tx, ref.ID)
		if err != nil {
			return 0, err
		}
		var records []store.PowerRecord
		// без мощности MMP нет — сэмплы зря не читаем
		if w.HasPower {
			samples, err := store.RawSamples(tx, ref.ID)
			if err != nil {
				return 0, err
			}
			records = mmpRecords(samples)
		}
		if err := store.SetWorkoutMMP(tx, ref.ID, records, mmpVersion); err != nil {
			return 0, err
		}
	}
	return len(refs), tx.Commit()
}
