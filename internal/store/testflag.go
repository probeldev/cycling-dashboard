package store

import "database/sql"

// SetWorkoutTest помечает тренировку как тестовую (замер) или снимает флаг.
// Участие в рекордах MMP/CP решается на чтении, поэтому пересчитывать
// power_records не нужно: флаг сразу меняет выдачу records/cp/prs.
func SetWorkoutTest(db DBTX, id int64, isTest bool) error {
	v := 0
	if isTest {
		v = 1
	}
	res, err := db.Exec(`UPDATE workouts SET is_test = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
