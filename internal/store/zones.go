package store

import (
	"database/sql"
	"math"
	"time"
)

// SetWorkoutZones заменяет зоны тренировки (Z1–Z7) и помечает их актуальными.
// Нулевые зоны строк не создают. При вызове с *sql.DB работает в собственной
// транзакции: DELETE + INSERT + UPDATE версии не должны применяться частично.
func SetWorkoutZones(q DBTX, id int64, zones [7]int, version int) error {
	if db, ok := q.(*sql.DB); ok {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := setWorkoutZones(tx, id, zones, version); err != nil {
			return err
		}
		return tx.Commit()
	}
	return setWorkoutZones(q, id, zones, version)
}

func setWorkoutZones(q DBTX, id int64, zones [7]int, version int) error {
	if _, err := q.Exec(`DELETE FROM workout_zones WHERE workout_id = ?`, id); err != nil {
		return err
	}
	for j, secs := range zones {
		if secs <= 0 {
			continue
		}
		if _, err := q.Exec(`INSERT INTO workout_zones (workout_id, zone, secs) VALUES (?,?,?)`,
			id, j+1, secs); err != nil {
			return err
		}
	}
	_, err := q.Exec(`UPDATE workouts SET zones_version = ? WHERE id = ?`, version, id)
	return err
}

// WorkoutZones — секунды по зонам Z1–Z7; отсутствующие зоны — 0.
func WorkoutZones(q DBTX, id int64) ([7]int, error) {
	var zones [7]int
	rows, err := q.Query(`SELECT zone, secs FROM workout_zones WHERE workout_id = ?`, id)
	if err != nil {
		return zones, err
	}
	defer rows.Close()
	for rows.Next() {
		var zone, secs int
		if err := rows.Scan(&zone, &secs); err != nil {
			return zones, err
		}
		if zone >= 1 && zone <= 7 {
			zones[zone-1] = secs
		}
	}
	return zones, rows.Err()
}

// ZoneTime — сумма секунд в зонах Z1–Z7 за [from, to) для тренировок с
// указанной базой FTP: ftpKind = "trainer" | "outdoor". Зоны посчитаны от
// разных FTP, смешивать их нельзя; тип тренировки не решает — у уличной
// тренировки без ftp_outdoor база станковая (ftp_kind = "trainer").
func ZoneTime(db DBTX, from, to time.Time, ftpKind string) ([7]int, error) {
	var zones [7]int
	rows, err := db.Query(`SELECT z.zone, SUM(z.secs) FROM workout_zones z
	    JOIN workouts w ON w.id = z.workout_id
	    WHERE w.ftp_kind = ? AND w.start_time >= ? AND w.start_time < ?
	    GROUP BY z.zone`,
		ftpKind, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return zones, err
	}
	defer rows.Close()
	for rows.Next() {
		var zone, secs int
		if err := rows.Scan(&zone, &secs); err != nil {
			return zones, err
		}
		if zone >= 1 && zone <= 7 {
			zones[zone-1] = secs
		}
	}
	return zones, rows.Err()
}

