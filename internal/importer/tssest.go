package importer

import (
	"database/sql"
	"time"

	"cycling-dashboard/internal/hrtss"
	"cycling-dashboard/internal/store"
)

// BackfillTSSEst оценивает TSS для тренировок без поверметра, но с пульсом,
// на основе ближайших по времени тренировок с мощностью и пульсом (до или
// после — исторический backlog тоже покрывается). Идемпотентен:
// повторный прогон ничего не меняет, пока не появятся новые данные.
// Если у тренировки позже появляются данные мощности, оценка очищается.
func BackfillTSSEst(db *sql.DB) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Гигиена: появилась мощность — оценка по пульсу больше не нужна.
	if _, err := tx.Exec(`UPDATE workouts SET tss_est = NULL, tss_est_low = 0
	    WHERE tss_est IS NOT NULL AND tss IS NOT NULL`); err != nil {
		return 0, err
	}

	rows, err := tx.Query(`SELECT id, avg_hr, duration_sec, start_time FROM workouts
	    WHERE tss IS NULL AND tss_est IS NULL
	      AND avg_hr IS NOT NULL AND avg_hr > 0 AND duration_sec > 0
	    ORDER BY start_time ASC`)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id    int64
		hr    int
		dur   int64
		start time.Time
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		var hr, dur sql.NullInt64
		var start string
		if err := rows.Scan(&c.id, &hr, &dur, &start); err != nil {
			rows.Close()
			return 0, err
		}
		if !hr.Valid || !dur.Valid {
			continue
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			rows.Close()
			return 0, err
		}
		c.hr = int(hr.Int64)
		c.dur = dur.Int64
		c.start = t
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var n int
	for _, c := range cands {
		rides, err := store.PowerHRRidesClosest(tx, c.start, 10)
		if err != nil {
			return 0, err
		}
		model, ok := hrtss.Fit(rides)
		if !ok {
			continue
		}
		cov, err := hrCoverage(tx, c.id)
		if err != nil {
			return 0, err
		}
		if cov < 0.5 {
			continue
		}
		tss, low, ok := model.Estimate(c.hr, c.dur)
		if !ok {
			continue
		}
		lowInt := 0
		if low {
			lowInt = 1
		}
		if _, err := tx.Exec(`UPDATE workouts SET tss_est = ?, tss_est_low = ? WHERE id = ?`,
			tss, lowInt, c.id); err != nil {
			return 0, err
		}
		n++
	}
	return n, tx.Commit()
}

// hrCoverage возвращает долю сэмплов тренировки с hr > 0.
// Если сэмплов нет, считаем покрытие полным (используем avg_hr).
func hrCoverage(q store.DBTX, workoutID int64) (float64, error) {
	var total, covered int
	err := q.QueryRow(`SELECT COUNT(*),
	    COALESCE(SUM(CASE WHEN hr > 0 THEN 1 ELSE 0 END), 0)
	    FROM samples WHERE workout_id = ?`, workoutID).Scan(&total, &covered)
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 1, nil
	}
	return float64(covered) / float64(total), nil
}
