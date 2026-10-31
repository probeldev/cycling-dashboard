package importer

import (
	"database/sql"
	"math"
	"sort"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// mmpVersion — версия расчёта MMP; поднимать при изменении формулы.
// 2 — накат (power == 0) входит в окно временем;
// 3 — дропауты заполняются последним валидным значением (как NP).
const mmpVersion = 3

// mmpWindows — окна MMP в секундах.
var mmpWindows = []int{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 900, 1200, 1800, 2700, 3600, 7200}

// mmpOf — лучшие средние мощности по окнам. В окно входят все записи:
// power > 0 даёт работу, накат — только время и честно разбавляет среднюю,
// дропаут датчика заполняется последним валидным значением (fill-forward,
// как в NP): иначе его время выпадало из окна и длинное окно могло оказаться
// выше короткого (на 308 окно 1800 с покрывало 2214 с реального времени).
// Время — ridedata.Timings.Secs; пауза обрывает сегмент, поэтому окно не
// склеивает мощность до и после остановки. Дропауты до первой валидной
// мощности заполнять нечем — сегмент начинается с первого валидного значения.
//
// Окно — ровно window секунд реального времени: если запись заходит за правую
// границу, берём её частично (доля по времени). Окно не может начинаться с
// записи длиннее самого окна — иначе «1-секундный пик» стал бы средним за 60 с.
// Если запись короче окна — значения нет.
func mmpOf(samples []store.Sample) map[int]float64 {
	times := ridedata.Timings(samples)

	type segment struct{ t, p, dt, pw []float64 } // время, работа, длительности, мощности
	var segments []segment
	cur := segment{t: []float64{0}, p: []float64{0}}
	last, hasLast := 0, false
	for i, s := range samples {
		if s.HasPwr {
			last, hasLast = s.Power, true
		}
		if hasLast {
			cur.t = append(cur.t, cur.t[len(cur.t)-1]+times[i].Secs)
			cur.p = append(cur.p, cur.p[len(cur.p)-1]+float64(last)*times[i].Secs)
			cur.dt = append(cur.dt, times[i].Secs)
			cur.pw = append(cur.pw, float64(last))
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
					continue // запись длиннее окна не может его представлять
				}
				target := sg.t[i] + wf
				j := sort.SearchFloat64s(sg.t, target)
				if j >= len(sg.t) {
					break
				}
				var avg float64
				if sg.t[j] > target {
					// запись пересекает правую границу — берём её частично
					k := j - 1
					frac := (target - sg.t[k]) / sg.dt[k]
					avg = ((sg.p[k] - sg.p[i]) + frac*sg.dt[k]*sg.pw[k]) / wf
				} else {
					avg = (sg.p[j] - sg.p[i]) / wf
				}
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
