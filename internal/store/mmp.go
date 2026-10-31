package store

import (
	"database/sql"
)

// PowerRecord — лучшее среднее за окно MMP.
type PowerRecord struct {
	WindowSec int     `json:"secs"`
	Watts     float64 `json:"watts"`
}

// SetWorkoutMMP заменяет MMP тренировки и помечает версию. При вызове с *sql.DB
// работает в собственной транзакции.
func SetWorkoutMMP(q DBTX, id int64, records []PowerRecord, version int) error {
	if db, ok := q.(*sql.DB); ok {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := setWorkoutMMP(tx, id, records, version); err != nil {
			return err
		}
		return tx.Commit()
	}
	return setWorkoutMMP(q, id, records, version)
}

func setWorkoutMMP(q DBTX, id int64, records []PowerRecord, version int) error {
	if _, err := q.Exec(`DELETE FROM power_records WHERE workout_id = ?`, id); err != nil {
		return err
	}
	for _, r := range records {
		if _, err := q.Exec(`INSERT INTO power_records (workout_id, window_sec, watts) VALUES (?,?,?)`,
			id, r.WindowSec, r.Watts); err != nil {
			return err
		}
	}
	_, err := q.Exec(`UPDATE workouts SET mmp_version = ? WHERE id = ?`, version, id)
	return err
}

// WorkoutMMP — MMP тренировки по возрастанию окна.
func WorkoutMMP(q DBTX, id int64) ([]PowerRecord, error) {
	rows, err := q.Query(`SELECT window_sec, watts FROM power_records
	    WHERE workout_id = ? ORDER BY window_sec`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PowerRecord{}
	for rows.Next() {
		var r PowerRecord
		if err := rows.Scan(&r.WindowSec, &r.Watts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WorkoutsNeedingMMP — тренировки, у которых MMP ещё не посчитан этой версией.
func WorkoutsNeedingMMP(q DBTX, version int) ([]FileRef, error) {
	rows, err := q.Query(`SELECT id, file_path FROM workouts WHERE mmp_version < ? ORDER BY id`, version)
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
