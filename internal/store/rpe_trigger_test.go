package store

import (
	"testing"
	"time"
)

// TestRPECheckTrigger — инвариант 1–10 держит и прямой SQL, а не только веб:
// колонка добавлена ALTER'ом, поэтому вместо CHECK стоит триггер.
func TestRPECheckTrigger(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: time.Hour},
		"hash-rpe-trigger", "/tmp/c.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{0, 11} {
		if _, err := db.Exec(`UPDATE workouts SET rpe = ? WHERE id = ?`, bad, id); err == nil {
			t.Errorf("rpe=%d принят напрямую в SQL", bad)
		}
	}
	if _, err := db.Exec(`UPDATE workouts SET rpe = 10 WHERE id = ?`, id); err != nil {
		t.Errorf("rpe=10 отклонён: %v", err)
	}
	if _, err := db.Exec(`UPDATE workouts SET rpe = NULL WHERE id = ?`, id); err != nil {
		t.Errorf("rpe=NULL отклонён: %v", err)
	}
}
