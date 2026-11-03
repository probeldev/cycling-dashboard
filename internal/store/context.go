package store

import "database/sql"

// WorkoutContextUpdate — частичное обновление контекста тренировки:
// nil-поле не трогаем. Указатель на пустую строку — сбросить в NULL.
type WorkoutContextUpdate struct {
	Notes *string // до 2000 символов (проверяет вызывающий)
}

// SetWorkoutContext обновляет только переданные поля. При вызове с *sql.DB —
// в одной транзакции.
func SetWorkoutContext(db DBTX, id int64, u WorkoutContextUpdate) error {
	if u.Notes == nil {
		return nil
	}
	if d, ok := db.(*sql.DB); ok {
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := setWorkoutContext(tx, id, u); err != nil {
			return err
		}
		return tx.Commit()
	}
	return setWorkoutContext(db, id, u)
}

func setWorkoutContext(q DBTX, id int64, u WorkoutContextUpdate) error {
	if u.Notes != nil {
		var v any
		if *u.Notes != "" {
			v = *u.Notes
		}
		if _, err := q.Exec(`UPDATE workouts SET notes = ? WHERE id = ?`, v, id); err != nil {
			return err
		}
	}
	return nil
}
