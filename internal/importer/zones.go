package importer

import (
	"database/sql"
	"math"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// zonesVersion — версия расчёта зон. Поднимать при изменении формулы зон:
// бэкфилл пересчитает workout_zones.
const zonesVersion = 1

// zonesOf — секунды по зонам Z1–Z7 из общего расчёта ridedata.PowerTime
// (тот же, что у /dist): не плодим вторую формулу.
func zonesOf(samples []store.Sample, ftp int) [7]int {
	var out [7]int
	if ftp <= 0 {
		return out
	}
	zs, _ := ridedata.PowerTime(samples, ftp)
	for j, z := range zs {
		out[j] = int(math.Round(z))
	}
	return out
}

// BackfillZones заполняет workout_zones для тренировок, импортированных до
// появления таблицы (или посчитанных старой версией). Идемпотентен: после
// прогона zones_version актуален.
func BackfillZones(db *sql.DB) (int, error) {
	refs, err := store.WorkoutsNeedingZones(db, zonesVersion)
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
		var zones [7]int
		// без мощности зон нет — сэмплы зря не читаем
		if w.FTP > 0 && w.NormPower > 0 {
			samples, err := store.RawSamples(tx, ref.ID)
			if err != nil {
				return 0, err
			}
			zones = zonesOf(samples, w.FTP)
		}
		if err := store.SetWorkoutZones(tx, ref.ID, zones, zonesVersion); err != nil {
			return 0, err
		}
	}
	return len(refs), tx.Commit()
}
