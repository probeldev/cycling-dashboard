package store

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// TestWorkoutContextStore — RPE/заметка сохраняются, sRPE считается на лету,
// пустые значения пишутся как NULL.
func TestWorkoutContextStore(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: 90 * time.Minute},
		"hash-ctx-store", "/tmp/c.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutContext(db, id, 7, "темповая, ветер"); err != nil {
		t.Fatal(err)
	}
	w, err := GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.RPE != 7 || w.Notes != "темповая, ветер" {
		t.Errorf("RPE=%d Notes=%q", w.RPE, w.Notes)
	}
	if math.Abs(w.SRPE-630) > 1e-9 {
		t.Errorf("SRPE=%v, хочу 630 (7 × 90 мин)", w.SRPE)
	}

	if err := SetWorkoutContext(db, id, 0, ""); err != nil {
		t.Fatal(err)
	}
	var rpe sql.NullInt64
	var notes sql.NullString
	if err := db.QueryRow(`SELECT rpe, notes FROM workouts WHERE id = ?`, id).Scan(&rpe, &notes); err != nil {
		t.Fatal(err)
	}
	if rpe.Valid || notes.Valid {
		t.Errorf("после очистки не NULL: rpe=%v notes=%v", rpe, notes)
	}
	if w, _ := GetWorkout(db, id); w.RPE != 0 || w.SRPE != 0 {
		t.Errorf("после очистки: RPE=%d SRPE=%v", w.RPE, w.SRPE)
	}
}

// TestWeeksSRPE — сумма sRPE по неделе: RPE × минуты.
func TestWeeksSRPE(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	ws := WeekStart(now)
	for _, w := range []struct {
		hash string
		dur  time.Duration
		rpe  int
	}{
		{"hash-srpe-1", time.Hour, 5},        // 300
		{"hash-srpe-2", 30 * time.Minute, 7}, // 210
	} {
		id, err := InsertWorkout(db, &Workout{StartTime: ws.Add(12 * time.Hour), Duration: w.dur},
			w.hash, filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutContext(db, id, w.rpe, ""); err != nil {
			t.Fatal(err)
		}
	}
	weeks, err := Weeks(db, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := weeks[len(weeks)-1].SRPESum; math.Abs(got-510) > 1e-9 {
		t.Errorf("SRPESum = %v, хочу 510", got)
	}
}