// ZoneDays — число локальных дней с рассчитанными зонами по базе FTP
// в [from, to). Несколько тренировок в один день считаются один раз.
func ZoneDays(db DBTX, from, to time.Time, ftpKind string) (int, error) {
	rows, err := db.Query(`SELECT DISTINCT w.start_time FROM workout_zones z
	    JOIN workouts w ON w.id = z.workout_id
	    WHERE w.ftp_kind = ? AND w.start_time >= ? AND w.start_time < ?`,
		ftpKind, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	days := map[time.Time]bool{}
	for rows.Next() {
		var start string
		if err := rows.Scan(&start); err != nil {
			return 0, err
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return 0, err
		}
		days[StartOfDay(t.Local())] = true
	}
	return len(days), rows.Err()
}

// WorkoutsNeedingZones — тренировки, у которых зоны ещё не посчитаны этой версией.
func WorkoutsNeedingZones(q DBTX, version int) ([]FileRef, error) {
	rows, err := q.Query(`SELECT id, file_path FROM workouts WHERE zones_version < ? ORDER BY id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRef
	for rows.Next() {
		var r FileRef
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Week — ISO-неделя (понедельник–воскресенье) с нагрузкой и зонами.
type Week struct {
	Start        time.Time `json:"start"` // понедельник, локальная полночь
	TSS          float64   `json:"tss"`
	Hours        float64   `json:"hours"`
	DistanceKm   float64   `json:"distanceKm"`
	Rides        int       `json:"rides"`
	TrainerHours float64   `json:"trainerHours"`
	OutdoorHours float64   `json:"outdoorHours"`
	Zones        [7]int    `json:"zones"`
	LowPct       float64   `json:"lowPct"`  // Z1+Z2
	HighPct      float64   `json:"highPct"` // Z4..Z7
	GrayPct      float64   `json:"grayPct"` // Z3
}

// WeekStart — понедельник 00:00 локального времени.
func WeekStart(t time.Time) time.Time {
	wd := int(t.Weekday()) // воскресенье = 0
	if wd == 0 {
		wd = 7
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1-wd)
}

// Weeks — последние n ISO-недель (включая текущую) с TSS, временем, дистанцией
// и зонами. Недели без тренировок присутствуют с нулями. Тренировки с
// start_time позже now не учитываются: будущая запись не должна раздувать
// текущую неделю. TSS эффективный: у тренировки без мощности берётся оценка
// по пульсу (tss_est); зон у неё нет — в Zones она не попадает.
func Weeks(db DBTX, n int, now time.Time) ([]Week, error) {
	cur := WeekStart(now)
	from := cur.AddDate(0, 0, -7*(n-1))
	qFrom := from.AddDate(0, 0, -2).UTC().Format(time.RFC3339)
	qTo := cur.AddDate(0, 0, 8).UTC().Format(time.RFC3339)
	qNow := now.UTC().Format(time.RFC3339)

	rows, err := db.Query(`SELECT id, start_time, duration_sec, COALESCE(distance_m, 0),
	    trainer, COALESCE(tss, tss_est, 0)
	    FROM workouts WHERE start_time >= ? AND start_time < ? AND start_time <= ?
	    ORDER BY start_time`, qFrom, qTo, qNow)
	if err != nil {
		return nil, err
	}
	byWeek := map[time.Time]*Week{}
	weekOf := map[int64]time.Time{}
	for rows.Next() {
		var id, dur int64
		var start string
		var dist, tss float64
		var trainer bool
		if err := rows.Scan(&id, &start, &dur, &dist, &trainer, &tss); err != nil {
			rows.Close()
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			rows.Close()
			return nil, err
		}
		lt := t.Local()
		if lt.After(now) || lt.Before(from) || !lt.Before(cur.AddDate(0, 0, 7)) {
			continue
		}
		wk := WeekStart(lt)
		w := byWeek[wk]
		if w == nil {
			w = &Week{Start: wk}
			byWeek[wk] = w
		}
		h := float64(dur) / 3600
		w.TSS += tss
		w.Hours += h
		w.DistanceKm += dist / 1000
		w.Rides++
		if trainer {
			w.TrainerHours += h
		} else {
			w.OutdoorHours += h
		}
		weekOf[id] = wk
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	zrows, err := db.Query(`SELECT z.workout_id, z.zone, z.secs
	    FROM workout_zones z JOIN workouts w ON w.id = z.workout_id
	    WHERE w.start_time >= ? AND w.start_time < ? AND w.start_time <= ?`,
		qFrom, qTo, qNow)
	if err != nil {
		return nil, err
	}
	for zrows.Next() {
		var id int64
		var zone, secs int
		if err := zrows.Scan(&id, &zone, &secs); err != nil {
			zrows.Close()
			return nil, err
		}
		wk, ok := weekOf[id]
		if !ok || zone < 1 || zone > 7 {
			continue
		}
		if w := byWeek[wk]; w != nil {
			w.Zones[zone-1] += secs
		}
	}
	zrows.Close()
	if err := zrows.Err(); err != nil {
		return nil, err
	}

	out := make([]Week, 0, n)
	for i := 0; i < n; i++ {
		ws := from.AddDate(0, 0, 7*i)
		w := Week{Start: ws}
		if a := byWeek[ws]; a != nil {
			w = *a
		}
		w.Start = ws
		total := 0
		for _, z := range w.Zones {
			total += z
		}
		if total > 0 {
			low := w.Zones[0] + w.Zones[1]
			high := w.Zones[3] + w.Zones[4] + w.Zones[5] + w.Zones[6]
			gray := w.Zones[2]
			w.LowPct = roundPct(low, total)
			w.HighPct = roundPct(high, total)
			w.GrayPct = roundPct(gray, total)
		}
		out = append(out, w)
	}
	return out, nil
}

func roundPct(v, total int) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(v)*1000/float64(total)) / 10
}
