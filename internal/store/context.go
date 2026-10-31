package store

// SetWorkoutContext сохраняет RPE (1–10) и заметку. rpe=0 и notes="" —
// сбросить в NULL: пустая заметка не равна пустой строке в базе.
func SetWorkoutContext(db DBTX, id int64, rpe int, notes string) error {
	var r, n any
	if rpe > 0 {
		r = rpe
	}
	if notes != "" {
		n = notes
	}
	_, err := db.Exec(`UPDATE workouts SET rpe = ?, notes = ? WHERE id = ?`, r, n, id)
	return err
}
