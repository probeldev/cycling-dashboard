package store

import (
	"math"
	"testing"
	"time"
)

// TestBestRecords — лучшее по окну за период; при равенстве побеждает более
// ранняя тренировка.
func TestBestRecords(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local) }
	mk := func(hash string, start time.Time, records ...PowerRecord) int64 {
		t.Helper()
		id, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
			hash, "/tmp/"+hash+".fit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutMMP(db, id, records, 1); err != nil {
			t.Fatal(err)
		}
		return id
	}

	a := mk("hash-rec-1", day(1), PowerRecord{WindowSec: 5, Watts: 300}, PowerRecord{WindowSec: 60, Watts: 200})
	b := mk("hash-rec-2", day(2), PowerRecord{WindowSec: 5, Watts: 350}, PowerRecord{WindowSec: 60, Watts: 150})

	recs, err := BestRecords(db, time.Time{}, day(10))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("%d окон, хочу 2", len(recs))
	}
	if recs[0].WindowSec != 5 || math.Abs(recs[0].Watts-350) > 1e-9 || recs[0].WorkoutID != b {
		t.Errorf("5 с: %+v, хочу b/350", recs[0])
	}
	if recs[1].WindowSec != 60 || math.Abs(recs[1].Watts-200) > 1e-9 || recs[1].WorkoutID != a {
		t.Errorf("60 с: %+v, хочу a/200", recs[1])
	}

	// равное значение позже — рекорд остаётся у более ранней
	c := mk("hash-rec-3", day(3), PowerRecord{WindowSec: 5, Watts: 350})
	recs, err = BestRecords(db, time.Time{}, day(10))
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].WorkoutID != b {
		t.Errorf("при равенстве рекорд не у ранней: id=%d (b=%d, c=%d)", recs[0].WorkoutID, b, c)
	}

	// период: в [day(3), day(10)) остаётся только c
	recs, err = BestRecords(db, day(3), day(10))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].WorkoutID != c || recs[0].WindowSec != 5 {
		t.Errorf("период: %+v, хочу только c/5", recs)
	}
}

// TestWorkoutPRs — рекордные окна по периодам; равенство отдаётся ранней.
func TestWorkoutPRs(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	id, err := InsertWorkout(db, &Workout{StartTime: now, Duration: time.Hour},
		"hash-pr-1", "/tmp/pr.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutMMP(db, id, []PowerRecord{
		{WindowSec: 5, Watts: 300}, {WindowSec: 60, Watts: 200},
	}, 1); err != nil {
		t.Fatal(err)
	}
	prs, err := WorkoutPRs(db, id, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{5, 60} {
		got := prs[w]
		if len(got) != 4 || got[0] != "all" || got[1] != "365d" || got[2] != "90d" || got[3] != "30d" {
			t.Errorf("окно %d: %v, хочу [all 365d 90d 30d]", w, got)
		}
	}
}
